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
// Phase 2 adds the workspace-artifact surfaces (migration 0018, parsed by
// internal/wsingest/artifacts.go) and the evals importer (internal/evals):
//
//   - /api/retro/lessons  — the retro_lessons feed joined through tasks
//   - /api/retro/tasks    — estimation accuracy + loop/delegation counts
//   - retroAgents rows gain re_dispatch_rate (task_delegations ledger) and
//     eval (latest eval_runs row for the registry agent)
//
// Phase 3 surfaces the internal/advisor rule engine (migration 0019):
//
//   - /api/retro/recommendations       — list, ?status= CSV filter
//   - PATCH /api/retro/recommendations/{id} — accept/dismiss (422 otherwise)
//   - POST /api/retro/advise           — run the engine on demand
//
// Aggregation grains deliberately reuse the analytics helpers so numbers agree
// across pages: runs come from subagent_start events folded by normAgentType
// (breakdownRuns), $/tokens from turns folded by agentKey (agentTurnTotals),
// success rates from agentOutcomeRates, per-agent errors from the
// parent_event_id attribution (statsTools grain — see tools.go), error
// grouping from errorGroups (shared with /api/stats/errors). error_rate is
// the BEHAVIOR-failed-run share (distinct runs with ≥1 behavior-fixable error
// / runs, at the advisor.Classify grain, clamped to ≤1 — a run spanning the
// window start can contribute a failed run without contributing to the run
// count), not raw error events per run — one run spraying many tool errors
// counts once, and harness/infra noise doesn't count at all.

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/trajeval"
)

// ── /api/retro/agents ─────────────────────────────────────────────────────

type retroMainDTO struct {
	CostUSD   float64 `json:"cost_usd"`
	TokensOut int64   `json:"tokens_out"`
	Errors    int64   `json:"errors"`
	// ErrorsByClass tallies raw error events per advisor.ErrClass key.
	ErrorsByClass map[string]int64 `json:"errors_by_class"`
}

type retroPrevDTO struct {
	Runs      int64   `json:"runs"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	CostUSD   float64 `json:"cost_usd"`
}

// retroEvalDTO is the latest imported eval run for a registry agent
// (swarmery evals-import).
type retroEvalDTO struct {
	Passed     int64  `json:"passed"`
	Failed     int64  `json:"failed"`
	FinishedAt string `json:"finished_at"`
}

type retroAgentDTO struct {
	Agent     string  `json:"agent"`
	Runs      int64   `json:"runs"`
	Sessions  int64   `json:"sessions"`
	CostUSD   float64 `json:"cost_usd"`
	TokensOut int64   `json:"tokens_out"`
	// Errors is the raw error-event count; ErrorRate is the BEHAVIOR-failed-
	// run share (distinct runs with ≥1 behavior_fixable error / runs — infra
	// noise and harness mechanics excluded) — see retroAgentWindow. The same
	// grain the advisor's R2 fires on (behavior_failed_run_share).
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	// ErrorsByClass tallies raw error events per advisor.ErrClass key.
	ErrorsByClass map[string]int64 `json:"errors_by_class"`
	// avg/p95 over subagent_start durations; nil when no run carried one.
	AvgMs *float64 `json:"avg_ms"`
	P95Ms *int64   `json:"p95_ms"`
	// success/(success+fail) over judged sessions (agentOutcomeRates grain);
	// nil when the agent has no judged session in range.
	SuccessRate *float64 `json:"success_rate"`
	// redispatch-classified ledger rows / total ledger rows (task_delegations
	// via tasks in range); nil when the agent has no ledger row there.
	ReDispatchRate *float64 `json:"re_dispatch_rate"`
	// latest imported eval run for the registry agent; nil when none.
	Eval *retroEvalDTO `json:"eval"`
	// Improvable is true when the agent resolves to a live registry row with an
	// editable definition file — the agents the rewriter (internal/improve) can
	// act on. Built-in agents (Explore, general-purpose, debugger) are false, so
	// the UI hides their "Improve" button.
	Improvable bool         `json:"improvable"`
	Prev       retroPrevDTO `json:"prev"`
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
	// failedRunKeys dedupes the runs that carried ≥1 error: keyed by the run's
	// subagent_start event id when the error row is parented to one, else by
	// the (unparented) subagent_stop's own event id. len() = failed runs.
	failedRunKeys map[int64]struct{}
	// behaviorFailedRunKeys is the same dedupe restricted to errors whose
	// group key classifies as behavior_fixable (advisor.Classify) — the share
	// error_rate reports, matching what the advisor's R2 fires on.
	behaviorFailedRunKeys map[int64]struct{}
	// byClass tallies raw error events per advisor.ErrClass key
	// (advisor.Classify over the normalizeErrKey fold).
	byClass   map[string]int64
	durations []int64
	// lastActive is the newest subagent_start ts seen for the agent in the
	// window (RFC3339 UTC as stored). "" when the agent had no run. The Agent
	// Hub roster surfaces it as "last active"; retroAgents ignores it.
	lastActive string
}

// failedRuns is the number of distinct runs with at least one error.
func (a *retroAgentWin) failedRuns() int64 { return int64(len(a.failedRunKeys)) }

// behaviorFailedRuns is the number of distinct runs with at least one
// behavior-fixable error.
func (a *retroAgentWin) behaviorFailedRuns() int64 { return int64(len(a.behaviorFailedRunKeys)) }

// retroAgentWindow computes one [dr.start, dr.end) window's per-agent map:
// runs/sessions/durations from subagent_start events (breakdownRuns grain),
// cost/tokens_out from turns (agentTurnTotals), errors from status='error'
// events attributed through parent_event_id (the statsTools grain). Errors
// are additionally classified (advisor.Classify over the normalizeErrKey
// fold) into byClass and folded into failedRunKeys / behaviorFailedRunKeys —
// the DISTINCT runs with ≥1 (behavior-fixable) error — the latter backing
// error_rate as a behavior-failed-run share (clamped to ≤1 — a run spanning
// the window start can contribute a failed run without contributing to the
// run count) instead of raw error events per run.
func (h *Handler) retroAgentWindow(dr dateRange, pf string, pargs []any) (map[string]*retroAgentWin, error) {
	acc := map[string]*retroAgentWin{}
	get := func(key string) *retroAgentWin {
		a := acc[key]
		if a == nil {
			a = &retroAgentWin{
				sessions:              map[int64]struct{}{},
				failedRunKeys:         map[int64]struct{}{},
				behaviorFailedRunKeys: map[int64]struct{}{},
				byClass:               map[string]int64{},
			}
			acc[key] = a
		}
		return a
	}

	// Runs + sessions + durations: subagent_start events, name-folded — the
	// same attribution as breakdownRuns/statsTools.
	rk := runKind["agent"]
	rows, err := h.DB.Query(
		`SELECT `+rk.nameExpr+` AS n, e.session_id, e.duration_ms, e.ts
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
		var ts sql.NullString
		if err := rows.Scan(&name, &sess, &durMs, &ts); err != nil {
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
		if ts.Valid && ts.String > a.lastActive {
			a.lastActive = ts.String
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
	//
	// On top of the raw count, each agent-attributed error row marks its RUN
	// as failed: a row parented to a subagent_start dedupes on that start's
	// event id (many sidechain errors in one run → one failed run); an
	// unparented subagent_stop has no reachable start, so it stands in for its
	// own run and dedupes on its own event id (ingest writes one stop per run).
	// Unparented "main" errors are excluded — the orchestrator has no runs, so
	// main keeps only the raw error count, never a rate.
	erows, err := h.DB.Query(`
		SELECT e.id, e.parent_event_id, e.type,
		       json_extract(e.payload, '$.agentType'),
		       COALESCE(pe.type, ''),
		       json_extract(pe.payload, '$.subagent_type'),
		       e.tool_name, e.payload
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
		var id int64
		var parentID sql.NullInt64
		var typ, parentType string
		var ownType, subType, toolName, payload sql.NullString
		if err := erows.Scan(&id, &parentID, &typ, &ownType, &parentType, &subType, &toolName, &payload); err != nil {
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
		a := get(key)
		a.errors++
		// api's own normalizeErrKey stays the key producer; only the class
		// table lives in advisor (single source — never fork it here).
		class := advisor.Classify(normalizeErrKey(extractErrMsg(typ, toolName, payload)))
		a.byClass[string(class)]++
		if key != "main" {
			runKey := id // unparented subagent_stop: the stop IS the run's proxy
			if parentType == "subagent_start" && parentID.Valid {
				runKey = parentID.Int64
			}
			a.failedRunKeys[runKey] = struct{}{}
			if class == advisor.BehaviorFixable {
				a.behaviorFailedRunKeys[runKey] = struct{}{}
			}
		}
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

// errRate is failedRuns/runs — the failed-run share (call sites pass the
// BEHAVIOR-failed-run count) — 0 when the agent had no counted run. Clamped
// to ≤1: an error inside the window whose parent run STARTED before the
// window adds a failed-run key absent from the in-window run count.
func errRate(failedRuns, runs int64) float64 {
	if runs == 0 {
		return 0
	}
	return min(1, float64(failedRuns)/float64(runs))
}

// redispatchRe classifies a ledger verdict as a re-dispatch (vs an accept).
// Ledger verdicts are bilingual — agent-work.sh writes a Ukrainian header
// (`| Агент | Фаза | Вердикт | Артефакт |`) and verdict cells appear in either
// language — so both vocabularies are covered:
//
//	re-?dispatch      — the canonical tech-lead verdict (RE-DISPATCH / redispatch)
//	redo              — "redo the phase"
//	\bfail(ed|ure)?\b — fail / failed / failure, word-anchored so benign
//	                    substrings ("failsafe") don't hit
//	\breject(ed)?\b   — reject / rejected, word-anchored
//	повтор            — "повтор" / "повторити" / "повторно" (redo)
//	відхил            — "відхилено" / "відхилити" (rejected)
//	провал            — "провал" / "провалено" (failed)
//	фейл              — transliterated "fail"
var redispatchRe = regexp.MustCompile(`(?i)(re-?dispatch|redo|\bfail(ed|ure)?\b|\breject(ed)?\b|повтор|відхил|провал|фейл)`)

// redispatchVerdictMaxRunes caps how long a verdict cell may be and still be
// classified: a real verdict is a short token ("OK", "RE-DISPATCH",
// "відхилено"); a long prose cell that merely MENTIONS a failure ("OK, flaky
// test failure noted …") is commentary, not a re-dispatch verdict.
const redispatchVerdictMaxRunes = 40

// isRedispatch reports whether a ledger verdict cell records a re-dispatch.
// twin: internal/advisor/rules.go — keep in lockstep (incl. redispatchRe and
// redispatchVerdictMaxRunes above).
func isRedispatch(verdict string) bool {
	v := strings.TrimSpace(verdict)
	if len([]rune(v)) > redispatchVerdictMaxRunes {
		return false
	}
	return redispatchRe.MatchString(v)
}

// delegationRates aggregates the task_delegations ledger over tasks STARTED in
// the range: per normalized agent, (redispatch rows, total rows). The ledger
// lives on workspace tasks, so the range rides tasks.started_at — the card's
// calendar date — not event timestamps.
func (h *Handler) delegationRates(dr dateRange, pf string, pargs []any) (map[string][2]int64, error) {
	rows, err := h.DB.Query(`
		SELECT td.agent, COALESCE(td.verdict, '')
		  FROM task_delegations td
		  JOIN tasks t ON t.id = td.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var agent, verdict string
		if err := rows.Scan(&agent, &verdict); err != nil {
			return nil, err
		}
		// The ledger stores agents lowercased (wsingest normalizeLedgerAgent);
		// lowercase the fold here too so mixed-case scorecard keys still line
		// up. Deliberately NOT baked into normAgentType — other call sites
		// depend on its case-preserving behavior.
		key := strings.ToLower(normAgentType(agent))
		c := out[key]
		c[1]++
		if isRedispatch(verdict) {
			c[0]++
		}
		out[key] = c
	}
	return out, rows.Err()
}

// latestEvals returns the newest imported eval run per registry agent (folded
// by normAgentType so registry names line up with scorecard keys). Not
// range-filtered on purpose: the chip answers "how does the CURRENT prompt
// score", whatever day the suite last ran.
func (h *Handler) latestEvals() (map[string]retroEvalDTO, error) {
	rows, err := h.DB.Query(`
		SELECT a.name, COALESCE(r.passed, 0), COALESCE(r.failed, 0),
		       COALESCE(r.finished_at, r.started_at)
		  FROM eval_runs r
		  JOIN eval_suites s ON s.id = r.suite_id
		  JOIN agents a ON a.id = s.agent_id
		 WHERE a.deleted = 0
		 ORDER BY r.started_at DESC, r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]retroEvalDTO{}
	for rows.Next() {
		var name string
		var e retroEvalDTO
		if err := rows.Scan(&name, &e.Passed, &e.Failed, &e.FinishedAt); err != nil {
			return nil, err
		}
		// Same lowercase fold as delegationRates: registry names are free-form,
		// scorecard keys are lowercase in practice (normAgentType itself stays
		// case-preserving for its other call sites).
		key := strings.ToLower(normAgentType(name))
		if _, seen := out[key]; !seen { // rows are newest-first
			out[key] = e
		}
	}
	return out, rows.Err()
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
	out, err := h.buildRetroAgents(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// buildRetroAgents computes the scorecard payload for one resolved window and
// scope. Split out of the handler so /api/retro/report (retro_report.go) can
// reuse it verbatim instead of re-deriving the same aggregates.
func (h *Handler) buildRetroAgents(dr dateRange, pf string, pargs []any) (retroAgentsDTO, error) {
	cur, err := h.retroAgentWindow(dr, pf, pargs)
	if err != nil {
		return retroAgentsDTO{}, err
	}
	prev, err := h.retroAgentWindow(prevWindow(dr), pf, pargs)
	if err != nil {
		return retroAgentsDTO{}, err
	}
	rates, err := h.agentOutcomeRates(dr, pf, pargs)
	if err != nil {
		return retroAgentsDTO{}, err
	}
	delRates, err := h.delegationRates(dr, pf, pargs)
	if err != nil {
		return retroAgentsDTO{}, err
	}
	evalByAgent, err := h.latestEvals()
	if err != nil {
		return retroAgentsDTO{}, err
	}
	// One registry lookup for the whole table: the set of agents the rewriter
	// can act on, keyed by the same normalized name as the scorecard rows. A nil
	// Improve service (generation disabled) leaves every row improvable=false.
	var registrySet map[string]struct{}
	if h.Improve != nil {
		registrySet, err = h.Improve.RegistryAgentSet()
		if err != nil {
			return retroAgentsDTO{}, err
		}
	}

	out := retroAgentsDTO{
		From: dr.days[0], To: dr.days[len(dr.days)-1],
		Main:   retroMainDTO{ErrorsByClass: map[string]int64{}},
		Agents: make([]retroAgentDTO, 0, len(cur)),
	}
	for key, a := range cur {
		if key == "main" {
			// The orchestrator never has a subagent_start of its own, so a
			// "runs" figure would always be 0 — deliberately not exposed.
			out.Main = retroMainDTO{CostUSD: a.cost, TokensOut: a.tokensOut, Errors: a.errors, ErrorsByClass: a.byClass}
			continue
		}
		row := retroAgentDTO{
			Agent: key, Runs: a.runs, Sessions: int64(len(a.sessions)),
			CostUSD: a.cost, TokensOut: a.tokensOut,
			Errors: a.errors, ErrorRate: errRate(a.behaviorFailedRuns(), a.runs),
			ErrorsByClass: a.byClass,
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
		if c, ok := delRates[key]; ok && c[1] >= 1 {
			rr := float64(c[0]) / float64(c[1])
			row.ReDispatchRate = &rr
		}
		if e, ok := evalByAgent[key]; ok {
			ee := e
			row.Eval = &ee
		}
		// Gate the Improve button: fold the scorecard key the same way
		// RegistryAgentSet folds registry names (advisor.NormAgent lowercases;
		// the scorecard key is only prefix-stripped) so the lookup lines up.
		if _, ok := registrySet[advisor.NormAgent(key)]; ok {
			row.Improvable = true
		}
		if p, ok := prev[key]; ok {
			row.Prev = retroPrevDTO{Runs: p.runs, Errors: p.errors, ErrorRate: errRate(p.behaviorFailedRuns(), p.runs), CostUSD: p.cost}
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
		return retroAgentsDTO{}, err
	}
	if !rolled {
		if fromDay, perr := time.ParseInLocation(dayFmt, dr.days[0], time.Local); perr == nil {
			n := len(dr.days)
			rolled, err = h.hasRolledUpDays(
				fromDay.AddDate(0, 0, -n).Format(dayFmt),
				fromDay.AddDate(0, 0, -1).Format(dayFmt), pf, pargs)
			if err != nil {
				return retroAgentsDTO{}, err
			}
		}
	}
	out.Approx = rolled
	return out, nil
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
	out, err := h.buildRetroFriction(dr, pf, pargs, r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// buildRetroFriction computes the friction board for one resolved window and
// scope. project is the RAW ?project= value: the approval-rule lookup resolves
// it itself (enabledRulePatterns) rather than through the SQL scope predicate.
// Split out of the handler so /api/retro/report can reuse it.
func (h *Handler) buildRetroFriction(dr dateRange, pf string, pargs []any, project string) (frictionDTO, error) {
	out := frictionDTO{
		DeniedTools: []frictionDeniedDTO{},
		ErrorGroups: []frictionErrGroupDTO{},
	}

	// Denied tools: the statsTools event skeleton, aggregated, filtered,
	// ranked and capped entirely in SQL — no row streaming.
	rules, err := h.enabledRulePatterns(project)
	if err != nil {
		return frictionDTO{}, err
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
		return frictionDTO{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var d frictionDeniedDTO
		if err := rows.Scan(&d.Tool, &d.Calls, &d.Denied); err != nil {
			return frictionDTO{}, err
		}
		d.HasRule = ruleCoversTool(rules, d.Tool)
		out.DeniedTools = append(out.DeniedTools, d)
	}
	if err := rows.Err(); err != nil {
		return frictionDTO{}, err
	}

	// Error groups: the shared grouping helper (also backing /api/stats/errors).
	groups, err := h.errorGroups(dr, pf, pargs)
	if err != nil {
		return frictionDTO{}, err
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
		return frictionDTO{}, err
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
		return frictionDTO{}, err
	}
	rolled, err := h.hasRolledUpDays(dr.days[0], dr.days[len(dr.days)-1], pf, pargs)
	if err != nil {
		return frictionDTO{}, err
	}
	out.Approx = rolled
	return out, nil
}

// ── /api/retro/lessons ────────────────────────────────────────────────────

type retroLessonDTO struct {
	TaskExternalID string `json:"task_external_id"`
	TaskTitle      string `json:"task_title"`
	Date           string `json:"date"` // task card calendar day, YYYY-MM-DD
	// 1-based lesson order within the task's retro doc — with the task
	// external id it forms a stable render key for the UI.
	Seq    int64   `json:"seq"`
	Title  string  `json:"title"`
	Action *string `json:"action"`
	Body   *string `json:"body"`
}

type retroLessonsDTO struct {
	Lessons []retroLessonDTO `json:"lessons"`
}

// retroLessonGroupDTO is one lesson IDENTITY (wsingest.NormalizeLessonTitle,
// migration 0070) folded across every task that learned it. The flat feed above
// answers "what did we learn"; this answers "what do we keep re-learning", which
// is the only one of the two a human can act on.
type retroLessonGroupDTO struct {
	NormTitle string `json:"norm_title"`
	// Title is the most recent wording — the flat feed keeps every variant, and
	// the newest one is the one the operator just saw.
	Title string `json:"title"`
	// Tasks are the external task ids that carried the lesson, newest first.
	Tasks []string `json:"tasks"`
	// Count is len(Tasks): DISTINCT tasks, never lesson rows. Two lessons in one
	// retro that fold together are one occurrence, not two.
	Count int `json:"count"`
	// LatestAction is the `**Action**:` line of the most recent occurrence, or
	// null when that occurrence carried none.
	LatestAction *string `json:"latest_action"`
}

type retroLessonGroupsDTO struct {
	Groups []retroLessonGroupDTO `json:"groups"`
}

const retroLessonsLimit = 100

// retroLessonGroupsLimit caps the grouped view. Groups are far coarser than
// rows, so this is generous — it exists to bound the response, not to filter.
const retroLessonGroupsLimit = 100

// retroLessonGroupsScanLimit bounds the ROWS the grouped query loads, which is a
// different bound from the output cap above: one group is many rows, so a
// LIMIT 100 on rows would truncate the very counts this view exists to show.
// It is needed because the fold happens in Go — without it a wide window pulls
// every lesson row in the database into memory while holding the process's only
// SQLite connection (store.Open sets SetMaxOpenConns(1)).
//
// Rows come back newest-task-first, so a window wider than this folds the newest
// retroLessonGroupsScanLimit rows and the tail is dropped — the same truncation
// rule the flat feed's LIMIT already applies, not a new one.
const retroLessonGroupsScanLimit = 5000

// GET /api/retro/lessons?from&to&project[&group=1] — the lessons-learned feed
// parsed from 09-retrospective.md docs (wsingest artifacts), joined through
// tasks and filtered on the task's start date. Newest tasks first, capped at
// 100.
//
// With group=1 the same window is folded by retro_lessons.norm_title instead:
// `{"groups": [{norm_title, title, tasks, count, latest_action}]}`, ordered by
// count descending. Same window, same scope, different question — so the page
// can toggle between them without a second endpoint whose filters could drift.
func (h *Handler) retroLessons(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	if isTruthyParam(r.URL.Query(), "group") {
		grouped, gerr := h.buildRetroLessonGroups(dr, pf, pargs)
		if gerr != nil {
			writeErr(w, gerr)
			return
		}
		writeJSON(w, grouped, nil)
		return
	}
	out, err := h.buildRetroLessons(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// isTruthyParam reads a boolean query flag the forgiving way — `?group=1`,
// `?group=true` and a bare `?group` all mean the same thing to whoever typed the
// URL, and disagreeing with them is never the interesting failure. `?group=0`
// and `?group=false` are explicit opt-outs and stay false.
func isTruthyParam(q url.Values, key string) bool {
	if !q.Has(key) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(q.Get(key))) {
	case "", "1", "true", "yes", "on":
		return true
	}
	return false
}

// buildRetroLessonGroups folds the window's lessons by norm_title.
//
// The fold is done in Go rather than in SQL on purpose: "newest first" for the
// task list, "the most recent wording" for the title, and "the most recent
// action" all need the SAME row to win, and expressing that as one correlated
// SQL aggregate is how the three quietly start disagreeing.
//
// Rows with an empty norm_title are excluded. ” is the "not folded yet" marker
// (migration 0070 + BackfillNormTitles), not an identity — grouping by it would
// pile unrelated lessons into one bogus, high-count group at the top of exactly
// the view meant to show what recurs.
//
// tasks.external_id is NULLABLE (migration 0006), so it is COALESCEd to the row
// id exactly as advisor/rules_lesson.go lessonGroups does — scanning a NULL into
// a string 500s the endpoint, while the same row silently counts in R11. The two
// queries are documented as twins; this is part of keeping them so.
func (h *Handler) buildRetroLessonGroups(dr dateRange, pf string, pargs []any) (retroLessonGroupsDTO, error) {
	rows, err := h.DB.Query(`
		SELECT l.norm_title, l.title, l.action,
		       COALESCE(t.external_id, CAST(t.id AS TEXT)), t.started_at
		  FROM retro_lessons l
		  JOIN task_retros r ON r.id = l.retro_id
		  JOIN tasks t ON t.id = r.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0
		   AND l.norm_title <> ''`+pf+`
		 ORDER BY t.started_at DESC, t.external_id DESC, l.seq ASC
		 LIMIT ?`,
		append(append([]any{dr.start, dr.end}, pargs...), retroLessonGroupsScanLimit)...)
	if err != nil {
		return retroLessonGroupsDTO{}, err
	}
	defer rows.Close()

	out := retroLessonGroupsDTO{Groups: []retroLessonGroupDTO{}}
	index := map[string]int{}
	seenTask := map[string]bool{}
	for rows.Next() {
		var norm, title, externalID, startedAt string
		var action sql.NullString
		if err := rows.Scan(&norm, &title, &action, &externalID, &startedAt); err != nil {
			return retroLessonGroupsDTO{}, err
		}
		i, known := index[norm]
		if !known {
			// First row wins the display fields: the query is ordered newest
			// first, so this IS the most recent occurrence.
			g := retroLessonGroupDTO{NormTitle: norm, Title: title, Tasks: []string{}}
			if action.Valid {
				a := action.String
				g.LatestAction = &a
			}
			out.Groups = append(out.Groups, g)
			i = len(out.Groups) - 1
			index[norm] = i
		}
		if key := norm + "\x00" + externalID; !seenTask[key] {
			seenTask[key] = true
			out.Groups[i].Tasks = append(out.Groups[i].Tasks, externalID)
			out.Groups[i].Count++
		}
	}
	if err := rows.Err(); err != nil {
		return retroLessonGroupsDTO{}, err
	}

	// Most-recurring first; norm_title breaks ties so the order is stable
	// between two calls over an unchanged window.
	sort.SliceStable(out.Groups, func(a, b int) bool {
		if out.Groups[a].Count != out.Groups[b].Count {
			return out.Groups[a].Count > out.Groups[b].Count
		}
		return out.Groups[a].NormTitle < out.Groups[b].NormTitle
	})
	if len(out.Groups) > retroLessonGroupsLimit {
		out.Groups = out.Groups[:retroLessonGroupsLimit]
	}
	return out, nil
}

// buildRetroLessons computes the lessons feed for one resolved window and
// scope. Split out of the handler so /api/retro/report can reuse it.
func (h *Handler) buildRetroLessons(dr dateRange, pf string, pargs []any) (retroLessonsDTO, error) {

	// COALESCE for the same reason as the grouped twin above: tasks.external_id
	// is NULLABLE (migration 0006) and scanning a NULL into a string 500s the feed.
	rows, err := h.DB.Query(`
		SELECT COALESCE(t.external_id, CAST(t.id AS TEXT)),
		       t.title, t.started_at, l.seq, l.title, l.action, l.body
		  FROM retro_lessons l
		  JOIN task_retros r ON r.id = l.retro_id
		  JOIN tasks t ON t.id = r.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`+pf+`
		 ORDER BY t.started_at DESC, t.external_id DESC, l.seq ASC
		 LIMIT ?`,
		append(append([]any{dr.start, dr.end}, pargs...), retroLessonsLimit)...)
	if err != nil {
		return retroLessonsDTO{}, err
	}
	defer rows.Close()

	out := retroLessonsDTO{Lessons: []retroLessonDTO{}}
	for rows.Next() {
		var d retroLessonDTO
		var startedAt string
		var action, body sql.NullString
		if err := rows.Scan(&d.TaskExternalID, &d.TaskTitle, &startedAt, &d.Seq, &d.Title, &action, &body); err != nil {
			return retroLessonsDTO{}, err
		}
		// tasks.started_at is the card's calendar date at UTC midnight — the
		// date IS its first 10 chars (a tz conversion could shift the day).
		if len(startedAt) >= 10 {
			d.Date = startedAt[:10]
		}
		if action.Valid {
			d.Action = &action.String
		}
		if body.Valid {
			d.Body = &body.String
		}
		out.Lessons = append(out.Lessons, d)
	}
	if err := rows.Err(); err != nil {
		return retroLessonsDTO{}, err
	}
	return out, nil
}

// ── /api/retro/tasks ──────────────────────────────────────────────────────

type retroVerdictsDTO struct {
	OK         int64 `json:"ok"`
	Redispatch int64 `json:"redispatch"`
}

type retroTaskDTO struct {
	ExternalID     string           `json:"external_id"`
	Title          string           `json:"title"`
	EstimatedHours *float64         `json:"estimated_hours"`
	ActualHours    *float64         `json:"actual_hours"`
	VariancePct    *float64         `json:"variance_pct"`
	Loops          int64            `json:"loops"`
	Delegations    int64            `json:"delegations"`
	Verdicts       retroVerdictsDTO `json:"verdicts"`
}

type retroTasksDTO struct {
	Tasks []retroTaskDTO `json:"tasks"`
	// Closure is the window's closure measurables — how many tasks carry a filled
	// Completion Report, and re-dispatch computed across the tasks that do.
	//
	// It rides on THIS endpoint rather than a new one because this is the churn
	// table: the retro that asked for these numbers could compute churn for two
	// tasks, and the fix is the same page answering how many tasks are reportable
	// at all. Computed by internal/advisor.Closure, which reuses isRedispatch —
	// so this and the digest cannot disagree about what a re-dispatch is.
	Closure retroClosureDTO `json:"closure"`
}

// retroClosureDTO is advisor.ClosureStats on the wire.
type retroClosureDTO struct {
	Tasks            int64   `json:"tasks"`
	TasksWithReport  int64   `json:"tasksWithReport"`
	Phases           int64   `json:"phases"`
	PhasesWithReport int64   `json:"phasesWithReport"`
	Delegations      int64   `json:"delegations"`
	Redispatches     int64   `json:"redispatches"`
	ReportRate       float64 `json:"reportRate"`
	RedispatchRate   float64 `json:"redispatchRate"`
	Summary          string  `json:"summary"`
}

// retroTasksLimit caps the estimation table — one row per workspace task in
// range, newest first; 200 covers months of tasks while bounding the response
// (same idea as retroLessonsLimit).
const retroTasksLimit = 200

// GET /api/retro/tasks?from&to&project — estimation accuracy + orchestration
// churn per workspace task in range. Only tasks with at least one parsed
// artifact (retro doc, loop journal, or ledger) appear; newest first.
func (h *Handler) retroTasks(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	out, err := h.buildRetroTasks(dr, pf, pargs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// buildRetroTasks computes the estimation table for one resolved window and
// scope. Split out of the handler so /api/retro/report can reuse it.
func (h *Handler) buildRetroTasks(dr dateRange, pf string, pargs []any) (retroTasksDTO, error) {
	// Before the cursor: single-connection pool, so a nested query on an open
	// cursor deadlocks.
	closure, cerr := advisor.Closure(h.DB, dr.start, dr.end)
	if cerr != nil {
		return retroTasksDTO{}, cerr
	}

	rows, err := h.DB.Query(`
		SELECT t.id, t.external_id, t.title,
		       r.estimated_hours, r.actual_hours, r.variance_pct,
		       (SELECT COUNT(*) FROM task_loops tl WHERE tl.task_id = t.id)
		  FROM tasks t
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN task_retros r ON r.task_id = t.id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`+pf+`
		   AND (r.id IS NOT NULL
		        OR EXISTS (SELECT 1 FROM task_loops tl WHERE tl.task_id = t.id)
		        OR EXISTS (SELECT 1 FROM task_delegations td WHERE td.task_id = t.id))
		 ORDER BY t.started_at DESC, t.external_id DESC
		 LIMIT ?`,
		append(append([]any{dr.start, dr.end}, pargs...), retroTasksLimit)...)
	if err != nil {
		return retroTasksDTO{}, err
	}
	defer rows.Close()

	out := retroTasksDTO{Tasks: []retroTaskDTO{}}
	index := map[int64]int{} // task id → out.Tasks slot, for the verdict pass
	var ids []any
	for rows.Next() {
		var d retroTaskDTO
		var id int64
		var est, act, variance sql.NullFloat64
		if err := rows.Scan(&id, &d.ExternalID, &d.Title, &est, &act, &variance, &d.Loops); err != nil {
			return retroTasksDTO{}, err
		}
		if est.Valid {
			d.EstimatedHours = &est.Float64
		}
		if act.Valid {
			d.ActualHours = &act.Float64
		}
		if variance.Valid {
			d.VariancePct = &variance.Float64
		}
		index[id] = len(out.Tasks)
		ids = append(ids, id)
		out.Tasks = append(out.Tasks, d)
	}
	if err := rows.Err(); err != nil {
		return retroTasksDTO{}, err
	}

	// Verdict split: classify every ledger row of the selected tasks in Go
	// (the same isRedispatch as the per-agent rate).
	if len(ids) > 0 {
		q := `SELECT task_id, COALESCE(verdict, '') FROM task_delegations WHERE task_id IN (?` +
			strings.Repeat(",?", len(ids)-1) + `)`
		vrows, err := h.DB.Query(q, ids...)
		if err != nil {
			return retroTasksDTO{}, err
		}
		defer vrows.Close()
		for vrows.Next() {
			var id int64
			var verdict string
			if err := vrows.Scan(&id, &verdict); err != nil {
				return retroTasksDTO{}, err
			}
			slot, ok := index[id]
			if !ok {
				continue
			}
			out.Tasks[slot].Delegations++
			if isRedispatch(verdict) {
				out.Tasks[slot].Verdicts.Redispatch++
			} else {
				out.Tasks[slot].Verdicts.OK++
			}
		}
		if err := vrows.Err(); err != nil {
			return retroTasksDTO{}, err
		}
	}
	out.Closure = retroClosureDTO{
		Tasks:            closure.Tasks,
		TasksWithReport:  closure.TasksWithReport,
		Phases:           closure.Phases,
		PhasesWithReport: closure.PhasesWithReport,
		Delegations:      closure.Delegations,
		Redispatches:     closure.Redispatches,
		ReportRate:       closure.ReportRate(),
		RedispatchRate:   closure.RedispatchRate(),
		Summary:          closure.Summary(),
	}
	return out, nil
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
			`SELECT id FROM projects WHERE `+projectMatchExpr(""),
			scopeArgs(project)...).Scan(&pid)
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

// ── /api/retro/recommendations (phase 3 — internal/advisor) ───────────────

type recommendationDTO struct {
	ID         int64  `json:"id"`
	Rule       string `json:"rule"`
	TargetKind string `json:"target_kind"`
	Target     string `json:"target"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	// Evidence is the advisor's evidence JSON passed through verbatim.
	Evidence json.RawMessage `json:"evidence"`
	// Baseline is the advisor's metric snapshot (written on accept, extended
	// with adopted_at on adoption) passed through verbatim; null before accept.
	// The UI derives the "verifying in N days" countdown and the metric-vs-
	// baseline progress from it.
	Baseline  json.RawMessage `json:"baseline"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// scanBaseline folds a nullable recommendations.baseline column into the DTO:
// NULL stays a JSON null, anything else passes through verbatim.
func (d *recommendationDTO) scanBaseline(base sql.NullString) {
	if base.Valid {
		d.Baseline = json.RawMessage(base.String)
	} else {
		d.Baseline = json.RawMessage("null")
	}
}

type recommendationsDTO struct {
	Recommendations []recommendationDTO `json:"recommendations"`
}

// recStatuses is the closed status vocabulary of migration 0019, widened by
// 0063 with `resolved` — the condition stopped reproducing, so the advisor
// closed the row itself. It is a terminal state like `verified` and
// `dismissed`, and it is deliberately NOT in the actionable rail below: a row
// nobody has to act on must not keep asking to be acted on.
var recStatuses = map[string]bool{
	"proposed": true, "accepted": true, "dismissed": true,
	"adopted": true, "verified": true, "resolved": true,
}

// GET /api/retro/recommendations?status=proposed,accepted — advisor
// recommendations, newest activity first. Default filter is the "actionable
// rail" set (proposed,accepted,adopted); status=all disables filtering (the
// UI fetches status=verified lazily for its history section).
//
// ?projectId=<slug|id> narrows the OUTPUT to recommendations attributable to
// that project — those whose evidence session_ids resolve to one of the
// project's sessions. Recommendations are global by identity (dedup_key =
// rule:target, aggregated cross-project), so this is a post-filter on the
// evidence rather than a scoped query: fleet-level rules with no session
// attribution (R5 process / R6 config / R7 architecture) drop out of a
// project-scoped view, which is correct — they belong to the whole fleet.
func (h *Handler) retroRecommendations(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("status")
	if filter == "" {
		filter = defaultRecStatusFilter
	}
	var statuses []string
	if filter != "all" {
		for _, p := range strings.Split(filter, ",") {
			p = strings.TrimSpace(p)
			if !recStatuses[p] {
				writeClientErr(w, http.StatusBadRequest, "unknown status "+p)
				return
			}
			statuses = append(statuses, p)
		}
	}
	out, err := h.buildRetroRecommendations(statuses, strings.TrimSpace(r.URL.Query().Get("projectId")))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// defaultRecStatusFilter is the "actionable rail" set the UI asks for when it
// passes no ?status= at all.
const defaultRecStatusFilter = "proposed,accepted,adopted"

// buildRetroRecommendations lists advisor recommendations, newest activity
// first. A nil statuses slice means "every status" (?status=all); a non-empty
// projectID post-filters on evidence attribution. Split out of the handler so
// /api/retro/report can reuse it — the status vocabulary is validated by the
// caller, because only an HTTP request can answer a bad one with a 400.
func (h *Handler) buildRetroRecommendations(statuses []string, projectID string) (recommendationsDTO, error) {
	q := `SELECT id, rule, target_kind, target, title, detail, evidence, baseline,
	             status, created_at, updated_at
	        FROM recommendations`
	var args []any
	if len(statuses) > 0 {
		ph := make([]string, 0, len(statuses))
		for _, s := range statuses {
			ph = append(ph, "?")
			args = append(args, s)
		}
		q += ` WHERE status IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY updated_at DESC, id DESC`

	// Resolve the optional project scope to its evidence-session set up front
	// (empty set ⇒ nothing matches, an unknown project yields no recs).
	var projectUUIDs map[string]struct{}
	scoped := false
	if projectID != "" {
		scoped = true
		set, err := h.projectSessionUUIDs(projectID)
		if err != nil {
			return recommendationsDTO{}, err
		}
		projectUUIDs = set
	}

	rows, err := h.DB.Query(q, args...)
	if err != nil {
		return recommendationsDTO{}, err
	}
	defer rows.Close()
	out := recommendationsDTO{Recommendations: []recommendationDTO{}}
	for rows.Next() {
		var d recommendationDTO
		var evidence string
		var base sql.NullString
		if err := rows.Scan(&d.ID, &d.Rule, &d.TargetKind, &d.Target, &d.Title,
			&d.Detail, &evidence, &base, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return recommendationsDTO{}, err
		}
		if scoped && !evidenceInProject(evidence, projectUUIDs) {
			continue
		}
		d.Evidence = json.RawMessage(evidence)
		d.scanBaseline(base)
		out.Recommendations = append(out.Recommendations, d)
	}
	return out, rows.Err()
}

// projectSessionUUIDs returns the set of session_uuids belonging to a project
// (resolved by slug or id). Used to attribute recommendations to a project via
// their evidence session_ids.
func (h *Handler) projectSessionUUIDs(project string) (map[string]struct{}, error) {
	rows, err := h.DB.Query(`
		SELECT s.session_uuid
		  FROM sessions s
		  JOIN projects p ON p.id = s.project_id
		 WHERE `+projectMatchExpr("p."), scopeArgs(project)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]struct{}{}
	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			return nil, err
		}
		set[uuid] = struct{}{}
	}
	return set, rows.Err()
}

// evidenceInProject reports whether a recommendation's evidence JSON references
// at least one session in the project set. The advisor embeds a capped sample
// of session_ids (rules.go) for exactly the rules that are project-attributable
// (R1 tool / R2 agent / R3 error_group / R4 re-dispatch); rules without a
// session_ids array (R5/R6/R7) are fleet-level and never match a project.
func evidenceInProject(evidence string, projectUUIDs map[string]struct{}) bool {
	if len(projectUUIDs) == 0 {
		return false
	}
	var parsed struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.Unmarshal([]byte(evidence), &parsed); err != nil {
		return false // unparseable evidence can't be attributed
	}
	for _, uuid := range parsed.SessionIDs {
		if _, ok := projectUUIDs[uuid]; ok {
			return true
		}
	}
	return false
}

// legalRecTransition guards the user-driven part of the lifecycle: accept or
// dismiss a proposal, dismiss an already-accepted one (changed your mind
// before adoption). Everything else — adopted/verified — is the advisor's
// automation, never a PATCH.
func legalRecTransition(from, to string) bool {
	switch {
	case from == "proposed" && (to == "accepted" || to == "dismissed"):
		return true
	case from == "accepted" && to == "dismissed":
		return true
	}
	return false
}

// PATCH /api/retro/recommendations/{id} — body {"status":"accepted"|"dismissed"}.
// Illegal transitions are 422. Accepting snapshots the rule's current metric
// as the verification baseline (advisor.BaselineFor, with accepted_at baked
// into the JSON for the adoption detector).
func (h *Handler) patchRecommendation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Status != "accepted" && body.Status != "dismissed" {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"status must be accepted or dismissed")
		return
	}

	var rule, target, status string
	err := h.DB.QueryRow(
		`SELECT rule, target, status FROM recommendations WHERE id = ?`, id).
		Scan(&rule, &target, &status)
	if err == sql.ErrNoRows {
		writeClientErr(w, http.StatusNotFound, "recommendation not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if !legalRecTransition(status, body.Status) {
		writeClientErr(w, http.StatusUnprocessableEntity,
			"illegal transition "+status+" -> "+body.Status)
		return
	}

	now := time.Now()
	nowS := now.UTC().Format("2006-01-02T15:04:05.000Z")
	if h.recPatchHook != nil {
		h.recPatchHook() // test seam: mutate between the read and the guarded write
	}
	// Guarded write: the status predicate re-checks the status we validated
	// the transition against, so a concurrent writer (advisor Run flipping to
	// adopted/verified, another PATCH) can't be silently overwritten.
	var res sql.Result
	if body.Status == "accepted" {
		base, berr := advisor.BaselineFor(h.DB, rule, target, now)
		if berr != nil {
			writeErr(w, berr)
			return
		}
		res, err = h.DB.Exec(`UPDATE recommendations
			SET status = 'accepted', baseline = ?, updated_at = ?
			WHERE id = ? AND status = ?`, base, nowS, id, status)
	} else {
		res, err = h.DB.Exec(`UPDATE recommendations
			SET status = 'dismissed', updated_at = ?
			WHERE id = ? AND status = ?`, nowS, id, status)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, aerr := res.RowsAffected(); aerr != nil {
		writeErr(w, aerr)
		return
	} else if n == 0 {
		// Lost the race — surface the CURRENT status so the client can resync.
		var cur string
		if rerr := h.DB.QueryRow(
			`SELECT status FROM recommendations WHERE id = ?`, id).Scan(&cur); rerr != nil {
			writeErr(w, rerr)
			return
		}
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error":  "status changed concurrently: now " + cur,
			"status": cur,
		})
		return
	}

	var d recommendationDTO
	var evidence string
	var base sql.NullString
	err = h.DB.QueryRow(`SELECT id, rule, target_kind, target, title, detail,
			evidence, baseline, status, created_at, updated_at
		 FROM recommendations WHERE id = ?`, id).
		Scan(&d.ID, &d.Rule, &d.TargetKind, &d.Target, &d.Title, &d.Detail,
			&evidence, &base, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	d.Evidence = json.RawMessage(evidence)
	d.scanBaseline(base)
	writeJSON(w, d, nil)
}

// POST /api/retro/advise — run the advisor engine now ("Analyze now") and
// return its Stats tally.
//
// ?projectId=<slug|id> is accepted for API symmetry with the project-scoped
// Insights card, but the engine ALWAYS runs over the full corpus: the rules
// compute cross-project rates (an agent's error rate, a tool's denial rate) and
// scoping the input to one project's slice would produce statistically wrong
// numbers. Per-project narrowing happens on the READ side (the recommendations
// GET filters by evidence session). An unknown project id is not an error —
// the run proceeds fleet-wide either way.
func (h *Handler) retroAdvise(w http.ResponseWriter, r *http.Request) {
	// "Analyze now" runs the DETERMINISTIC path only: trajeval (local, free,
	// System-excluded) + the rule engine. It deliberately does NOT fire the
	// LLM-judge (trajjudge.Score) — that spawned up to capN headless `claude -p`
	// runs per click, which read as a burst of mystery "System" sessions and
	// burned tokens on a manual refresh. The LLM judge now runs only on the
	// daemon's 24h schedule, gated by the restart cooldown.
	go func() {
		if err := trajeval.Compute(h.DB, time.Now()); err != nil {
			log.Printf("trajeval.Compute: %v", err)
		}
	}()
	stats, err := advisor.Run(h.DB, time.Now())
	writeJSON(w, stats, err)
}
