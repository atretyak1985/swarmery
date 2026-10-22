package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
)

// maxDecisionNoteLen bounds the operator's free-text note on an answer.
const maxDecisionNoteLen = 2000

// answerPhaseDecision — POST /api/epics/{taskId}/phases/{phaseId}/decisions/{decisionId}
// {option: "allow-add-to-scope"|"reject", note?}. requireLocalOrigin.
//
// Answers a guard decision (the needs-you phase_decision item): appends
// `- **D-n** (stop) — … → …` to the phase doc's `## Decisions` and, for
// allow-add-to-scope, the decision's paths to the guard block's `allow:` list so
// the next run may edit them; then stamps the row decided (phaserun.AnswerGuardDecision).
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
	d, err := phaserun.AnswerGuardDecision(h.DB, taskID, phaseID, decisionID,
		phaserun.GuardAnswer{Option: body.Option, Note: body.Note}, time.Now())
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
