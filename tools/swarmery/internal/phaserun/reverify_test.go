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
		{"branch under a previous id", func(t *testing.T, db *sql.DB, _ *Service, _ *existingStubWt, p1 int64, _ int64) {
			mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-424242' WHERE id=?`, p1)
		}, ReverifyCodeNoRunBranch, http.StatusUnprocessableEntity},
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

// TestReverifyUnknownPhase: a missing row travels as ErrPhaseNotFound (404).
func TestReverifyUnknownPhase(t *testing.T) {
	db, _, _, _ := fixture(t)
	s, _, _ := newReverifyService(t, db)
	if _, err := s.AdmitReverify(99999); !errors.Is(err, ErrPhaseNotFound) {
		t.Errorf("err = %v, want ErrPhaseNotFound", err)
	}
}
