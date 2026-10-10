package api

// Phase-run baseline (phase-run outcomes plan, phase 1):
//
//	GET /api/phaseruns/report?from=YYYY-MM-DD&to=YYYY-MM-DD → 200 phasereport.Report
//
// Both edges are required and inclusive (from 00:00:00 UTC, to 23:59:59 UTC); a
// missing or malformed one is a 400. The body is internal/phasereport's Report
// verbatim — the same struct `swarmery phase-report --json` prints, so the
// Health tab and the CLI cannot disagree.

import (
	"errors"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasereport"
)

func (h *Handler) phaseRunsReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, err := phasereport.ParseWindow(q.Get("from"), q.Get("to"))
	if err != nil {
		if errors.Is(err, phasereport.ErrBadWindow) {
			writeClientErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, err)
		return
	}
	rep, err := phasereport.Build(h.DB, from, to)
	writeJSON(w, rep, err)
}
