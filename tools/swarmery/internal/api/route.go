package api

// Complexity routing (phase 3): the shadow data made readable.
//
//	GET /api/route/report?surface=dispatch|phaserun&days=30 → route.ReportResult
//	     settles up to route.SettleBatch pending rows first, then groups the
//	     window by tier, by the model that ran, and — for shadow rows whose pick
//	     differed — by (tier, pick, ran). Groups under route.MinSamples are NOT
//	     in the response; only their count is (hiddenGroups / hiddenRuns).
//	GET /api/route/decision?subject=task:<id>|phase:<id> → route.DecisionView
//	     the subject's newest decision; 404 when it has none.
//
// Both are reads. Errors: 400 a bad surface, days or subject.

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// routeReportDays is the default window; routeReportMaxDays caps it.
const (
	routeReportDays    = 30
	routeReportMaxDays = 3650
)

var routeSubjectRe = regexp.MustCompile(`^(task|phase):[1-9][0-9]*$`)

func (h *Handler) routeReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	surface := q.Get("surface")
	switch route.Surface(surface) {
	case "", route.SurfaceDispatch, route.SurfacePhaseRun:
	default:
		writeClientErr(w, http.StatusBadRequest, "surface must be dispatch or phaserun")
		return
	}
	days := routeReportDays
	if s := q.Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > routeReportMaxDays {
			writeClientErr(w, http.StatusBadRequest, "days must be an integer from 1 to 3650")
			return
		}
		days = n
	}
	// Settling is best effort: a failure leaves yesterday's outcomes in place,
	// which is still a correct (if staler) report.
	if n, err := route.Settle(h.DB); err != nil {
		log.Printf("warn: api: route settle: %v", err)
	} else if n > 0 {
		log.Printf("route: settled %d decision row(s)", n)
	}
	rep, err := route.Report(h.DB, surface, days)
	writeJSON(w, rep, err)
}

func (h *Handler) routeDecision(w http.ResponseWriter, r *http.Request) {
	subject := r.URL.Query().Get("subject")
	if !routeSubjectRe.MatchString(subject) {
		writeClientErr(w, http.StatusBadRequest, "subject must be task:<id> or phase:<id>")
		return
	}
	v, err := route.LatestDecision(h.DB, subject)
	if errors.Is(err, route.ErrNoDecision) {
		writeClientErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, v, err)
}
