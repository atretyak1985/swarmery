package api

// Wave C (metrics branch): today-stats endpoint.
//
// Response shape is FROZEN by web/src/api/types.ts → StatsToday:
//   {sessions, active, tokens_in, tokens_out, cost_usd, errors}
// (snake_case, unlike the camelCase entity DTOs — do not "fix" this).
//
// The day-window aggregation helpers below are shared with the parity-wave
// /api/stats/overview endpoint (overview.go) — same window semantics, same
// cost NULL rule.

import (
	"database/sql"
	"log"
	"net/http"
	"time"
)

type statsTodayDTO struct {
	Sessions  int64    `json:"sessions"`
	Active    int64    `json:"active"`
	TokensIn  int64    `json:"tokens_in"`
	TokensOut int64    `json:"tokens_out"`
	CostUSD   *float64 `json:"cost_usd"`
	Errors    int64    `json:"errors"`
	// Test-run aggregates over the window (additive optional): null when the
	// window has NO test_run events, so the client can degrade the "Quality"
	// tile instead of showing a misleading zero.
	TestsPassed  *int64 `json:"tests_passed,omitempty"`
	TestsFailed  *int64 `json:"tests_failed,omitempty"`
	TestsSkipped *int64 `json:"tests_skipped,omitempty"`
}

// dayBounds converts a LOCAL-midnight day start into UTC bounds compared
// against the stored ISO-8601 UTC timestamps (lexicographic compare is safe
// for same-format RFC 3339 strings; bounds are rendered without a zone suffix
// so they sort correctly against both "…Z" and fractional-second forms).
func dayBounds(dayStart time.Time) (start, end string) {
	// Zone-suffix-free UTC bounds: "2026-07-12T00:00:00".
	const bound = "2006-01-02T15:04:05"
	return dayStart.UTC().Format(bound), dayStart.AddDate(0, 0, 1).UTC().Format(bound)
}

// windowAgg holds the aggregates for one [start, end) window.
type windowAgg struct {
	Sessions    int64
	TokensIn    int64
	TokensOut   int64
	CostUSD     *float64
	PricedTurns int64
	UsageTurns  int64
	Errors      int64
	// Test-run aggregates (summed from test_run event payloads). TestRuns is
	// the count of test_run events in the window — zero means "no test signal",
	// which callers map to a null Quality tile rather than a zero.
	TestRuns     int64
	TestsPassed  int64
	TestsFailed  int64
	TestsSkipped int64
}

// tests returns the test aggregates as nullable pointers: all nil when the
// window saw no test_run events (degrade signal), values otherwise.
func (a windowAgg) tests() (passed, failed, skipped *int64) {
	if a.TestRuns == 0 {
		return nil, nil, nil
	}
	p, f, s := a.TestsPassed, a.TestsFailed, a.TestsSkipped
	return &p, &f, &s
}

// windowAggregates computes the day-window aggregates shared by
// /api/stats/today and /api/stats/overview:
//
//   - Sessions: sessions STARTED inside the window.
//   - TokensIn/TokensOut: SUM over turns started inside the window (usage is
//     already deduplicated per API message at ingest, C1).
//   - CostUSD: SUM over the window's PRICED turns; NULL only if the window
//     has usage-bearing turns and none of them are priced (honesty rule — a
//     partial sum is still reported; callers may log the skipped count from
//     PricedTurns/UsageTurns).
//   - Errors: events with status='error' inside the window (covers both
//     api_error events and failed tool calls).
//
// Direct queries, no rollup tables — fine at MVP volumes (daily_rollups is
// Phase 6).
func (h *Handler) windowAggregates(start, end, projFilter string, projArgs []any) (windowAgg, error) {
	var a windowAgg
	args := append([]any{start, end}, projArgs...)

	// Sessions started inside the window.
	err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM sessions s JOIN projects p ON p.id = s.project_id
		WHERE s.started_at >= ? AND s.started_at < ?`+projFilter, args...).Scan(&a.Sessions)
	if err != nil {
		return a, err
	}

	// Token and cost aggregates over the window's turns.
	var costSum sql.NullFloat64
	err = h.DB.QueryRow(`
		SELECT COALESCE(SUM(t.tokens_in), 0),
		       COALESCE(SUM(t.tokens_out), 0),
		       SUM(t.cost_usd),
		       COUNT(t.cost_usd),
		       COALESCE(SUM(CASE WHEN t.tokens_in IS NOT NULL OR t.tokens_out IS NOT NULL
		                           OR t.tokens_cache_read IS NOT NULL OR t.tokens_cache_write IS NOT NULL
		                         THEN 1 ELSE 0 END), 0)
		FROM turns t
		JOIN sessions s ON s.id = t.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE t.started_at >= ? AND t.started_at < ?`+projFilter, args...).Scan(
		&a.TokensIn, &a.TokensOut, &costSum, &a.PricedTurns, &a.UsageTurns)
	if err != nil {
		return a, err
	}
	// SUM rule: NULL only if ALL usage-bearing turns are unpriced; otherwise
	// sum the priced ones (a partial sum beats lying with zero).
	if a.UsageTurns > 0 && a.PricedTurns == 0 {
		a.CostUSD = nil
	} else {
		v := 0.0
		if costSum.Valid {
			v = costSum.Float64
		}
		a.CostUSD = &v
	}

	// Errors: api_error events and failed tool calls both carry status='error'.
	err = h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.status = 'error' AND e.ts >= ? AND e.ts < ?`+projFilter, args...).Scan(&a.Errors)
	if err != nil {
		return a, err
	}

	// Test runs: sum passed/failed/skipped from test_run event payloads (emitted
	// at ingest for recognised test-runner Bash calls). TestRuns==0 → no signal.
	err = h.DB.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(json_extract(e.payload, '$.passed')), 0),
		       COALESCE(SUM(json_extract(e.payload, '$.failed')), 0),
		       COALESCE(SUM(json_extract(e.payload, '$.skipped')), 0)
		FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.type = 'test_run' AND e.ts >= ? AND e.ts < ?`+projFilter, args...).Scan(
		&a.TestRuns, &a.TestsPassed, &a.TestsFailed, &a.TestsSkipped)
	return a, err
}

// activeSessions counts currently-active sessions (not day-scoped: a session
// started yesterday that is still running counts here).
func (h *Handler) activeSessions(projFilter string, projArgs []any) (int64, error) {
	var n int64
	err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM sessions s JOIN projects p ON p.id = s.project_id
		WHERE s.status = 'active'`+projFilter, projArgs...).Scan(&n)
	return n, err
}

// GET /api/stats/today?project=<slug|id>
//
// "Today" is the LOCAL calendar day (see dayBounds). Aggregate semantics are
// documented on windowAggregates; "active" is documented on activeSessions.
func (h *Handler) statsToday(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start, end := dayBounds(dayStart)

	projFilter := ""
	projArgs := []any{}
	if project := r.URL.Query().Get("project"); project != "" {
		projFilter = ` AND (p.slug = ? OR CAST(p.id AS TEXT) = ?)`
		projArgs = []any{project, project}
	}

	agg, err := h.windowAggregates(start, end, projFilter, projArgs)
	if err != nil {
		writeErr(w, err)
		return
	}
	if skipped := agg.UsageTurns - agg.PricedTurns; agg.CostUSD != nil && skipped > 0 {
		log.Printf("warn: stats/today: %d of %d usage-bearing turns unpriced (unknown model) — cost_usd is a partial sum", skipped, agg.UsageTurns)
	}

	active, err := h.activeSessions(projFilter, projArgs)
	if err != nil {
		writeErr(w, err)
		return
	}

	passed, failed, skipped := agg.tests()
	writeJSON(w, statsTodayDTO{
		Sessions:     agg.Sessions,
		Active:       active,
		TokensIn:     agg.TokensIn,
		TokensOut:    agg.TokensOut,
		CostUSD:      agg.CostUSD,
		Errors:       agg.Errors,
		TestsPassed:  passed,
		TestsFailed:  failed,
		TestsSkipped: skipped,
	}, nil)
}
