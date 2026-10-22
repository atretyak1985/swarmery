package phaserun

import (
	"log"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// The bound on automatic review rounds (D42) and the hand-over to the operator.
//
// A review cycle is: round 1 after the run; on FAIL, ONE fix re-run and round 2
// after it. That is all the daemon does on its own. A review that is incomplete
// (D20) at any round, or still failing after round 2, ends the cycle: the
// daemon opens a decision (source `review`, options repeat-review /
// accept-as-is) and review_fix_round is parked at maxAutoReviewRounds, so
// nothing automatic follows. A third review is only ever the operator's — the
// repeat-review answer, AdmitRepeatReview.

// maxAutoReviewRounds is how many reviews the daemon runs on its own per cycle.
const maxAutoReviewRounds = 2

// Review decision vocabulary — the decision row's source and its two options.
const (
	ReviewDecisionSource = "review"
	ReviewOptionRepeat   = "repeat-review"
	ReviewOptionAccept   = "accept-as-is"
)

// ReviewDecision is the operator decision a review cycle hands over.
type ReviewDecision struct {
	PhaseID         int64
	WorkspaceTaskID int64
	DocPath         string
	// Round is the review round that ended the cycle.
	Round int
	// Reason is why: an incomplete review's reason (incompleteReason) or "the
	// review still fails after round N".
	Reason  string
	Source  string   // ReviewDecisionSource
	Options []string // ReviewOptionRepeat, ReviewOptionAccept
}

// ReviewDecider records a ReviewDecision as an open operator decision (the
// phase_decisions row and its needs-you item). Answering repeat-review calls
// AdmitRepeatReview; accept-as-is records the acceptance and clears the hold.
type ReviewDecider interface {
	OpenReviewDecision(ReviewDecision) error
}

// handOverReview ends the phase's review cycle: no more automatic rounds, and
// the decision opened for the operator.
func (s *Service) handOverReview(phaseID int64, info phaseInfo, round int, reason string) {
	s.setReviewFixRound(phaseID, maxAutoReviewRounds)
	d := ReviewDecision{
		PhaseID: phaseID, WorkspaceTaskID: info.WorkspaceTaskID, DocPath: info.DocPath,
		Round: round, Reason: reason, Source: ReviewDecisionSource,
		Options: []string{ReviewOptionRepeat, ReviewOptionAccept},
	}
	if s.ReviewDecisions == nil {
		log.Printf("phaserun: phase=%d review handed over at round %d: %s — no decision sink is wired", phaseID, round, reason)
		return
	}
	if err := s.ReviewDecisions.OpenReviewDecision(d); err != nil {
		log.Printf("error: phaserun: phase=%d review decision (round %d) could not be opened: %v", phaseID, round, err)
	}
}

// Repeat-review refusal codes, beside the reverify ones the shared admission
// (admitOnRunBranch) answers with.
const (
	ReviewCodeOff      = "review-off"
	ReviewCodeNotWired = "reviewer-not-attached"
)

var opRepeatReview = runBranchOp{gerund: "repeating the review", participle: "reviewed again", noun: "review", again: "repeat the review"}

// AdmitRepeatReview is the repeat-review answer: one more review of the phase's
// run branch as it stands, outside any run. It counts as manual — it never
// starts a fix re-run and never uses the round-2 brief — and its outcome is
// handled like any round's: a PASS closes the cycle (and, being a complete
// round, lifts the doc's hold), a FAIL or an incomplete review hands over again.
//
// Admission mirrors AdmitReverify: refusals are *ReverifyRefusal; on success the
// phase's run slot is held and start runs the review on a checkout of the run
// branch, then releases both.
func (s *Service) AdmitRepeatReview(phaseID int64) (start func(), err error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return nil, err
	}
	switch {
	case info.ReviewMode != wsingest.ReviewOn:
		return nil, refuseReverify(http.StatusUnprocessableEntity, ReviewCodeOff,
			"this phase does not ask to be reviewed (its doc has no **Review:** on line)")
	case info.RunBranch == "":
		return nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			"the phase has no recorded run branch to review")
	case info.RunState == "running":
		return nil, refuseReverify(http.StatusConflict, ReverifyCodeAlreadyRunning,
			"a run is already active for this phase")
	case s.Review == nil:
		return nil, refuseReverify(http.StatusServiceUnavailable, ReviewCodeNotWired,
			"the phase reviewer is not attached")
	}
	if busy, err := s.planRunActive(info.WorkspaceTaskID); err != nil {
		return nil, err
	} else if busy {
		return nil, refuseReverify(http.StatusConflict, ReverifyCodePlanRunning,
			"a plan run is active for this plan — wait for it or cancel it before repeating the review")
	}
	return s.admitOnRunBranch(phaseID, info, opRepeatReview, func(acq worktree.Acquired) {
		// AcquireExisting reports the branch TIP as the start point; the review's
		// diff needs the run's recorded one (as reverifyBody's does).
		acq.StartPoint = info.RunStartPoint
		fixRound, runUUID := s.reviewState(phaseID)
		s.reviewCycle(phaseID, info, acq, fixRound, runUUID, true)
		s.notify(info.WorkspaceTaskID)
	})
}
