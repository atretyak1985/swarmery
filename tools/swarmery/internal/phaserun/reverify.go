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
// kept — unless it was a clean leftover this call merely reused; a leftover
// holding uncommitted work is refused (worktree-dirty) and never touched.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	ReverifyCodeWorktreeDirty  = "worktree-dirty"
	ReverifyCodeNotWired       = "verifier-not-attached"
)

// Eligibility is the row-only input of ReverifyEligible: every field comes from
// the epic_phases row, so the endpoint and the phase JSON (canReverify) answer
// the same question from the same facts.
type Eligibility struct {
	VerifyMode, VerifyVerdict, RunState, RunBranch string
	CriteriaDone, CriteriaTotal                    int
	// LandOpen / ManualOpen: the unticked `[LAND]` / `[MANUAL]` criteria
	// (epic_phases.criteria_land_open / criteria_manual_open). They are not the
	// executor's work, so the verifier has nothing to grade in them — a phase whose
	// only open criteria are classed is as re-gradable as a fully ticked one.
	LandOpen, ManualOpen int
}

// executableMet reports whether every criterion that is NOT an open `[LAND]` /
// `[MANUAL]` is ticked — the same effective total a run is settled against. A
// phase with no criteria at all is still unprovable, as in phasegate.
func (e Eligibility) executableMet() bool {
	return e.CriteriaTotal > 0 && e.CriteriaDone+e.LandOpen+e.ManualOpen >= e.CriteriaTotal
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
	case !e.executableMet():
		return refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeCriteriaOpen,
			fmt.Sprintf("the phase's executable acceptance criteria are not all ticked (%d/%d, %d [LAND] and %d [MANUAL] open) — finish the phase first",
				e.CriteriaDone, e.CriteriaTotal, e.LandOpen, e.ManualOpen))
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
// carries: the stamped verdict and the criteria counts, classed-open included.
func (s *Service) reverifyRow(phaseID int64, info phaseInfo) (Eligibility, error) {
	el := Eligibility{VerifyMode: info.VerifyMode, RunState: info.RunState, RunBranch: info.RunBranch}
	err := s.DB.QueryRow(`
		SELECT COALESCE(verify_verdict, ''), checkboxes_done, checkboxes_total,
		       COALESCE(criteria_land_open, 0), COALESCE(criteria_manual_open, 0)
		  FROM epic_phases WHERE id = ?`, phaseID).Scan(&el.VerifyVerdict, &el.CriteriaDone, &el.CriteriaTotal,
		&el.LandOpen, &el.ManualOpen)
	if errors.Is(err, sql.ErrNoRows) {
		return el, ErrPhaseNotFound
	}
	return el, err
}

// AdmitReverify runs every admission gate of a re-verification synchronously and,
// when all pass, returns the body to run asynchronously (decision D6). On
// success the phase's run slot is HELD and a worktree of its run branch exists;
// start releases the slot and removes the worktree if this call created it. Every
// refusal after the slot was taken releases it.
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
	// The task name — and with it the worktree path and the swarm/<taskName> branch
	// the manager checks out — is derived from the STAMPED run_branch, not from the
	// current row id: a re-keyed row (archived plan, renamed doc) carries the branch
	// it ran on under its previous id, and that branch is where its work lives. Only
	// the exact swarm/phase-<digits> shape is accepted, so a hand-made or foreign
	// branch is never guessed at. The run slot stays keyed on the current id.
	taskName, ok := runcore.PhaseTaskNameFromBranch(info.RunBranch)
	if !ok {
		return nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			fmt.Sprintf("the phase's run branch %s is not a phase run branch (swarm/phase-<id>) — it cannot be re-verified",
				info.RunBranch))
	}
	// One owner per branch: two rows grading one branch would race their verdicts,
	// and the stamp's lineage fallback (verify.stampPhaseVerdict) relies on a branch
	// naming exactly one row.
	if other, err := s.otherBranchOwner(phaseID, info.RunBranch); err != nil {
		return nil, err
	} else if other != 0 {
		return nil, refuseReverify(http.StatusConflict, ReverifyCodeBranchBusy,
			fmt.Sprintf("run branch %s is also claimed by phase %d — resolve the duplicate before re-verifying",
				info.RunBranch, other))
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
	// The slot alone cannot exclude a second re-verification of the same branch: a
	// re-key while this one grades (wsingest moves the row to a new id) leaves the
	// successor's id slot free, and its admission would warm-reuse the checkout this
	// one is about to remove. So the branch itself is claimed too, for exactly as
	// long as the slot is held.
	if !s.claimReverifyBranch(info.RunBranch) {
		cancel()
		releaseSlot()
		return nil, refuseReverify(http.StatusConflict, ReverifyCodeAlreadyRunning,
			fmt.Sprintf("a re-verification of run branch %s is already active — the phase was re-keyed while it ran; wait for it to finish",
				info.RunBranch))
	}
	release := func() {
		s.releaseReverifyBranch(info.RunBranch)
		cancel()
		releaseSlot()
	}

	// AcquireExisting warm-reuses a checkout already registered at the phase's path
	// (a crashed run's leftover) and reclaims a foreign one in place. Either may hold
	// the only copy of uncommitted work: grading it would stamp a verdict on edits
	// the branch does not contain, and the removal below would then destroy them.
	// So look first, under the slot, and refuse rather than touch a dirty one — the
	// same posture StartWith takes towards a leftover it did not create. The path
	// inspected is the derived task name's, i.e. the checkout AcquireExisting reaches.
	left, err := s.leftoverWorktree(info.RepoRoot, info.ProjectSlug, taskName)
	if err != nil {
		release()
		return nil, fmt.Errorf("worktree inspect: %w", err)
	}
	if left.dirty {
		release()
		return nil, refuseReverify(http.StatusConflict, ReverifyCodeWorktreeDirty,
			fmt.Sprintf("a leftover worktree of this phase at %s holds uncommitted changes — commit them to %s "+
				"or remove the worktree (`git worktree remove`, after saving anything worth keeping), then verify again",
				left.path, info.RunBranch))
	}

	// D2: the branch as it stands — no ref is deleted or re-cut.
	acq, err := ex.AcquireExisting(info.RepoRoot, info.ProjectSlug, taskName)
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

	// A clean checkout already on the run branch at this path is the one
	// AcquireExisting warm-reused: this call did not create it, so it stays. Any
	// other outcome (nothing there, a stale registration, a foreign branch reclaimed
	// in place) is a checkout this call cut, and it is removed afterwards.
	created := !(left.registered && left.branch == acq.Branch)

	return func() {
		// Worktree FIRST, slot LAST (defers run LIFO) — the invariant runAndHandle
		// keeps, so no run can warm-reuse a worktree that is about to be removed.
		defer release()
		if created {
			defer s.removeWorktree(info.RepoRoot, acq)
		}
		s.reverifyBody(phaseID, info, acq)
	}, nil
}

// claimReverifyBranch marks branch as being re-verified; false when it already is.
func (s *Service) claimReverifyBranch(branch string) bool {
	s.reverifyMu.Lock()
	defer s.reverifyMu.Unlock()
	if _, busy := s.reverifyBranches[branch]; busy {
		return false
	}
	if s.reverifyBranches == nil {
		s.reverifyBranches = map[string]struct{}{}
	}
	s.reverifyBranches[branch] = struct{}{}
	return true
}

// releaseReverifyBranch drops the claim claimReverifyBranch took.
func (s *Service) releaseReverifyBranch(branch string) {
	s.reverifyMu.Lock()
	defer s.reverifyMu.Unlock()
	delete(s.reverifyBranches, branch)
}

// otherBranchOwner returns the id of another epic_phases row that claims branch,
// or 0 when phaseID is its only owner.
func (s *Service) otherBranchOwner(phaseID int64, branch string) (int64, error) {
	var other int64
	err := s.DB.QueryRow(`SELECT id FROM epic_phases WHERE run_branch = ? AND id <> ? LIMIT 1`,
		branch, phaseID).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("run branch owner lookup: %w", err)
	}
	return other, nil
}

// leftoverCheckout is what leftoverWorktree saw at the phase's worktree path.
type leftoverCheckout struct {
	path       string
	registered bool   // git has a live checkout registered at path
	branch     string // the branch it has checked out ("" when detached)
	dirty      bool   // `git status` reports work (see statusHasWork)
}

// leftoverWorktree inspects the checkout AcquireExisting would hand this phase
// BEFORE it is called: whether git has one registered at that path, on which
// branch, and whether it holds uncommitted work (tracked edits or untracked
// files other than the ones the manager placed there itself). A registration whose directory is gone is a stale one AcquireExisting
// prunes and replaces, so it reads as nothing there. Every git failure is an
// error — the caller refuses rather than guesses whether work would be lost.
func (s *Service) leftoverWorktree(repoRoot, projectSlug, taskName string) (leftoverCheckout, error) {
	path, err := s.Wt.Path(projectSlug, taskName)
	if err != nil {
		return leftoverCheckout{}, err
	}
	left := leftoverCheckout{path: path}
	list, err := s.Git.Run(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return left, fmt.Errorf("list worktrees: %w", err)
	}
	for _, e := range worktree.ParseWorktreeList(list) {
		if !sameCheckout(e.Path, path) {
			continue
		}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return left, nil
		}
		status, err := s.Git.Run(path, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return left, fmt.Errorf("status of %s: %w", path, err)
		}
		left.registered, left.branch = true, e.Branch
		left.dirty = statusHasWork(status, worktree.ManagerPlacedPaths(path))
		return left, nil
	}
	return left, nil
}

// statusHasWork reports whether `git status --porcelain=v1` output names any
// entry that may be someone's uncommitted work. The one exception is an
// UNTRACKED entry on a path the worktree manager places into every checkout it
// acquires (placed: a lent dependency symlink such as `node_modules`, a copied
// untracked .claude config file) — git lists those in a perfectly clean
// leftover. A tracked, modified or staged entry on such a path, and any other
// untracked path, is still work.
func statusHasWork(status string, placed []string) bool {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, untracked := strings.CutPrefix(line, "?? ")
		if !untracked || !isPlacedPath(rest, placed) {
			return true
		}
	}
	return false
}

// isPlacedPath matches a porcelain path against placed, exactly or with git's
// trailing "/" for a directory. Porcelain quotes a path holding unusual
// characters; the surrounding quotes are stripped, nothing is unescaped.
func isPlacedPath(p string, placed []string) bool {
	if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		p = p[1 : len(p)-1]
	}
	p = strings.TrimSuffix(p, "/")
	for _, want := range placed {
		if p == want {
			return true
		}
	}
	return false
}

// sameCheckout reports whether a and b name the same directory. git prints the
// worktree paths it registered with symlinks resolved (/private/var on macOS),
// while the manager derives its path from the configured root, so a textual
// comparison would miss the leftover it is looking for.
func sameCheckout(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(fa, fb)
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
