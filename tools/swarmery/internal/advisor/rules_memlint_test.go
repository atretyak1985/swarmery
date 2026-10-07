package advisor

// memory-engineering phase 1 — R13 tests. The fixture project row is pointed at
// a throwaway git repository (the merge commits live there), and its auto-memory
// directory under a redirected claude dir carries the shared stale lines.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint/memlinttest"
)

// seedR13 creates a temp git repo carrying the #366 merge and #224 squash
// commits, redirects the auto-memory resolver at a temp claude dir (restored on
// cleanup) and writes the shared fixture lines into the repo's memory dir.
// Returns the memory dir and the repo path.
func seedR13(t *testing.T) (dir, repo string) {
	t.Helper()
	repo = memlinttest.Repo(t)
	claude := t.TempDir()
	prev := memconsolidate.ClaudeDir()
	memconsolidate.SetClaudeDir(claude)
	t.Cleanup(func() { memconsolidate.SetClaudeDir(prev) })
	dir = memconsolidate.AutoMemoryDirIn(claude, repo)
	memlinttest.WriteLines(t, dir)
	return dir, repo
}

func TestR13FiresOnStaleClaims(t *testing.T) {
	db := testDB(t)
	dir, repo := seedR13(t)
	mustExec(t, db, `UPDATE projects SET path = ? WHERE id = 1`, repo)

	fs, err := r13StaleMemory(db, evalWindow())
	if err != nil {
		t.Fatalf("r13StaleMemory: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.rule != "R13" || f.targetKind != "memory" || f.target != "-work-alpha" {
		t.Fatalf("finding identity = %s/%s/%s, want R13/memory/-work-alpha", f.rule, f.targetKind, f.target)
	}
	if !strings.Contains(f.detail, "model-lineup.md:3 says PR #366 is open; it merged ") ||
		!strings.Contains(f.detail, "opus5.md:3 says PR #224 is open; it merged ") {
		t.Fatalf("detail does not name both findings: %q", f.detail)
	}
	counts, ok := f.evidence["counts"].(map[string]int)
	if !ok || counts["claims"] != 4 || counts["stale"] != 2 {
		t.Fatalf("evidence counts = %+v", f.evidence["counts"])
	}
	found, ok := f.evidence["findings"].([]memlint.Finding)
	if !ok || len(found) != 2 {
		t.Fatalf("evidence findings = %+v", f.evidence["findings"])
	}
	if ip, _ := f.evidence["index_path"].(string); ip != filepath.Join(dir, "MEMORY.md") {
		t.Fatalf("evidence index_path = %q", ip)
	}
}

// Rewriting the two stale lines makes the rule go quiet — which, R13 being
// self-checking, is what closes the row.
func TestR13IsAbsentOnceTheLinesAreFixed(t *testing.T) {
	db := testDB(t)
	dir, repo := seedR13(t)
	mustExec(t, db, `UPDATE projects SET path = ? WHERE id = 1`, repo)

	fix := func(name, from, to string) {
		t.Helper()
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(data), from, to, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fix("model-lineup.md", "PR #366 UNMERGED (needs a review approval)", "PR #366 MERGED 2026-09-22")
	fix("opus5.md", "PR #224 open 2026-08-11", "PR #224 MERGED 2026-08-14")

	fs, err := r13StaleMemory(db, evalWindow())
	if err != nil {
		t.Fatalf("r13StaleMemory: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none after the fix", fs)
	}
}

// A project whose path is not a git repository has claims it cannot verify,
// and that is not a finding.
func TestR13DoesNotFireWithoutGit(t *testing.T) {
	db := testDB(t) // path /work/alpha has no .git
	claude := t.TempDir()
	prev := memconsolidate.ClaudeDir()
	memconsolidate.SetClaudeDir(claude)
	t.Cleanup(func() { memconsolidate.SetClaudeDir(prev) })
	memlinttest.WriteLines(t, memconsolidate.AutoMemoryDirIn(claude, "/work/alpha"))

	fs, err := r13StaleMemory(db, evalWindow())
	if err != nil {
		t.Fatalf("r13StaleMemory: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none without a .git to verify against", fs)
	}
}

func TestR13SkipsProjectsWithoutMemoryAndArchivedOnes(t *testing.T) {
	db := testDB(t)
	claude := t.TempDir()
	prev := memconsolidate.ClaudeDir()
	memconsolidate.SetClaudeDir(claude)
	t.Cleanup(func() { memconsolidate.SetClaudeDir(prev) })

	fs, err := r13StaleMemory(db, evalWindow())
	if err != nil || len(fs) != 0 {
		t.Fatalf("no memory dir: findings=%+v err=%v, want none", fs, err)
	}

	_, repo := seedR13(t)
	mustExec(t, db, `UPDATE projects SET path = ?, archived = 1 WHERE id = 1`, repo)
	fs, err = r13StaleMemory(db, evalWindow())
	if err != nil || len(fs) != 0 {
		t.Fatalf("archived: findings=%+v err=%v, want none", fs, err)
	}
}

func TestR13PersistsThroughRun(t *testing.T) {
	db := testDB(t)
	_, repo := seedR13(t)
	mustExec(t, db, `UPDATE projects SET path = ? WHERE id = 1`, repo)

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE rule = 'R13' AND target_kind = 'memory' AND dedup_key = 'R13:-work-alpha'`); n != 1 {
		t.Fatalf("persisted R13 rows = %d, want 1", n)
	}
	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE rule = 'R13'`); n != 1 {
		t.Fatalf("after a second pass R13 rows = %d, want 1", n)
	}
}

func TestR13MetricValue(t *testing.T) {
	db := testDB(t)
	dir, repo := seedR13(t)
	mustExec(t, db, `UPDATE projects SET path = ? WHERE id = 1`, repo)

	name, v, ok, err := metricValue(db, "R13", "-work-alpha", evalWindow())
	if err != nil || !ok {
		t.Fatalf("metricValue: name=%s v=%v ok=%v err=%v", name, v, ok, err)
	}
	if name != "memory_stale_claims" || v != 2 {
		t.Fatalf("metric = %s %v, want memory_stale_claims 2", name, v)
	}
	if imp := relImprovement("R13", v, 0); imp <= 0 {
		t.Fatalf("relImprovement(2 → 0) = %v, want positive (fewer stale claims is better)", imp)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := metricValue(db, "R13", "-work-alpha", evalWindow()); ok || err != nil {
		t.Fatalf("metricValue after the dir vanished: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if _, _, ok, err := metricValue(db, "R13", "-work-nonexistent", evalWindow()); ok || err != nil {
		t.Fatalf("metricValue for an unknown slug: ok=%v err=%v", ok, err)
	}
}

func TestR13IsSelfChecking(t *testing.T) {
	if !selfCheckingRules["R13"] {
		t.Fatal("R13 must be self-checking: it re-reads the memory dir and git every pass, so not firing means the lines were fixed")
	}
}

func TestR13DetailCapsAtThree(t *testing.T) {
	rep := memlint.Report{}
	for i := 1; i <= 5; i++ {
		rep.Findings = append(rep.Findings, memlint.Finding{File: "f.md", LineNo: i, PR: i, MergeSHA: "0123456789abcdef", MergedAt: "2026-09-01T00:00:00Z"})
	}
	d := r13Detail(rep)
	if !strings.Contains(d, "f.md:3 says PR #3") || strings.Contains(d, "f.md:4 says") || !strings.Contains(d, "(+2 more)") {
		t.Fatalf("detail = %q", d)
	}
}
