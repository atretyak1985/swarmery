package advisor

// The six deterministic rules (R1..R6). Each rule folds data already in
// SQLite over the trailing evaluation window into zero or more findings —
// no LLM anywhere. Aggregation grains deliberately mirror internal/api so
// the numbers a recommendation cites agree with what the Retro/Analytics
// pages show:
//
//	R1 denied tools        — the /api/retro/friction denied-tools skeleton
//	R2 agent error rate    — the retroAgentWindow parent_event_id attribution
//	                         (rate = failed-run share, not error events / runs)
//	R3 recurring errors    — the errorGroups normalizeErrKey fold
//	R4 re-dispatch share   — the delegationRates isRedispatch classifier
//	R5 stale improvements  — retro_improvements via task_retros/tasks
//	R6 cache regression    — the Analytics timeseriesCache hit-rate math
//
// The tiny classifiers (agent-name fold, error-message normalization,
// re-dispatch verdict grammar) are duplicated here rather than imported:
// internal/api imports this package (BaselineFor/Run from the handlers), so
// importing api back would cycle. Each twin carries a pointer to its
// original — keep them in lockstep.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// Rule thresholds. Windows are days; shares are 0..1 fractions.
const (
	// WindowDays is the trailing evaluation window of every rule (and of
	// BaselineFor snapshots).
	WindowDays = 14
	// R1MinDenied: a tool must be denied at least this often to propose a rule.
	R1MinDenied = 5
	// R2MinRuns: agents below this run count are excluded from the error-rate
	// comparison (both as candidates and from the median).
	R2MinRuns = 10
	// R2MedianFactor: flag an agent when error_rate > factor × median.
	R2MedianFactor = 2.0
	// R2MinErrors: absolute floor on RAW ERROR EVENTS (not failed runs) — an
	// agent with fewer error events than this in the window is never flagged,
	// however badly its failed-run share beats the median test (1–2 errors
	// over 10 runs is statistical noise, not a pattern). The floor applies to
	// CANDIDATES only; the median is still computed over all agents with
	// ≥ R2MinRuns runs so it stays representative.
	R2MinErrors = 3
	// R3MinDays: an error group must recur on at least this many distinct
	// local days.
	R3MinDays = 3
	// R4MinRows: minimum delegation-ledger rows before the share is judged.
	R4MinRows = 3
	// R4ShareThreshold: flag an agent when redispatch share exceeds this.
	R4ShareThreshold = 0.25
	// R5StaleDays: a high-priority improvement is stale when its retro was
	// ingested more than this many days ago and it is still open.
	R5StaleDays = 14
	// R6DropPP: flag when the cache hit rate dropped by more than this many
	// percentage points (as a 0..1 fraction) vs the preceding window.
	R6DropPP = 0.10
)

// finding is one rule hit, pre-persistence.
type finding struct {
	rule       string
	targetKind string
	target     string
	title      string
	detail     string
	evidence   map[string]any
}

type window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ── duplicated classifiers (api twins) ────────────────────────────────────

// normAgent folds "core:tech-lead" and "tech-lead" to the same lowercase key.
// Twin of internal/api normAgentType (system_history.go) + the lowercase fold
// delegationRates applies; lowercased here unconditionally because advisor
// targets must be case-stable dedup keys.
func normAgent(t string) string {
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		t = t[i+1:]
	}
	return strings.ToLower(t)
}

var (
	reErrIDToken = regexp.MustCompile(`[a-z0-9_]*[0-9][a-z0-9_]*`)
	reErrSpace   = regexp.MustCompile(`\s+`)
)

// normalizeErrKey folds one raw error message to its group key. Twin of
// internal/api/errors.go normalizeErrKey — keep in lockstep.
func normalizeErrKey(msg string) string {
	k := strings.ToLower(msg)
	k = reErrIDToken.ReplaceAllString(k, "#")
	k = reErrSpace.ReplaceAllString(k, " ")
	k = strings.TrimSpace(k)
	if r := []rune(k); len(r) > 80 {
		k = string(r[:80])
	}
	return k
}

// extractErrMsg pulls a human-readable message out of one error event payload.
// Twin of internal/api/errors.go extractErrMsg — keep in lockstep.
func extractErrMsg(typ string, toolName, payload sql.NullString) string {
	var p map[string]any
	if payload.Valid {
		_ = json.Unmarshal([]byte(payload.String), &p)
	}
	if typ == "error" { // system api_error: payload.error
		switch e := p["error"].(type) {
		case string:
			if e != "" {
				return e
			}
		case map[string]any:
			if m, ok := e["message"].(string); ok && m != "" {
				return m
			}
			if inner, ok := e["error"].(map[string]any); ok {
				if m, ok := inner["message"].(string); ok && m != "" {
					return m
				}
			}
			if b, err := json.Marshal(e); err == nil {
				return string(b)
			}
		}
		return "api error"
	}
	switch res := p["result"].(type) {
	case string:
		if res != "" {
			return res
		}
	case map[string]any:
		if m, ok := res["message"].(string); ok && m != "" {
			return m
		}
		if b, err := json.Marshal(res); err == nil {
			return string(b)
		}
	}
	if toolName.Valid && toolName.String != "" {
		return toolName.String + " error"
	}
	return "error"
}

// redispatchRe / redispatchVerdictMaxRunes / isRedispatch classify a
// delegation-ledger verdict cell as a re-dispatch. Twin of
// internal/api/retro.go isRedispatch (bilingual grammar + prose guard) —
// keep in lockstep.
var redispatchRe = regexp.MustCompile(`(?i)(re-?dispatch|redo|\bfail(ed|ure)?\b|\breject(ed)?\b|повтор|відхил|провал|фейл)`)

const redispatchVerdictMaxRunes = 40

func isRedispatch(verdict string) bool {
	v := strings.TrimSpace(verdict)
	if len([]rune(v)) > redispatchVerdictMaxRunes {
		return false
	}
	return redispatchRe.MatchString(v)
}

// capRunes truncates s to n runes, appending an ellipsis when cut.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// localDay converts a stored UTC timestamp to its local YYYY-MM-DD day.
// Twin of internal/api/analytics.go localDay — keep in lockstep.
func localDay(ts string) (string, bool) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// zone-suffix-free bound form ("2006-01-02T15:04:05").
		t, err = time.Parse("2006-01-02T15:04:05", ts)
		if err != nil {
			return "", false
		}
	}
	return t.Local().Format("2006-01-02"), true
}

// ── R1: tool friction ─────────────────────────────────────────────────────

// r1DeniedTools proposes an auto-approve rule for every tool denied at least
// R1MinDenied times in the window with no enabled approval rule covering it.
// Rule coverage uses the REAL rule grammar (approvals.ParseRulePattern), the
// same check as the friction board's has_rule — a Tool(argGlob) rule still
// counts as covering the tool. Advisor runs unscoped, so rules of EVERY
// project scope count as coverage (a scoped rule is still a signal the
// friction is being handled).
func r1DeniedTools(db *sql.DB, win window) ([]finding, error) {
	rules, err := enabledRulePatterns(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT e.tool_name, COALESCE(e.status, ''), s.session_uuid
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.tool_name IS NOT NULL
		   AND e.type IN ('tool_call', 'skill_use', 'subagent_start')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		calls, denied int64
		sessions      []string
		seen          map[string]struct{}
	}
	byTool := map[string]*acc{}
	for rows.Next() {
		var tool, status, uuid string
		if err := rows.Scan(&tool, &status, &uuid); err != nil {
			return nil, err
		}
		a := byTool[tool]
		if a == nil {
			a = &acc{seen: map[string]struct{}{}}
			byTool[tool] = a
		}
		a.calls++
		if status == "denied" {
			a.denied++
			if _, ok := a.seen[uuid]; !ok && len(a.sessions) < 3 {
				a.seen[uuid] = struct{}{}
				a.sessions = append(a.sessions, uuid)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []finding
	for tool, a := range byTool {
		if a.denied < R1MinDenied || ruleCoversTool(rules, tool) {
			continue
		}
		out = append(out, finding{
			rule: "R1", targetKind: "tool", target: tool,
			title: "Add auto-approve rule for " + tool,
			detail: fmt.Sprintf(
				"%s was denied %d times across %d calls in the last %d days and no enabled approval rule covers it.",
				tool, a.denied, a.calls, WindowDays),
			evidence: map[string]any{
				"window":      win,
				"counts":      map[string]int64{"denied": a.denied, "calls": a.calls},
				"session_ids": a.sessions,
			},
		})
	}
	sortFindings(out)
	return out, nil
}

// enabledRulePatterns loads every enabled approval-rule pattern (all project
// scopes), parsed with the shared grammar; unparseable rows are skipped,
// mirroring the evaluator.
func enabledRulePatterns(db *sql.DB) ([]approvals.RulePattern, error) {
	rows, err := db.Query(`SELECT tool_pattern FROM approval_rules WHERE enabled = 1`)
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

// ruleCoversTool — twin of internal/api/retro.go ruleCoversTool.
func ruleCoversTool(rules []approvals.RulePattern, tool string) bool {
	for _, rp := range rules {
		if rp.Tool == tool {
			return true
		}
	}
	return false
}

// ── R2: agent error rate ──────────────────────────────────────────────────

// agentErrStats is one agent's window aggregates for R2 / the R2 metric.
type agentErrStats struct {
	runs   int64
	errors int64
	// failedRunKeys dedupes the runs with ≥1 error: keyed by the parent
	// subagent_start event id when the error row is parented to one, else by
	// the (unparented) subagent_stop's own event id — twin of internal/api
	// retroAgentWin.failedRunKeys. len() = failed runs.
	failedRunKeys map[int64]struct{}
	errKeys       map[string]int64
	sessions      []string
	seen          map[string]struct{}
}

// failedRuns is the number of distinct runs with at least one error.
func (a *agentErrStats) failedRuns() int64 { return int64(len(a.failedRunKeys)) }

// agentErrorWindow computes per-agent runs (subagent_start fold) and errors
// (status='error' events attributed through the parent_event_id chain — the
// SAME grain as internal/api retroAgentWindow, duplicated with the identical
// classification if/else so numbers agree with the Retro scorecards; see
// that function for the full rationale of the type set, the subagent_start
// exclusion, and the failed-run dedupe backing the failed-run share). The
// "main" bucket (orchestrator) is dropped.
func agentErrorWindow(db *sql.DB, win window) (map[string]*agentErrStats, error) {
	acc := map[string]*agentErrStats{}
	get := func(key string) *agentErrStats {
		a := acc[key]
		if a == nil {
			a = &agentErrStats{
				failedRunKeys: map[int64]struct{}{},
				errKeys:       map[string]int64{},
				seen:          map[string]struct{}{},
			}
			acc[key] = a
		}
		return a
	}

	rows, err := db.Query(`
		SELECT json_extract(e.payload, '$.subagent_type')
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.type = 'subagent_start'
		   AND json_extract(e.payload, '$.subagent_type') IS NOT NULL
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if key := normAgent(name.String); key != "" {
			get(key).runs++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	erows, err := db.Query(`
		SELECT e.id, e.parent_event_id, e.type,
		       json_extract(e.payload, '$.agentType'),
		       COALESCE(pe.type, ''),
		       json_extract(pe.payload, '$.subagent_type'),
		       e.tool_name, e.payload, s.session_uuid
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		  LEFT JOIN events pe ON pe.id = e.parent_event_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var id int64
		var parentID sql.NullInt64
		var typ, parentType, uuid string
		var ownType, subType, toolName, payload sql.NullString
		if err := erows.Scan(&id, &parentID, &typ, &ownType, &parentType, &subType, &toolName, &payload, &uuid); err != nil {
			return nil, err
		}
		key := ""
		if typ == "subagent_stop" && ownType.Valid && ownType.String != "" {
			key = normAgent(ownType.String)
		} else if parentType == "subagent_start" && subType.Valid && subType.String != "" {
			key = normAgent(subType.String)
		}
		if key == "" {
			continue // orchestrator ("main") — not an R2 target
		}
		a := get(key)
		a.errors++
		runKey := id // unparented subagent_stop: the stop IS the run's proxy
		if parentType == "subagent_start" && parentID.Valid {
			runKey = parentID.Int64
		}
		a.failedRunKeys[runKey] = struct{}{}
		a.errKeys[normalizeErrKey(extractErrMsg(typ, toolName, payload))]++
		if _, ok := a.seen[uuid]; !ok && len(a.sessions) < 3 {
			a.seen[uuid] = struct{}{}
			a.sessions = append(a.sessions, uuid)
		}
	}
	return acc, erows.Err()
}

// r2AgentErrorRate flags agents with ≥ R2MinRuns runs AND ≥ R2MinErrors raw
// error events whose failed-run share (distinct runs with ≥1 error / runs —
// the same grain as the Retro scorecards' error_rate) exceeds R2MedianFactor
// × the median share among all agents with ≥ R2MinRuns runs in the window.
func r2AgentErrorRate(db *sql.DB, win window) ([]finding, error) {
	acc, err := agentErrorWindow(db, win)
	if err != nil {
		return nil, err
	}
	type cand struct {
		agent string
		stats *agentErrStats
		rate  float64
	}
	var cands []cand
	var rates []float64
	for agent, a := range acc {
		if a.runs < R2MinRuns {
			continue
		}
		// Clamped to ≤1 — a run spanning the window start can contribute a
		// failed run without contributing to the run count (twin of the
		// internal/api errRate clamp and the R2 metricValue clamp).
		rate := min(1, float64(a.failedRuns())/float64(a.runs))
		cands = append(cands, cand{agent, a, rate})
		rates = append(rates, rate)
	}
	if len(rates) == 0 {
		return nil, nil
	}
	sort.Float64s(rates)
	median := rates[len(rates)/2]
	if len(rates)%2 == 0 {
		median = (rates[len(rates)/2-1] + rates[len(rates)/2]) / 2
	}

	var out []finding
	for _, c := range cands {
		if c.rate <= R2MedianFactor*median || c.stats.errors < R2MinErrors {
			continue
		}
		topKey, topCount := "", int64(0)
		for k, n := range c.stats.errKeys {
			if n > topCount || (n == topCount && k < topKey) {
				topKey, topCount = k, n
			}
		}
		out = append(out, finding{
			rule: "R2", targetKind: "agent", target: c.agent,
			title: "Review error rate of agent " + c.agent,
			detail: fmt.Sprintf(
				"Agent %s failed on %d of %d runs (%.0f%%; %d error events) in the last %d days — more than %.0f× the %.0f%% median failed-run share among agents with ≥%d runs. Top error group: %q.",
				c.agent, c.stats.failedRuns(), c.stats.runs, c.rate*100,
				c.stats.errors, WindowDays,
				R2MedianFactor, median*100, R2MinRuns, topKey),
			evidence: map[string]any{
				"window": win,
				"counts": map[string]any{
					"runs": c.stats.runs, "failed_runs": c.stats.failedRuns(),
					"errors":     c.stats.errors,
					"error_rate": c.rate, "median_error_rate": median,
				},
				"top_error_group": topKey,
				"session_ids":     c.stats.sessions,
			},
		})
	}
	sortFindings(out)
	return out, nil
}

// ── R3: recurring error groups ────────────────────────────────────────────

// r3RecurringErrors flags error groups (normalizeErrKey fold — the SAME type
// set as internal/api errorGroups, including subagent_start) recurring on
// ≥ R3MinDays distinct local days in the window.
func r3RecurringErrors(db *sql.DB, win window) ([]finding, error) {
	rows, err := db.Query(`
		SELECT e.type, e.tool_name, e.payload, e.ts, s.session_uuid
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_start','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0
		 ORDER BY e.ts DESC`,
		win.From, win.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		example  string
		count    int64
		days     map[string]struct{}
		sessions []string
		seen     map[string]struct{}
	}
	byKey := map[string]*acc{}
	for rows.Next() {
		var typ, ts, uuid string
		var toolName, payload sql.NullString
		if err := rows.Scan(&typ, &toolName, &payload, &ts, &uuid); err != nil {
			return nil, err
		}
		msg := extractErrMsg(typ, toolName, payload)
		key := normalizeErrKey(msg)
		a := byKey[key]
		if a == nil {
			// Rows arrive ts DESC → the first message of a group is its newest.
			a = &acc{example: capRunes(msg, 160), days: map[string]struct{}{}, seen: map[string]struct{}{}}
			byKey[key] = a
		}
		a.count++
		if day, ok := localDay(ts); ok {
			a.days[day] = struct{}{}
		}
		if _, ok := a.seen[uuid]; !ok && len(a.sessions) < 3 {
			a.seen[uuid] = struct{}{}
			a.sessions = append(a.sessions, uuid)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []finding
	for key, a := range byKey {
		if len(a.days) < R3MinDays {
			continue
		}
		days := make([]string, 0, len(a.days))
		for d := range a.days {
			days = append(days, d)
		}
		sort.Strings(days)
		out = append(out, finding{
			rule: "R3", targetKind: "error_group", target: key,
			title: "Recurring error: " + capRunes(a.example, 80),
			detail: fmt.Sprintf(
				"Error group %q occurred %d times on %d distinct days in the last %d days (example: %q).",
				key, a.count, len(days), WindowDays, a.example),
			evidence: map[string]any{
				"window":      win,
				"counts":      map[string]int64{"errors": a.count},
				"days":        days,
				"session_ids": a.sessions,
			},
		})
	}
	sortFindings(out)
	return out, nil
}

// ── R4: re-dispatch share ─────────────────────────────────────────────────

// delegationShares aggregates the task_delegations ledger over tasks STARTED
// in the window — twin of internal/api delegationRates (same tasks.started_at
// range semantics, same lowercase fold, same isRedispatch classifier).
func delegationShares(db *sql.DB, win window) (map[string][2]int64, error) {
	rows, err := db.Query(`
		SELECT td.agent, COALESCE(td.verdict, '')
		  FROM task_delegations td
		  JOIN tasks t ON t.id = td.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`,
		win.From, win.To)
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
		key := normAgent(agent)
		c := out[key]
		c[1]++
		if isRedispatch(verdict) {
			c[0]++
		}
		out[key] = c
	}
	return out, rows.Err()
}

// r4Redispatch flags agents with ≥ R4MinRows ledger rows in the window whose
// re-dispatch share exceeds R4ShareThreshold.
func r4Redispatch(db *sql.DB, win window) ([]finding, error) {
	shares, err := delegationShares(db, win)
	if err != nil {
		return nil, err
	}
	var out []finding
	for agent, c := range shares {
		redis, total := c[0], c[1]
		if total < R4MinRows {
			continue
		}
		share := float64(redis) / float64(total)
		if share <= R4ShareThreshold {
			continue
		}
		out = append(out, finding{
			rule: "R4", targetKind: "agent", target: agent,
			title: "Reduce re-dispatch rate of agent " + agent,
			detail: fmt.Sprintf(
				"Agent %s was re-dispatched on %d of %d delegations (%.0f%%) in the last %d days — above the %.0f%% threshold. Its brief or acceptance criteria likely need sharpening.",
				agent, redis, total, share*100, WindowDays, R4ShareThreshold*100),
			evidence: map[string]any{
				"window": win,
				"counts": map[string]any{
					"redispatches": redis, "delegations": total, "share": share,
				},
			},
		})
	}
	sortFindings(out)
	return out, nil
}

// ── R5: stale high-priority improvements ──────────────────────────────────

var (
	r5PriorityRe = regexp.MustCompile(`(?i)high|p0|p1`)
	r5DoneRe     = regexp.MustCompile(`(?i)done|closed|виконано`)
)

// r5StaleImprovements flags high-priority retro improvements still open more
// than R5StaleDays after their retro doc was ingested. Deliberately NOT
// window-bound on task dates: a stale improvement only gets MORE relevant
// with age. The target (external task id + improvement rowid) is the stable
// dedup key.
func r5StaleImprovements(db *sql.DB, win window, now time.Time) ([]finding, error) {
	rows, err := db.Query(`
		SELECT ri.id, ri.text, COALESCE(ri.priority, ''), COALESCE(ri.status, ''),
		       COALESCE(t.external_id, CAST(t.id AS TEXT)), tr.ingested_at
		  FROM retro_improvements ri
		  JOIN task_retros tr ON tr.id = ri.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE p.archived = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cutoff := now.AddDate(0, 0, -R5StaleDays)
	var out []finding
	for rows.Next() {
		var rowid int64
		var text, priority, status, taskID, ingestedAt string
		if err := rows.Scan(&rowid, &text, &priority, &status, &taskID, &ingestedAt); err != nil {
			return nil, err
		}
		if !r5PriorityRe.MatchString(priority) || r5DoneRe.MatchString(status) {
			continue
		}
		ing, err := time.Parse(time.RFC3339, ingestedAt)
		if err != nil || !ing.Before(cutoff) {
			continue
		}
		ageDays := int(now.Sub(ing).Hours() / 24)
		out = append(out, finding{
			rule: "R5", targetKind: "process",
			target: fmt.Sprintf("%s#%d", taskID, rowid),
			title:  "Stale improvement: " + capRunes(text, 80),
			detail: fmt.Sprintf(
				"High-priority improvement %q from task %s is still open %d days after its retrospective (priority %q, status %q).",
				capRunes(text, 160), taskID, ageDays, priority, status),
			evidence: map[string]any{
				"window":      win,
				"counts":      map[string]int{"age_days": ageDays},
				"source_rows": []int64{rowid},
				"task":        taskID,
				"priority":    priority,
				"status":      status,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortFindings(out)
	return out, nil
}

// ── R6: cache hit-rate regression ─────────────────────────────────────────

// cacheHitRate computes SUM(cache_read) / (SUM(cache_read)+SUM(tokens_in))
// over turns in the window — the same math as the Analytics cache summary
// (internal/api analytics.go timeseriesCache). ok=false when there was no
// token traffic at all.
func cacheHitRate(db *sql.DB, win window) (rate float64, ok bool, err error) {
	var cr, tin int64
	err = db.QueryRow(`
		SELECT COALESCE(SUM(t.tokens_cache_read), 0), COALESCE(SUM(t.tokens_in), 0)
		  FROM turns t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE t.started_at >= ? AND t.started_at < ? AND p.archived = 0`,
		win.From, win.To).Scan(&cr, &tin)
	if err != nil || cr+tin == 0 {
		return 0, false, err
	}
	return float64(cr) / float64(cr+tin), true, nil
}

// r6CacheRegression flags a > R6DropPP percentage-point drop of the cache
// hit rate vs the preceding window of equal length. Both windows must carry
// token traffic; target is the fixed 'cache-hit-rate' config knob.
func r6CacheRegression(db *sql.DB, win window, now time.Time) ([]finding, error) {
	prevWin := window{
		From: fmtTS(now.AddDate(0, 0, -2*WindowDays)),
		To:   win.From,
	}
	cur, curOK, err := cacheHitRate(db, win)
	if err != nil {
		return nil, err
	}
	prev, prevOK, err := cacheHitRate(db, prevWin)
	if err != nil {
		return nil, err
	}
	if !curOK || !prevOK || prev-cur <= R6DropPP {
		return nil, nil
	}
	return []finding{{
		rule: "R6", targetKind: "config", target: "cache-hit-rate",
		title: "Investigate cache hit-rate regression",
		detail: fmt.Sprintf(
			"Cache hit rate dropped %.1f percentage points: %.1f%% over the last %d days vs %.1f%% in the preceding %d days. Check for prompt-prefix churn (system prompt, tool definitions, CLAUDE.md edits).",
			(prev-cur)*100, cur*100, WindowDays, prev*100, WindowDays),
		evidence: map[string]any{
			"window":      win,
			"prev_window": prevWin,
			"counts": map[string]float64{
				"hit_rate": cur, "prev_hit_rate": prev, "drop": prev - cur,
			},
		},
	}}, nil
}

// sortFindings orders findings by target for deterministic upsert order.
func sortFindings(fs []finding) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].target < fs[j].target })
}
