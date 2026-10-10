package phaserun

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// planRepo is a real git repository holding a two-phase plan's work: a base
// commit, swarm/phase-1 with one file, and swarm/phase-2 stacked on it with a
// second. git runs one command in it and returns the trimmed output.
type planRepo struct {
	t    *testing.T
	dir  string
	base string
}

func (r planRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r planRepo) commitFile(name, body, msg string) string {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(r.dir, name)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", "--", name)
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func newPlanRepo(t *testing.T) planRepo {
	t.Helper()
	r := planRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.base = r.commitFile("README", "base\n", "base")
	r.git("checkout", "-q", "-b", "swarm/phase-1")
	r.commitFile("store/schema.sql", "CREATE TABLE x (id INTEGER);\n", "phase 1: schema")
	r.git("checkout", "-q", "-b", "swarm/phase-2")
	r.commitFile("api/x.go", "package api // reads x.id\n", "phase 2: api")
	r.git("checkout", "-q", "main")
	return r
}

// planReviewFixture: the two-phase fixture whose project IS a real repo, both
// phases stamped with run branches and start points, phase 1 finished in the
// store, phase 2 ticked on disk (its stamp is the trigger). Review is wired to a
// fake reviewer; the throwaway worktree is the stub's, fingerprinted constant.
func planReviewFixture(t *testing.T) (*Service, *sql.DB, int64, int64, int64, string, planRepo, *fakeReviewer, *stubWt) {
	t.Helper()
	db, taskID, p1, p2 := fixture(t)
	repo := newPlanRepo(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	tip1 := repo.git("rev-parse", "swarm/phase-1")
	mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-1', run_start_point=?, run_state='done',
		checkboxes_done=2 WHERE id=?`, repo.base, p1)
	mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-2', run_start_point=?, run_state='done'
		WHERE id=?`, tip1, p2)

	var doc2 string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p2).Scan(&doc2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc2, []byte("# Phase 2 — UI\n\n- [x] c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(doc2), "README.md"), []byte("# My Epic\n\nPhase 1 adds x; phase 2 reads it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	rv := &fakeReviewer{outputs: []string{"- P1 api/x.go:1 — reads x.id, which phase 1 never indexes.\nVERDICT: FAIL"}}
	s.Review = rv
	s.treeFingerprint = func(string) (string, error) { return "tree-plan", nil }
	return s, db, taskID, p1, p2, doc2, repo, rv, wt
}

type planReviewRow struct {
	phaseID                    sql.NullInt64
	taskID, key, verdict       string
	detail, findings, treeHash string
}

func planReviewRows(t *testing.T, db *sql.DB) []planReviewRow {
	t.Helper()
	rows, err := db.Query(`SELECT phase_id, workspace_task_id, run_session_uuid, verdict, detail, findings, tree_before
		FROM phase_reviews WHERE scope='plan' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []planReviewRow
	for rows.Next() {
		var r planReviewRow
		if err := rows.Scan(&r.phaseID, &r.taskID, &r.key, &r.verdict, &r.detail, &r.findings, &r.treeHash); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// Stamping the plan's last phase `done` starts exactly one plan branch review
// over both run branches, in a throwaway worktree with the phase reviewer's tool
// denial, and records it as a scope='plan' row. Re-stamping the same tips starts
// nothing; a moved tip starts a new review.
func TestPlanReviewFiresOnceWhenTheLastPhaseCompletes(t *testing.T) {
	s, db, taskID, _, p2, doc2, repo, rv, wt := planReviewFixture(t)

	s.stamp(p2, doc2, "done", "")

	calls := rv.calls()
	if len(calls) != 1 {
		t.Fatalf("plan reviews spawned = %d, want 1", len(calls))
	}
	spec := calls[0]
	if got := strings.Join(verify.ToolDenyArgs(spec.DisallowedTools), " "); got != "--disallowedTools Edit,Write,MultiEdit,NotebookEdit,Bash" {
		t.Errorf("plan reviewer denial = %q", got)
	}
	if spec.Model != verify.DefaultModel {
		t.Errorf("plan reviewer model = %q, want %q", spec.Model, verify.DefaultModel)
	}
	wantCwd := fmt.Sprintf("/wt/p/planreview-%d", taskID)
	if spec.Cwd != wantCwd {
		t.Errorf("plan reviewer cwd = %q, want the throwaway worktree %q", spec.Cwd, wantCwd)
	}
	for _, want := range []string{
		"VERDICT: PASS | FAIL | INCONCLUSIVE", // the phase reviewer's contract
		"focus on what phase N hands over",    // the seam focus
		"=== PHASE 1 — Phase 1 — Schema · branch swarm/phase-1",
		"=== PHASE 2 — Phase 2 — UI · branch swarm/phase-2",
		"CREATE TABLE x (id INTEGER);",     // phase 1's diff
		"package api // reads x.id",        // phase 2's diff
		"Phase 1 adds x; phase 2 reads it", // the plan README
	} {
		if !strings.Contains(strings.ToLower(spec.Prompt), strings.ToLower(want)) {
			t.Errorf("plan review prompt lacks %q", want)
		}
	}
	// Phase 2's range starts at phase 1's tip: phase 1's schema must not be
	// counted again as phase 2's work.
	if !strings.Contains(spec.Prompt, "swarm/phase-2 in "+repo.dir+": 1 file(s).") {
		t.Errorf("phase 2's range is not <its start point>...<its branch>:\n%s", spec.Prompt)
	}

	rows := planReviewRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("scope=plan rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.phaseID.Valid {
		t.Errorf("plan review phase_id = %d, want NULL", r.phaseID.Int64)
	}
	if r.taskID != fmt.Sprint(taskID) || r.verdict != "fail" || !strings.Contains(r.findings, "api/x.go:1") || r.treeHash != "tree-plan" {
		t.Errorf("plan review row = %+v", r)
	}
	if !strings.HasPrefix(r.key, planReviewKeyPrefix) {
		t.Errorf("dedupe key %q lacks the %s prefix", r.key, planReviewKeyPrefix)
	}
	// The throwaway worktree is removed WITH its branch; nothing else is.
	wt.mu.Lock()
	removed, keep := append([]string(nil), func() []string {
		var p []string
		for _, a := range wt.removed {
			p = append(p, a.Path)
		}
		return p
	}()...), append([]bool(nil), wt.keepBranch...)
	wt.mu.Unlock()
	if len(removed) != 1 || removed[0] != wantCwd || keep[0] {
		t.Errorf("worktree removals = %v keepBranch=%v, want the throwaway removed with its branch", removed, keep)
	}
	// Advisory: no executor run was started, no doc was touched.
	if n := s.Run.(*stubRunner).specCount(); n != 0 {
		t.Errorf("executor runs started = %d, want 0", n)
	}
	if b, _ := os.ReadFile(doc2); strings.Contains(string(b), "Review findings") {
		t.Error("the plan review wrote into a phase doc")
	}

	// Same branches, same tips ⇒ no second review.
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews after re-stamping the same tips = %d, want 1", n)
	}

	// A moved tip (a fix commit on phase 2) ⇒ a new review under a new key.
	repo.git("checkout", "-q", "swarm/phase-2")
	repo.commitFile("api/x.go", "package api // reads x.id, now indexed\n", "phase 2: fix")
	repo.git("checkout", "-q", "main")
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 2 {
		t.Fatalf("plan reviews after the tip moved = %d, want 2", n)
	}
	rows = planReviewRows(t, db)
	if len(rows) != 2 || rows[0].key == rows[1].key {
		t.Errorf("rows after the tip moved = %+v, want two rows with different keys", rows)
	}
}

// A plan that is not finished starts nothing, and neither does a state other
// than done.
func TestPlanReviewWaitsForEveryPhase(t *testing.T) {
	s, db, _, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=1 WHERE id=?`, p1) // 1 of 2

	s.stamp(p2, doc2, "done", "")
	s.stamp(p2, doc2, "partial", "")
	if n := len(rv.calls()); n != 0 {
		t.Fatalf("plan reviews with phase 1 unfinished = %d, want 0", n)
	}

	// `Status: done` in phase 1's doc finishes it without its criteria; a
	// `partial` stamp still does not trigger.
	mustExec(t, db, `UPDATE epic_phases SET doc_status='done' WHERE id=?`, p1)
	s.stamp(p2, doc2, "partial", "")
	if n := len(rv.calls()); n != 0 {
		t.Fatalf("plan reviews on a partial stamp = %d, want 0", n)
	}
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews once phase 1's doc says done = %d, want 1", n)
	}
	if len(planReviewRows(t, db)) != 1 {
		t.Error("no scope=plan row recorded")
	}
}

// More than planReviewMaxFiles changed files in total ⇒ an inconclusive
// `not-verifiable: <n> files` row, no reviewer, no worktree.
func TestPlanReviewFileBoundIsNotVerifiable(t *testing.T) {
	s, db, _, _, p2, doc2, repo, rv, wt := planReviewFixture(t)
	repo.git("checkout", "-q", "swarm/phase-2")
	for i := 0; i < planReviewMaxFiles; i++ {
		name := filepath.Join(repo.dir, "gen", fmt.Sprintf("f%03d.txt", i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo.git("add", "gen")
	repo.git("commit", "-q", "-m", "phase 2: generated")
	repo.git("checkout", "-q", "main")

	s.stamp(p2, doc2, "done", "")

	if n := len(rv.calls()); n != 0 {
		t.Fatalf("reviewer spawned %d time(s) over the file bound", n)
	}
	if n := wt.acquiredCount(); n != 0 {
		t.Errorf("worktrees acquired = %d over the file bound, want 0", n)
	}
	rows := planReviewRows(t, db)
	// phase 1: 1 file; phase 2: api/x.go + 300 generated.
	if len(rows) != 1 || rows[0].verdict != "inconclusive" || rows[0].detail != "not-verifiable: 302 files" {
		t.Fatalf("rows = %+v, want one inconclusive `not-verifiable: 302 files`", rows)
	}
	// The bound is recorded under the key too: the same tips are not retried.
	s.stamp(p2, doc2, "done", "")
	if n := len(planReviewRows(t, db)); n != 1 {
		t.Errorf("rows after re-stamping = %d, want 1", n)
	}
}

// A reviewer that changes the throwaway worktree voids its verdict.
func TestPlanReviewMutatedTreeIsInconclusive(t *testing.T) {
	s, db, _, _, p2, doc2, _, rv, _ := planReviewFixture(t)
	n := 0
	s.treeFingerprint = func(string) (string, error) {
		n++
		return fmt.Sprintf("tree-%d", n), nil
	}
	rv.outputs = []string{"VERDICT: PASS"}

	s.stamp(p2, doc2, "done", "")

	rows := planReviewRows(t, db)
	if len(rows) != 1 || rows[0].verdict != "inconclusive" || !strings.HasPrefix(rows[0].detail, ClassReviewerMutatedTree) {
		t.Fatalf("rows = %+v, want one inconclusive %s", rows, ClassReviewerMutatedTree)
	}
}

// Without a wired reviewer the hook does nothing at all.
func TestPlanReviewOffWithoutReviewer(t *testing.T) {
	s, db, _, _, p2, doc2, _, _, wt := planReviewFixture(t)
	s.Review = nil
	s.stamp(p2, doc2, "done", "")
	if len(planReviewRows(t, db)) != 0 || wt.acquiredCount() != 0 {
		t.Error("the plan review ran without a wired reviewer")
	}
}

// Two stamps racing for one branch set: the second claim loses while the first is
// in flight (its row is written only when the reviewer exits), and wins again once
// released with no row recorded.
func TestPlanReviewClaimIsSingleFlight(t *testing.T) {
	s, _, taskID, _, _, _, _, _, _ := planReviewFixture(t)
	if !s.claimPlanReview(taskID, "branchset:k") {
		t.Fatal("first claim refused")
	}
	if s.claimPlanReview(taskID, "branchset:k") {
		t.Fatal("second claim of an in-flight key granted")
	}
	if !s.claimPlanReview(taskID, "branchset:other") {
		t.Error("a different key was refused")
	}
	s.releasePlanReview("branchset:k")
	if !s.claimPlanReview(taskID, "branchset:k") {
		t.Error("a released key with no recorded row was refused")
	}
}

// The key depends on the set of branch tips, not on their order.
func TestPlanBranchKeyIsOrderFree(t *testing.T) {
	a := []planBranch{{Branch: "swarm/phase-1", Tip: "aa"}, {Branch: "swarm/phase-2", Tip: "bb"}}
	b := []planBranch{a[1], a[0]}
	if planBranchKey(a) != planBranchKey(b) {
		t.Error("the key depends on the order of the branches")
	}
	c := []planBranch{a[0], {Branch: "swarm/phase-2", Tip: "cc"}}
	if planBranchKey(a) == planBranchKey(c) {
		t.Error("a moved tip kept the key")
	}
}
