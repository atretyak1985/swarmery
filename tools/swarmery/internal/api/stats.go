package api

// Wave C (metrics branch): today-stats endpoint.
//
// Response shape is FROZEN by web/src/api/types.ts → StatsToday:
//   {sessions, active, tokens_in, tokens_out, cost_usd, errors}
// (snake_case, unlike the camelCase entity DTOs — do not "fix" this).

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
}

// GET /api/stats/today?project=<slug|id>
//
// "Today" is the LOCAL calendar day, converted to UTC bounds and compared
// against the stored ISO-8601 UTC timestamps (lexicographic compare is safe
// for same-format RFC 3339 strings; bounds are rendered without a zone suffix
// so they sort correctly against both "…Z" and fractional-second forms).
//
// Semantics:
//   - sessions: sessions STARTED today.
//   - active:   sessions currently active (regardless of start day) — a
//     session started yesterday that is still running counts here.
//   - tokens_in/tokens_out: SUM over turns started today (usage is already
//     deduplicated per API message at ingest, C1).
//   - cost_usd: SUM over today's PRICED turns; NULL only if today has
//     usage-bearing turns and none of them are priced (honesty rule — a
//     partial sum is reported and the skipped count is logged).
//   - errors: events with status='error' today (covers both api_error events
//     and failed tool calls).
//
// Direct queries, no rollup tables — fine at MVP volumes (daily_rollups is
// Phase 6).
func (h *Handler) statsToday(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// Zone-suffix-free UTC bounds: "2026-07-12T00:00:00".
	const bound = "2006-01-02T15:04:05"
	start := dayStart.UTC().Format(bound)
	end := dayStart.AddDate(0, 0, 1).UTC().Format(bound)

	projFilter := ""
	projArgs := []any{}
	if project := r.URL.Query().Get("project"); project != "" {
		projFilter = ` AND (p.slug = ? OR CAST(p.id AS TEXT) = ?)`
		projArgs = []any{project, project}
	}

	var s statsTodayDTO

	// Sessions started today.
	err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM sessions s JOIN projects p ON p.id = s.project_id
		WHERE s.started_at >= ? AND s.started_at < ?`+projFilter,
		append([]any{start, end}, projArgs...)...).Scan(&s.Sessions)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Currently active sessions (not day-scoped: still-running counts).
	err = h.DB.QueryRow(`
		SELECT COUNT(*) FROM sessions s JOIN projects p ON p.id = s.project_id
		WHERE s.status = 'active'`+projFilter, projArgs...).Scan(&s.Active)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Token and cost aggregates over today's turns.
	var costSum sql.NullFloat64
	var pricedTurns, usageTurns int64
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
		WHERE t.started_at >= ? AND t.started_at < ?`+projFilter,
		append([]any{start, end}, projArgs...)...).Scan(
		&s.TokensIn, &s.TokensOut, &costSum, &pricedTurns, &usageTurns)
	if err != nil {
		writeErr(w, err)
		return
	}
	// SUM rule: NULL only if ALL usage-bearing turns are unpriced; otherwise
	// sum the priced ones and log how many were skipped.
	if usageTurns > 0 && pricedTurns == 0 {
		s.CostUSD = nil
	} else {
		v := 0.0
		if costSum.Valid {
			v = costSum.Float64
		}
		s.CostUSD = &v
		if skipped := usageTurns - pricedTurns; skipped > 0 {
			log.Printf("warn: stats/today: %d of %d usage-bearing turns unpriced (unknown model) — cost_usd is a partial sum", skipped, usageTurns)
		}
	}

	// Errors today: api_error events and failed tool calls both carry status='error'.
	err = h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.status = 'error' AND e.ts >= ? AND e.ts < ?`+projFilter,
		append([]any{start, end}, projArgs...)...).Scan(&s.Errors)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, s, nil)
}
