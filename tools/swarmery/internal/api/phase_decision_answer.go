package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// maxDecisionNoteLen bounds the operator's free-text note on an answer.
const maxDecisionNoteLen = 2000

// answerPhaseDecision — POST /api/epics/{taskId}/phases/{phaseId}/decisions/{decisionId}
// {option: "allow-add-to-scope"|"reject", note?}. requireLocalOrigin.
//
// Answers a review decision (source 'review', verify-not-trust phase 9) with
// {option: "repeat-review"|"accept-as-is"}: repeat-review admits one more review
// of the run branch FIRST (a refusal answers with its code and leaves the
// decision open), then records the answer and starts the review in the
// background; accept-as-is records it and lifts the review-incomplete hold. Both
// append `- **D-n** (review) — … → …` to the phase doc (phaserun.AnswerReviewDecision).
//
// Answers a guard decision (the needs-you phase_decision item): appends
// `- **D-n** (stop) — … → …` to the phase doc's `## Decisions` and, for
// allow-add-to-scope, the decision's paths to the guard block's `allow:` list so
// the next run may edit them; then stamps the row decided (phaserun.AnswerGuardDecision).
//
// Answers an agent-declared stop (verify-not-trust phase 10, the item's
// `trigger` is set) with {option: <one of the stop's option ids>, note?} or
// {option: "other", note: <the operator's text>}: appends
// `- **D-n** (stop: <trigger>) — <reason> → <option id | other: text>` and, for a
// protected-path stop answered allow-add-to-scope, the paths to `allow:`.
//
// 200 the decided row; 400 a bad body or an unknown option; 404 an unknown
// phase or decision; 409 a decision already answered (or not a guard decision),
// or a phase whose run is still in flight — the run edits its own doc.
func (h *Handler) answerPhaseDecision(w http.ResponseWriter, r *http.Request) {
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	taskID, _ := strconv.ParseInt(r.PathValue("taskId"), 10, 64) // validated by parsePhaseRunParams
	decisionID := strings.TrimSpace(r.PathValue("decisionId"))
	var body struct {
		Option string `json:"option"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "body must be {option, note?}")
		return
	}
	if len(body.Note) > maxDecisionNoteLen {
		writeClientErr(w, http.StatusBadRequest, "note is too long")
		return
	}
	if phaserunSvc != nil && phaserunSvc.InFlight(phaseID) {
		writeClientErr(w, http.StatusConflict, "the phase is running — answer once its run has finished")
		return
	}
	ans := phaserun.GuardAnswer{Option: body.Option, Note: body.Note}
	var (
		d   store.Decision
		err error
	)
	if existing, gerr := store.GetTaskDecision(h.DB, taskID, decisionID); gerr == nil && existing.Source == store.DecisionSourceReview {
		var written bool
		d, written, err = h.answerReviewDecision(w, taskID, phaseID, decisionID, ans)
		if written {
			return // a refused repeat review, already answered
		}
	} else {
		d, err = phaserun.AnswerGuardDecision(h.DB, taskID, phaseID, decisionID, ans, time.Now())
	}
	switch {
	case errors.Is(err, phaserun.ErrDecisionBadOption):
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, phaserun.ErrDecisionNotFound):
		writeClientErr(w, http.StatusNotFound, "decision not found")
		return
	case errors.Is(err, phaserun.ErrDecisionNotOpen):
		writeClientErr(w, http.StatusConflict, "the decision is already answered")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	publishPlanUpdated(taskID)
	writeJSON(w, d, nil)
}

// answerReviewDecision answers one review decision. For repeat-review the review
// is admitted before the answer is recorded: a refusal (no run branch, a run in
// flight, the reviewer not attached, …) is written here as the refusal it is —
// written=true — leaving the decision open to be answered again.
func (h *Handler) answerReviewDecision(w http.ResponseWriter, taskID, phaseID int64, decisionID string, ans phaserun.GuardAnswer) (d store.Decision, written bool, err error) {
	if !phaserun.ValidReviewOption(ans.Option) {
		return store.Decision{}, false, phaserun.ErrDecisionBadOption
	}
	var start, abort func()
	if strings.TrimSpace(ans.Option) == phaserun.ReviewOptionRepeat {
		if phaserunSvc == nil {
			writeClientErr(w, http.StatusServiceUnavailable, "phase runs are not attached — the review cannot be repeated")
			return store.Decision{}, true, nil
		}
		var err error
		start, abort, err = phaserunSvc.AdmitRepeatReviewAbortable(phaseID)
		if err != nil {
			writeReverifyRefusal(w, err)
			return store.Decision{}, true, nil
		}
	}
	d, err = phaserun.AnswerReviewDecision(h.DB, taskID, phaseID, decisionID, ans, time.Now())
	if err != nil {
		if abort != nil {
			abort()
		}
		return store.Decision{}, false, err
	}
	if start != nil {
		go start()
	}
	return d, false, nil
}
