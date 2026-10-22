package phaserun

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
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

// ReviewDecider records a ReviewDecision as an open operator decision. The
// daemon's own is raiseReviewDecision (a source='review' phase_decisions row the
// needs-you queue lists as kind phase_decision); the interface is the seam a
// test replaces it through.
type ReviewDecider interface {
	OpenReviewDecision(ReviewDecision) error
}

// reviewQuestion is the review decision's question.
func reviewQuestion(d ReviewDecision) string {
	return fmt.Sprintf("Review round %d did not settle: %s. Repeat the review or accept it as is?", d.Round, d.Reason)
}

// raiseReviewDecision writes (or refreshes) the phase's open review decision —
// one per phase, continuing the plan's D-n sequence (store.UpsertReviewDecision).
func (s *Service) raiseReviewDecision(d ReviewDecision) error {
	row, created, err := store.UpsertReviewDecision(s.DB, d.WorkspaceTaskID, d.PhaseID, reviewQuestion(d), d.Options, s.ts())
	if err != nil {
		return err
	}
	verb := "refreshed"
	if created {
		verb = "raised"
	}
	log.Printf("phaserun: phase=%d review decision %s %s: %s", d.PhaseID, row.DecisionID, verb, row.Question)
	s.notify(d.WorkspaceTaskID)
	return nil
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
	open := s.raiseReviewDecision
	if s.ReviewDecisions != nil {
		open = s.ReviewDecisions.OpenReviewDecision
	}
	if err := open(d); err != nil {
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
	start, _, err = s.AdmitRepeatReviewAbortable(phaseID)
	return start, err
}

// AdmitRepeatReviewAbortable is AdmitRepeatReview with the way back: abort
// releases what admission holds (the slot, the branch claim, a checkout it cut)
// without reviewing — for a caller that admits first and may then fail to record
// the answer that asked for the review. Call exactly one of start and abort.
func (s *Service) AdmitRepeatReviewAbortable(phaseID int64) (start, abort func(), err error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case info.ReviewMode != wsingest.ReviewOn:
		return nil, nil, refuseReverify(http.StatusUnprocessableEntity, ReviewCodeOff,
			"this phase does not ask to be reviewed (its doc has no **Review:** on line)")
	case info.RunBranch == "":
		return nil, nil, refuseReverify(http.StatusUnprocessableEntity, ReverifyCodeNoRunBranch,
			"the phase has no recorded run branch to review")
	case info.RunState == "running":
		return nil, nil, refuseReverify(http.StatusConflict, ReverifyCodeAlreadyRunning,
			"a run is already active for this phase")
	case s.Review == nil:
		return nil, nil, refuseReverify(http.StatusServiceUnavailable, ReviewCodeNotWired,
			"the phase reviewer is not attached")
	}
	if busy, err := s.planRunActive(info.WorkspaceTaskID); err != nil {
		return nil, nil, err
	} else if busy {
		return nil, nil, refuseReverify(http.StatusConflict, ReverifyCodePlanRunning,
			"a plan run is active for this plan — wait for it or cancel it before repeating the review")
	}
	return s.admitOnRunBranchAbortable(phaseID, info, opRepeatReview, func(acq worktree.Acquired) {
		// AcquireExisting reports the branch TIP as the start point; the review's
		// diff needs the run's recorded one (as reverifyBody's does).
		acq.StartPoint = info.RunStartPoint
		fixRound, runUUID := s.reviewState(phaseID)
		s.reviewCycle(phaseID, info, acq, fixRound, runUUID, true, GuardResult{})
		s.notify(info.WorkspaceTaskID)
	})
}

// Review decision answers, as recorded on the row and in the doc's `## Decisions`.
const (
	ReviewAnswerRepeat = "repeat the review"
	ReviewAnswerAccept = "accept as is"
)

// ValidReviewOption reports whether option is one a review decision offers.
func ValidReviewOption(option string) bool {
	option = strings.TrimSpace(option)
	return option == ReviewOptionRepeat || option == ReviewOptionAccept
}

// AnswerReviewDecision records the operator's answer to a review decision of
// phase phaseID: the `- **D-n** (review) — … → …` line under the phase doc's
// `## Decisions`, then the row stamped decided. accept-as-is is what lifts the
// review-incomplete hold (phasegate reads the latest review decision);
// repeat-review only records the answer — the caller runs the review it asked
// for (AdmitRepeatReviewAbortable), admitted BEFORE this call so a refused
// review leaves the decision open. The doc is written before the row, as in
// AnswerGuardDecision. Errors: ErrDecisionNotFound, ErrDecisionNotOpen,
// ErrDecisionBadOption.
func AnswerReviewDecision(db *sql.DB, taskID, phaseID int64, decisionID string, ans GuardAnswer, now time.Time) (store.Decision, error) {
	answerMu.Lock()
	defer answerMu.Unlock()
	d, err := store.GetTaskDecision(db, taskID, decisionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (d.PhaseID == nil || *d.PhaseID != phaseID)) {
		return store.Decision{}, ErrDecisionNotFound
	}
	if err != nil {
		return store.Decision{}, err
	}
	if d.Source != store.DecisionSourceReview || d.Status != store.DecisionUndecided {
		return store.Decision{}, ErrDecisionNotOpen
	}
	ans.Option = strings.TrimSpace(ans.Option)
	if !ValidReviewOption(ans.Option) {
		return store.Decision{}, fmt.Errorf("%w %q (want %s or %s)", ErrDecisionBadOption, ans.Option, ReviewOptionRepeat, ReviewOptionAccept)
	}
	var docPath string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id = ? AND workspace_task_id = ?`, phaseID, taskID).
		Scan(&docPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.Decision{}, ErrDecisionNotFound
		}
		return store.Decision{}, err
	}
	answer := ReviewAnswerRepeat
	if ans.Option == ReviewOptionAccept {
		answer = ReviewAnswerAccept
	}
	if note := strings.Join(strings.Fields(ans.Note), " "); note != "" {
		answer += " — " + note
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return store.Decision{}, fmt.Errorf("read phase doc: %w", err)
	}
	doc := wsingest.AppendPhaseDecision(string(body),
		wsingest.FormatPhaseDecisionLine(d.Seq, store.DecisionSourceReview, d.Question, answer))
	if err := wsingest.WriteDocAtomic(docPath, doc); err != nil {
		return store.Decision{}, fmt.Errorf("write phase doc: %w", err)
	}
	ok, err := store.DecideDecision(db, d.ID, answer, now.UTC().Format(time.RFC3339))
	if err != nil {
		return store.Decision{}, err
	}
	if !ok {
		return store.Decision{}, ErrDecisionNotOpen
	}
	return store.GetDecision(db, d.ID)
}
