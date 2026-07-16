package api

// Projects health (multi-project UX): GET /api/projects/health — one row per
// non-archived project comparing week-over-week cost, the 7-day tool error
// rate, the 7-day average session duration, and last activity. Camel-case
// JSON like the other /api/projects surfaces. Rolling windows: "week" is
// [now-7d, now), "prev week" is [now-14d, now-7d), both rendered in the same
// zone-suffix-free UTC bound format as dayBounds (stats.go) so they compare
// lexicographically against the stored ISO timestamps. All aggregates are
// single grouped passes merged in Go — no per-project queries (no N+1).
//
// Retention assumption: this endpoint reads LIVE tables only (turns, events,
// sessions), never daily_rollups, so it silently assumes prune retention is
// at least 14 days. With a shorter retention the prev-week window is empty
// and costPrevWeekUsd goes null here while Analytics still shows that week's
// cost from rollups.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

type projectHealthDTO struct {
	ID     int64    `json:"id"`
	Slug   string   `json:"slug"`
	Name   *string  `json:"name"`
	Pinned bool     `json:"pinned"`
	Tags   []string `json:"tags"`
	// Σ turns.cost_usd per rolling week; null when the window has no priced
	// turn (honesty rule — never a lying zero).
	CostWeekUSD     *float64 `json:"costWeekUsd"`
	CostPrevWeekUSD *float64 `json:"costPrevWeekUsd"`
	// error tool_calls / total tool_calls over the last 7 days; null when the
	// window has no tool_call events at all.
	ErrorRate *float64 `json:"errorRate"`
	// Mean wall-clock duration (ms) of sessions started in the last 7 days
	// that have ended; null when none ended yet.
	AvgSessionMs *int64  `json:"avgSessionMs"`
	LastActivity *string `json:"lastActivity"`
}

// healthBoundFmt mirrors dayBounds' zone-suffix-free UTC bound form.
const healthBoundFmt = "2006-01-02T15:04:05"

// projectsHealth handles GET /api/projects/health.
func (h *Handler) projectsHealth(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	weekStart := now.AddDate(0, 0, -7).Format(healthBoundFmt)
	prevStart := now.AddDate(0, 0, -14).Format(healthBoundFmt)
	end := now.Format(healthBoundFmt)

	// Base rows: every non-archived project, pinned first (list parity).
	rows, err := h.DB.Query(`
		SELECT id, slug, name, pinned, tags, last_activity
		FROM projects WHERE archived = 0
		ORDER BY pinned DESC, last_activity DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []projectHealthDTO{}
	index := map[int64]int{}
	for rows.Next() {
		var p projectHealthDTO
		var pinned int
		var tagsRaw string
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &pinned, &tagsRaw, &p.LastActivity); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		p.Pinned = pinned != 0
		p.Tags = []string{}
		if err := json.Unmarshal([]byte(tagsRaw), &p.Tags); err != nil || p.Tags == nil {
			p.Tags = []string{}
		}
		index[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Cost per project, BOTH windows in one grouped pass. COUNT(cost_usd)
	// counts priced turns only — null (not 0) when nothing in a window is priced.
	rows, err = h.DB.Query(`
		SELECT s.project_id,
		       SUM(CASE WHEN t.started_at >= ? THEN t.cost_usd END),
		       COUNT(CASE WHEN t.started_at >= ? THEN t.cost_usd END),
		       SUM(CASE WHEN t.started_at < ? THEN t.cost_usd END),
		       COUNT(CASE WHEN t.started_at < ? THEN t.cost_usd END)
		FROM turns t
		JOIN sessions s ON s.id = t.session_id
		WHERE t.started_at >= ? AND t.started_at < ?
		GROUP BY s.project_id`,
		weekStart, weekStart, weekStart, weekStart, prevStart, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var pid, pricedWeek, pricedPrev int64
		var costWeek, costPrev sql.NullFloat64
		if err := rows.Scan(&pid, &costWeek, &pricedWeek, &costPrev, &pricedPrev); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		i, ok := index[pid]
		if !ok {
			continue // archived project — not in the response
		}
		if pricedWeek > 0 && costWeek.Valid {
			v := costWeek.Float64
			out[i].CostWeekUSD = &v
		}
		if pricedPrev > 0 && costPrev.Valid {
			v := costPrev.Float64
			out[i].CostPrevWeekUSD = &v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Error rate: error tool_calls / total tool_calls, last 7 days.
	rows, err = h.DB.Query(`
		SELECT s.project_id,
		       COUNT(*),
		       SUM(CASE WHEN e.status = 'error' THEN 1 ELSE 0 END)
		FROM events e
		JOIN sessions s ON s.id = e.session_id
		WHERE e.type = 'tool_call' AND e.ts >= ? AND e.ts < ?
		GROUP BY s.project_id`, weekStart, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var pid, total, errored int64
		if err := rows.Scan(&pid, &total, &errored); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		if i, ok := index[pid]; ok && total > 0 {
			rate := float64(errored) / float64(total)
			out[i].ErrorRate = &rate
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Avg session duration: sessions STARTED in the window that have ended.
	rows, err = h.DB.Query(`
		SELECT s.project_id,
		       CAST(AVG((julianday(s.ended_at) - julianday(s.started_at)) * 86400000.0) AS INTEGER)
		FROM sessions s
		WHERE s.ended_at IS NOT NULL AND s.started_at >= ? AND s.started_at < ?
		GROUP BY s.project_id`, weekStart, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var pid int64
		var avgMs sql.NullInt64
		if err := rows.Scan(&pid, &avgMs); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		if i, ok := index[pid]; ok && avgMs.Valid {
			v := avgMs.Int64
			out[i].AvgSessionMs = &v
		}
	}
	rows.Close()
	writeJSON(w, out, rows.Err())
}
