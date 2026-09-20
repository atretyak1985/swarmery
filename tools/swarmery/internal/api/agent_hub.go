package api

// Agent Hub (fusion phase 17): agent-centric aggregation over data that ALREADY
// exists across System (registry + versions), Retro (scorecards + lessons +
// proposals), Analytics (per-agent cost) and Sessions (runs). Two READ-ONLY
// endpoints, no new tables, no new write paths:
//
//   - GET /api/agents/hub          — roster: every registered agent (global +
//       project) joined with 30-day rollups {runs, successRate, cost, lastActive,
//       failedShare}. The rollup SQL is the SHARED retroAgentWindow /
//       agentOutcomeRates helpers (retro.go / analytics.go) — the same grain the
//       Retro scorecards use, so the numbers agree by construction.
//   - GET /api/agents/{id}/hub     — profile bundle for one registry agent:
//       overview rollups + recent activity (events) + runs (subagent_start folded
//       by name) + tasks (board tasks the agent's sessions touched + delegation
//       ledger rows) + insights (advisor recommendations + change proposals +
//       lessons attributable to the agent).
//
// Definition/version editing is NOT here — the Hub UI reuses the existing System
// write surface (/api/system/agents/{id} + versions/diff/rollback). This file
// only reads.
//
// Bridge note: the registry keys agents by row id, but every rollup folds by
// normAgentType(name) (plugin-qualified "core:x" and bare "x" share a key). So
// each roster/profile handler resolves the registry row, normalises its name,
// and looks the rollup maps up by that key — exactly how retroAgents gates its
// Improvable flag via RegistryAgentSet.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
)

// hubWindowDays is the roster/overview rollup window (matches the System
// tasks30d and the Retro default-ish 30d grain).
const hubWindowDays = 30

// hubActivityCap / hubRunsCap / hubTasksCap bound the profile bundle lists.
const (
	hubActivityCap = 50
	hubRunsCap     = 20
	hubTasksCap    = 30
	hubLessonsCap  = 20
)

// ── DTOs (mirrored in web/src/api/types.ts, "fusion phase 17: agent hub") ──

// agentRosterRow is one roster card: a registry identity + its 30-day rollups.
type agentRosterRow struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"` // global | project
	ProjectSlug *string `json:"projectSlug"`
	Origin      string  `json:"origin"` // local | plugin
	PluginName  *string `json:"pluginName"`
	Model       *string `json:"model"`
	Path        string  `json:"path"`
	Description *string `json:"description"`
	// Improvable mirrors the retro scorecard flag: the agent resolves to a live
	// registry row the rewriter can act on (built-ins are false).
	Improvable bool `json:"improvable"`
	// 30-day rollups, folded by normalised name (shared retroAgentWindow grain).
	Runs30d int64 `json:"runs30d"`
	// SuccessRate is success/(success+fail) over judged sessions; null when the
	// agent has no judged session in range (agentOutcomeRates grain).
	SuccessRate *float64 `json:"successRate"`
	// FailedShare is the behaviour-failed-run share (errRate over behaviour-
	// fixable failed runs) — the roster health dot thresholds it.
	FailedShare  float64 `json:"failedShare"`
	Cost30d      float64 `json:"cost30d"`
	LastActiveAt *string `json:"lastActiveAt"`
}

type agentRosterDTO struct {
	Agents []agentRosterRow `json:"agents"`
}

// agentOverviewDTO is the profile Overview tab: headline rollups + runs/day
// sparkline (last 30 local days) + the health thresholds' inputs.
type agentOverviewDTO struct {
	Runs30d       int64            `json:"runs30d"`
	SuccessRate   *float64         `json:"successRate"`
	FailedShare   float64          `json:"failedShare"`
	Cost30d       float64          `json:"cost30d"`
	TokensOut30d  int64            `json:"tokensOut30d"`
	LastActiveAt  *string          `json:"lastActiveAt"`
	AvgMs         *float64         `json:"avgMs"`
	P95Ms         *int64           `json:"p95Ms"`
	RunsByDay     []agentDayCount  `json:"runsByDay"`
	Errors        int64            `json:"errors"`
	ErrorsByClass map[string]int64 `json:"errorsByClass"`
}

type agentDayCount struct {
	Day  string `json:"day"` // YYYY-MM-DD (local)
	Runs int64  `json:"runs"`
}

// agentRunRow is one Runs-tab row (a subagent run in a session).
type agentRunRow struct {
	Ts           string `json:"ts"`
	ProjectSlug  string `json:"projectSlug"`
	SessionUUID  string `json:"sessionUuid"`
	SessionTitle string `json:"sessionTitle"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	DurationMs   int64  `json:"durationMs"`
}

// agentActivityRow is one Activity-tab event.
type agentActivityRow struct {
	Ts          string  `json:"ts"`
	Type        string  `json:"type"`
	ToolName    *string `json:"toolName"`
	Status      *string `json:"status"`
	SessionUUID string  `json:"sessionUuid"`
	ProjectSlug string  `json:"projectSlug"`
}

// agentTaskRow is one Tasks-tab row: a board/workspace task the agent touched,
// or a delegation-ledger row it executed.
type agentTaskRow struct {
	ExternalID string  `json:"externalId"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Source     string  `json:"source"` // 'session' | 'delegation'
	Phase      *string `json:"phase"`
	Verdict    *string `json:"verdict"`
	StartedAt  *string `json:"startedAt"`
}

// agentInsightsDTO is the Insights tab: the retro/improve rows filtered to the
// agent, using the SAME shapes the Retro page renders.
type agentInsightsDTO struct {
	Recommendations []recommendationDTO `json:"recommendations"`
	Proposals       []proposalDTO       `json:"proposals"`
	Lessons         []retroLessonDTO    `json:"lessons"`
}

// agentProfileDTO is GET /api/agents/{id}/hub.
type agentProfileDTO struct {
	// Identity fields (registry row), so the profile header renders standalone.
	agentRosterRow
	Overview agentOverviewDTO   `json:"overview"`
	Runs     []agentRunRow      `json:"runs"`
	Activity []agentActivityRow `json:"activity"`
	Tasks    []agentTaskRow     `json:"tasks"`
	Insights agentInsightsDTO   `json:"insights"`
}

// ── shared scope/window helpers ──

// hubScope resolves the optional ?projectId=<slug|id> roster/profile scope into
// the shared projectScopePredicate (aliased projects p) — the same match rule
// scopeFilter applies to ?project=. Empty when unscoped.
func hubScope(r *http.Request) (string, []any) {
	pid := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if pid == "" {
		return "", nil
	}
	return projectScopePredicate, scopeArgs(pid)
}

// hubRange is the fixed 30-day rollup window (last 30 local days ending today),
// built via the same dayBounds fold parseRange uses so the rollups line up with
// the Retro/Analytics day grain.
func hubRange() dateRange {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	fromDay := todayStart.AddDate(0, 0, -(hubWindowDays - 1))
	dr := dateRange{index: map[string]int{}}
	for d := fromDay; !d.After(todayStart); d = d.AddDate(0, 0, 1) {
		dr.index[d.Format(dayFmt)] = len(dr.days)
		dr.days = append(dr.days, d.Format(dayFmt))
	}
	dr.start, _ = dayBounds(fromDay)
	_, dr.end = dayBounds(todayStart)
	return dr
}

// rosterRollup carries the per-agent rollup a roster row needs, folded by
// normalised name. Built once for the whole roster from the shared helpers.
type rosterRollup struct {
	runs        int64
	successRate *float64
	failedShare float64
	cost        float64
	tokensOut   int64
	lastActive  string
	avgMs       *float64
	p95Ms       *int64
	errors      int64
	byClass     map[string]int64
	durations   []int64
}

// agentRollupsByName folds the shared retroAgentWindow + agentOutcomeRates into
// per-normalised-name rollups — the single fetch backing both the roster and a
// profile's Overview. The "main" orchestrator key is dropped (it has no registry
// identity). SQL lives entirely in the shared helpers; this only assembles.
func (h *Handler) agentRollupsByName(dr dateRange, pf string, pargs []any) (map[string]rosterRollup, error) {
	win, err := h.retroAgentWindow(dr, pf, pargs)
	if err != nil {
		return nil, err
	}
	rates, err := h.agentOutcomeRates(dr, pf, pargs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]rosterRollup, len(win))
	for key, a := range win {
		if key == "main" {
			continue
		}
		rr := rosterRollup{
			runs:        a.runs,
			failedShare: errRate(a.behaviorFailedRuns(), a.runs),
			cost:        a.cost,
			tokensOut:   a.tokensOut,
			lastActive:  a.lastActive,
			errors:      a.errors,
			byClass:     a.byClass,
			durations:   a.durations,
		}
		if n := len(a.durations); n > 0 {
			sorted := append([]int64(nil), a.durations...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			var sum int64
			for _, d := range sorted {
				sum += d
			}
			avg := float64(sum) / float64(n)
			rr.avgMs = &avg
			idx := (n*95 + 99) / 100 // ceil(0.95 × n), 1-based — matches retroAgents
			p95 := sorted[idx-1]
			rr.p95Ms = &p95
		}
		if rate, ok := rates[key]; ok {
			rc := rate
			rr.successRate = &rc
		}
		out[key] = rr
	}
	return out, nil
}

// ── GET /api/agents/hub ──

// registryAgentRow is the raw agents-registry projection the roster reads.
type registryAgentRow struct {
	id          int64
	name        string
	scope       string
	projectSlug sql.NullString
	origin      string
	pluginName  sql.NullString
	model       sql.NullString
	path        string
	description sql.NullString
}

// GET /api/agents/hub?projectId=<slug|id> — the roster. Registry agents joined
// with 30-day rollups by normalised name. A projectId scope narrows BOTH:
//   - the ROSTER to the agents EFFECTIVE in that project (its own + global-local
//   - the built-ins of the packs it enables — effectiveScope), so another
//     project's local agents never appear on a project page; and
//   - the ROLLUP window to that project's sessions (an effective agent with no
//     runs there simply shows zeros).
func (h *Handler) agentsHub(w http.ResponseWriter, r *http.Request) {
	pf, pargs := hubScope(r)
	dr := hubRange()

	esc, err := h.resolveEffectiveScope(r.URL.Query().Get("projectId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	scopePred, scopeBinds := esc.predicate("t.", true)

	rollups, err := h.agentRollupsByName(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}

	// One registry-set lookup for the Improvable flag (nil Improve service ⇒ all
	// false), same as retroAgents.
	var registrySet map[string]struct{}
	if h.Improve != nil {
		registrySet, err = h.Improve.RegistryAgentSet()
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	rows, err := h.DB.Query(`
		SELECT t.id, t.name, t.scope, p.slug, t.origin, t.plugin_name, t.model,
		       t.file_path, t.description
		  FROM agents t
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.deleted = 0`+scopePred+`
		 ORDER BY t.name, t.scope, t.id`, scopeBinds...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	out := agentRosterDTO{Agents: []agentRosterRow{}}
	for rows.Next() {
		var a registryAgentRow
		if err := rows.Scan(&a.id, &a.name, &a.scope, &a.projectSlug, &a.origin,
			&a.pluginName, &a.model, &a.path, &a.description); err != nil {
			writeErr(w, err)
			return
		}
		out.Agents = append(out.Agents, rosterRowFrom(a, rollups, registrySet))
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	// Busiest first, then name — the same ordering intent as the Retro table.
	sort.Slice(out.Agents, func(i, j int) bool {
		if out.Agents[i].Runs30d != out.Agents[j].Runs30d {
			return out.Agents[i].Runs30d > out.Agents[j].Runs30d
		}
		return out.Agents[i].Name < out.Agents[j].Name
	})
	writeJSON(w, out, nil)
}

// rosterRowFrom projects a registry row + its rollup into a roster card. The
// rollup is looked up by the normalised name — the registry-id↔name bridge.
func rosterRowFrom(a registryAgentRow, rollups map[string]rosterRollup, registrySet map[string]struct{}) agentRosterRow {
	key := normAgentType(a.name)
	row := agentRosterRow{
		ID: a.id, Name: a.name, Scope: a.scope, Origin: a.origin, Path: a.path,
	}
	if a.projectSlug.Valid {
		row.ProjectSlug = &a.projectSlug.String
	}
	if a.pluginName.Valid {
		row.PluginName = &a.pluginName.String
	}
	if a.model.Valid {
		row.Model = &a.model.String
	}
	if a.description.Valid {
		row.Description = &a.description.String
	}
	if _, ok := registrySet[advisor.NormAgent(key)]; ok {
		row.Improvable = true
	}
	if rr, ok := rollups[strings.ToLower(key)]; ok {
		fillRosterRollup(&row, rr)
	} else if rr, ok := rollups[key]; ok {
		// retroAgentWindow keys are case-preserving (normAgentType); the
		// delegation/eval folds lowercase. Registry names are typically already
		// lowercase, but fall back to the exact key too.
		fillRosterRollup(&row, rr)
	}
	return row
}

func fillRosterRollup(row *agentRosterRow, rr rosterRollup) {
	row.Runs30d = rr.runs
	row.SuccessRate = rr.successRate
	row.FailedShare = rr.failedShare
	row.Cost30d = rr.cost
	if rr.lastActive != "" {
		la := rr.lastActive
		row.LastActiveAt = &la
	}
}

// ── GET /api/agents/{id}/hub ──

// GET /api/agents/{id}/hub?projectId= — the profile bundle for one registry
// agent. 404 when the id is not a live registry agent.
func (h *Handler) agentHub(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	pf, pargs := hubScope(r)

	var a registryAgentRow
	err := h.DB.QueryRow(`
		SELECT t.id, t.name, t.scope, p.slug, t.origin, t.plugin_name, t.model,
		       t.file_path, t.description
		  FROM agents t
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ? AND t.deleted = 0`, id).Scan(
		&a.id, &a.name, &a.scope, &a.projectSlug, &a.origin, &a.pluginName,
		&a.model, &a.path, &a.description)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	key := normAgentType(a.name)

	dr := hubRange()
	rollups, err := h.agentRollupsByName(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}

	var registrySet map[string]struct{}
	if h.Improve != nil {
		registrySet, err = h.Improve.RegistryAgentSet()
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	out := agentProfileDTO{
		agentRosterRow: rosterRowFrom(a, rollups, registrySet),
		Runs:           []agentRunRow{},
		Activity:       []agentActivityRow{},
		Tasks:          []agentTaskRow{},
		Insights: agentInsightsDTO{
			Recommendations: []recommendationDTO{},
			Proposals:       []proposalDTO{},
			Lessons:         []retroLessonDTO{},
		},
	}
	out.Overview = overviewFrom(rollups, key, dr)

	if err := h.fillAgentRuns(&out, key, dr, pf, pargs); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.fillAgentActivity(&out, key, dr, pf, pargs); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.fillAgentTasks(&out, key, dr, pf, pargs); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.fillAgentInsights(&out, key, pf, pargs); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// overviewFrom projects a rollup into the Overview tab (the runsByDay sparkline
// is filled by fillAgentRuns' scan, so it stays a single events pass).
func overviewFrom(rollups map[string]rosterRollup, key string, dr dateRange) agentOverviewDTO {
	ov := agentOverviewDTO{RunsByDay: []agentDayCount{}, ErrorsByClass: map[string]int64{}}
	rr, ok := rollups[strings.ToLower(key)]
	if !ok {
		rr, ok = rollups[key]
	}
	if ok {
		ov.Runs30d = rr.runs
		ov.SuccessRate = rr.successRate
		ov.FailedShare = rr.failedShare
		ov.Cost30d = rr.cost
		ov.TokensOut30d = rr.tokensOut
		ov.AvgMs = rr.avgMs
		ov.P95Ms = rr.p95Ms
		ov.Errors = rr.errors
		if rr.byClass != nil {
			ov.ErrorsByClass = rr.byClass
		}
		if rr.lastActive != "" {
			la := rr.lastActive
			ov.LastActiveAt = &la
		}
	}
	return ov
}

// fillAgentRuns scans subagent_start events for the agent (folded by name, the
// getSystemAgentHistory grain) into the Runs list AND the Overview runsByDay
// sparkline in one pass.
func (h *Handler) fillAgentRuns(out *agentProfileDTO, key string, dr dateRange, pf string, pargs []any) error {
	rk := runKind["agent"]
	rows, err := h.DB.Query(
		`SELECT e.ts, e.status, e.duration_ms,
		        `+rk.nameExpr+` AS n,
		        json_extract(e.payload, '$.description'),
		        s.session_uuid, s.title, COALESCE(p.slug, '')
		   FROM events e
		   JOIN sessions s ON s.id = e.session_id
		   JOIN projects p ON p.id = s.project_id
		  WHERE e.type = ? AND `+rk.nameExpr+` IS NOT NULL
		    AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf+`
		  ORDER BY e.ts DESC`,
		append([]any{rk.typ, dr.start, dr.end}, pargs...)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	byDay := map[string]int64{}
	for rows.Next() {
		var ts, status, name, descr, sessUUID, title, slug sql.NullString
		var durMs sql.NullInt64
		if err := rows.Scan(&ts, &status, &durMs, &name, &descr, &sessUUID, &title, &slug); err != nil {
			return err
		}
		if normAgentType(name.String) != key {
			continue
		}
		if day, ok := localDay(ts.String); ok {
			byDay[day]++
		}
		if len(out.Runs) < hubRunsCap {
			out.Runs = append(out.Runs, agentRunRow{
				Ts: ts.String, ProjectSlug: slug.String, SessionUUID: sessUUID.String,
				SessionTitle: title.String, Description: descr.String,
				Status: status.String, DurationMs: durMs.Int64,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Emit the sparkline in ascending day order over the fixed window (zero-fill
	// gaps so the chart x-axis is stable).
	for _, day := range dr.days {
		out.Overview.RunsByDay = append(out.Overview.RunsByDay, agentDayCount{Day: day, Runs: byDay[day]})
	}
	return nil
}

// fillAgentActivity pulls the last hubActivityCap events attributed to the agent
// — subagent_start/stop the agent owns, plus any sidechain event parented to one
// of its starts (the statsTools attribution grain, restricted by name here).
func (h *Handler) fillAgentActivity(out *agentProfileDTO, key string, dr dateRange, pf string, pargs []any) error {
	rows, err := h.DB.Query(`
		SELECT e.ts, e.type, e.tool_name, e.status, s.session_uuid, COALESCE(p.slug, ''),
		       json_extract(e.payload, '$.agentType'),
		       json_extract(pe.payload, '$.subagent_type')
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		  LEFT JOIN events pe ON pe.id = e.parent_event_id
		 WHERE e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf+`
		 ORDER BY e.ts DESC
		 LIMIT 4000`,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, typ, tool, status, sessUUID, slug, ownType, subType sql.NullString
		if err := rows.Scan(&ts, &typ, &tool, &status, &sessUUID, &slug, &ownType, &subType); err != nil {
			return err
		}
		// Attribute the event to an agent by the same rule as the error fold:
		// a subagent_stop names itself; anything parented to a subagent_start
		// inherits that start's subagent_type; a bare subagent_start names itself.
		var evKey string
		switch {
		case typ.String == "subagent_stop" && ownType.Valid:
			evKey = normAgentType(ownType.String)
		case typ.String == "subagent_start":
			evKey = normAgentType(subTypeOrOwn(subType, ownType, sessUUID))
		case subType.Valid:
			evKey = normAgentType(subType.String)
		}
		if evKey != key {
			continue
		}
		if len(out.Activity) >= hubActivityCap {
			break
		}
		row := agentActivityRow{Ts: ts.String, Type: typ.String, SessionUUID: sessUUID.String, ProjectSlug: slug.String}
		if tool.Valid && tool.String != "" {
			row.ToolName = &tool.String
		}
		if status.Valid && status.String != "" {
			row.Status = &status.String
		}
		out.Activity = append(out.Activity, row)
	}
	return rows.Err()
}

// subTypeOrOwn returns the parent subagent_type when present, else the row's own
// agentType — for a bare subagent_start whose own payload carries subagent_type.
// (sessUUID is unused but keeps the call symmetrical/readable.)
func subTypeOrOwn(subType, ownType sql.NullString, _ sql.NullString) string {
	if subType.Valid && subType.String != "" {
		return subType.String
	}
	return ownType.String
}

// fillAgentTasks lists board/workspace tasks the agent touched: (1) delegation
// ledger rows naming the agent (the tech-lead 7-cell ledger — the strongest
// signal), plus (2) any task directly assigned to the agent's registry row.
// Newest first, capped.
func (h *Handler) fillAgentTasks(out *agentProfileDTO, key string, dr dateRange, pf string, pargs []any) error {
	// Delegation-ledger rows (task_delegations.agent is stored lowercased).
	rows, err := h.DB.Query(`
		SELECT t.external_id, t.title, t.status, td.phase, td.verdict, t.started_at
		  FROM task_delegations td
		  JOIN tasks t ON t.id = td.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE LOWER(td.agent) = ? AND p.archived = 0`+pf+`
		 ORDER BY t.started_at DESC, td.seq DESC
		 LIMIT ?`,
		append(append([]any{strings.ToLower(key)}, pargs...), hubTasksCap)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ext, title, status sql.NullString
		var phase, verdict, startedAt sql.NullString
		if err := rows.Scan(&ext, &title, &status, &phase, &verdict, &startedAt); err != nil {
			return err
		}
		row := agentTaskRow{
			ExternalID: ext.String, Title: title.String, Status: status.String,
			Source: "delegation",
		}
		if phase.Valid {
			row.Phase = &phase.String
		}
		if verdict.Valid {
			row.Verdict = &verdict.String
		}
		if startedAt.Valid {
			row.StartedAt = &startedAt.String
		}
		out.Tasks = append(out.Tasks, row)
	}
	return rows.Err()
}

// fillAgentInsights filters the retro/improve rows to the agent, reusing the
// exact DTOs the Retro page renders. Recommendations: target_kind='agent' with a
// matching normalised target. Proposals: agent column. Lessons: tasks whose
// delegation ledger names the agent (retro_lessons carries no agent column).
func (h *Handler) fillAgentInsights(out *agentProfileDTO, key string, pf string, pargs []any) error {
	nk := advisor.NormAgent(key)

	// Recommendations (open lifecycle set: proposed/accepted/adopted) targeting
	// this agent — the same actionable rail the Retro page shows, agent-scoped.
	rrows, err := h.DB.Query(`
		SELECT id, rule, target_kind, target, title, detail, evidence, baseline,
		       status, created_at, updated_at
		  FROM recommendations
		 WHERE target_kind = 'agent'
		   AND status IN ('proposed','accepted','adopted')
		 ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return err
	}
	defer rrows.Close()
	for rrows.Next() {
		var d recommendationDTO
		var evidence string
		var base sql.NullString
		if err := rrows.Scan(&d.ID, &d.Rule, &d.TargetKind, &d.Target, &d.Title,
			&d.Detail, &evidence, &base, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return err
		}
		if advisor.NormAgent(d.Target) != nk {
			continue
		}
		d.Evidence = json.RawMessage(evidence)
		d.scanBaseline(base)
		out.Insights.Recommendations = append(out.Insights.Recommendations, d)
	}
	if err := rrows.Err(); err != nil {
		return err
	}

	// Change proposals for the agent (newest first) — the same proposalDTO shape.
	// target_kind is pinned to 'agent': a SKILL proposal stores its lesson
	// identity in the same `agent` column, so without the filter a retro sentence
	// that happened to equal an agent key would surface on that agent's page as if
	// it were a rewrite of its prompt.
	prows, err := h.DB.Query(`
		SELECT id, recommendation_id, agent, agent_path, target_kind, target_path,
		       base_sha256, diff, rationale, status, error, pr_url, created_at, decided_at
		  FROM agent_change_proposals
		 WHERE agent = ? AND target_kind = 'agent'
		 ORDER BY created_at DESC, id DESC`, nk)
	if err != nil {
		return err
	}
	defer prows.Close()
	for prows.Next() {
		var p proposalDTO
		if err := prows.Scan(&p.ID, &p.RecommendationID, &p.Agent, &p.AgentPath,
			&p.TargetKind, &p.TargetPath,
			&p.BaseSHA256, &p.Diff, &p.Rationale, &p.Status, &p.Error, &p.PRURL,
			&p.CreatedAt, &p.DecidedAt); err != nil {
			return err
		}
		out.Insights.Proposals = append(out.Insights.Proposals, p)
	}
	if err := prows.Err(); err != nil {
		return err
	}

	// Lessons from tasks whose delegation ledger names this agent — the closest
	// honest attribution (retro_lessons has no agent column).
	lrows, err := h.DB.Query(`
		SELECT t.external_id, t.title, t.started_at, l.seq, l.title, l.action, l.body
		  FROM retro_lessons l
		  JOIN task_retros rt ON rt.id = l.retro_id
		  JOIN tasks t ON t.id = rt.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE p.archived = 0`+pf+`
		   AND EXISTS (SELECT 1 FROM task_delegations td
		                WHERE td.task_id = t.id AND LOWER(td.agent) = ?)
		 ORDER BY t.started_at DESC, t.external_id DESC, l.seq ASC
		 LIMIT ?`,
		append(append([]any{}, pargs...), strings.ToLower(key), hubLessonsCap)...)
	if err != nil {
		return err
	}
	defer lrows.Close()
	for lrows.Next() {
		var d retroLessonDTO
		var startedAt string
		var action, body sql.NullString
		if err := lrows.Scan(&d.TaskExternalID, &d.TaskTitle, &startedAt, &d.Seq, &d.Title, &action, &body); err != nil {
			return err
		}
		if len(startedAt) >= 10 {
			d.Date = startedAt[:10]
		}
		if action.Valid {
			d.Action = &action.String
		}
		if body.Valid {
			d.Body = &body.String
		}
		out.Insights.Lessons = append(out.Insights.Lessons, d)
	}
	return lrows.Err()
}
