package api

// Parity wave: /api/stats/overview — the dashboard overview for one LOCAL
// calendar day. Response shape is FROZEN by the parity contract (snake_case).
//
// Scalar fields share the exact day-window and cost NULL-rule semantics of
// /api/stats/today (windowAggregates in stats.go); "active" counts
// currently-active sessions only when the requested day is today, else 0.

import (
	"net/http"
	"time"
)

const dayFmt = "2006-01-02"

type seriesPointDTO struct {
	Day      string   `json:"day"`
	Sessions int64    `json:"sessions"`
	Tokens   int64    `json:"tokens"`
	CostUSD  *float64 `json:"cost_usd"`
	Errors   int64    `json:"errors"`
}

type projectErrorsDTO struct {
	Slug   string  `json:"slug"`
	Name   *string `json:"name"`
	Errors int64   `json:"errors"`
}

type modelCostDTO struct {
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd"`
}

type projectSessionsDTO struct {
	Slug     string  `json:"slug"`
	Name     *string `json:"name"`
	Sessions int64   `json:"sessions"`
}

type statsOverviewDTO struct {
	Day             string               `json:"day"`
	Sessions        int64                `json:"sessions"`
	Active          int64                `json:"active"`
	WaitingApproval int64                `json:"waiting_approval"`
	TokensIn        int64                `json:"tokens_in"`
	TokensOut       int64                `json:"tokens_out"`
	CostUSD         *float64             `json:"cost_usd"`
	Errors          int64                `json:"errors"`
	Series          []seriesPointDTO     `json:"series"`
	ErrorsByProject []projectErrorsDTO   `json:"errors_by_project"`
	CostByModel     []modelCostDTO       `json:"cost_by_model"`
	Projects        []projectSessionsDTO `json:"projects"`
}

// GET /api/stats/overview?day=YYYY-MM-DD (local timezone; default today)
func (h *Handler) statsOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayStart := todayStart
	if q := r.URL.Query().Get("day"); q != "" {
		parsed, err := time.ParseInLocation(dayFmt, q, time.Local)
		if err != nil {
			http.Error(w, `{"error":"invalid day, want YYYY-MM-DD"}`, http.StatusBadRequest)
			return
		}
		dayStart = parsed
	}
	start, end := dayBounds(dayStart)

	agg, err := h.windowAggregates(start, end, "", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	o := statsOverviewDTO{
		Day:       dayStart.Format(dayFmt),
		Sessions:  agg.Sessions,
		TokensIn:  agg.TokensIn,
		TokensOut: agg.TokensOut,
		CostUSD:   agg.CostUSD,
		Errors:    agg.Errors,
		// waiting_approval is a contract placeholder — approvals are not
		// tracked yet, so it is a literal 0 for now.
		WaitingApproval: 0,
		Series:          make([]seriesPointDTO, 0, 14),
		ErrorsByProject: []projectErrorsDTO{},
		CostByModel:     []modelCostDTO{},
		Projects:        []projectSessionsDTO{},
	}

	// "active" is a now-property, meaningful only for the current day.
	if dayStart.Equal(todayStart) {
		if o.Active, err = h.activeSessions("", nil); err != nil {
			writeErr(w, err)
			return
		}
	}

	// series: the last 14 local days ending at `day`, ascending, zero days
	// included (each day reuses the exact stats/today window semantics).
	for i := 13; i >= 0; i-- {
		d := dayStart.AddDate(0, 0, -i)
		ds, de := dayBounds(d)
		da, err := h.windowAggregates(ds, de, "", nil)
		if err != nil {
			writeErr(w, err)
			return
		}
		o.Series = append(o.Series, seriesPointDTO{
			Day:      d.Format(dayFmt),
			Sessions: da.Sessions,
			Tokens:   da.TokensIn + da.TokensOut,
			CostUSD:  da.CostUSD,
			Errors:   da.Errors,
		})
	}

	// errors_by_project: that day, descending, max 8.
	rows, err := h.DB.Query(`
		SELECT p.slug, p.name, COUNT(*) AS n
		FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.status = 'error' AND e.ts >= ? AND e.ts < ?
		GROUP BY p.id ORDER BY n DESC, p.slug LIMIT 8`, start, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var pe projectErrorsDTO
		if err := rows.Scan(&pe.Slug, &pe.Name, &pe.Errors); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		o.ErrorsByProject = append(o.ErrorsByProject, pe)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// cost_by_model: that day, descending, priced turns only.
	rows, err = h.DB.Query(`
		SELECT COALESCE(t.model, 'unknown') AS model, SUM(t.cost_usd) AS c
		FROM turns t
		WHERE t.cost_usd IS NOT NULL AND t.started_at >= ? AND t.started_at < ?
		GROUP BY model ORDER BY c DESC, model`, start, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var mc modelCostDTO
		if err := rows.Scan(&mc.Model, &mc.CostUSD); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		o.CostByModel = append(o.CostByModel, mc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// projects: sessions started that day, descending.
	rows, err = h.DB.Query(`
		SELECT p.slug, p.name, COUNT(*) AS n
		FROM sessions s
		JOIN projects p ON p.id = s.project_id
		WHERE s.started_at >= ? AND s.started_at < ?
		GROUP BY p.id ORDER BY n DESC, p.slug`, start, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var ps projectSessionsDTO
		if err := rows.Scan(&ps.Slug, &ps.Name, &ps.Sessions); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		o.Projects = append(o.Projects, ps)
	}
	rows.Close()
	writeJSON(w, o, rows.Err())
}
