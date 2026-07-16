package api

// Analytics wave: interactive token/cost/usage analytics over an arbitrary
// local-day range. Three endpoints back one page (web/src/pages/Analytics.tsx):
//
//   - /api/stats/timeseries — daily series for the main (stacked) chart
//   - /api/stats/breakdown  — ranked totals for the current pivot
//   - /api/stats/matrix     — agents|skills × projects cross-tab (run counts)
//
// PHASE 1 data reality (see the design spec): tokens & cost live on `turns`
// and cover ONLY the orchestrator session — the ingester records NO turns for
// subagents (ingest.go: sidechain mode). So $/tokens are sliceable by
// project/model/time only; agents & skills carry RUN COUNTS (from `events`,
// attributed by the payload NAME — events.agent_id/skill_id are never
// populated; commit 45c26f3), never $. Exact per-agent $ is Phase 2 (an
// ingest change), deliberately out of scope here.
//
// Response shapes are FROZEN by web/src/api/types.ts (snake_case, like the
// other stats endpoints).

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// maxRangeDays caps the requested span so a hostile ?from is not a fan-out.
const maxRangeDays = 366

// dateRange is the resolved analytics window: local-day buckets plus the UTC
// bounds used to filter the ISO-8601 UTC timestamps in storage.
type dateRange struct {
	days  []string // "2006-01-02" ascending, inclusive [from, to]
	start string   // UTC bound for from's local midnight
	end   string   // UTC bound for (to+1)'s local midnight
	index map[string]int
}

// parseRange resolves ?from=&to= (YYYY-MM-DD, LOCAL) into a dateRange. Default
// is the last 14 local days ending today. Bucketing is by LOCAL day: rows are
// fetched over the UTC [start,end) span and folded to their local day in Go
// (DST-correct without a per-day query loop).
func parseRange(r *http.Request) (dateRange, error) {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	toDay := todayStart
	fromDay := todayStart.AddDate(0, 0, -13) // 14 days inclusive

	parseDay := func(q string) (time.Time, error) {
		return time.ParseInLocation(dayFmt, q, time.Local)
	}
	if q := r.URL.Query().Get("to"); q != "" {
		p, err := parseDay(q)
		if err != nil {
			return dateRange{}, fmt.Errorf("invalid to, want YYYY-MM-DD")
		}
		toDay = p
	}
	if q := r.URL.Query().Get("from"); q != "" {
		p, err := parseDay(q)
		if err != nil {
			return dateRange{}, fmt.Errorf("invalid from, want YYYY-MM-DD")
		}
		fromDay = p
	}
	if toDay.Before(fromDay) {
		return dateRange{}, fmt.Errorf("to is before from")
	}

	dr := dateRange{index: map[string]int{}}
	for d := fromDay; !d.After(toDay); d = d.AddDate(0, 0, 1) {
		dr.index[d.Format(dayFmt)] = len(dr.days)
		dr.days = append(dr.days, d.Format(dayFmt))
		if len(dr.days) > maxRangeDays {
			return dateRange{}, fmt.Errorf("range too large (max %d days)", maxRangeDays)
		}
	}
	dr.start, _ = dayBounds(fromDay)
	_, dr.end = dayBounds(toDay)
	return dr, nil
}

// localDay maps a stored UTC timestamp string to its local YYYY-MM-DD.
func localDay(utcTS string) (string, bool) {
	t, err := time.Parse(time.RFC3339, utcTS)
	if err != nil {
		// zone-suffix-free bound form ("2006-01-02T15:04:05").
		t, err = time.Parse("2006-01-02T15:04:05", utcTS)
		if err != nil {
			return "", false
		}
	}
	return t.Local().Format(dayFmt), true
}

// projLabel resolves a project's display name: its name when set, else the
// slug (mirrors web/src/lib/format.ts projectLabel).
func projLabel(name sql.NullString, slug string) string {
	if name.Valid && name.String != "" {
		return name.String
	}
	return slug
}

// agentKey folds a turn's agent_name to the analytics grain (phase 2): a NULL
// agent_name is the orchestrator ("main"); otherwise normAgentType strips any
// plugin prefix so "core:x" and "x" share a key with the events-based counts.
func agentKey(agentName sql.NullString) string {
	if agentName.Valid && agentName.String != "" {
		return normAgentType(agentName.String)
	}
	return "main"
}

// runKind maps an events-based dimension to its event type + payload NAME
// expression — identical to system.go's systemKind usage attribution so
// counts agree across pages (folded by normAgentType in Go).
var runKind = map[string]struct{ typ, nameExpr string }{
	"agent": {"subagent_start", `json_extract(payload, '$.subagent_type')`},
	"skill": {"skill_use", `json_extract(payload, '$.input.skill')`},
}

// ── /api/stats/timeseries ─────────────────────────────────────────────────

type seriesDTO struct {
	Key    string    `json:"key"`
	Name   string    `json:"name"`
	Total  float64   `json:"total"`
	Values []float64 `json:"values"`
}

type timeseriesDTO struct {
	From    string      `json:"from"`
	To      string      `json:"to"`
	Metric  string      `json:"metric"`
	Group   string      `json:"group"`
	Buckets []string    `json:"buckets"`
	Series  []seriesDTO `json:"series"`
	// approx is always false in Phase 1 (no per-agent $); it exists so the UI
	// can render an "~approx" badge unchanged once Phase 2 lands.
	Approx bool `json:"approx"`
}

// validTimeseries gates metric×group: $/tokens come from turns — by
// project/model, or by agent now that subagent turns are recorded (phase 2);
// runs come from events (agent/skill).
func validTimeseries(metric, group string) bool {
	switch metric {
	case "cost", "tokens":
		return group == "project" || group == "model" || group == "agent"
	case "runs":
		return group == "agent" || group == "skill"
	}
	return false
}

// GET /api/stats/timeseries?from&to&metric=cost|tokens|runs&group=project|model|agent|skill
func (h *Handler) statsTimeseries(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	group := r.URL.Query().Get("group")
	if !validTimeseries(metric, group) {
		http.Error(w, `{"error":"invalid metric/group combo"}`, http.StatusBadRequest)
		return
	}
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// acc[key] = {name, values aligned to dr.days, total}
	type acc struct {
		name   string
		values []float64
		total  float64
	}
	series := map[string]*acc{}
	get := func(key, name string) *acc {
		a := series[key]
		if a == nil {
			a = &acc{name: name, values: make([]float64, len(dr.days))}
			series[key] = a
		}
		return a
	}

	if metric == "runs" {
		rk := runKind[group]
		rows, err := h.DB.Query(
			`SELECT e.ts, `+rk.nameExpr+` AS n
			   FROM events e
			   JOIN sessions s ON s.id = e.session_id
			   JOIN projects p ON p.id = s.project_id
			  WHERE e.type = ? AND `+rk.nameExpr+` IS NOT NULL
			    AND e.ts >= ? AND e.ts < ? AND p.archived = 0`, rk.typ, dr.start, dr.end)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var ts, name sql.NullString
			if err := rows.Scan(&ts, &name); err != nil {
				writeErr(w, err)
				return
			}
			key := normAgentType(name.String)
			day, ok := localDay(ts.String)
			if key == "" || !ok {
				continue
			}
			idx, ok := dr.index[day]
			if !ok {
				continue
			}
			a := get(key, key)
			a.values[idx]++
			a.total++
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		// turns-based: project or model, cost or tokens.
		rows, err := h.DB.Query(`
			SELECT t.started_at, p.slug, p.name,
			       COALESCE(t.model, s.model, 'unknown') AS model,
			       t.agent_name,
			       t.tokens_in, t.tokens_out, t.cost_usd
			  FROM turns t
			  JOIN sessions s ON s.id = t.session_id
			  JOIN projects p ON p.id = s.project_id
			 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`, dr.start, dr.end)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var startedAt, slug, model string
			var name, agentName sql.NullString
			var tin, tout sql.NullInt64
			var cost sql.NullFloat64
			if err := rows.Scan(&startedAt, &slug, &name, &model, &agentName, &tin, &tout, &cost); err != nil {
				writeErr(w, err)
				return
			}
			day, ok := localDay(startedAt)
			if !ok {
				continue
			}
			idx, ok := dr.index[day]
			if !ok {
				continue
			}
			var key, label string
			switch group {
			case "project":
				key, label = slug, projLabel(name, slug)
			case "model":
				key, label = model, model
			default: // agent — NULL agent_name is the orchestrator ("main")
				key = agentKey(agentName)
				label = key
			}
			var v float64
			if metric == "cost" {
				if !cost.Valid {
					continue // unpriced turn contributes nothing to a $ series
				}
				v = cost.Float64
			} else {
				v = float64(tin.Int64 + tout.Int64)
			}
			a := get(key, label)
			a.values[idx] += v
			a.total += v
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	}

	out := timeseriesDTO{
		From: dr.days[0], To: dr.days[len(dr.days)-1],
		Metric: metric, Group: group, Buckets: dr.days,
		Series: make([]seriesDTO, 0, len(series)),
	}
	for key, a := range series {
		out.Series = append(out.Series, seriesDTO{Key: key, Name: a.name, Total: a.total, Values: a.values})
	}
	// Deterministic order: total desc, then key asc (stable legend).
	sort.Slice(out.Series, func(i, j int) bool {
		if out.Series[i].Total != out.Series[j].Total {
			return out.Series[i].Total > out.Series[j].Total
		}
		return out.Series[i].Key < out.Series[j].Key
	})
	writeJSON(w, out, nil)
}

// ── /api/stats/breakdown ──────────────────────────────────────────────────

type breakdownRow struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	CostUSD   *float64 `json:"cost_usd"`
	TokensIn  *int64   `json:"tokens_in"`
	TokensOut *int64   `json:"tokens_out"`
	Runs      *int64   `json:"runs"`
	Sessions  int64    `json:"sessions"`
	LastUsed  *string  `json:"last_used"`
	// success/(success+fail) over outcome-carrying sessions that contain this
	// agent's turns in range (agent pivot only; 'abandoned' excluded). Null
	// for the other pivots and for agents with no judged sessions.
	SuccessRate *float64 `json:"success_rate"`
}

// GET /api/stats/breakdown?from&to&by=project|model|agent|skill
func (h *Handler) statsBreakdown(w http.ResponseWriter, r *http.Request) {
	by := r.URL.Query().Get("by")
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	var out []breakdownRow
	switch by {
	case "project", "model":
		out, err = h.breakdownTurns(by, dr)
	case "agent":
		out, err = h.breakdownAgent(dr)
	case "skill":
		out, err = h.breakdownRuns("skill", dr)
	default:
		http.Error(w, `{"error":"invalid by, want project|model|agent|skill"}`, http.StatusBadRequest)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// breakdownTurns ranks project|model by cost (turns-based). tokens & sessions
// travel along; runs/last_used are null (Phase 1).
func (h *Handler) breakdownTurns(by string, dr dateRange) ([]breakdownRow, error) {
	keyExpr, labelIsName := "p.slug", true
	if by == "model" {
		keyExpr, labelIsName = "COALESCE(t.model, s.model, 'unknown')", false
	}
	rows, err := h.DB.Query(`
		SELECT `+keyExpr+` AS k, p.name,
		       COALESCE(SUM(t.cost_usd), 0)   AS cost,
		       COUNT(t.cost_usd)              AS priced,
		       COALESCE(SUM(t.tokens_in), 0)  AS tin,
		       COALESCE(SUM(t.tokens_out), 0) AS tout,
		       COUNT(DISTINCT t.session_id)   AS sess
		  FROM turns t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0
		 GROUP BY k
		 ORDER BY cost DESC, k`, dr.start, dr.end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []breakdownRow
	for rows.Next() {
		var key string
		var name sql.NullString
		var cost float64
		var priced, tin, tout, sess int64
		if err := rows.Scan(&key, &name, &cost, &priced, &tin, &tout, &sess); err != nil {
			return nil, err
		}
		row := breakdownRow{Key: key, Sessions: sess, TokensIn: &tin, TokensOut: &tout}
		if labelIsName {
			row.Name = projLabel(name, key)
		} else {
			row.Name = key
		}
		// Honesty rule: only surface a $ figure when at least one turn was priced.
		if priced > 0 {
			c := cost
			row.CostUSD = &c
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// breakdownRuns ranks agent|skill by run count (events-based, name-folded).
// cost/tokens are null in Phase 1.
func (h *Handler) breakdownRuns(by string, dr dateRange) ([]breakdownRow, error) {
	rk := runKind[by]
	rows, err := h.DB.Query(
		`SELECT `+rk.nameExpr+` AS n, e.ts, e.session_id
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		   JOIN projects p ON p.id = s.project_id
		  WHERE e.type = ? AND `+rk.nameExpr+` IS NOT NULL
		    AND e.ts >= ? AND e.ts < ? AND p.archived = 0`, rk.typ, dr.start, dr.end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		runs     int64
		lastUsed string
		sessions map[int64]struct{}
	}
	acc := map[string]*agg{}
	for rows.Next() {
		var name, ts sql.NullString
		var sess sql.NullInt64
		if err := rows.Scan(&name, &ts, &sess); err != nil {
			return nil, err
		}
		key := normAgentType(name.String)
		if key == "" {
			continue
		}
		a := acc[key]
		if a == nil {
			a = &agg{sessions: map[int64]struct{}{}}
			acc[key] = a
		}
		a.runs++
		if ts.Valid && ts.String > a.lastUsed {
			a.lastUsed = ts.String
		}
		if sess.Valid {
			a.sessions[sess.Int64] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]breakdownRow, 0, len(acc))
	for key, a := range acc {
		runs := a.runs
		row := breakdownRow{Key: key, Name: key, Runs: &runs, Sessions: int64(len(a.sessions))}
		if a.lastUsed != "" {
			lu := a.lastUsed
			row.LastUsed = &lu
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if *out[i].Runs != *out[j].Runs {
			return *out[i].Runs > *out[j].Runs
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// agentTot accumulates a folded agent's turn totals (phase 2).
type agentTot struct {
	cost   float64
	priced int64
	tin    int64
	tout   int64
}

// agentTurnTotals sums subagent (and "main") turn cost/tokens over the range,
// folded by agentKey — the source of exact per-agent $ (phase 2).
func (h *Handler) agentTurnTotals(dr dateRange) (map[string]*agentTot, error) {
	rows, err := h.DB.Query(`
		SELECT t.agent_name, t.tokens_in, t.tokens_out, t.cost_usd
		  FROM turns t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`, dr.start, dr.end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := map[string]*agentTot{}
	for rows.Next() {
		var an sql.NullString
		var tin, tout sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&an, &tin, &tout, &cost); err != nil {
			return nil, err
		}
		key := agentKey(an)
		t := acc[key]
		if t == nil {
			t = &agentTot{}
			acc[key] = t
		}
		t.tin += tin.Int64
		t.tout += tout.Int64
		if cost.Valid {
			t.cost += cost.Float64
			t.priced++
		}
	}
	return acc, rows.Err()
}

// agentOutcomeRates computes per-agent success/(success+fail) over sessions
// that carry a manual outcome AND contain turns of that agent within the
// range. Attribution uses turns.agent_name folded by agentKey — the same
// grain as agentTurnTotals, so the rate column lines up with the $ column.
// 'abandoned' is excluded from the denominator by the WHERE clause.
func (h *Handler) agentOutcomeRates(dr dateRange) (map[string]float64, error) {
	rows, err := h.DB.Query(`
		SELECT DISTINCT t.agent_name, t.session_id, s.outcome
		  FROM turns t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE s.outcome IN ('success','fail')
		   AND t.started_at >= ? AND t.started_at < ? AND p.archived = 0`, dr.start, dr.end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type sf struct{ success, fail map[int64]struct{} }
	acc := map[string]*sf{}
	for rows.Next() {
		var an, outcome sql.NullString
		var sess int64
		if err := rows.Scan(&an, &sess, &outcome); err != nil {
			return nil, err
		}
		key := agentKey(an)
		a := acc[key]
		if a == nil {
			a = &sf{success: map[int64]struct{}{}, fail: map[int64]struct{}{}}
			acc[key] = a
		}
		if outcome.String == "success" {
			a.success[sess] = struct{}{}
		} else {
			a.fail[sess] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]float64{}
	for key, a := range acc {
		if n := len(a.success) + len(a.fail); n > 0 {
			out[key] = float64(len(a.success)) / float64(n)
		}
	}
	return out, nil
}

// breakdownAgent ranks agents with EXACT $ (phase 2): run counts from events
// (subagents that ran) merged with cost/tokens from their turns, plus the
// orchestrator ("main") which has turns but no subagent_start event.
func (h *Handler) breakdownAgent(dr dateRange) ([]breakdownRow, error) {
	rows, err := h.breakdownRuns("agent", dr)
	if err != nil {
		return nil, err
	}
	byKey := map[string]int{}
	for i := range rows {
		byKey[rows[i].Key] = i
	}
	totals, err := h.agentTurnTotals(dr)
	if err != nil {
		return nil, err
	}
	for key, tot := range totals {
		tin, tout := tot.tin, tot.tout
		var cost *float64
		if tot.priced > 0 {
			c := tot.cost
			cost = &c
		}
		if i, ok := byKey[key]; ok {
			rows[i].CostUSD, rows[i].TokensIn, rows[i].TokensOut = cost, &tin, &tout
		} else {
			// "main" orchestrator (no subagent_start) or an agent with turns
			// but no counted run — surface it with $ and no runs.
			rows = append(rows, breakdownRow{Key: key, Name: key, CostUSD: cost, TokensIn: &tin, TokensOut: &tout})
		}
	}
	rates, err := h.agentOutcomeRates(dr)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rate, ok := rates[rows[i].Key]; ok {
			r := rate
			rows[i].SuccessRate = &r
		}
	}
	costOf := func(r breakdownRow) float64 {
		if r.CostUSD != nil {
			return *r.CostUSD
		}
		return 0
	}
	runsOf := func(r breakdownRow) int64 {
		if r.Runs != nil {
			return *r.Runs
		}
		return 0
	}
	sort.Slice(rows, func(i, j int) bool {
		if ci, cj := costOf(rows[i]), costOf(rows[j]); ci != cj {
			return ci > cj
		}
		if ri, rj := runsOf(rows[i]), runsOf(rows[j]); ri != rj {
			return ri > rj
		}
		return rows[i].Key < rows[j].Key
	})
	return rows, nil
}

// ── /api/stats/matrix ─────────────────────────────────────────────────────

type keyName struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type matrixCell struct {
	Row  string   `json:"row"`
	Col  string   `json:"col"`
	Runs int64    `json:"runs"`
	Cost *float64 `json:"cost,omitempty"`
}

type matrixDTO struct {
	Metric string       `json:"metric"`
	Rows   []keyName    `json:"rows"`
	Cols   []keyName    `json:"cols"`
	Cells  []matrixCell `json:"cells"`
}

// GET /api/stats/matrix?from&to&rows=agent|skill&cols=project&metric=runs|cost
//
// metric=runs (default) counts events (agent|skill). metric=cost sums turn
// cost (phase 2) and requires rows=agent — only agents own turns; the "main"
// orchestrator appears as its own row.
func (h *Handler) statsMatrix(w http.ResponseWriter, r *http.Request) {
	rowsDim := r.URL.Query().Get("rows")
	colsDim := r.URL.Query().Get("cols")
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "runs"
	}
	if colsDim != "project" || (metric != "runs" && metric != "cost") {
		http.Error(w, `{"error":"cols must be project; metric must be runs|cost"}`, http.StatusBadRequest)
		return
	}
	rk, isRun := runKind[rowsDim]
	if metric == "runs" && !isRun {
		http.Error(w, `{"error":"rows must be agent|skill"}`, http.StatusBadRequest)
		return
	}
	if metric == "cost" && rowsDim != "agent" {
		http.Error(w, `{"error":"metric=cost requires rows=agent"}`, http.StatusBadRequest)
		return
	}
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	cells := map[[2]string]float64{}
	rowTotals := map[string]float64{}
	colTotals := map[string]float64{}
	colName := map[string]string{}
	add := func(rowKey, slug string, pname sql.NullString, v float64) {
		if rowKey == "" || slug == "" {
			return
		}
		cells[[2]string{rowKey, slug}] += v
		rowTotals[rowKey] += v
		colTotals[slug] += v
		colName[slug] = projLabel(pname, slug)
	}

	if metric == "runs" {
		rows, err := h.DB.Query(
			`SELECT `+rk.nameExpr+` AS n, p.slug, p.name
			   FROM events e
			   JOIN sessions s ON s.id = e.session_id
			   JOIN projects p ON p.id = s.project_id
			  WHERE e.type = ? AND `+rk.nameExpr+` IS NOT NULL
			    AND e.ts >= ? AND e.ts < ? AND p.archived = 0`, rk.typ, dr.start, dr.end)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var name, slug, pname sql.NullString
			if err := rows.Scan(&name, &slug, &pname); err != nil {
				writeErr(w, err)
				return
			}
			add(normAgentType(name.String), slug.String, pname, 1)
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	} else { // cost: agent × project from turns (phase 2)
		rows, err := h.DB.Query(`
			SELECT t.agent_name, p.slug, p.name, t.cost_usd
			  FROM turns t
			  JOIN sessions s ON s.id = t.session_id
			  JOIN projects p ON p.id = s.project_id
			 WHERE t.cost_usd IS NOT NULL AND t.started_at >= ? AND t.started_at < ? AND p.archived = 0`, dr.start, dr.end)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var an, slug, pname sql.NullString
			var cost sql.NullFloat64
			if err := rows.Scan(&an, &slug, &pname, &cost); err != nil {
				writeErr(w, err)
				return
			}
			add(agentKey(an), slug.String, pname, cost.Float64)
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	}

	out := matrixDTO{Metric: metric, Cells: make([]matrixCell, 0, len(cells))}
	for k, v := range cells {
		cell := matrixCell{Row: k[0], Col: k[1]}
		if metric == "cost" {
			c := v
			cell.Cost = &c
		} else {
			cell.Runs = int64(v)
		}
		out.Cells = append(out.Cells, cell)
	}
	for key := range rowTotals {
		out.Rows = append(out.Rows, keyName{Key: key, Name: key})
	}
	for key := range colTotals {
		out.Cols = append(out.Cols, keyName{Key: key, Name: colName[key]})
	}
	// Deterministic: rows/cols by total desc then key; cells by row,col.
	sort.Slice(out.Rows, func(i, j int) bool {
		if rowTotals[out.Rows[i].Key] != rowTotals[out.Rows[j].Key] {
			return rowTotals[out.Rows[i].Key] > rowTotals[out.Rows[j].Key]
		}
		return out.Rows[i].Key < out.Rows[j].Key
	})
	sort.Slice(out.Cols, func(i, j int) bool {
		if colTotals[out.Cols[i].Key] != colTotals[out.Cols[j].Key] {
			return colTotals[out.Cols[i].Key] > colTotals[out.Cols[j].Key]
		}
		return out.Cols[i].Key < out.Cols[j].Key
	})
	sort.Slice(out.Cells, func(i, j int) bool {
		if out.Cells[i].Row != out.Cells[j].Row {
			return out.Cells[i].Row < out.Cells[j].Row
		}
		return out.Cells[i].Col < out.Cells[j].Col
	})
	writeJSON(w, out, nil)
}
