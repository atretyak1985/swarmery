package api

// Retro improvement loop (phase 1): two endpoints backing the /retro page
// (web/src/pages/Retro.tsx) — per-agent health scorecards with a
// previous-window comparison, and a "friction board" (denied tools, error
// groups, approval waits). Built purely from data already in SQLite; no new
// tables.
//
//   - /api/retro/agents   — per-agent runs/cost/errors/durations + prev window
//   - /api/retro/friction — denied tools, top error groups, approval waits
//
// Aggregation grains deliberately reuse the analytics helpers so numbers agree
// across pages: runs come from subagent_start events folded by normAgentType
// (breakdownRuns), $/tokens from turns folded by agentKey (agentTurnTotals),
// success rates from agentOutcomeRates, per-agent errors from the
// parent_event_id attribution (statsTools grain — see tools.go), error
// grouping from errorGroups (shared with /api/stats/errors).

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// ── /api/retro/agents ─────────────────────────────────────────────────────

type retroMainDTO struct {
	CostUSD   float64 `json:"cost_usd"`
	TokensOut int64   `json:"tokens_out"`
	Errors    int64   `json:"errors"`
}

type retroPrevDTO struct {
	Runs      int64   `json:"runs"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	CostUSD   float64 `json:"cost_usd"`
}

type retroAgentDTO struct {
	Agent     string  `json:"agent"`
	Runs      int64   `json:"runs"`
	Sessions  int64   `json:"sessions"`
	CostUSD   float64 `json:"cost_usd"`
	TokensOut int64   `json:"tokens_out"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	// avg/p95 over subagent_start durations; nil when no run carried one.
	AvgMs *float64 `json:"avg_ms"`
	P95Ms *int64   `json:"p95_ms"`
	// success/(success+fail) over judged sessions (agentOutcomeRates grain);
	// nil when the agent has no judged session in range.
	SuccessRate *float64     `json:"success_rate"`
	Prev        retroPrevDTO `json:"prev"`
}

type retroAgentsDTO struct {
	From   string          `json:"from"`
	To     string          `json:"to"`
	Approx bool            `json:"approx"`
	Main   retroMainDTO    `json:"main"`
	Agents []retroAgentDTO `json:"agents"`
}

// retroAgentWin accumulates one window's per-agent aggregates, keyed by the
// folded agent name ("main" = orchestrator).
type retroAgentWin struct {
	runs      int64
	sessions  map[int64]struct{}
	cost      float64
	tokensOut int64
	errors    int64
	durations []int64
}

// retroAgentWindow computes one [dr.start, dr.end) window's per-agent map:
// runs/sessions/durations from subagent_start events (breakdownRuns grain),
// cost/tokens_out from turns (agentTurnTotals), errors from status='error'
// events attributed through parent_event_id (the statsTools grain).
func (h *Handler) retroAgentWindow(dr dateRange, pf string, pargs []any) (map[string]*retroAgentWin, error) {
	acc := map[string]*retroAgentWin{}
	get := func(key string) *retroAgentWin {
		a := acc[key]
		if a == nil {
			a = &retroAgentWin{sessions: map[int64]struct{}{}}
			acc[key] = a
		}
		return a
	}

	// Runs + sessions + durations: subagent_start events, name-folded — the
	// same attribution as breakdownRuns/statsTools.
	rk := runKind["agent"]
	rows, err := h.DB.Query(
		`SELECT `+rk.nameExpr+` AS n, e.session_id, e.duration_ms
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		   JOIN projects p ON p.id = s.project_id
		  WHERE e.type = ? AND `+rk.nameExpr+` IS NOT NULL
		    AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		append([]any{rk.typ, dr.start, dr.end}, pargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name sql.NullString
		var sess int64
		var durMs sql.NullInt64
		if err := rows.Scan(&name, &sess, &durMs); err != nil {
			return nil, err
		}
		key := normAgentType(name.String)
		if key == "" {
			continue
		}
		a := get(key)
		a.runs++
		a.sessions[sess] = struct{}{}
		if durMs.Valid {
			a.durations = append(a.durations, durMs.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Cost + tokens_out: turns folded by agentKey (creates the "main" entry).
	totals, err := h.agentTurnTotals(dr, pf, pargs)
	if err != nil {
		return nil, err
	}
	for key, tot := range totals {
		a := get(key)
		a.cost = tot.cost
		a.tokensOut = tot.tout
	}

	// Errors: status='error' events attributed like statsTools — through the
	// parent_event_id chain, NOT events.turn_id (ingest zeroes turn_id for
	// every sidechain event, so a turn join would blank out agent errors and
	// dump subagent_start failures on "main"). A sidechain event is parented
	// (adoptOrphanSidechainEvents) to its subagent_start, whose payload
	// carries subagent_type; a NULL parent is the orchestrator ("main" —
	// including unparented api_errors). subagent_stop rows name the agent in
	// their OWN payload (agentType), with the parent's subagent_type as
	// fallback. The if/else chain classifies each error row into exactly ONE
	// bucket, so the strip total stays Σ(main + agents). The type IN (…)
	// predicate rides idx_events_type; unlike errorGroups it deliberately
	// EXCLUDES subagent_start: ingest writes TWO error rows per failed run —
	// the subagent_stop insert AND a mirrored status='error' UPDATE onto the
	// parent subagent_start (ingest.go closeToolCall) — and the start row has
	// no parent, so counting it too would double-count every failed run and
	// add a phantom "main" error.
	erows, err := h.DB.Query(`
		SELECT e.type,
		       json_extract(e.payload, '$.agentType'),
		       COALESCE(pe.type, ''),
		       json_extract(pe.payload, '$.subagent_type')
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		  LEFT JOIN events pe ON pe.id = e.parent_event_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var typ, parentType string
		var ownType, subType sql.NullString
		if err := erows.Scan(&typ, &ownType, &parentType, &subType); err != nil {
			return nil, err
		}
		key := "main"
		if typ == "subagent_stop" && ownType.Valid && ownType.String != "" {
			key = normAgentType(ownType.String)
		} else if parentType == "subagent_start" && subType.Valid && subType.String != "" {
			key = normAgentType(subType.String)
		}
		if key == "" {
			key = "main"
		}
		get(key).errors++
	}
	return acc, erows.Err()
}

// prevWindow derives the preceding window of equal length: for an N-day range
// [from, to] it is [from-N, from-1] — its UTC end bound is exactly dr.start.
func prevWindow(dr dateRange) dateRange {
	n := len(dr.days)
	fromDay, err := time.ParseInLocation(dayFmt, dr.days[0], time.Local)
	if err != nil {
		return dateRange{start: dr.start, end: dr.start} // empty window, defensive
	}
	start, _ := dayBounds(fromDay.AddDate(0, 0, -n))
	return dateRange{start: start, end: dr.start}
}

// errRate is errors/runs, 0 when the agent had no counted run.
func errRate(errors, runs int64) float64 {
	if runs == 0 {
		return 0
	}
	return float64(errors) / float64(runs)
}

// GET /api/retro/agents?from&to&project — per-agent health scorecards. The
// "main" fold key (orchestrator) is excluded from agents[] and surfaced as
// the top-level main object.
func (h *Handler) retroAgents(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)

	cur, err := h.retroAgentWindow(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	prev, err := h.retroAgentWindow(prevWindow(dr), pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	rates, err := h.agentOutcomeRates(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}

	out := retroAgentsDTO{
		From: dr.days[0], To: dr.days[len(dr.days)-1],
		Agents: make([]retroAgentDTO, 0, len(cur)),
	}
	for key, a := range cur {
		if key == "main" {
			// The orchestrator never has a subagent_start of its own, so a
			// "runs" figure would always be 0 — deliberately not exposed.
			out.Main = retroMainDTO{CostUSD: a.cost, TokensOut: a.tokensOut, Errors: a.errors}
			continue
		}
		row := retroAgentDTO{
			Agent: key, Runs: a.runs, Sessions: int64(len(a.sessions)),
			CostUSD: a.cost, TokensOut: a.tokensOut,
			Errors: a.errors, ErrorRate: errRate(a.errors, a.runs),
		}
		if n := len(a.durations); n > 0 {
			// Same avg/p95 aggregation as statsTools.
			sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
			var sum int64
			for _, d := range a.durations {
				sum += d
			}
			avg := float64(sum) / float64(n)
			row.AvgMs = &avg
			idx := (n*95 + 99) / 100 // ceil(0.95 × n), 1-based
			p95 := a.durations[idx-1]
			row.P95Ms = &p95
		}
		if rate, ok := rates[key]; ok {
			rr := rate
			row.SuccessRate = &rr
		}
		if p, ok := prev[key]; ok {
			row.Prev = retroPrevDTO{Runs: p.runs, Errors: p.errors, ErrorRate: errRate(p.errors, p.runs), CostUSD: p.cost}
		}
		out.Agents = append(out.Agents, row)
	}
	sort.Slice(out.Agents, func(i, j int) bool {
		if out.Agents[i].Runs != out.Agents[j].Runs {
			return out.Agents[i].Runs > out.Agents[j].Runs
		}
		return out.Agents[i].Agent < out.Agents[j].Agent
	})
	// approx must be honest for BOTH windows on screen: the prev window is
	// strictly older, so it is the one MORE likely to overlap pruned
	// (rolled-up) days.
	rolled, err := h.hasRolledUpDays(dr.days[0], dr.days[len(dr.days)-1], pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !rolled {
		if fromDay, perr := time.ParseInLocation(dayFmt, dr.days[0], time.Local); perr == nil {
			n := len(dr.days)
			rolled, err = h.hasRolledUpDays(
				fromDay.AddDate(0, 0, -n).Format(dayFmt),
				fromDay.AddDate(0, 0, -1).Format(dayFmt), pf, pargs)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	out.Approx = rolled
	writeJSON(w, out, nil)
}

// ── /api/retro/friction ───────────────────────────────────────────────────

type frictionDeniedDTO struct {
	Tool    string `json:"tool"`
	Denied  int64  `json:"denied"`
	Calls   int64  `json:"calls"`
	HasRule bool   `json:"has_rule"`
}

type frictionErrGroupDTO struct {
	Key     string `json:"key"`
	Example string `json:"example"`
	Count   int64  `json:"count"`
	LastTs  string `json:"last_ts"`
	// Up to 3 distinct sample session uuids, newest first.
	Sessions []string `json:"sessions"`
}

type frictionApprovalsDTO struct {
	Resolved      int64    `json:"resolved"`
	AvgResolveSec *float64 `json:"avg_resolve_sec"` // nil when none resolved
	WaitTotalMin  float64  `json:"wait_total_min"`
	Pending       int64    `json:"pending"`
}

type frictionDTO struct {
	DeniedTools []frictionDeniedDTO   `json:"denied_tools"`
	ErrorGroups []frictionErrGroupDTO `json:"error_groups"`
	Approvals   frictionApprovalsDTO  `json:"approvals"`
	// Approx is true when the range overlaps pruned (rolled-up) days — rollups
	// carry no per-tool or error events, so the board silently undercounts
	// there. Same honesty rule as /api/retro/agents.
	Approx bool `json:"approx"`
}

const frictionTopN = 10

// ruleCoversTool reports whether an enabled approval-rule pattern already
// covers a tool. Patterns are pre-parsed with the REAL rule grammar
// (approvals.ParseRulePattern) so this can never drift from the evaluator; a
// `Tool(argGlob)` rule still counts as covering the tool.
func ruleCoversTool(rules []approvals.RulePattern, tool string) bool {
	for _, rp := range rules {
		if rp.Tool == tool {
			return true
		}
	}
	return false
}

// GET /api/retro/friction?from&to&project — denied tools, top error groups,
// approval-wait stats.
func (h *Handler) retroFriction(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	out := frictionDTO{
		DeniedTools: []frictionDeniedDTO{},
		ErrorGroups: []frictionErrGroupDTO{},
	}

	// Denied tools: the statsTools event skeleton, aggregated, filtered,
	// ranked and capped entirely in SQL — no row streaming.
	rules, err := h.enabledRulePatterns(r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.DB.Query(`
		SELECT e.tool_name,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN e.status = 'denied' THEN 1 ELSE 0 END) AS denied
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.tool_name IS NOT NULL
		   AND e.type IN ('tool_call', 'skill_use', 'subagent_start')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf+`
		 GROUP BY e.tool_name
		HAVING SUM(CASE WHEN e.status = 'denied' THEN 1 ELSE 0 END) > 0
		 ORDER BY denied DESC, e.tool_name ASC
		 LIMIT ?`,
		append(append([]any{dr.start, dr.end}, pargs...), frictionTopN)...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d frictionDeniedDTO
		if err := rows.Scan(&d.Tool, &d.Calls, &d.Denied); err != nil {
			writeErr(w, err)
			return
		}
		d.HasRule = ruleCoversTool(rules, d.Tool)
		out.DeniedTools = append(out.DeniedTools, d)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Error groups: the shared grouping helper (also backing /api/stats/errors).
	groups, err := h.errorGroups(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(groups) > frictionTopN {
		groups = groups[:frictionTopN]
	}
	for _, g := range groups {
		uuids := make([]string, 0, len(g.Samples))
		for _, s := range g.Samples {
			uuids = append(uuids, s.SessionUUID)
		}
		out.ErrorGroups = append(out.ErrorGroups, frictionErrGroupDTO{
			Key: g.Key, Example: g.Example, Count: g.Count, LastTs: g.LastTs,
			Sessions: uuids,
		})
	}

	// Approvals: the permission-request span computation from statsDurations.
	waits, err := h.querySpans(`
		SELECT pr.requested_at, pr.resolved_at
		  FROM permission_requests pr
		  JOIN sessions s ON s.id = pr.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE pr.resolved_at IS NOT NULL
		   AND pr.requested_at >= ? AND pr.requested_at < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...))
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Approvals.Resolved = int64(len(waits))
	if n := len(waits); n > 0 {
		var sum float64
		for _, s := range waits {
			sum += s
		}
		avg := sum / float64(n)
		out.Approvals.AvgResolveSec = &avg
		out.Approvals.WaitTotalMin = sum / 60
	}
	// Pending is "pending NOW" — deliberately NOT range-filtered: a request
	// opened outside the range still blocks work today. Project scope and the
	// archived filter still apply.
	err = h.DB.QueryRow(`
		SELECT COUNT(*)
		  FROM permission_requests pr
		  JOIN sessions s ON s.id = pr.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE pr.status = 'pending' AND p.archived = 0`+pf,
		pargs...).Scan(&out.Approvals.Pending)
	if err != nil {
		writeErr(w, err)
		return
	}
	rolled, err := h.hasRolledUpDays(dr.days[0], dr.days[len(dr.days)-1], pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Approx = rolled
	writeJSON(w, out, nil)
}

// enabledRulePatterns fetches the enabled approval-rule patterns backing the
// friction board's has_rule flag, parsed with the shared grammar (unparseable
// rows are skipped, mirroring the evaluator). A non-empty ?project=<slug|id>
// scope narrows to global (project_id IS NULL) rules plus that project's own —
// the same rule set the approvals evaluator would consider there; unscoped
// requests see every enabled rule.
func (h *Handler) enabledRulePatterns(project string) ([]approvals.RulePattern, error) {
	q := `SELECT tool_pattern FROM approval_rules WHERE enabled = 1`
	var args []any
	if project != "" {
		// Resolve the slug-or-id scope to a project id; an unknown project
		// (pid stays 0) keeps only the global rules.
		var pid int64
		err := h.DB.QueryRow(
			`SELECT id FROM projects WHERE slug = ? OR CAST(id AS TEXT) = ?`,
			project, project).Scan(&pid)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		q += ` AND (project_id IS NULL OR project_id = ?)`
		args = append(args, pid)
	}
	rows, err := h.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approvals.RulePattern
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		rp, perr := approvals.ParseRulePattern(p)
		if perr != nil {
			continue
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}
