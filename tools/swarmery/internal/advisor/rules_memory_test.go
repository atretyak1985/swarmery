package advisor

// agent-memory phase 3 — R10 tests. Three fixture indexes drive the two
// triggers and the healthy case, plus the persistence path (which is what
// proves migration 0071 widened the target_kind CHECK to accept 'memory').

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
)

// r10Fixture redirects the auto-memory resolver at a temp claude dir and writes
// index content for project 1 (/work/alpha). Returns the memory dir.
func r10Fixture(t *testing.T, index string) string {
	t.Helper()
	claude := t.TempDir()
	prev := memconsolidate.ClaudeDir()
	memconsolidate.SetClaudeDir(claude)
	t.Cleanup(func() { memconsolidate.SetClaudeDir(prev) })

	dir := memconsolidate.AutoMemoryDirIn(claude, "/work/alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(memconsolidate.IndexPath(dir), []byte(index), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	return dir
}

// indexLines builds n entries, the first `closed` of them carrying a closed
// marker and the rest an open tail. pad widens each hook so the byte-budget
// trigger can be driven independently of the line count.
func indexLines(n, closed, pad int) string {
	var b strings.Builder
	filler := strings.Repeat("x", pad)
	for i := 0; i < n; i++ {
		tail := "impl open."
		if i < closed {
			tail = "DONE 2026-08-01."
		}
		fmt.Fprintf(&b, "- [Topic %02d](topic-%02d.md) — %s %s\n", i, i, tail, filler)
	}
	return b.String()
}

func TestR10FiresOnClosedShare(t *testing.T) {
	db := testDB(t)
	r10Fixture(t, indexLines(12, 9, 0)) // 9/12 = 75 % closed, well under 6 KB

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.rule != "R10" || f.targetKind != "memory" || f.target != "-work-alpha" {
		t.Fatalf("finding identity = %s/%s/%s, want R10/memory/-work-alpha", f.rule, f.targetKind, f.target)
	}
	if !strings.Contains(f.detail, "9/12 lines closed (75%)") {
		t.Fatalf("detail does not carry the counts: %q", f.detail)
	}
	counts, ok := f.evidence["counts"].(map[string]int)
	if !ok || counts["closed_lines"] != 9 || counts["index_lines"] != 12 {
		t.Fatalf("evidence counts = %+v", f.evidence["counts"])
	}
}

func TestR10FiresOnIndexBytes(t *testing.T) {
	db := testDB(t)
	// 20 lines, only 2 closed (10 % — under the share trigger), but padded past
	// the 6 KiB budget: size alone must fire.
	index := indexLines(20, 2, 320)
	if len(index) <= R10IndexBytes {
		t.Fatalf("fixture is only %d bytes, needs > %d", len(index), R10IndexBytes)
	}
	r10Fixture(t, index)

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1 (byte budget): %+v", len(fs), fs)
	}
	if !strings.Contains(fs[0].detail, "2/20 lines closed (10%)") {
		t.Fatalf("detail = %q", fs[0].detail)
	}
}

func TestR10DoesNotFireOnAHealthyIndex(t *testing.T) {
	db := testDB(t)
	r10Fixture(t, indexLines(12, 3, 0)) // 25 % closed, tiny

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none", fs)
	}
}

// A short index that is mostly closed is noise, not a budget problem.
func TestR10DoesNotFireBelowMinLines(t *testing.T) {
	db := testDB(t)
	r10Fixture(t, indexLines(6, 6, 0)) // 100 % closed but only 6 lines

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none (under R10MinLines)", fs)
	}
}

// A project with no auto-memory directory at all is the healthy state.
func TestR10SkipsProjectsWithoutAnIndex(t *testing.T) {
	db := testDB(t)
	claude := t.TempDir()
	prev := memconsolidate.ClaudeDir()
	memconsolidate.SetClaudeDir(claude)
	t.Cleanup(func() { memconsolidate.SetClaudeDir(prev) })

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none", fs)
	}
}

func TestR10ArchivedProjectsAreSkipped(t *testing.T) {
	db := testDB(t)
	r10Fixture(t, indexLines(12, 9, 0))
	mustExec(t, db, `UPDATE projects SET archived = 1 WHERE id = 1`)

	fs, err := r10MemoryIndex(db, evalWindow())
	if err != nil {
		t.Fatalf("r10MemoryIndex: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none for an archived project", fs)
	}
}

// TestR10PersistsThroughRun is the CHECK-constraint proof: before migration
// 0071 widened recommendations.target_kind, this upsert failed and took the
// whole advisor pass down with it.
func TestR10PersistsThroughRun(t *testing.T) {
	db := testDB(t)
	r10Fixture(t, indexLines(12, 9, 0))

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE rule = 'R10' AND target_kind = 'memory'`); n != 1 {
		t.Fatalf("persisted R10 rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE dedup_key = 'R10:-work-alpha'`); n != 1 {
		t.Fatalf("dedup_key R10:-work-alpha rows = %d, want 1", n)
	}
	// A second pass refreshes in place — never a duplicate row.
	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE rule = 'R10'`); n != 1 {
		t.Fatalf("after a second pass R10 rows = %d, want 1", n)
	}
}

// TestR10MetricValue: the baseline snapshot is the closed share, and lower is
// better (relImprovement's default direction), so consolidating improves it.
func TestR10MetricValue(t *testing.T) {
	db := testDB(t)
	dir := r10Fixture(t, indexLines(12, 9, 0))

	name, v, ok, err := metricValue(db, "R10", "-work-alpha", evalWindow())
	if err != nil || !ok {
		t.Fatalf("metricValue: name=%s v=%v ok=%v err=%v", name, v, ok, err)
	}
	if name != "memory_index_closed_share" {
		t.Fatalf("metric name = %q", name)
	}
	if v != 0.75 {
		t.Fatalf("metric value = %v, want 0.75", v)
	}
	if imp := relImprovement("R10", v, 0.25); imp <= 0 {
		t.Fatalf("relImprovement(0.75 → 0.25) = %v, want positive (lower closed share is better)", imp)
	}

	// An index that vanished reports ok=false: absence of data never verifies.
	if err := os.Remove(filepath.Join(dir, "MEMORY.md")); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if _, _, ok, err := metricValue(db, "R10", "-work-alpha", evalWindow()); ok || err != nil {
		t.Fatalf("metricValue after removal: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	// An unknown target likewise.
	if _, _, ok, err := metricValue(db, "R10", "-work-nonexistent", evalWindow()); ok || err != nil {
		t.Fatalf("metricValue for an unknown slug: ok=%v err=%v", ok, err)
	}
}

func TestR10IsSelfChecking(t *testing.T) {
	if !selfCheckingRules["R10"] {
		t.Fatal("R10 must be self-checking: it re-reads the index every pass, so not firing means the index is fine")
	}
}
