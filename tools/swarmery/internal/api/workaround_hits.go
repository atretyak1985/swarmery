package api

import (
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

// Workaround hits (verify-not-trust phase 5, D23): the places where a phase's
// agent said it changed course — "instead", "for now", "pre-existing", "поки
// що" — with one sentence of context each side. The phase card lists them and
// the run-plan controller hands them to its reviewer as "look first". A hint,
// never a gate.

// listWorkaroundHits is GET /api/epics/{taskId}/phases/{phaseId}/workaround-hits:
// every session's hits on the phase in transcript order, and the total including
// the key-only ones past the per-session cap. ?session=<uuid> narrows to one run.
func (h *Handler) listWorkaroundHits(w http.ResponseWriter, r *http.Request) {
	taskID, phaseID, ok := parseLandingParams(w, r)
	if !ok {
		return
	}
	if _, ok := h.loadReopenTarget(w, taskID, phaseID); !ok {
		return
	}
	hits, err := workaround.ForPhase(h.DB, phaseID, r.URL.Query().Get("session"))
	writeJSON(w, hits, err)
}

// phaseWorkaroundHits is the epic payload's batch read: one query for the whole
// plan, after the phases cursor is closed. Like phaseReopens it degrades to an
// empty map — the list decorates the Plans page and must never take it down.
func (h *Handler) phaseWorkaroundHits(taskID int64) map[int64]workaround.PhaseHits {
	out, err := workaround.ForTask(h.DB, taskID)
	if err != nil {
		return map[int64]workaround.PhaseHits{}
	}
	return out
}
