package phaserun

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// existingStubWt is stubWt plus AcquireExisting — the ExistingBranchWorktrees
// extension a re-verification needs. It reports the branch TIP as the start
// point, exactly like the real manager, which is what makes the D3 assertion
// (StartPoint == run_start_point, never the tip) meaningful.
type existingStubWt struct {
	*stubWt
	existErr error
	mu       sync.Mutex
	existing []string
}

const stubBranchTip = "tip0ffee"

func (w *existingStubWt) AcquireExisting(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.existing = append(w.existing, taskID)
	if w.existErr != nil {
		return worktree.Acquired{}, w.existErr
	}
	return worktree.Acquired{
		Path:       w.pathFor(projectSlug, taskID),
		Branch:     "swarm/" + taskID,
		StartPoint: stubBranchTip,
	}, nil
}

// readyForReverify stamps the row a finished, graded-inconclusive phase run
// leaves behind: criteria all ticked, a recorded run branch and start point, and
// a doc that opted into verification.
func readyForReverify(t *testing.T, db *sql.DB, phaseID int64, startPoint string) {
	t.Helper()
	mustExec(t, db, `UPDATE epic_phases
		SET run_state='done', run_branch=?, run_start_point=NULLIF(?, ''),
		    verify_mode='normal', verify_verdict='inconclusive',
		    checkboxes_done=checkboxes_total, run_checkboxes_after=checkboxes_total
		WHERE id=?`, runcore.PhaseBranch(phaseID), startPoint, phaseID)
	doc := phaseDocPath(t, db, phaseID)
	body, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteDoc(t, doc, strings.ReplaceAll(string(body), "- [ ] ", "- [x] "))
}

// newReverifyService is newTestService wired for a re-verification: a manager
// that can check an existing branch out, a git seam that resolves the branch,
// and a stub verifier.
func newReverifyService(t *testing.T, db *sql.DB) (*Service, *existingStubWt, *stubVerifier) {
	t.Helper()
	ew := &existingStubWt{stubWt: &stubWt{}}
	s := newTestService(db, &stubRunner{}, ew.stubWt)
	s.Wt = ew
	s.Git = &stubGit{head: stubBranchTip}
	v := &stubVerifier{}
	s.Verify = v
	return s, ew, v
}

func asRefusal(t *testing.T, err error) *ReverifyRefusal {
	t.Helper()
	var ref *ReverifyRefusal
	if !errors.As(err, &ref) {
		t.Fatalf("err = %v (%T), want a *ReverifyRefusal", err, err)
	}
	return ref
}

func TestReverifyEligible(t *testing.T) {
	ok := Eligibility{VerifyMode: "normal", RunState: "done", RunBranch: "swarm/phase-1", CriteriaDone: 3, CriteriaTotal: 3}
	with := func(f func(*Eligibility)) Eligibility { e := ok; f(&e); return e }
	cases := []struct {
		name   string
		in     Eligibility
		code   string
		status int
	}{
		{"verify mode empty", with(func(e *Eligibility) { e.VerifyMode = "" }), ReverifyCodeVerifyOff, http.StatusUnprocessableEntity},
		{"verify mode off", with(func(e *Eligibility) { e.VerifyMode = "off" }), ReverifyCodeVerifyOff, http.StatusUnprocessableEntity},
		{"criteria open", with(func(e *Eligibility) { e.CriteriaDone = 2 }), ReverifyCodeCriteriaOpen, http.StatusUnprocessableEntity},
		{"no criteria at all", with(func(e *Eligibility) { e.CriteriaDone, e.CriteriaTotal = 0, 0 }), ReverifyCodeCriteriaOpen, http.StatusUnprocessableEntity},
		// Classed criteria are not the verifier's to grade: only executable ones gate.
		{"only [MANUAL] open", with(func(e *Eligibility) { e.CriteriaDone, e.ManualOpen = 2, 1 }), "", 0},
		{"only [LAND] open", with(func(e *Eligibility) { e.CriteriaDone, e.LandOpen = 2, 1 }), "", 0},
		{"[LAND] and [MANUAL] open", with(func(e *Eligibility) { e.CriteriaDone, e.LandOpen, e.ManualOpen = 1, 1, 1 }), "", 0},
		{"executable open beside [MANUAL]", with(func(e *Eligibility) { e.CriteriaDone, e.ManualOpen = 1, 1 }), ReverifyCodeCriteriaOpen, http.StatusUnprocessableEntity},
		{"no run branch", with(func(e *Eligibility) { e.RunBranch = "" }), ReverifyCodeNoRunBranch, http.StatusUnprocessableEntity},
		{"already passed", with(func(e *Eligibility) { e.VerifyVerdict = "pass" }), ReverifyCodeAlreadyPassed, http.StatusConflict},
		{"already running", with(func(e *Eligibility) { e.RunState = "running" }), ReverifyCodeAlreadyRunning, http.StatusConflict},
		// Order: verify-off outranks everything after it.
		{"off and running", with(func(e *Eligibility) { e.VerifyMode = "off"; e.RunState = "running" }), ReverifyCodeVerifyOff, http.StatusUnprocessableEntity},
		{"strict, never graded", with(func(e *Eligibility) { e.VerifyMode = "strict" }), "", 0},
		{"verdict fail", with(func(e *Eligibility) { e.VerifyVerdict = "fail" }), "", 0},
		{"verdict inconclusive", with(func(e *Eligibility) { e.VerifyVerdict = "inconclusive" }), "", 0},
		{"verdict empty", with(func(e *Eligibility) { e.VerifyVerdict = "" }), "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReverifyEligible(c.in)
			if c.code == "" {
				if got != nil {
					t.Fatalf("refused %+v with %q, want eligible", c.in, got.Code)
				}
				return
			}
			if got == nil {
				t.Fatalf("eligible, want refusal %q", c.code)
			}
			if got.Code != c.code || got.Status != c.status || got.Msg == "" || got.Error() != got.Msg {
				t.Errorf("refusal = %+v, want code %q status %d with a message", got, c.code, c.status)
			}
		})
	}
}

// TestReverifyGradesTheRunIntervalAndTouchesNothing — real git, real
// worktree.Manager (SC-1, SC-2, SC-4). The verifier is called once, on a
// worktree of the existing swarm/phase-<id> branch, with the RECORDED
// run_start_point as its diff base (not the tip AcquireExisting reports). Run
// state, ticked criteria and the branch tip are untouched afterwards; the
// worktree is gone and the branch is kept.
func TestReverifyGradesTheRunIntervalAndTouchesNothing(t *testing.T) {
	repo := newTempRepo(t)
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	mainTip := repo.tip("main")
	branch := runcore.PhaseBranch(p1)
	branchTip := repo.branch(branch, "main", 2)
	readyForReverify(t, db, p1, mainTip)

	s := newTestService(db, &stubRunner{}, &stubWt{})
	wtRoot := filepath.Join(t.TempDir(), "wts")
	s.Wt = &worktree.Manager{Git: repo.git, Root: wtRoot}
	s.Git = repo.git
	var headAtVerify string
	var heldDuringVerify bool
	v := &stubVerifier{}
	v.onVerify = func() {
		req := v.reqs[len(v.reqs)-1] // onVerify runs after the append, outside the lock
		headAtVerify = strings.TrimSpace(repo.runIn(req.WorktreePath, "rev-parse", "HEAD"))
		heldDuringVerify = s.InFlight(p1)
	}
	s.Verify = v

	var stateBefore string
	var doneBefore int
	if err := db.QueryRow(`SELECT run_state, checkboxes_done FROM epic_phases WHERE id=?`, p1).Scan(&stateBefore, &doneBefore); err != nil {
		t.Fatal(err)
	}

	start, err := s.AdmitReverify(p1)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()

	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("VerifyPhase calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.StartPoint != mainTip {
		t.Errorf("StartPoint = %q, want run_start_point %s", req.StartPoint, mainTip)
	}
	if req.StartPoint == branchTip {
		t.Error("StartPoint is the branch tip — the verifier would diff the branch against itself")
	}
	if req.Branch != branch || req.PhaseID != p1 || req.WorkspaceTaskID != taskID || req.Mode != "normal" {
		t.Errorf("request = %+v, want branch %s phase %d task %d mode normal", req, branch, p1, taskID)
	}
	if !strings.HasPrefix(req.WorktreePath, wtRoot) {
		t.Errorf("WorktreePath = %q, want a worktree under %s", req.WorktreePath, wtRoot)
	}
	if headAtVerify != branchTip {
		t.Errorf("worktree HEAD at verify = %s, want the run branch tip %s", headAtVerify, branchTip)
	}
	if !strings.Contains(req.Prompt, "- [x] a") {
		t.Errorf("prompt is not the phase doc as it stands: %q", req.Prompt)
	}
	if !heldDuringVerify {
		t.Error("the phase's run slot was not held while the verifier ran")
	}

	// Nothing a run owns moved.
	var stateAfter string
	var doneAfter int
	if err := db.QueryRow(`SELECT run_state, checkboxes_done FROM epic_phases WHERE id=?`, p1).Scan(&stateAfter, &doneAfter); err != nil {
		t.Fatal(err)
	}
	if stateAfter != stateBefore || doneAfter != doneBefore {
		t.Errorf("run_state/checkboxes_done = %s/%d, want unchanged %s/%d", stateAfter, doneAfter, stateBefore, doneBefore)
	}
	if got := runBranchOf(t, db, p1); got.String != branch {
		t.Errorf("run_branch = %q, want %q", got.String, branch)
	}
	if sp := phaseStartPoint(t, db, p1); sp.String != mainTip {
		t.Errorf("run_start_point = %q, want %s unchanged", sp.String, mainTip)
	}
	if got := repo.tip("refs/heads/" + branch); got != branchTip {
		t.Errorf("branch tip = %s, want %s unchanged (and the branch kept)", got, branchTip)
	}
	if _, err := os.Stat(req.WorktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still on disk (stat err %v), want it removed", req.WorktreePath, err)
	}
	if s.InFlight(p1) {
		t.Error("run slot still held after the re-verification finished")
	}
	if got := repo.tip("main"); got != mainTip {
		t.Errorf("main moved to %s", got)
	}
}

// TestReverifyEmptyStartPointPassesEmpty: a pre-0057 row has no recorded start
// point; the request carries "" (the verifier skips the scope gate), never the tip.
func TestReverifyEmptyStartPointPassesEmpty(t *testing.T) {
	db, _, p1, _ := fixture(t)
	readyForReverify(t, db, p1, "")
	s, ew, v := newReverifyService(t, db)
	start, err := s.AdmitReverify(p1)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()
	calls := v.calls()
	if len(calls) != 1 || calls[0].StartPoint != "" {
		t.Fatalf("calls = %+v, want one with an empty StartPoint", calls)
	}
	if ew.removedCount() != 1 || !ew.keepBranch[0] {
		t.Errorf("removed = %d keepBranch = %v, want the worktree removed with the branch kept", ew.removedCount(), ew.keepBranch)
	}
	if ew.acquiredCount() != 0 || len(ew.reclaimedList()) != 0 || len(ew.deleted) != 0 {
		t.Error("a re-verification cut, reclaimed or deleted a branch")
	}
}

// TestReverifySlotExclusion — D4 in both directions: a held slot refuses a
// re-verification, and StartWith answers ErrRunning while a re-verification holds it.
func TestReverifySlotExclusion(t *testing.T) {
	db, _, p1, _ := fixture(t)
	readyForReverify(t, db, p1, "start0ffee")
	s, _, v := newReverifyService(t, db)

	// A run holds the slot ⇒ already-running.
	release, err := s.Slots.TryAcquire(s.slotKey(p1), "run-uuid", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AdmitReverify(p1)
	if ref := asRefusal(t, err); ref.Code != ReverifyCodeAlreadyRunning || ref.Status != http.StatusConflict {
		t.Errorf("refusal = %+v, want 409 already-running", ref)
	}
	release()

	// A re-verification holds the slot ⇒ StartWith refuses.
	start, err := s.AdmitReverify(p1)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	if _, err := s.StartWith(p1, StartOptions{}); !errors.Is(err, ErrRunning) {
		t.Errorf("StartWith during a re-verification: err = %v, want ErrRunning", err)
	}
	// A second re-verification is refused the same way.
	_, err = s.AdmitReverify(p1)
	if ref := asRefusal(t, err); ref.Code != ReverifyCodeAlreadyRunning {
		t.Errorf("second reverify refusal = %+v, want already-running", ref)
	}
	start()
	if s.InFlight(p1) {
		t.Error("slot still held after start returned")
	}
	if n := len(v.calls()); n != 1 {
		t.Errorf("VerifyPhase calls = %d, want 1", n)
	}
}

// TestReverifyRefusals — the live gates AdmitReverify adds on top of the pure
// predicate, each with its stable code; none leaves the slot held.
func TestReverifyRefusals(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, db *sql.DB, s *Service, ew *existingStubWt, p1 int64, taskID int64)
		code   string
		status int
	}{
		{"plan-running", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, _ int64, taskID int64) {
			mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state) VALUES (?, 'running')`, taskID)
		}, ReverifyCodePlanRunning, http.StatusConflict},
		{"branch-busy", func(_ *testing.T, _ *sql.DB, _ *Service, ew *existingStubWt, _ int64, _ int64) {
			ew.existErr = fmt.Errorf("%w: /elsewhere is on swarm/phase-x", worktree.ErrBranchBusy)
		}, ReverifyCodeBranchBusy, http.StatusConflict},
		{"missing branch", func(_ *testing.T, _ *sql.DB, s *Service, _ *existingStubWt, _ int64, _ int64) {
			s.Git = &stubGit{err: errors.New("fatal: Needed a single revision")}
		}, ReverifyCodeNoRunBranch, http.StatusUnprocessableEntity},
		// A branch that is not of the swarm/phase-<digits> shape names no checkout the
		// manager can reach: refused, never guessed at. (A swarm/phase-<other id>
		// branch — a re-keyed row — is admitted: TestReverifyReKeyedRow*.)
		{"foreign branch", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, p1 int64, _ int64) {
			mustExec(t, db, `UPDATE epic_phases SET run_branch='feat/foo' WHERE id=?`, p1)
		}, ReverifyCodeNoRunBranch, http.StatusUnprocessableEntity},
		{"phase-shaped branch with a non-numeric id", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, p1 int64, _ int64) {
			mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-12a' WHERE id=?`, p1)
		}, ReverifyCodeNoRunBranch, http.StatusUnprocessableEntity},
		// D5: one owner per branch — a second row claiming the same run branch.
		{"branch claimed by another row", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, p1 int64, _ int64) {
			mustExec(t, db, `UPDATE epic_phases SET run_branch=? WHERE id<>?`, runcore.PhaseBranch(p1), p1)
		}, ReverifyCodeBranchBusy, http.StatusConflict},
		{"verifier not wired", func(_ *testing.T, _ *sql.DB, s *Service, _ *existingStubWt, _ int64, _ int64) {
			s.Verify = nil
		}, ReverifyCodeNotWired, http.StatusServiceUnavailable},
		{"manager cannot check out a branch", func(_ *testing.T, _ *sql.DB, s *Service, ew *existingStubWt, _ int64, _ int64) {
			s.Wt = ew.stubWt
		}, ReverifyCodeNotWired, http.StatusServiceUnavailable},
		{"row not eligible", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, p1 int64, _ int64) {
			mustExec(t, db, `UPDATE epic_phases SET verify_verdict='pass' WHERE id=?`, p1)
		}, ReverifyCodeAlreadyPassed, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, taskID, p1, _ := fixture(t)
			readyForReverify(t, db, p1, "start0ffee")
			s, ew, v := newReverifyService(t, db)
			c.setup(t, db, s, ew, p1, taskID)
			start, err := s.AdmitReverify(p1)
			if start != nil {
				t.Fatal("refused admission returned a start body")
			}
			if ref := asRefusal(t, err); ref.Code != c.code || ref.Status != c.status {
				t.Errorf("refusal = %+v, want %d %s", ref, c.status, c.code)
			}
			if s.InFlight(p1) {
				t.Error("refusal left the run slot held")
			}
			if n := len(v.calls()); n != 0 {
				t.Errorf("VerifyPhase calls = %d, want 0", n)
			}
		})
	}
}

// TestReverifyBranchBusyNamesTheOtherOwner — D5: when another row claims the
// run branch, the refusal names that row, no checkout is attempted, and no slot
// is held afterwards. The same holds for a re-keyed row whose carried branch is
// still claimed elsewhere.
func TestReverifyBranchBusyNamesTheOtherOwner(t *testing.T) {
	for _, rekeyed := range []bool{false, true} {
		t.Run(fmt.Sprintf("rekeyed=%v", rekeyed), func(t *testing.T) {
			db, _, p1, p2 := fixture(t)
			readyForReverify(t, db, p1, "start0ffee")
			branch := runcore.PhaseBranch(p1)
			if rekeyed {
				branch = runcore.PhaseBranch(p1 + 40000)
				mustExec(t, db, `UPDATE epic_phases SET run_branch=? WHERE id=?`, branch, p1)
			}
			mustExec(t, db, `UPDATE epic_phases SET run_branch=? WHERE id=?`, branch, p2)
			s, ew, v := newReverifyService(t, db)

			start, err := s.AdmitReverify(p1)
			if start != nil {
				t.Fatal("refused admission returned a start body")
			}
			ref := asRefusal(t, err)
			if ref.Code != ReverifyCodeBranchBusy || ref.Status != http.StatusConflict {
				t.Errorf("refusal = %+v, want 409 %s", ref, ReverifyCodeBranchBusy)
			}
			if !strings.Contains(ref.Msg, branch) || !strings.Contains(ref.Msg, fmt.Sprintf("phase %d", p2)) {
				t.Errorf("message %q does not name the branch %s and the other owner %d", ref.Msg, branch, p2)
			}
			if s.InFlight(p1) {
				t.Error("refusal left the run slot held")
			}
			if len(ew.existing) != 0 || len(v.calls()) != 0 {
				t.Errorf("checkouts = %v, verifier calls = %d, want neither", ew.existing, len(v.calls()))
			}
		})
	}
}

// rekeyedEnv is a re-keyed row in a real repository: the row's id is phaseID, but
// its run_branch is swarm/phase-<oldID> — what carryAcrossRenames leaves on the
// successor of an archived plan or a renamed phase doc. That branch exists with
// two commits of its own; swarm/phase-<phaseID> does not exist at all.
type rekeyedEnv struct {
	repo                                 *tempRepo
	db                                   *sql.DB
	s                                    *Service
	mgr                                  *worktree.Manager
	wtRoot                               string
	phaseID, taskID                      int64
	branch, taskName, mainTip, branchTip string
}

func newRekeyedEnv(t *testing.T) *rekeyedEnv {
	t.Helper()
	e := &rekeyedEnv{repo: newTempRepo(t)}
	e.db, e.taskID, e.phaseID, _ = fixture(t)
	mustExec(t, e.db, `UPDATE projects SET path=? WHERE id=1`, e.repo.dir)
	e.mainTip = e.repo.tip("main")
	readyForReverify(t, e.db, e.phaseID, e.mainTip)
	oldID := e.phaseID + 40000
	e.taskName, e.branch = runcore.PhaseTaskName(oldID), runcore.PhaseBranch(oldID)
	e.branchTip = e.repo.branch(e.branch, "main", 2)
	mustExec(t, e.db, `UPDATE epic_phases SET run_branch=? WHERE id=?`, e.branch, e.phaseID)

	e.s = newTestService(e.db, &stubRunner{}, &stubWt{})
	e.wtRoot = filepath.Join(t.TempDir(), "wts")
	e.mgr = &worktree.Manager{Git: e.repo.git, Root: e.wtRoot}
	e.s.Wt = e.mgr
	e.s.Git = e.repo.git
	return e
}

// TestReverifyReKeyedRowGradesTheCarriedBranch — D4 / SC-4 / SC-6, real git and
// a real worktree.Manager. The row is admitted, the verifier is called once on a
// checkout of the CARRIED branch (the task name derived from run_branch, not from
// the current id) with the recorded run_start_point as its diff base, and the run
// slot keyed on the CURRENT id is held meanwhile. Afterwards the worktree is gone,
// the branch is kept at its tip, the slot is free, and no swarm/phase-<current id>
// branch was cut.
func TestReverifyReKeyedRowGradesTheCarriedBranch(t *testing.T) {
	e := newRekeyedEnv(t)
	var headAtVerify string
	var heldDuringVerify bool
	v := &stubVerifier{}
	v.onVerify = func() {
		req := v.reqs[len(v.reqs)-1]
		headAtVerify = strings.TrimSpace(e.repo.runIn(req.WorktreePath, "rev-parse", "HEAD"))
		heldDuringVerify = e.s.InFlight(e.phaseID)
	}
	e.s.Verify = v

	start, err := e.s.AdmitReverify(e.phaseID)
	if err != nil {
		t.Fatalf("AdmitReverify on a re-keyed row: %v", err)
	}
	start()

	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("VerifyPhase calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.Branch != e.branch || req.PhaseID != e.phaseID || req.WorkspaceTaskID != e.taskID {
		t.Errorf("request = %+v, want branch %s for the current phase %d of task %d", req, e.branch, e.phaseID, e.taskID)
	}
	if req.StartPoint != e.mainTip {
		t.Errorf("StartPoint = %q, want run_start_point %s", req.StartPoint, e.mainTip)
	}
	if !strings.HasPrefix(req.WorktreePath, e.wtRoot) || filepath.Base(req.WorktreePath) != e.taskName {
		t.Errorf("WorktreePath = %q, want <%s>/<slug>/%s (the derived task name)", req.WorktreePath, e.wtRoot, e.taskName)
	}
	if headAtVerify != e.branchTip {
		t.Errorf("worktree HEAD at verify = %s, want the carried branch tip %s", headAtVerify, e.branchTip)
	}
	if !heldDuringVerify {
		t.Error("the current phase id's run slot was not held while the verifier ran")
	}

	if _, err := os.Stat(req.WorktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still on disk (stat err %v), want it removed", req.WorktreePath, err)
	}
	if got := e.repo.tip("refs/heads/" + e.branch); got != e.branchTip {
		t.Errorf("carried branch tip = %s, want %s unchanged (and the branch kept)", got, e.branchTip)
	}
	if out := strings.TrimSpace(e.repo.run("branch", "--list", runcore.PhaseBranch(e.phaseID))); out != "" {
		t.Errorf("a branch for the current id was cut: %q", out)
	}
	if got := runBranchOf(t, e.db, e.phaseID); got.String != e.branch {
		t.Errorf("run_branch = %q, want %q unchanged", got.String, e.branch)
	}
	if e.s.InFlight(e.phaseID) {
		t.Error("run slot still held after the re-verification finished")
	}
}

// TestReverifyReKeyedRowInspectsTheDerivedLeftover — SC-6: the leftover-worktree
// check looks at the checkout of the DERIVED task name, the one AcquireExisting
// would warm-reuse. A dirty leftover there is refused and left untouched.
func TestReverifyReKeyedRowInspectsTheDerivedLeftover(t *testing.T) {
	e := newRekeyedEnv(t)
	v := &stubVerifier{}
	e.s.Verify = v
	info, err := e.s.loadPhase(e.phaseID)
	if err != nil {
		t.Fatal(err)
	}
	acq, err := e.mgr.AcquireExisting(e.repo.dir, info.ProjectSlug, e.taskName)
	if err != nil {
		t.Fatalf("leftover acquire: %v", err)
	}
	wip := filepath.Join(acq.Path, "uncommitted.txt")
	mustWriteDoc(t, wip, "the only copy of a crashed run's work\n")

	start, err := e.s.AdmitReverify(e.phaseID)
	if start != nil {
		t.Fatal("refused admission returned a start body")
	}
	ref := asRefusal(t, err)
	if ref.Code != ReverifyCodeWorktreeDirty || !strings.Contains(ref.Msg, acq.Path) {
		t.Errorf("refusal = %+v, want %s naming the derived leftover %s", ref, ReverifyCodeWorktreeDirty, acq.Path)
	}
	if _, err := os.Stat(wip); err != nil {
		t.Errorf("the uncommitted file is gone (stat err %v)", err)
	}
	if len(v.calls()) != 0 || e.s.InFlight(e.phaseID) {
		t.Errorf("verifier calls = %d, slot held = %v, want neither", len(v.calls()), e.s.InFlight(e.phaseID))
	}
}

// passVerifier is the REAL verify.Service with only the claude session stubbed:
// it answers VERDICT: PASS, and the real worktree.Manager reads the tree hash and
// the diff. Its Stamp is verify's own stampPhaseVerdict — nothing here writes the
// verdict by hand.
func passVerifier(e *rekeyedEnv, onRun func(verify.RunSpec)) (*verify.Service, *fakeReviewer) {
	r := &fakeReviewer{onRun: onRun, answer: func(verify.RunSpec) string { return "all criteria met\nVERDICT: PASS" }}
	return verify.NewService(e.db, verify.Config{}, r, e.mgr), r
}

func phaseVerdictOf(t *testing.T, db *sql.DB, phaseID int64) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT verify_verdict FROM epic_phases WHERE id=?`, phaseID).Scan(&v); err != nil {
		t.Fatalf("read verify_verdict of %d: %v", phaseID, err)
	}
	return v.String
}

// TestReverifyReKeyedRowStampsTheVerdictOnTheCurrentRow — end-to-end (SC-4): a
// re-keyed row graded through the real verify.Service. The verdict lands on the
// CURRENT row id, and verification_runs records it under that id's key.
func TestReverifyReKeyedRowStampsTheVerdictOnTheCurrentRow(t *testing.T) {
	e := newRekeyedEnv(t)
	svc, r := passVerifier(e, nil)
	e.s.Verify = svc

	start, err := e.s.AdmitReverify(e.phaseID)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()

	if n := len(r.calls()); n != 1 {
		t.Fatalf("verifier sessions = %d, want 1", n)
	}
	if got := phaseVerdictOf(t, e.db, e.phaseID); got != "pass" {
		t.Errorf("current row %d verify_verdict = %q, want pass", e.phaseID, got)
	}
	var status string
	if err := e.db.QueryRow(`SELECT status FROM verification_runs WHERE target_key=? ORDER BY id DESC LIMIT 1`,
		verify.PhaseKey(e.phaseID)).Scan(&status); err != nil {
		t.Fatalf("verification_runs for %s: %v", verify.PhaseKey(e.phaseID), err)
	}
	if status != "pass" {
		t.Errorf("verification_runs status = %q, want pass", status)
	}
	if e.s.InFlight(e.phaseID) {
		t.Error("run slot still held after the re-verification finished")
	}
}

// TestReverifyVerdictFollowsAReKeyDuringGrading — end-to-end with phase 1's stamp:
// the plan is archived AGAIN while the verifier grades, so the row the grading
// started under is pruned and a successor carries its run branch. The verdict
// lands on that successor, the row that is current when the stamp runs.
func TestReverifyVerdictFollowsAReKeyDuringGrading(t *testing.T) {
	e := newRekeyedEnv(t)
	var successor int64
	svc, _ := passVerifier(e, func(verify.RunSpec) {
		successor = rekeyRow(t, e.db, e.phaseID, filepath.Join(t.TempDir(), "archive", "phase-1-schema.md"))
	})
	e.s.Verify = svc

	start, err := e.s.AdmitReverify(e.phaseID)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()

	if successor == 0 {
		t.Fatal("the re-key never ran")
	}
	if got := phaseVerdictOf(t, e.db, successor); got != "pass" {
		t.Errorf("successor row %d verify_verdict = %q, want pass", successor, got)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM epic_phases WHERE id=?`, e.phaseID).Scan(&n); err != nil || n != 0 {
		t.Errorf("the pre-archive row %d is still there (n=%d, err=%v)", e.phaseID, n, err)
	}
}

// rekeyRow is the SQL equivalent of wsingest's rename handling in one transaction
// (the same emulation internal/verify's phase-stamp tests use): insert the row for
// the new doc path, carry the daemon-owned columns 1:1 the way carryAcrossRenames
// does, then prune the vanished row. Returns the successor's id.
func rekeyRow(t *testing.T, db *sql.DB, oldID int64, newDocPath string) int64 {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.Exec(`
		INSERT INTO epic_phases(workspace_task_id, seq, name, doc_path, depends_on,
		                        checkboxes_done, checkboxes_total, verify_mode)
		SELECT workspace_task_id, seq, name, ?, depends_on,
		       checkboxes_done, checkboxes_total, verify_mode
		  FROM epic_phases WHERE id=?`, newDocPath, oldID)
	if err != nil {
		t.Fatalf("re-key: insert successor: %v", err)
	}
	newID, _ := res.LastInsertId()
	if _, err := tx.Exec(`
		UPDATE epic_phases
		   SET (run_state, run_branch, run_start_point, verify_verdict, verify_detail) =
		       (SELECT run_state, run_branch, run_start_point, verify_verdict, verify_detail
		          FROM epic_phases WHERE id=?)
		 WHERE id=?`, oldID, newID); err != nil {
		t.Fatalf("re-key: carry: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM epic_phases WHERE id=?`, oldID); err != nil {
		t.Fatalf("re-key: prune: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return newID
}

// TestReverifyUnknownPhase: a missing row travels as ErrPhaseNotFound (404).
func TestReverifyUnknownPhase(t *testing.T) {
	db, _, _, _ := fixture(t)
	s, _, _ := newReverifyService(t, db)
	if _, err := s.AdmitReverify(99999); !errors.Is(err, ErrPhaseNotFound) {
		t.Errorf("err = %v, want ErrPhaseNotFound", err)
	}
}

// leftoverReverifyEnv is the real-git setup of the leftover-worktree tests: a run
// branch with commits, a ready row, a real worktree.Manager, and a crashed run's
// leftover checkout of that branch ALREADY registered at the phase's own path —
// the one AcquireExisting would warm-reuse.
func leftoverReverifyEnv(t *testing.T) (repo *tempRepo, s *Service, v *stubVerifier, p1 int64, leftover string) {
	t.Helper()
	repo = newTempRepo(t)
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	mainTip := repo.tip("main")
	repo.branch(runcore.PhaseBranch(p1), "main", 1)
	readyForReverify(t, db, p1, mainTip)

	s = newTestService(db, &stubRunner{}, &stubWt{})
	m := &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	s.Wt = m
	s.Git = repo.git
	v = &stubVerifier{}
	s.Verify = v

	info, err := s.loadPhase(p1)
	if err != nil {
		t.Fatal(err)
	}
	acq, err := m.AcquireExisting(repo.dir, info.ProjectSlug, runcore.PhaseTaskName(p1))
	if err != nil {
		t.Fatalf("leftover acquire: %v", err)
	}
	return repo, s, v, p1, acq.Path
}

// TestReverifyRefusesADirtyLeftoverWorktree — a crashed run's leftover checkout
// holds an uncommitted file. Grading it would stamp a verdict on work the branch
// does not contain, and removing it afterwards would destroy the only copy: the
// admission refuses worktree-dirty, never calls the verifier, leaves the file and
// the checkout in place, and frees the slot.
func TestReverifyRefusesADirtyLeftoverWorktree(t *testing.T) {
	_, s, v, p1, leftover := leftoverReverifyEnv(t)
	wip := filepath.Join(leftover, "uncommitted.txt")
	mustWriteDoc(t, wip, "the only copy of a crashed run's work\n")

	start, err := s.AdmitReverify(p1)
	if start != nil {
		t.Fatal("refused admission returned a start body")
	}
	ref := asRefusal(t, err)
	if ref.Code != ReverifyCodeWorktreeDirty || ref.Status != http.StatusConflict {
		t.Errorf("refusal = %+v, want 409 %s", ref, ReverifyCodeWorktreeDirty)
	}
	if !strings.Contains(ref.Msg, leftover) {
		t.Errorf("message %q does not name the leftover worktree %s", ref.Msg, leftover)
	}
	if n := len(v.calls()); n != 0 {
		t.Errorf("VerifyPhase calls = %d, want 0", n)
	}
	if _, err := os.Stat(wip); err != nil {
		t.Errorf("the uncommitted file is gone (stat err %v)", err)
	}
	if s.InFlight(p1) {
		t.Error("refusal left the run slot held")
	}
}

// TestReverifyKeepsACleanLeftoverWorktree — a clean leftover on the run branch is
// warm-reused and graded, but this call did not create it, so it is still there
// afterwards (and the slot is free).
func TestReverifyKeepsACleanLeftoverWorktree(t *testing.T) {
	repo, s, v, p1, leftover := leftoverReverifyEnv(t)

	start, err := s.AdmitReverify(p1)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()

	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("VerifyPhase calls = %d, want 1", len(calls))
	}
	if !sameCheckout(calls[0].WorktreePath, leftover) {
		t.Errorf("graded %s, want the reused leftover %s", calls[0].WorktreePath, leftover)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Errorf("the reused leftover worktree was removed (stat err %v)", err)
	}
	if out := repo.run("worktree", "list", "--porcelain"); !strings.Contains(out, "branch refs/heads/"+runcore.PhaseBranch(p1)) {
		t.Errorf("the leftover is no longer registered on its branch:\n%s", out)
	}
	if s.InFlight(p1) {
		t.Error("run slot still held after the re-verification finished")
	}
}

// TestReverifyIgnoresManagerPlacedUntrackedPaths — the worktree manager lends a
// `node_modules` symlink (which a dir-only `node_modules/` ignore rule does not
// match) and copies an untracked .claude/settings.local.json into every checkout
// it acquires. A leftover whose only untracked entries are those is clean: it is
// graded, and kept.
func TestReverifyIgnoresManagerPlacedUntrackedPaths(t *testing.T) {
	t.Setenv(worktree.LendEnv, "") // the defaults, whatever the caller's env says
	repo, s, v, p1, leftover := leftoverReverifyEnv(t)
	// A developer's global ignore (e.g. ~/.config/git/ignore listing
	// settings.local.json) would hide the entry this test is about.
	repo.run("config", "core.excludesFile", os.DevNull)
	deps := t.TempDir()
	if err := os.Symlink(deps, filepath.Join(leftover, "node_modules")); err != nil {
		t.Fatal(err)
	}
	writeNested(t, filepath.Join(leftover, ".claude", "settings.local.json"), "{}\n")
	status := repo.runIn(leftover, "status", "--porcelain=v1", "--untracked-files=all")
	for _, want := range []string{"?? node_modules", "?? .claude/settings.local.json"} {
		if !strings.Contains(status, want) {
			t.Fatalf("precondition: git status lacks %q:\n%s", want, status)
		}
	}

	start, err := s.AdmitReverify(p1)
	if err != nil {
		t.Fatalf("AdmitReverify: %v", err)
	}
	start()

	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("VerifyPhase calls = %d, want 1", len(calls))
	}
	if !sameCheckout(calls[0].WorktreePath, leftover) {
		t.Errorf("graded %s, want the reused leftover %s", calls[0].WorktreePath, leftover)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Errorf("the reused leftover worktree was removed (stat err %v)", err)
	}
	if s.InFlight(p1) {
		t.Error("run slot still held after the re-verification finished")
	}
}

// writeNested is mustWriteDoc for a path whose parent directories may not exist.
func writeNested(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteDoc(t, path, body)
}

// TestReverifyRefusesOtherUntrackedPaths — an untracked path that merely starts
// like a placed one, or any other untracked file, is still uncommitted work.
func TestReverifyRefusesOtherUntrackedPaths(t *testing.T) {
	for _, rel := range []string{"node_modules_extra/x", "src/new.go"} {
		t.Run(rel, func(t *testing.T) {
			t.Setenv(worktree.LendEnv, "")
			_, s, v, p1, leftover := leftoverReverifyEnv(t)
			wip := filepath.Join(leftover, filepath.FromSlash(rel))
			writeNested(t, wip, "work\n")

			start, err := s.AdmitReverify(p1)
			if start != nil {
				t.Fatal("refused admission returned a start body")
			}
			if ref := asRefusal(t, err); ref.Code != ReverifyCodeWorktreeDirty {
				t.Errorf("refusal = %+v, want %s", ref, ReverifyCodeWorktreeDirty)
			}
			if n := len(v.calls()); n != 0 {
				t.Errorf("VerifyPhase calls = %d, want 0", n)
			}
			if _, err := os.Stat(wip); err != nil {
				t.Errorf("the untracked file is gone (stat err %v)", err)
			}
		})
	}
}

func TestStatusHasWork(t *testing.T) {
	placed := []string{"node_modules", ".venv", ".claude/settings.json", ".claude/settings.local.json"}
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"", false},
		{"\n", false},
		{"?? node_modules\n", false},
		{"?? node_modules/\n", false},
		{"?? \"node_modules\"\n", false},
		{"?? .claude/settings.local.json\n?? .venv\n", false},
		{"?? node_modules_extra/x\n", true},
		{"?? node_modules/x\n", true},
		{"?? src/new.go\n", true},
		{"?? node_modules\n?? src/new.go\n", true},
		{" M .claude/settings.json\n", true},
		{"M  node_modules\n", true},
		{"A  .claude/settings.local.json\n", true},
		{" M main.go\n", true},
	} {
		if got := statusHasWork(tc.status, placed); got != tc.want {
			t.Errorf("statusHasWork(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
