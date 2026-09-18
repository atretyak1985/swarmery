package memconsolidate

// agent-memory phase 3: the consolidation engine's tests.
//
// Three things are load-bearing and each has its own case:
//   - the classifier, including the two AMBIGUOUS shapes the phase doc calls
//     out ("MERGED … OPEN: …" keeps, "FULLY CLOSED …; no open tail" closes) and
//     the "SHIPPED …; tail = …" shape the open-marker set must veto;
//   - the [[link]] dependency guard — a closed entry an OPEN memory still links
//     to is never consolidated;
//   - dry-run purity — BuildPlan leaves the directory byte-identical.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// memoryDir writes an index plus one topic file per named entry into a temp
// auto-memory dir and returns it.
func memoryDir(t *testing.T, index string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, IndexPath(dir), index)
	for name, body := range files {
		writeFile(t, filepath.Join(dir, name), body)
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// dirHash fingerprints every file under dir (relative path + content), so a
// test can assert that an operation changed literally nothing.
func dirHash(t *testing.T, dir string) string {
	t.Helper()
	var parts []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		parts = append(parts, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// ── classifier ──────────────────────────────────────────────────────────────

func TestIsClosed(t *testing.T) {
	cases := []struct {
		name string
		hook string
		want bool
	}{
		// The two ambiguous shapes the phase doc names explicitly.
		{
			name: "ambiguous: closed marker with an OPEN: tail stays",
			hook: "MERGED + deployed 2026-08-24 (#260, #261), core 2.17.0; OPEN: flip the guard to enforce 2026-08-31.",
			want: false,
		},
		{
			name: "ambiguous: FULLY CLOSED with an explicit no-open-tail closes",
			hook: "FULLY CLOSED 2026-08-11: exporter LIVE (12/12) + service reinstalled; no open tail.",
			want: true,
		},
		// The `tail = …` veto — dead under the doc's literal trailing-\b form.
		{
			name: "SHIPPED with a tail = remainder stays",
			hook: "SHIPPED 2026-08-11 (#215–#218 merged, #219 open); tail = deploy + amnesty.",
			want: false,
		},
		{
			name: "LIVE with a tail= remainder stays",
			hook: "the loop is LIVE; tail= the guardrails plan it produced.",
			want: false,
		},
		{
			name: "lowercase open: veto",
			hook: "DONE 2026-08-03; open: the follow-up ticket.",
			want: false,
		},
		{name: "impl open veto", hook: "MERGED 2026-08-11 (5 phases locked); impl open.", want: false},
		{name: "awaits veto", hook: "SHIPPED 2026-08-11; the unification spec awaits D1–D5.", want: false},
		// Plain closes.
		{name: "DONE", hook: "DONE 2026-07-27: slugs name-derived.", want: true},
		{name: "RESOLVED", hook: "RESOLVED #235 (2026-08-14): stale sha → golden file.", want: true},
		{name: "MERGED then DONE", hook: "MERGED 2026-08-10 (PR #211) + deployed; DONE.", want: true},
		{name: "CLOSED", hook: "CLOSED 2026-07-30, all 6 phases ticked.", want: true},
		{name: "SHIPPED with no tail", hook: "SHIPPED 2026-08-03 (PR #176); ticket-class fix merged into dev.", want: true},
		// Plain opens (no closed marker at all).
		{name: "no marker", hook: "7-phase plan written 2026-09-18; impl open.", want: false},
		{name: "empty hook", hook: "", want: false},
		// Narration must not count as a status stamp.
		{name: "lowercase merged is narration", hook: "the branch was merged into dev yesterday.", want: false},
		{name: "lowercase done is narration", hook: "nearly done, one review left.", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClosed(tc.hook); got != tc.want {
				t.Fatalf("IsClosed(%q) = %v, want %v", tc.hook, got, tc.want)
			}
		})
	}
}

func TestParseIndexShapes(t *testing.T) {
	index := strings.Join([]string{
		"# Index",
		"",
		"Some prose that is not an entry.",
		"- [Em dash](em-dash.md) — DONE 2026-01-01.",
		"- [En dash](en-dash.md) – DONE 2026-01-02.",
		"- [Double hyphen](double.md) -- DONE 2026-01-03.",
		"- [No dash](no-dash.md) DONE 2026-01-04.",
		"* [Star bullet](star.md) — impl open.",
		"- not a link at all",
		"",
	}, "\n")
	got := ParseIndex(index)
	if len(got) != 5 {
		t.Fatalf("ParseIndex returned %d entries, want 5: %+v", len(got), got)
	}
	want := []struct {
		file   string
		closed bool
		lineNo int
	}{
		{"em-dash.md", true, 4},
		{"en-dash.md", true, 5},
		{"double.md", true, 6},
		{"no-dash.md", true, 7},
		{"star.md", false, 8},
	}
	for i, w := range want {
		if got[i].File != w.file || got[i].Closed != w.closed || got[i].LineNo != w.lineNo {
			t.Errorf("entry %d = %+v, want file=%s closed=%v lineNo=%d", i, got[i], w.file, w.closed, w.lineNo)
		}
	}
	if got[0].Title != "Em dash" {
		t.Errorf("title = %q, want %q", got[0].Title, "Em dash")
	}
	if got[3].Hook != "DONE 2026-01-04." {
		t.Errorf("dashless hook = %q", got[3].Hook)
	}
}

// TestInspectTestdataIndex pins the realistic index fixture (the shape of a real
// auto-memory index, neutral titles) so a classifier change shows up as a count.
func TestInspectTestdataIndex(t *testing.T) {
	dir := t.TempDir()
	body := readFile(t, filepath.Join("testdata", "MEMORY.md"))
	writeFile(t, IndexPath(dir), body)

	st, err := Inspect(dir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.TotalLines != 15 {
		t.Fatalf("TotalLines = %d, want 15 (the heading and blank lines must not count)", st.TotalLines)
	}
	// The last two fixture lines are the live open-tail shapes ("…; tail: …" and
	// "…; 4 deferrals open.") that the original marker set mis-closed. If either
	// regresses this count goes to 8 or 9.
	if st.ClosedCount != 7 {
		t.Fatalf("ClosedCount = %d, want 7", st.ClosedCount)
	}
	if fmt.Sprintf("%.0f", st.ClosedShare*100) != "47" {
		t.Fatalf("ClosedShare = %.4f (%.0f%%), want ~47%%", st.ClosedShare, st.ClosedShare*100)
	}
	if st.IndexBytes != len(body) {
		t.Fatalf("IndexBytes = %d, want %d", st.IndexBytes, len(body))
	}
}

func TestInspectMissingIndex(t *testing.T) {
	if _, err := Inspect(t.TempDir()); !os.IsNotExist(err) {
		t.Fatalf("Inspect on a dir without MEMORY.md: err = %v, want IsNotExist", err)
	}
}

// ── the plan ────────────────────────────────────────────────────────────────

func planFixture(t *testing.T) string {
	t.Helper()
	index := strings.Join([]string{
		"- [Order line items](order-line-items.md) — 7-phase plan; impl open.",
		"- [Search indexer](search-indexer.md) — DONE 2026-07-27: slugs name-derived.",
		"- [Billing migration](billing-migration.md) — MERGED 2026-08-10 + deployed; DONE.",
		"- [Inventory audit](inventory-audit.md) — SHIPPED 2026-08-11; tail = deploy.",
		"- [Vanished topic](vanished-topic.md) — DONE 2026-06-01, file already gone.",
		"",
	}, "\n")
	return memoryDir(t, index, map[string]string{
		// The OPEN entry links to billing-migration — the dependency guard.
		"order-line-items.md":  "---\nname: order-line-items\n---\n\nDepends on [[billing-migration]] for the price column.\n",
		"search-indexer.md":    "---\nname: search-indexer\ndescription: slugs\nmetadata:\n  type: project\n---\n\nSlugs are name-derived.\n",
		"billing-migration.md": "---\nname: billing-migration\n---\n\nThe migration landed in PR #211.\n",
		"inventory-audit.md":   "---\nname: inventory-audit\n---\n\nStill waiting on the deploy.\n",
	})
}

func TestBuildPlanLinkGuardAndReasons(t *testing.T) {
	dir := planFixture(t)
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.ClosedCount != 3 {
		t.Fatalf("ClosedCount = %d, want 3 (search-indexer, billing-migration, vanished-topic)", plan.ClosedCount)
	}
	if len(plan.Move) != 1 || plan.Move[0].File != "search-indexer.md" {
		t.Fatalf("Move = %+v, want only search-indexer.md", plan.Move)
	}
	keep := map[string]string{}
	for _, a := range plan.Keep {
		keep[a.File] = a.Reason
	}
	if keep["billing-migration.md"] != ReasonLinked {
		t.Errorf("billing-migration.md reason = %q, want %q (linked from the open order-line-items memory)",
			keep["billing-migration.md"], ReasonLinked)
	}
	if keep["vanished-topic.md"] != ReasonMissingFile {
		t.Errorf("vanished-topic.md reason = %q, want %q", keep["vanished-topic.md"], ReasonMissingFile)
	}
	if _, held := keep["inventory-audit.md"]; held {
		t.Errorf("inventory-audit.md is open (tail =), so it must not appear in Keep at all")
	}
}

// TestBuildPlanIgnoresLinksBetweenClosedEntries: two closed memories that link
// to each other must both consolidate — only an OPEN memory's link holds one back.
func TestBuildPlanIgnoresLinksBetweenClosedEntries(t *testing.T) {
	index := strings.Join([]string{
		"- [Alpha](alpha.md) — DONE 2026-01-01.",
		"- [Beta](beta.md) — DONE 2026-01-02.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{
		"alpha.md": "see [[beta]]\n",
		"beta.md":  "see [[alpha]]\n",
	})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 2 {
		t.Fatalf("Move = %+v, want both entries", plan.Move)
	}
	if len(plan.Keep) != 0 {
		t.Fatalf("Keep = %+v, want empty", plan.Keep)
	}
}

// TestBuildPlanLinkGuardSeesUnindexedFiles: a memory file with no index line is
// still loaded on demand, so its links must hold a closed entry open too.
func TestBuildPlanLinkGuardSeesUnindexedFiles(t *testing.T) {
	index := "- [Alpha](alpha.md) — DONE 2026-01-01.\n"
	dir := memoryDir(t, index, map[string]string{
		"alpha.md":  "closed content\n",
		"orphan.md": "still relies on [[alpha]]\n",
	})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 0 || len(plan.Keep) != 1 || plan.Keep[0].Reason != ReasonLinked {
		t.Fatalf("Move=%+v Keep=%+v, want alpha held back by the link guard", plan.Move, plan.Keep)
	}
}

func TestBuildPlanRejectsNonFlatLinkTargets(t *testing.T) {
	index := strings.Join([]string{
		"- [Escape](../outside.md) — DONE 2026-01-01.",
		"- [Nested](sub/inner.md) — DONE 2026-01-02.",
		"- [External](https://example.invalid/x) — DONE 2026-01-03.",
		"",
	}, "\n")
	dir := memoryDir(t, index, nil)
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 0 {
		t.Fatalf("Move = %+v, want nothing moved", plan.Move)
	}
	for _, a := range plan.Keep {
		if a.Reason != ReasonNotAFile {
			t.Errorf("%s reason = %q, want %q", a.File, a.Reason, ReasonNotAFile)
		}
	}
}

// TestBuildPlanHoldsBackAMemoryNamedIndexMd pins the ledger-name guard.
//
// INDEX.md is the closed/ ledger. On a FIRST run there is no ledger yet, so the
// closed/-collision hold cannot fire, and an auto-memory literally named
// INDEX.md would be moved into closed/ — after which appendClosedIndex reads it
// as `prev` and appends the ledger heading plus every consolidated line onto
// that memory's own body, leaving one file that is neither a clean memory nor a
// clean ledger. Nothing is lost, but the shape is corrupted.
func TestBuildPlanHoldsBackAMemoryNamedIndexMd(t *testing.T) {
	index := strings.Join([]string{
		"- [Index conventions](INDEX.md) — DONE 2026-01-01: conventions frozen.",
		"- [Search indexer](search-indexer.md) — DONE 2026-01-02: slugs name-derived.",
		"",
	}, "\n")
	body := "---\nname: index-conventions\n---\n\nThe index conventions, frozen.\n"
	dir := memoryDir(t, index, map[string]string{
		"INDEX.md":          body,
		"search-indexer.md": "---\nname: search-indexer\n---\n\nSlugs are name-derived.\n",
	})
	// No closed/ dir yet: this is a first run, so ReasonClosedCollision cannot be
	// what holds INDEX.md back — only the name check can.
	if _, err := os.Stat(ClosedDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("fixture already has a closed/ dir: %v", err)
	}

	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 1 || plan.Move[0].File != "search-indexer.md" {
		t.Fatalf("Move = %+v, want only search-indexer.md", plan.Move)
	}
	if len(plan.Keep) != 1 || plan.Keep[0].File != "INDEX.md" || plan.Keep[0].Reason != ReasonNotAFile {
		t.Fatalf("Keep = %+v, want INDEX.md held back with %q", plan.Keep, ReasonNotAFile)
	}

	if _, err := Apply(dir, plan, ApplyOptions{Now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The memory stayed where it was, byte for byte, and was never stamped.
	if got := readFile(t, filepath.Join(dir, "INDEX.md")); got != body {
		t.Fatalf("memory/INDEX.md was touched:\n%s", got)
	}
	// The ledger is a fresh ledger, not the memory's body with entries appended.
	ledger := readFile(t, ClosedIndexPath(dir))
	if !strings.HasPrefix(ledger, "# Closed memories\n") {
		t.Fatalf("closed/INDEX.md is not a fresh ledger:\n%s", ledger)
	}
	if strings.Contains(ledger, "The index conventions, frozen.") {
		t.Fatalf("the memory body leaked into the ledger:\n%s", ledger)
	}
	if !strings.Contains(ledger, "- [Search indexer](search-indexer.md)") {
		t.Fatalf("ledger lost the consolidated line:\n%s", ledger)
	}
	// A held-back entry keeps its index line.
	if !strings.Contains(readFile(t, IndexPath(dir)), "(INDEX.md)") {
		t.Fatalf("the held-back INDEX.md line was dropped from the index")
	}
}

// TestBuildPlanIsReadOnly is the dry-run contract the API and the CLI rest on.
func TestBuildPlanIsReadOnly(t *testing.T) {
	dir := planFixture(t)
	before := dirHash(t, dir)
	if _, err := BuildPlan(dir); err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if after := dirHash(t, dir); after != before {
		t.Fatalf("BuildPlan mutated the directory: %s != %s", after, before)
	}
}

// ── apply ───────────────────────────────────────────────────────────────────

func TestApplyMovesStampsAndRewrites(t *testing.T) {
	dir := planFixture(t)
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var backedUp []string
	res, err := Apply(dir, plan, ApplyOptions{
		Now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Backup: func(paths []string) (string, error) {
			backedUp = append([]string{}, paths...)
			return "2026-09-18T12-00-00Z", nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.BackupID != "2026-09-18T12-00-00Z" {
		t.Fatalf("BackupID = %q", res.BackupID)
	}
	// Backup must cover the index AND every moved file, before any write.
	wantBackup := []string{IndexPath(dir), filepath.Join(dir, "search-indexer.md")}
	if strings.Join(backedUp, "|") != strings.Join(wantBackup, "|") {
		t.Fatalf("backup paths = %v, want %v", backedUp, wantBackup)
	}

	// The topic file moved, it did not get copied.
	if _, err := os.Stat(filepath.Join(dir, "search-indexer.md")); !os.IsNotExist(err) {
		t.Fatalf("search-indexer.md still in the memory dir: %v", err)
	}
	moved := readFile(t, filepath.Join(ClosedDir(dir), "search-indexer.md"))
	if !strings.Contains(moved, "\nclosed_at: 2026-09-18\n") || !strings.Contains(moved, "\nstatus: closed\n") {
		t.Fatalf("frontmatter not stamped:\n%s", moved)
	}
	if !strings.Contains(moved, "metadata:\n  type: project\n") {
		t.Fatalf("existing frontmatter was damaged:\n%s", moved)
	}
	if !strings.HasPrefix(moved, "---\nname: search-indexer\n") {
		t.Fatalf("frontmatter head rewritten:\n%s", moved)
	}

	// The index lost exactly that line and kept every other byte.
	idx := readFile(t, IndexPath(dir))
	if strings.Contains(idx, "search-indexer.md") {
		t.Fatalf("index still carries the consolidated line:\n%s", idx)
	}
	for _, keep := range []string{"order-line-items.md", "billing-migration.md", "inventory-audit.md", "vanished-topic.md"} {
		if !strings.Contains(idx, keep) {
			t.Fatalf("index lost %s:\n%s", keep, idx)
		}
	}

	// The ledger records the verbatim index line under a dated heading.
	ledger := readFile(t, ClosedIndexPath(dir))
	if !strings.Contains(ledger, "## 2026-09-18") {
		t.Fatalf("ledger has no dated heading:\n%s", ledger)
	}
	if !strings.Contains(ledger, "- [Search indexer](search-indexer.md) — DONE 2026-07-27: slugs name-derived.") {
		t.Fatalf("ledger lost the verbatim index line:\n%s", ledger)
	}
}

func TestApplyNoopWhenNothingClosed(t *testing.T) {
	dir := memoryDir(t, "- [Order line items](order-line-items.md) — impl open.\n",
		map[string]string{"order-line-items.md": "body\n"})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	before := dirHash(t, dir)
	calls := 0
	res, err := Apply(dir, plan, ApplyOptions{Backup: func([]string) (string, error) { calls++; return "x", nil }})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Moved) != 0 || calls != 0 {
		t.Fatalf("empty plan did work: moved=%v backupCalls=%d", res.Moved, calls)
	}
	if after := dirHash(t, dir); after != before {
		t.Fatalf("empty apply mutated the directory")
	}
}

func TestApplyAppendsToAnExistingLedger(t *testing.T) {
	index := strings.Join([]string{
		"- [Alpha](alpha.md) — DONE 2026-01-01.",
		"- [Beta](beta.md) — DONE 2026-01-02.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{"alpha.md": "a\n", "beta.md": "b\n"})
	writeFile(t, ClosedIndexPath(dir), "# Closed memories\n\n## 2026-01-01\n\n- [Old](old.md) — DONE.\n")

	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := Apply(dir, plan, ApplyOptions{Now: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ledger := readFile(t, ClosedIndexPath(dir))
	if !strings.Contains(ledger, "- [Old](old.md) — DONE.") {
		t.Fatalf("existing ledger content lost:\n%s", ledger)
	}
	if strings.Count(ledger, "# Closed memories") != 1 {
		t.Fatalf("ledger header duplicated:\n%s", ledger)
	}
	if !strings.Contains(ledger, "- [Alpha](alpha.md)") || !strings.Contains(ledger, "- [Beta](beta.md)") {
		t.Fatalf("ledger missing the new lines:\n%s", ledger)
	}
}

func TestStampClosed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		not  []string
	}{
		{
			name: "no frontmatter gets a block",
			in:   "just a note\n",
			want: []string{"---\nclosed_at: 2026-09-18\nstatus: closed\n---\n\njust a note\n"},
		},
		{
			name: "nested metadata is untouched and the stamp is top-level",
			in:   "---\nname: x\nmetadata:\n  type: project\n---\n\nbody\n",
			want: []string{"\nclosed_at: 2026-09-18\n", "\nstatus: closed\n---\n", "metadata:\n  type: project\n"},
		},
		{
			name: "an existing top-level status is not duplicated",
			in:   "---\nname: x\nstatus: archived\n---\n\nbody\n",
			want: []string{"closed_at: 2026-09-18", "status: archived"},
			not:  []string{"status: closed"},
		},
		{
			name: "unterminated frontmatter is treated as no frontmatter",
			in:   "---\nname: x\nbody with no closing fence\n",
			want: []string{"---\nclosed_at: 2026-09-18\nstatus: closed\n---\n\n---\nname: x\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stampClosed([]byte(tc.in), "2026-09-18"))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output missing %q:\n%s", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("output unexpectedly contains %q:\n%s", n, got)
				}
			}
		})
	}
}

// ── root resolution ─────────────────────────────────────────────────────────

func TestAutoMemoryDirMirrorsTheApiRoot(t *testing.T) {
	got := AutoMemoryDirIn("/tmp/claude", "/Volumes/Work/example/")
	want := filepath.Join("/tmp/claude", "projects", "-Volumes-Work-example", "memory")
	if got != want {
		t.Fatalf("AutoMemoryDirIn = %q, want %q", got, want)
	}
}

func TestSetClaudeDir(t *testing.T) {
	prev := ClaudeDir()
	t.Cleanup(func() { claudeDir = prev })
	SetClaudeDir("/tmp/other")
	if ClaudeDir() != "/tmp/other" {
		t.Fatalf("ClaudeDir = %q", ClaudeDir())
	}
	SetClaudeDir("") // empty keeps the current value
	if ClaudeDir() != "/tmp/other" {
		t.Fatalf("empty SetClaudeDir changed the value to %q", ClaudeDir())
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{512, "512 B"}, {8294, "8.1 KB"}, {2 * 1024 * 1024, "2.0 MB"}} {
		if got := HumanBytes(tc.in); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatPlanMentionsCountsAndReasons(t *testing.T) {
	dir := planFixture(t)
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	out := FormatPlan(plan)
	for _, want := range []string{"3/5 lines closed", "consolidate 1", "held back 2", ReasonLinked} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatPlan output missing %q:\n%s", want, out)
		}
	}
}
