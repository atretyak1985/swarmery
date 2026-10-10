package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
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
	branches, err := s.planBranches(taskID)
	if err != nil {
		t.Fatal(err)
	}
	key := planBranchKey(branches)
	wantCwd := fmt.Sprintf("/wt/p/planreview-%d-%s", taskID, strings.TrimPrefix(key, planReviewKeyPrefix)[:planReviewNameKeyLen])
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
	if r.key != key {
		t.Errorf("dedupe key = %q, want the branch-set key %q", r.key, key)
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

// The claim is per plan, not per key: while a plan's review is in flight a second
// claim loses under ANY key and is left behind as the pending trigger (the latest
// wins), another plan still claims, and the holder hands the pending trigger over
// before it releases the slot.
func TestPlanReviewClaimIsSingleFlight(t *testing.T) {
	s, _, taskID, _, p2, doc2, _, _, _ := planReviewFixture(t)
	first := planReviewTrigger{phaseID: p2, docPath: doc2}
	if !s.claimPlanReview(taskID, "branchset:k", first) {
		t.Fatal("first claim refused")
	}
	if s.claimPlanReview(taskID, "branchset:k", first) {
		t.Fatal("second claim of an in-flight key granted")
	}
	if s.claimPlanReview(taskID, "branchset:other", planReviewTrigger{phaseID: 98}) {
		t.Fatal("a different key of the same plan was granted while its review is in flight")
	}
	if s.claimPlanReview(taskID, "branchset:newest", planReviewTrigger{phaseID: 99}) {
		t.Fatal("a third key of the same plan was granted while its review is in flight")
	}
	if !s.claimPlanReview(taskID+1, "branchset:other", first) {
		t.Error("another plan's review was refused")
	}
	next, more := s.nextPlanReview(taskID)
	if !more || next.phaseID != 99 {
		t.Fatalf("handed-over trigger = %+v more=%v, want the latest (phase 99)", next, more)
	}
	if _, more := s.nextPlanReview(taskID); more {
		t.Fatal("a second hand-over with no new trigger")
	}
	if !s.claimPlanReview(taskID, "branchset:k", first) {
		t.Error("a released plan with no recorded row was refused")
	}
}

// Tips moving while the plan review runs (the last phase's own review FAILed and
// its fix re-run stamped `done` again): the second trigger does not start a review
// beside the running one, and once that one finishes the NEW set of tips is
// reviewed, in a worktree of its own, and recorded under its own key.
func TestPlanReviewTipsMovedMidReviewAreReviewedAfter(t *testing.T) {
	s, db, taskID, _, p2, doc2, repo, rv, wt := planReviewFixture(t)
	rv.outputs = []string{"- P1 api/x.go:1 — seam.\nVERDICT: FAIL", "VERDICT: PASS"}
	during := -1
	rv.onRun = func(verify.RunSpec) {
		if len(rv.calls()) != 1 {
			return
		}
		repo.git("checkout", "-q", "swarm/phase-2")
		repo.commitFile("api/x.go", "package api // reads x.id, fixed\n", "phase 2: review fix")
		repo.git("checkout", "-q", "main")
		s.stamp(p2, doc2, "done", "") // the fix re-run's stamp, mid-review
		during = len(rv.calls())
	}

	s.stamp(p2, doc2, "done", "")

	if during != 1 {
		t.Fatalf("reviews started while the first ran = %d, want 1 (no second review beside it)", during)
	}
	calls := rv.calls()
	if len(calls) != 2 {
		t.Fatalf("plan reviews = %d, want 2 (the moved tips reviewed after the first)", len(calls))
	}
	if calls[0].Cwd == calls[1].Cwd {
		t.Errorf("both reviews used worktree %q", calls[0].Cwd)
	}
	branches, err := s.planBranches(taskID)
	if err != nil {
		t.Fatal(err)
	}
	final := planBranchKey(branches)
	rows := planReviewRows(t, db)
	if len(rows) != 2 || rows[0].key == final || rows[1].key != final || rows[1].verdict != "pass" {
		t.Fatalf("rows = %+v, want the first set then the final set %q (pass)", rows, final)
	}
	if n := wt.acquiredCount(); n != 2 {
		t.Errorf("worktrees acquired = %d, want 2", n)
	}
	// The final set is recorded: re-stamping it starts nothing.
	rv.onRun = nil
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 2 {
		t.Errorf("plan reviews after re-stamping the final tips = %d, want 2", n)
	}
}

// A trigger over the SAME tips while the review runs is not reviewed twice.
func TestPlanReviewSameTipsMidReviewRunsOnce(t *testing.T) {
	s, db, _, _, p2, doc2, _, rv, _ := planReviewFixture(t)
	rv.onRun = func(verify.RunSpec) {
		if len(rv.calls()) == 1 {
			s.stamp(p2, doc2, "done", "")
		}
	}
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews = %d, want 1", n)
	}
	if n := len(planReviewRows(t, db)); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// A transient failure (no review worktree) is recorded without the dedupe key, so
// the next trigger over the same tips reviews them; a settled verdict then dedupes.
func TestPlanReviewTransientFailureIsRetried(t *testing.T) {
	s, db, _, _, p2, doc2, _, rv, wt := planReviewFixture(t)
	wt.mu.Lock()
	wt.acquireErr = errors.New("worktree root is gone")
	wt.mu.Unlock()

	s.stamp(p2, doc2, "done", "")

	rows := planReviewRows(t, db)
	if len(rows) != 1 || rows[0].verdict != "inconclusive" || rows[0].key != "" ||
		!strings.HasPrefix(rows[0].detail, classReviewerNotStarted+":") {
		t.Fatalf("rows = %+v, want one inconclusive %s row with no key", rows, classReviewerNotStarted)
	}
	if n := len(rv.calls()); n != 0 {
		t.Fatalf("reviewer spawned %d time(s) without a worktree", n)
	}

	wt.mu.Lock()
	wt.acquireErr = nil
	wt.mu.Unlock()
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews after the transient failure = %d, want 1 (retried)", n)
	}
	rows = planReviewRows(t, db)
	if len(rows) != 2 || rows[1].key == "" || rows[1].verdict != "fail" {
		t.Fatalf("rows = %+v, want the retry recorded under the key", rows)
	}
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Errorf("plan reviews after a settled verdict = %d, want 1 (deduped)", n)
	}
}

// A sibling phase whose run is still in flight keeps the plan unfinished: its own
// `done` stamp is the trigger that sees the final tips.
func TestPlanReviewWaitsForARunningSibling(t *testing.T) {
	s, db, _, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running' WHERE id=?`, p1)
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 0 {
		t.Fatalf("plan reviews with phase 1 still running = %d, want 0", n)
	}
	mustExec(t, db, `UPDATE epic_phases SET run_state='done' WHERE id=?`, p1)
	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews once phase 1 finished = %d, want 1", n)
	}
}

// Two reviews of one plan under different keys never share a worktree name.
func TestPlanReviewNameCarriesTheKey(t *testing.T) {
	a := planReviewName(7, planReviewKeyPrefix+"0123456789abcdef")
	b := planReviewName(7, planReviewKeyPrefix+"fedcba9876543210")
	if a != "planreview-7-0123456789ab" || a == b {
		t.Errorf("names = %q, %q", a, b)
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

// A panic inside a plan review must not keep the plan's slot: the next stamp
// has to be able to claim again (the spawner recovers the panic in production).
func TestPlanReviewSlotReleasedOnPanic(t *testing.T) {
	s, _, taskID, _, p2, doc2, _, _, _ := planReviewFixture(t)
	s.Review = panickingReviewer{}
	func() {
		defer func() { _ = recover() }()
		s.planBranchReview(p2, doc2)
	}()
	if !s.claimPlanReview(taskID, "branchset:after-panic", planReviewTrigger{phaseID: p2, docPath: doc2}) {
		t.Fatal("plan review slot still held after a panic")
	}
}

// panickingReviewer stands in for a reviewer whose spawn blows up.
type panickingReviewer struct{}

func (panickingReviewer) Run(context.Context, verify.RunSpec) (*verify.Run, error) {
	panic("reviewer exploded")
}

// isPlanReview tells the plan branch review's spec from a phase review's.
func isPlanReview(spec verify.RunSpec) bool {
	return strings.Contains(spec.Prompt, "THIS IS A PLAN BRANCH REVIEW")
}

// startLastPhase runs the plan's last phase (p2) for real through Start. The run
// branch Start names for it is the fixture's swarm/phase-2, which already exists
// in the plan repo. onRun, when set, is the executor; it gets the 1-based run
// number and the branch.
func startLastPhase(t *testing.T, s *Service, p2 int64, repo planRepo, onRun func(n int, branch string)) string {
	t.Helper()
	branch := "swarm/" + runcore.PhaseTaskName(p2)
	repo.git("rev-parse", "--verify", "refs/heads/"+branch)
	n := 0
	s.Run.(*stubRunner).runFn = func(spec RunSpec) (*Run, error) {
		n++
		if onRun != nil {
			onRun(n, branch)
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	if _, err := s.Start(p2, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return branch
}

// reviewKinds names each reviewer call "phase" or "plan", in order.
func reviewKinds(rv *fakeReviewer) string {
	var kinds []string
	for _, c := range rv.calls() {
		if isPlanReview(c) {
			kinds = append(kinds, "plan")
		} else {
			kinds = append(kinds, "phase")
		}
	}
	return strings.Join(kinds, ",")
}

// A `**Review:** on` last phase whose review FAILs once and PASSes on the fix
// re-run gets exactly ONE plan branch review, after the fix re-run's review, over
// the post-fix tip. stamp's own hook would have reviewed the rejected tip first and
// the fixed one again.
func TestPlanReviewWaitsForTheLastPhaseReviewAndItsFix(t *testing.T) {
	s, db, taskID, _, p2, _, repo, rv, _ := planReviewFixture(t)
	setReviewMode(t, db, p2, "on")
	phaseReviews := 0
	rv.answer = func(spec verify.RunSpec) string {
		if isPlanReview(spec) {
			return "- none\nVERDICT: PASS"
		}
		phaseReviews++
		if phaseReviews == 1 {
			return "- P1 api/x.go:1 — x.id is read before it exists.\nVERDICT: FAIL"
		}
		return "- none\nVERDICT: PASS"
	}

	var fixTip string
	branch := startLastPhase(t, s, p2, repo, func(n int, branch string) {
		if n == 2 { // the fix re-run commits its fix on the run branch
			repo.git("checkout", "-q", branch)
			fixTip = repo.commitFile("api/x.go", "package api // reads x.id, fixed\n", "phase 2: review fix")
			repo.git("checkout", "-q", "main")
		}
	})

	if n := s.Run.(*stubRunner).specCount(); n != 2 {
		t.Fatalf("executor runs = %d, want 2 (the run + the review's fix re-run)", n)
	}
	if got := reviewKinds(rv); got != "phase,phase,plan" {
		t.Fatalf("reviews = %s, want phase,phase,plan — the plan review only after the fix re-run's review", got)
	}
	rows := planReviewRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("scope=plan rows = %d, want 1", len(rows))
	}
	if fixTip == "" || repo.git("rev-parse", branch) != fixTip {
		t.Fatalf("the fix re-run did not move %s (fixTip=%q)", branch, fixTip)
	}
	branches, err := s.planBranches(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows[0].key, planBranchKey(branches); got != want {
		t.Errorf("plan review key = %q, want the post-fix branch set %q", got, want)
	}
	tipSeen := false
	for _, b := range branches {
		if b.Branch == branch && b.Tip == fixTip {
			tipSeen = true
		}
	}
	if !tipSeen {
		t.Errorf("the reviewed branch set %+v does not carry the post-fix tip %s", branches, fixTip)
	}
}

// A `**Review:** on` last phase whose review PASSes the first time gets its plan
// review right after that phase review, from runAndHandle — not at the stamp.
func TestPlanReviewAfterAPassingLastPhaseReview(t *testing.T) {
	s, db, _, _, p2, _, repo, rv, _ := planReviewFixture(t)
	setReviewMode(t, db, p2, "on")
	rv.answer = func(verify.RunSpec) string { return "- none\nVERDICT: PASS" }
	startLastPhase(t, s, p2, repo, nil)

	if got := reviewKinds(rv); got != "phase,plan" {
		t.Fatalf("reviews = %s, want phase,plan", got)
	}
	if rows := planReviewRows(t, db); len(rows) != 1 {
		t.Fatalf("scope=plan rows = %d, want 1", len(rows))
	}
}

// A `**Review:** off` last phase run through Start keeps stamp's hook: one plan
// review and no phase review.
func TestPlanReviewFromTheStampWhenTheLastPhaseHasNoReview(t *testing.T) {
	s, db, _, _, p2, _, repo, rv, _ := planReviewFixture(t)
	rv.answer = func(verify.RunSpec) string { return "- none\nVERDICT: PASS" }
	startLastPhase(t, s, p2, repo, nil)

	if got := reviewKinds(rv); got != "plan" {
		t.Fatalf("reviews = %s, want exactly one plan review", got)
	}
	if rows := planReviewRows(t, db); len(rows) != 1 {
		t.Fatalf("scope=plan rows = %d, want 1", len(rows))
	}
}
