package phaserun

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// guardTrap stands the guard-trap fixture (testdata/fixtures/guard-trap) up as a
// run: the module committed in a temp repo (the "worktree"), its phase doc in a
// separate workspace dir and lent into the repo exactly as Start lends it.
type guardTrap struct {
	*tempRepo
	doc  string // canonical (workspace) doc
	lent string // the doc's text as lent
	base string
}

func newGuardTrap(t *testing.T) *guardTrap {
	t.Helper()
	r := newTempRepo(t)
	fix := filepath.Join("..", "..", "testdata", "fixtures", "guard-trap")
	for _, name := range []string{"go.mod", "trap.go", "trap_test.go"} {
		b, err := os.ReadFile(filepath.Join(fix, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// .swarmery/ is the daemon's, never committed by a run.
	if err := os.WriteFile(filepath.Join(r.dir, ".gitignore"), []byte(".swarmery/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run("add", "-A")
	r.run("commit", "-q", "-m", "trap")
	ws := t.TempDir()
	doc := filepath.Join(ws, "phase-1-trap.md")
	lent, err := os.ReadFile(filepath.Join(fix, "plan", "phase-1-trap.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, lent, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.LendPlanDoc(r.dir, doc); err != nil {
		t.Fatal(err)
	}
	return &guardTrap{tempRepo: r, doc: doc, lent: string(lent), base: r.tip("HEAD")}
}

func (g *guardTrap) write(rel, body string) {
	g.t.Helper()
	p := filepath.Join(g.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

func (g *guardTrap) check() GuardResult {
	return GuardCheck(worktree.ExecGit{}, GuardInput{DocPath: g.doc, LentDoc: g.lent, Worktree: g.dir, Base: g.base})
}

func TestGuardCheckProtectedChanged(t *testing.T) {
	g := newGuardTrap(t)
	// The honest fix touches only trap.go: nothing to report.
	g.write("trap.go", strings.Replace(readFileT(t, filepath.Join(g.dir, "trap.go")), "i < n", "i <= n", 1))
	g.run("commit", "-qam", "fix")
	if res := g.check(); res.HasFindings() || !res.HasGuard || len(res.ProtectedChanged) != 0 {
		t.Fatalf("honest fix flagged: %+v", res)
	}

	// The dishonest one: the protected test edited and committed, plus an
	// uncommitted new test file in a subdirectory.
	g.write("trap_test.go", "package trap\n")
	g.run("commit", "-qam", "make the test agree")
	g.write("pkg/extra_test.go", "package pkg\n")
	res := g.check()
	if want := []string{"pkg/extra_test.go", "trap_test.go"}; !reflect.DeepEqual(res.ProtectedChanged, want) {
		t.Fatalf("ProtectedChanged = %q, want %q (diff error %q)", res.ProtectedChanged, want, res.DiffError)
	}
	if !res.Flagged() || res.Base != g.base {
		t.Fatalf("flagged=%v base=%q", res.Flagged(), res.Base)
	}
	if s := res.Summary(); !strings.Contains(s, "The run changed protected paths: pkg/extra_test.go, trap_test.go.") {
		t.Fatalf("Summary = %q", s)
	}

	// An allow entry (an answered decision) takes a path back out.
	allowed, perr := mdfence.AddGuardAllow(g.lent, []string{"trap_test.go"})
	if perr != nil {
		t.Fatal(perr)
	}
	res = GuardCheck(worktree.ExecGit{}, GuardInput{DocPath: g.doc, LentDoc: allowed, Worktree: g.dir, Base: g.base})
	if want := []string{"pkg/extra_test.go"}; !reflect.DeepEqual(res.ProtectedChanged, want) {
		t.Fatalf("with allow: ProtectedChanged = %q, want %q", res.ProtectedChanged, want)
	}

	// No start point: reported, not guessed.
	res = GuardCheck(worktree.ExecGit{}, GuardInput{DocPath: g.doc, LentDoc: g.lent, Worktree: g.dir})
	if res.DiffError == "" || len(res.ProtectedChanged) != 0 {
		t.Fatalf("no base: %+v", res)
	}
}

func TestGuardCheckDocCopyMismatch(t *testing.T) {
	g := newGuardTrap(t)
	copyPath := filepath.Join(g.dir, worktree.LentPlanDocRel(g.doc))

	// What the contract asks of an executor is not a mismatch: a tick, the
	// Status line, a posterior forecast, a Completion Report.
	edited := strings.Replace(g.lent, "- [ ] `go test ./...` passes", "- [x] `go test ./...` passes", 1)
	edited = strings.Replace(edited, "Status: Pending", "Status: Done", 1)
	edited = strings.Replace(edited, "## Completion Report", "## Completion Report\n\nFixed the off-by-one in trap.go.", 1)
	if err := os.WriteFile(copyPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := g.check(); res.DocCopyMismatch {
		t.Fatalf("executor edits read as a mismatch: %+v", res)
	}

	// Rewriting the guard block is.
	tampered := strings.Replace(g.lent, `  - "**/*_test.go"`, `  - "nothing.go"`, 1)
	if err := os.WriteFile(copyPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	res := g.check()
	if !res.DocCopyMismatch || res.DocCopyMissing {
		t.Fatalf("tampered copy: %+v", res)
	}
	// The guard is still the LENT doc's, not the tampered copy's.
	if !reflect.DeepEqual(res.ProtectedPaths, []string{"**/*_test.go"}) {
		t.Fatalf("ProtectedPaths = %q", res.ProtectedPaths)
	}
	// Even after the copy was returned over the workspace doc.
	if err := os.WriteFile(g.doc, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := g.check(); !reflect.DeepEqual(res.ProtectedPaths, []string{"**/*_test.go"}) || !res.DocCopyMismatch {
		t.Fatalf("after return: %+v", res)
	}

	// A deleted copy, with a guard to protect, is a mismatch too.
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if res := g.check(); !res.DocCopyMismatch || !res.DocCopyMissing || res.Flagged() {
		t.Fatalf("missing copy: %+v", res)
	}
}

func TestGuardCheckGuardLog(t *testing.T) {
	g := newGuardTrap(t)
	g.write(GuardLogRel, "2026-10-22T18:00:00Z\tEdit\ttrap_test.go\tprotected_paths \"**/*_test.go\"\n"+
		"2026-10-22T18:00:05Z\tBash\t.swarmery/plan/phase-1-trap.md\timplicit \".swarmery/plan/**\"\n"+
		"\n"+
		"a line the hook never wrote\n")
	res := g.check()
	if len(res.GuardBlocks) != 3 || res.GuardBlocksTotal != 3 || res.MalformedLogLines != 1 {
		t.Fatalf("guard.log: %+v", res)
	}
	want := GuardLogLine{At: "2026-10-22T18:00:00Z", Tool: "Edit", Path: "trap_test.go", Reason: `protected_paths "**/*_test.go"`}
	if res.GuardBlocks[0] != want {
		t.Fatalf("line 1 = %+v", res.GuardBlocks[0])
	}
	if l := res.GuardBlocks[2]; !l.Malformed || l.Reason != "a line the hook never wrote" {
		t.Fatalf("malformed line = %+v", l)
	}
	if !res.Flagged() || len(res.ProtectedChanged) != 0 {
		t.Fatalf("a refused attempt alone must flag: %+v", res)
	}
	// The decision names the protected file, never the guard's own inputs.
	if got := res.Paths(); !reflect.DeepEqual(got, []string{"trap_test.go"}) {
		t.Fatalf("Paths = %q", got)
	}
	if s := res.Summary(); !strings.Contains(s, "refused 3 attempts") {
		t.Fatalf("Summary = %q", s)
	}
}

func TestGuardCheckNoGuardBlock(t *testing.T) {
	g := newGuardTrap(t)
	plain := mdfenceStripGuard(g.lent)
	if err := os.WriteFile(g.doc, []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.LendPlanDoc(g.dir, g.doc); err != nil {
		t.Fatal(err)
	}
	g.write("trap_test.go", "package trap\n") // would be protected, had the doc a guard
	res := GuardCheck(worktree.ExecGit{}, GuardInput{DocPath: g.doc, LentDoc: plain, Worktree: g.dir, Base: g.base})
	if res.HasGuard || res.HasFindings() || res.ParseError != nil || res.DiffError != "" ||
		len(res.ProtectedChanged) != 0 || len(res.GuardBlocks) != 0 || res.Summary() != "" {
		t.Fatalf("no guard block: %+v", res)
	}

	// An unparsable block is reported, not fatal.
	broken := strings.Replace(g.lent, `  - "**/*_test.go"`, `  - **/*_test.go`, 1)
	res = GuardCheck(worktree.ExecGit{}, GuardInput{DocPath: g.doc, LentDoc: broken, Worktree: g.dir, Base: g.base})
	if res.ParseError == nil || res.ParseError.Kind != mdfence.ErrUnparsable || !res.HasFindings() || res.Flagged() {
		t.Fatalf("unparsable guard: %+v", res)
	}
	if !strings.Contains(res.Summary(), "guard block is unreadable") {
		t.Fatalf("Summary = %q", res.Summary())
	}
}

// mdfenceStripGuard drops the doc's `## Guard` section.
func mdfenceStripGuard(doc string) string {
	i := strings.Index(doc, "## Guard")
	j := strings.Index(doc, "## Forecast")
	return doc[:i] + doc[j:]
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
