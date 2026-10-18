// Turn forecasts (mods-swarmery-link phase 10): the read side of the link's
// turn.forecast / turn.actual events. The write side is POST /api/link/events
// (link.go → internal/link → internal/turnforecast); this file serves the
// Insights calibration and hydrates the turn_surprise WS frame.
//
// GET /api/insights/turn-forecasts is a read-only GET and, like every other
// analytics/retro GET, is not behind requireLocalOrigin (the Host fence in
// routes.go still applies).
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/turnforecast"
)

const (
	turnForecastDefaultDays = 14
	turnForecastMaxDays     = 90
	turnForecastTopN        = 10
)

// turnForecastsDTO is the GET /api/insights/turn-forecasts body.
type turnForecastsDTO struct {
	Days              int                 `json:"days"`
	N                 int                 `json:"n"`
	TooFew            bool                `json:"tooFew"`
	Bins              []turnforecast.Bin  `json:"bins"`
	Top               []turnforecast.Turn `json:"top"`
	AvgForecastTokens *float64            `json:"avgForecastTokens"`
}

// turnForecastDays parses ?days: absent or unparseable → 14, then clamped to
// [1, 90].
func turnForecastDays(r *http.Request) int {
	days := turnForecastDefaultDays
	if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	return min(max(days, 1), turnForecastMaxDays)
}

// GET /api/insights/turn-forecasts?projectId=&days=14 — the calibration of the
// window's scored turns (confidence bins × hit rate, hit = surprise < 0.25)
// and its 10 most surprising turns. Advisory: no thresholds, no alerts.
//
//	200 turnForecastsDTO
//	400 projectId is not an integer
func (h *Handler) turnForecasts(w http.ResponseWriter, r *http.Request) {
	var projectID *int64
	if v := strings.TrimSpace(r.URL.Query().Get("projectId")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeClientErr(w, http.StatusBadRequest, "projectId must be an integer")
			return
		}
		projectID = &id
	}
	days := turnForecastDays(r)
	since := time.Now().UTC().AddDate(0, 0, -days)

	st := &turnforecast.Store{DB: h.DB}
	cal, err := st.Calibration(r.Context(), projectID, since)
	if err != nil {
		writeErr(w, err)
		return
	}
	top, err := st.TopSurprises(r.Context(), projectID, since, turnForecastTopN)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, turnForecastsDTO{
		Days: days, N: cal.N, TooFew: cal.TooFew, Bins: cal.Bins, Top: top,
		AvgForecastTokens: cal.AvgForecastTokens,
	}, nil)
}

// turnSurprisePayload hydrates a turn_surprise frame from the stored turn.
// (nil, nil) when the row is gone or no longer scored.
func (h *Handler) turnSurprisePayload(id int64) (*turnforecast.Turn, error) {
	return (&turnforecast.Store{DB: h.DB}).LoadTurn(context.Background(), id)
}
