package api

// Parity wave: /api/stats/overview — the dashboard overview for one LOCAL
// calendar day. Response shape is FROZEN by the parity contract (snake_case).
//
// Scalar fields share the exact day-window and cost NULL-rule semantics of
// /api/stats/today (windowAggregates in stats.go); "active" counts
// currently-active sessions only when the requested day is today, else 0.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
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
	// Test-run aggregates over the day (additive optional): null when the day
	// has no test_run events, mirroring stats/today's Quality-tile degradation.
	TestsPassed  *int64 `json:"tests_passed,omitempty"`
	TestsFailed  *int64 `json:"tests_failed,omitempty"`
	TestsSkipped *int64 `json:"tests_skipped,omitempty"`
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

	// Global project scope (?project=<slug|id>) — same match rule as
	// /api/stats/today and /api/sessions.
	projFilter, projArgs := scopeFilter(r)

	agg, err := h.windowAggregates(start, end, projFilter, projArgs)
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
	o.TestsPassed, o.TestsFailed, o.TestsSkipped = agg.tests()

	// "active" is a now-property, meaningful only for the current day.
	if dayStart.Equal(todayStart) {
		if o.Active, err = h.activeSessions(projFilter, projArgs); err != nil {
			writeErr(w, err)
			return
		}
	}

	// series: the last 14 local days ending at `day`, ascending, zero days
	// included (each day has the exact stats/today window semantics).
	series, err := h.seriesAggregates(dayStart, projFilter, projArgs)
	if err != nil {
		writeErr(w, err)
		return
	}
	o.Series = series

	// errors_by_project: that day, descending, max 8.
	rows, err := h.DB.Query(`
		SELECT p.slug, p.name, COUNT(*) AS n
		FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.status = 'error' AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+projFilter+`
		GROUP BY p.id ORDER BY n DESC, p.slug LIMIT 8`,
		append([]any{start, end}, projArgs...)...)
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
		SELECT COALESCE(t.model, 'unknown') AS mdl, SUM(t.cost_usd) AS c
		FROM turns t
		JOIN sessions s ON s.id = t.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE t.cost_usd IS NOT NULL AND t.started_at >= ? AND t.started_at < ? AND p.archived = 0`+projFilter+`
		GROUP BY mdl ORDER BY c DESC, mdl`,
		append([]any{start, end}, projArgs...)...)
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
		WHERE s.started_at >= ? AND s.started_at < ? AND p.archived = 0`+projFilter+`
		GROUP BY p.id ORDER BY n DESC, p.slug`,
		append([]any{start, end}, projArgs...)...)
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

// seriesDays is the length of the overview's trailing series.
const seriesDays = 14

// seriesAggregates computes the overview's 14-day series ending at lastDay in
// one grouped pass per table: sessions, turns and error events. It used to call
// windowAggregates once per day — 14 × 4 queries, several scanning a table —
// which held the store's single connection for seconds on every overview
// request and stalled every other page behind it.
//
// Each row is assigned to its day by the SAME [start, end) bounds dayBounds
// gives windowAggregates, through a CASE ladder over those bounds rather than
// date arithmetic in SQL, so local days stay exact across DST changes and in
// zones with non-hour offsets. Every point therefore equals that day's own
// windowAggregates (TestStatsOverviewSeriesMatchesPerDayAggregates).
func (h *Handler) seriesAggregates(lastDay time.Time, projFilter string, projArgs []any) ([]seriesPointDTO, error) {
	days := make([]time.Time, seriesDays)
	bounds := make([]any, seriesDays+1) // bounds[i] .. bounds[i+1] is days[i]
	for i := range days {
		days[i] = lastDay.AddDate(0, 0, i-(seriesDays-1))
		start, end := dayBounds(days[i])
		bounds[i], bounds[i+1] = start, end
	}

	// grouped runs one aggregate over [bounds[0], bounds[14]) grouped by day
	// index, and hands each row (day index first) to scan.
	grouped := func(col, aggCols, from, where string, scan func(*sql.Rows) error) error {
		var day strings.Builder
		day.WriteString("CASE")
		for i := 1; i < seriesDays; i++ {
			day.WriteString(" WHEN " + col + " < ? THEN " + strconv.Itoa(i-1))
		}
		// Rows past bounds[14] never get here (WHERE), so ELSE is the last day.
		day.WriteString(" ELSE " + strconv.Itoa(seriesDays-1) + " END")

		args := append([]any{}, bounds[1:seriesDays]...)
		args = append(args, bounds[0], bounds[seriesDays])
		args = append(args, projArgs...)
		rows, err := h.DB.Query(`SELECT `+day.String()+` AS d, `+aggCols+` FROM `+from+
			` WHERE `+where+col+` >= ? AND `+col+` < ? AND p.archived = 0`+projFilter+
			` GROUP BY d`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}

	var (
		sessions, tokensIn, tokensOut, errs, priced, usage [seriesDays]int64
		costSum                                            [seriesDays]sql.NullFloat64
	)

	// Sessions started on each day.
	err := grouped("s.started_at", "COUNT(*)",
		"sessions s JOIN projects p ON p.id = s.project_id", "",
		func(r *sql.Rows) error {
			var d int
			var n int64
			if err := r.Scan(&d, &n); err != nil {
				return err
			}
			sessions[d] = n
			return nil
		})
	if err != nil {
		return nil, err
	}

	// Token and cost aggregates over each day's turns: the same columns, and
	// the same SUM-rule inputs, as windowAggregates.
	err = grouped("t.started_at", `COALESCE(SUM(t.tokens_in), 0),
		       COALESCE(SUM(t.tokens_out), 0),
		       SUM(t.cost_usd),
		       COUNT(t.cost_usd),
		       COALESCE(SUM(CASE WHEN t.tokens_in IS NOT NULL OR t.tokens_out IS NOT NULL
		                           OR t.tokens_cache_read IS NOT NULL OR t.tokens_cache_write IS NOT NULL
		                         THEN 1 ELSE 0 END), 0)`,
		"turns t JOIN sessions s ON s.id = t.session_id JOIN projects p ON p.id = s.project_id", "",
		func(r *sql.Rows) error {
			var d int
			var in, out, pr, us int64
			var c sql.NullFloat64
			if err := r.Scan(&d, &in, &out, &c, &pr, &us); err != nil {
				return err
			}
			tokensIn[d], tokensOut[d], costSum[d], priced[d], usage[d] = in, out, c, pr, us
			return nil
		})
	if err != nil {
		return nil, err
	}

	// Errors: api_error events and failed tool calls both carry status='error'.
	err = grouped("e.ts", "COUNT(*)",
		"events e JOIN sessions s ON s.id = e.session_id JOIN projects p ON p.id = s.project_id",
		"e.status = 'error' AND ",
		func(r *sql.Rows) error {
			var d int
			var n int64
			if err := r.Scan(&d, &n); err != nil {
				return err
			}
			errs[d] = n
			return nil
		})
	if err != nil {
		return nil, err
	}

	out := make([]seriesPointDTO, seriesDays)
	for i := range out {
		out[i] = seriesPointDTO{
			Day:      days[i].Format(dayFmt),
			Sessions: sessions[i],
			Tokens:   tokensIn[i] + tokensOut[i],
			CostUSD:  windowCost(costSum[i], priced[i], usage[i]),
			Errors:   errs[i],
		}
	}
	return out, nil
}
