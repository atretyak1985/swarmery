package api

// GET /api/epics/{taskId}/evidence (verify-not-trust phase 12, D19) — the plan's
// decision → phase → evidence table: every declared decision (`D-n`) and spec
// criterion (`SC-n`), the phases that claim it, the Completion Report / plan
// review evidence for it, and its chip (evidence | remaining | not-implemented |
// not-verified). Built by wsingest.LoadDecisionTable, the same table the
// change-request body and the plan review prompt read. Empty rows for a plan
// without `## Decisions` and without a spec: old plans are not rewritten.

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// getEpicEvidence → 200 wsingest.DecisionTable; 404 unknown task.
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
	writeJSON(w, t, nil)
}
