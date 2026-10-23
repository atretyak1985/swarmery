package api

// GET /api/epics/{taskId}/evidence → 200 epicEvidenceResponse (verify-not-trust
// phase 12, D19): the plan's decision → phase → evidence table, one row per
// declared decision (D-n) and spec criterion (SC-n), each with its chip
// (evidence | remaining | not-implemented | not-verified), the phases claiming
// it and the evidence rows behind the chip. 404 for an unknown workspace task.
// The same table rides on every epic of GET /api/epics as `evidence`.

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// epicEvidenceResponse is the endpoint's body: the table, keyed by its task.
type epicEvidenceResponse struct {
	TaskID int64 `json:"taskId"`
	wsingest.DecisionTable
}

func (h *Handler) getEpicEvidence(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	var one int
	err := h.DB.QueryRow(`SELECT 1 FROM tasks WHERE id = ? AND source = 'workspace'`, taskID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "workspace task not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := wsingest.LoadDecisionTable(h.DB, taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, epicEvidenceResponse{TaskID: taskID, DecisionTable: t}, nil)
}
