package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLendPlanDocPutsItInsideTheWorktree(t *testing.T) {
	wt := t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-thing.md")
	writeFile(t, doc, "# Phase 1\n\n- [ ] a criterion\n")

	rel, err := LendPlanDoc(wt, doc)
	if err != nil {
		t.Fatalf("lend: %v", err)
	}
	if rel != filepath.Join(PlanDocDir, "phase-1-thing.md") {
		t.Errorf("rel = %q, want it under %s", rel, PlanDocDir)
	}
	if filepath.IsAbs(rel) {
		t.Error("the lent path must be RELATIVE — an absolute path is what the sandbox refuses")
	}
	if got := readFile(t, filepath.Join(wt, rel)); !strings.Contains(got, "a criterion") {
		t.Errorf("the lent copy does not carry the document: %q", got)
	}
}

func TestLendPlanDocWithNothingToLend(t *testing.T) {
	wt := t.TempDir()
	if rel, err := LendPlanDoc(wt, ""); rel != "" || err != nil {
		t.Errorf("no doc = (%q, %v), want (\"\", nil) — a card without a plan is normal", rel, err)
	}
	if rel, err := LendPlanDoc("", "/x/doc.md"); rel != "" || err != nil {
		t.Errorf("no worktree = (%q, %v), want (\"\", nil)", rel, err)
	}
	if _, err := LendPlanDoc(wt, filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Error("a missing source document should report an error, not lend silently")
	}
}

// The whole reason lending is safe: an edit made in the worktree reaches the
// workspace document the dashboard reads.
func TestReturnPlanDocCarriesTheReportBack(t *testing.T) {
	wt := t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-thing.md")
	writeFile(t, doc, "# Phase 1\n\n- [ ] a criterion\n\n## Completion Report\n")

	rel, err := LendPlanDoc(wt, doc)
	if err != nil {
		t.Fatal(err)
	}
	// The agent ticks a box and writes its report into the LENT copy.
	edited := "# Phase 1\n\n- [x] a criterion\n\n## Completion Report\n\nShipped the thing.\n"
	writeFile(t, filepath.Join(wt, rel), edited)

	wrote, err := ReturnPlanDoc(wt, rel, doc)
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if !wrote {
		t.Fatal("an edited document was not returned")
	}
	got := readFile(t, doc)
	if !strings.Contains(got, "Shipped the thing.") {
		t.Errorf("the Completion Report did not reach the workspace doc:\n%s", got)
	}
	if !strings.Contains(got, "- [x] a criterion") {
		t.Errorf("the ticked checkbox did not reach the workspace doc:\n%s", got)
	}
}

// Returning an untouched file would rewrite the workspace doc's mtime on every
// run and make "was this phase touched?" unanswerable from the filesystem.
func TestReturnPlanDocLeavesAnUntouchedDocAlone(t *testing.T) {
	wt := t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-thing.md")
	writeFile(t, doc, "# Phase 1\n")
	rel, err := LendPlanDoc(wt, doc)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(doc)
	if err != nil {
		t.Fatal(err)
	}
	wrote, err := ReturnPlanDoc(wt, rel, doc)
	if err != nil || wrote {
		t.Fatalf("return of an untouched doc = (%v, %v), want (false, nil)", wrote, err)
	}
	after, err := os.Stat(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an untouched document was rewritten anyway")
	}
}

// An empty copy is damage, not an edit — the workspace document is the only
// record of the work and must not be destroyed by one.
func TestReturnPlanDocRefusesToOverwriteWithAnEmptyCopy(t *testing.T) {
	wt := t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-thing.md")
	writeFile(t, doc, "# Phase 1\n\nreal content\n")
	rel, err := LendPlanDoc(wt, doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, rel), "   \n\n")

	wrote, err := ReturnPlanDoc(wt, rel, doc)
	if wrote {
		t.Error("an empty copy overwrote the workspace document")
	}
	if err == nil {
		t.Error("an empty copy should be reported, not silently ignored")
	}
	if got := readFile(t, doc); !strings.Contains(got, "real content") {
		t.Errorf("the workspace document lost its content:\n%s", got)
	}
}

// A deleted or already-torn-down copy is nothing to return, and nothing to
// complain about — this runs on the failure path too, where the worktree may
// already be gone.
func TestReturnPlanDocToleratesAMissingCopy(t *testing.T) {
	wt := t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-thing.md")
	writeFile(t, doc, "# Phase 1\n")
	wrote, err := ReturnPlanDoc(wt, filepath.Join(PlanDocDir, "phase-1-thing.md"), doc)
	if wrote || err != nil {
		t.Errorf("missing copy = (%v, %v), want (false, nil)", wrote, err)
	}
	if wrote, err := ReturnPlanDoc(wt, "", doc); wrote || err != nil {
		t.Errorf("no rel path = (%v, %v), want (false, nil)", wrote, err)
	}
}

// ── CollectReport: the docless counterpart of ReturnPlanDoc ──

func TestCollectReportReadsWhatTheAgentWrote(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(ReportPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ReportPath), []byte("\n  shipped the thing\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CollectReport(wt); got != "shipped the thing" {
		t.Errorf("CollectReport = %q, want the trimmed report", got)
	}
}

// A run that wrote no report still shipped its commits, so every absent case is
// "" and never an error the caller has to handle.
func TestCollectReportIsSilentWhenThereIsNothingToCollect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"no worktree", func(*testing.T) string { return "" }},
		{"no report file", func(t *testing.T) string { return t.TempDir() }},
		{"empty report file", func(t *testing.T) string {
			wt := t.TempDir()
			if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(ReportPath)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, ReportPath), []byte("   \n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return wt
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CollectReport(tc.setup(t)); got != "" {
				t.Errorf("CollectReport = %q, want empty", got)
			}
		})
	}
}

// result_note renders inline on the board, so a pasted build log must not land
// there whole.
func TestCollectReportTruncatesAPastedLog(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(ReportPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", maxReportBytes*2)
	if err := os.WriteFile(filepath.Join(wt, ReportPath), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := CollectReport(wt)
	if len(got) >= len(big) {
		t.Errorf("CollectReport returned %d bytes, want it capped near %d", len(got), maxReportBytes)
	}
	if !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("a truncated report must say so; got the tail %q", got[max(0, len(got)-20):])
	}
}

func TestDocCopyHash(t *testing.T) {
	wt, ws := t.TempDir(), t.TempDir()
	doc := filepath.Join(ws, "phase-1-x.md")
	if err := os.WriteFile(doc, []byte("# Phase 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DocCopyHash(wt, doc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no copy yet: err = %v, want ErrNotExist", err)
	}
	if _, err := LendPlanDoc(wt, doc); err != nil {
		t.Fatal(err)
	}
	got, err := DocCopyHash(wt, doc)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := FileHash(doc)
	// sha256("# Phase 1\n"), pinned so the hash is the content's, not a path's.
	if got != want || got != "bedee2be41b4880554d301f7949823b2dffad1c90d065f339d6f3d4dfb9b8daf" {
		t.Fatalf("DocCopyHash = %s, want %s", got, want)
	}
	if err := os.WriteFile(filepath.Join(wt, LentPlanDocRel(doc)), []byte("# Phase 1 tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tampered, _ := DocCopyHash(wt, doc); tampered == want {
		t.Fatal("a tampered copy hashes like the canonical doc")
	}
}

func TestRelendPlanDoc(t *testing.T) {
	wt, ws := t.TempDir(), t.TempDir()
	doc := filepath.Join(ws, "phase-1-x.md")
	if err := os.WriteFile(doc, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No copy in the worktree: nothing is planted.
	if ok, err := RelendPlanDoc(wt, doc); ok || err != nil {
		t.Fatalf("RelendPlanDoc without a copy = %v, %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(wt, PlanDocDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a .swarmery/plan was planted: %v", err)
	}
	if _, err := LendPlanDoc(wt, doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("v2 revised\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := RelendPlanDoc(wt, doc); !ok || err != nil {
		t.Fatalf("RelendPlanDoc = %v, %v", ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, LentPlanDocRel(doc))); string(b) != "v2 revised\n" {
		t.Fatalf("copy = %q, want the revised doc", b)
	}
}
