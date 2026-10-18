package phaserun

// Re-verification of a finished phase ("Verify again"): the read-only verifier
// grades the phase's EXISTING run branch as it stands, with no executor and no
// reviewer. Nothing a run owns is touched — run_state, the checkboxes and the
// branch's commits stay exactly as they were; the only write is the verdict,
// which verify.Service.VerifyPhase stamps through its own Stamp + Notify.
//
// It reuses what a run already owns: the phase's run slot (so a run and a
// re-verification of the same phase can never overlap), the plan-run gate, and
// a worktree of the existing branch that is removed afterwards with the branch
// kept.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Reverify refusal codes. Stable wire values: the api renders them as the
// `code` of the refusal body and the web client switches on them.
const (
	ReverifyCodeVerifyOff      = "verify-off"
	ReverifyCodeCriteriaOpen   = "criteria-open"
	ReverifyCodeNoRunBranch    = "no-run-branch"
	ReverifyCodeAlreadyPassed  = "already-passed"
	ReverifyCodeAlreadyRunning = "already-running"
	ReverifyCodePlanRunning    = "plan-running"
	ReverifyCodeBranchBusy     = "branch-busy"
	ReverifyCodeNotWired       = "verifier-not-attached"
)

// Eligibility is the row-only input of ReverifyEligible: every field comes from
// the epic_phases row, so the endpoint and the phase JSON (canReverify) answer
// the same question from the same facts.
type Eligibility struct {
	VerifyMode, VerifyVerdict, RunState, RunBranch string
	CriteriaDone, CriteriaTotal                    int
}

// ReverifyRefusal is a typed admission refusal: the HTTP status it maps to, a
// stable code, and the operator-facing sentence.
type ReverifyRefusal struct {
	Status int
	Code   string
	Msg    string
}

func (e *ReverifyRefusal) Error() string { return e.Msg }

func refuseReverify(status int, code, msg string) *ReverifyRefusal {
	return &ReverifyRefusal{Status: status, Code: code, Msg: msg}
}

// ReverifyEligible is the one pure eligibility predicate (decision D5): nil means
// the row may be re-verified. Order: verify-off, criteria-open, no-run-branch,
// already-passed, already-running. Live conditions (the slot, a plan run, a busy
// branch) are not row facts and are checked only by AdmitReverify.
func ReverifyEligible(e Eligibility) *ReverifyRefusal {
	switch {
	case e.VerifyMode == "" || e.VerifyMode == wsingest.VerifyOff:
		return refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeVerifyOff,
			"this phase does not ask to be verified (its doc has no **Verify:** normal|strict line)")
	case !phasegate.CriteriaMetForDisplay(e.CriteriaDone, e.CriteriaTotal):
		return refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeCriteriaOpen,
			fmt.Sprintf("the phase's acceptance criteria are not all ticked (%d/%d) — finish the phase first",
				e.CriteriaDone, e.CriteriaTotal))
	case e.RunBranch == "":
		return refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			"the phase has no recorded run branch to verify")
	// D7: a pass is final — the tree-hash cache would answer the same, and a button
	// there only invites spend. A fail IS re-gradable: the branch may have been
	// fixed by hand.
	case e.VerifyVerdict == phasegate.VerdictPass:
		return refuseReverify(http.StatusConflict, ReverifyCodeAlreadyPassed,
			"the phase already passed verification")
	case e.RunState == "running":
		return refuseReverify(http.StatusConflict, ReverifyCodeAlreadyRunning,
			"a run is already active for this phase")
	}
	return nil
}

// reverifyRow reads the row facts eligibility needs beyond what loadPhase
// carries: the stamped verdict and the criteria counts.
func (s *Service) reverifyRow(phaseID int64, info phaseInfo) (Eligibility, error) {
	el := Eligibility{VerifyMode: info.VerifyMode, RunState: info.RunState, RunBranch: info.RunBranch}
	err := s.DB.QueryRow(`
		SELECT COALESCE(verify_verdict, ''), checkboxes_done, checkboxes_total
		  FROM epic_phases WHERE id = ?`, phaseID).Scan(&el.VerifyVerdict, &el.CriteriaDone, &el.CriteriaTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return el, ErrPhaseNotFound
	}
	return el, err
}

// AdmitReverify runs every admission gate of a re-verification synchronously and,
// when all pass, returns the body to run asynchronously (decision D6). On
// success the phase's run slot is HELD and a worktree of its run branch exists;
// start releases both. Every refusal after the slot was taken releases it.
//
// Refusals are *ReverifyRefusal, except ErrPhaseNotFound / ErrNoPath / repo
// resolution errors and a full run pool (*runcore.NoSlotError), which travel as
// themselves the way StartWith's do.
func (s *Service) AdmitReverify(phaseID int64) (start func(), err error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return nil, err
	}
	el, err := s.reverifyRow(phaseID, info)
	if err != nil {
		return nil, err
	}
	if ref := ReverifyEligible(el); ref != nil {
		return nil, ref
	}
	if busy, err := s.planRunActive(info.WorkspaceTaskID); err != nil {
		return nil, err
	} else if busy {
		return nil, refuseReverify(http.StatusConflict, ReverifyCodePlanRunning,
			"a plan run is active for this plan — wait for it or cancel it before re-verifying one phase")
	}
	if s.Verify == nil {
		return nil, refuseReverify(http.StatusServiceUnavailable, ReverifyCodeNotWired,
			"phase verification is not attached")
	}
	// The worktree manager checks out swarm/<taskName>, i.e. the DERIVED name. A row
	// whose stamped branch differs (renamed doc, older row id) holds its work on a
	// branch that name does not reach — refuse rather than guess.
	if want := runcore.PhaseBranch(phaseID); info.RunBranch != want {
		return nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			fmt.Sprintf("the phase's run branch is %s, not %s — it ran under a previous phase id, so it cannot be re-verified here",
				info.RunBranch, want))
	}
	if info.ProjectPath == "" {
		return nil, ErrNoPath
	}
	// loadPhase does not resolve the repository; StartWith does it itself, and so
	// must this — the branch lives in the repo the run resolved to.
	info.RepoRoot, err = s.runRoot(info)
	if err != nil {
		return nil, err
	}
	if s.branchTip(info.RepoRoot, info.RunBranch) == "" {
		return nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			fmt.Sprintf("the run branch %s does not exist in %s", info.RunBranch, info.RepoRoot))
	}
	ex, ok := s.Wt.(ExistingBranchWorktrees)
	if !ok {
		return nil, refuseReverify(http.StatusServiceUnavailable, ReverifyCodeNotWired,
			"the worktree manager cannot check out an existing branch")
	}

	// D4: the run's own slot, held for the whole re-verification, so StartWith
	// answers ErrRunning meanwhile and a re-verification refuses while a run holds it.
	// The cancel only frees the slot's bookkeeping: the verifier runs on its own
	// background context, the way verifyRun's does.
	_, cancel := context.WithCancel(context.Background())
	releaseSlot, err := s.Slots.TryAcquire(s.slotKey(phaseID), s.UUID(), cancel)
	if err != nil {
		cancel()
		if errors.Is(err, runcore.ErrBusy) {
			return nil, refuseReverify(http.StatusConflict, ReverifyCodeAlreadyRunning,
				"a run or a re-verification is already active for this phase")
		}
		return nil, err // runcore.ErrNoSlot travels as itself
	}
	release := func() {
		cancel()
		releaseSlot()
	}

	// D2: the branch as it stands — no ref is deleted or re-cut.
	acq, err := ex.AcquireExisting(info.RepoRoot, info.ProjectSlug, runcore.PhaseTaskName(phaseID))
	if err != nil {
		release()
		switch {
		case errors.Is(err, worktree.ErrBranchBusy):
			return nil, refuseReverify(http.StatusConflict, ReverifyCodeBranchBusy,
				"the run branch is checked out in another worktree — remove it or finish what holds it, then verify again")
		case errors.Is(err, worktree.ErrStartRefUnresolved):
			return nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
				fmt.Sprintf("the run branch %s no longer resolves", info.RunBranch))
		}
		return nil, fmt.Errorf("worktree acquire: %w", err)
	}

	return func() {
		// Worktree FIRST, slot LAST (defers run LIFO) — the invariant runAndHandle
		// keeps, so no run can warm-reuse a worktree that is about to be removed.
		defer release()
		defer s.removeWorktree(info.RepoRoot, acq)
		s.reverifyBody(phaseID, info, acq)
	}, nil
}

// reverifyBody grades the acquired worktree. Errors are logged and dropped: the
// verdict (or its absence) is the only outcome, and the phase's run facts are
// never touched.
func (s *Service) reverifyBody(phaseID int64, info phaseInfo, acq worktree.Acquired) {
	doc, err := os.ReadFile(info.DocPath)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d reverify skipped, doc %q unreadable: %v", phaseID, info.DocPath, err)
		return
	}
	log.Printf("phaserun: phase=%d reverify (%s) branch=%s base=%q worktree=%q",
		phaseID, info.VerifyMode, acq.Branch, info.RunStartPoint, acq.Path)
	if err := s.Verify.VerifyPhase(context.Background(), runcore.PhaseVerifyRequest{
		PhaseID:         phaseID,
		WorkspaceTaskID: info.WorkspaceTaskID,
		Mode:            info.VerifyMode,
		WorktreePath:    acq.Path,
		Branch:          acq.Branch,
		// D3: the run's recorded start point, NOT acq.StartPoint — AcquireExisting
		// reports the branch TIP, and a diff of the branch against its own tip is
		// empty by construction. "" (a pre-0057 row) makes the verifier skip the
		// scope gate, as it already does.
		StartPoint:  info.RunStartPoint,
		Title:       info.Name,
		Prompt:      string(doc),
		ProjectPath: info.ProjectPath,
	}); err != nil {
		log.Printf("error: phaserun: phase=%d reverify: %v", phaseID, err)
		return
	}
	log.Printf("phaserun: phase=%d reverify finished", phaseID)
}
