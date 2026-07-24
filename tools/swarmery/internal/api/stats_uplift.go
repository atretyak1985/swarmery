package api

// Analytics uplift (fusion phase 14): the Command-Center adoptions our store
// already backs — autonomy ratio, productivity (LOC/languages/durations/
// hours-saved-ESTIMATE), the SDLC funnel, and the per-playbook rollup. Each is
// a read over a local-day range (parseRange) scoped by the shared ?project=
// filter (scopeFilter); response shapes are camelCase DTOs mirrored in
// web/src/api/types.ts.
//
// Formulas are Fusion's, adopted verbatim (DESIGN §7) and documented at each
// handler. Every derived-precision figure that is an ESTIMATE (hours-saved)
// carries an explicit estimate flag + formula string so the UI never presents
// it as exact.

import (
	"database/sql"
	"net/http"
	"path"
	"sort"
	"strings"
)

// ── /api/stats/autonomy ────────────────────────────────────────────────────

type interventionsDTO struct {
	Approvals   int64 `json:"approvals"`   // human-resolved permission_requests
	UserPrompts int64 `json:"userPrompts"` // mid-session user_prompt events
	Total       int64 `json:"total"`
}

type autonomyDTO struct {
	From            string           `json:"from"`
	To              string           `json:"to"`
	ToolCalls       int64            `json:"toolCalls"`
	Interventions   interventionsDTO `json:"interventions"`
	Ratio           float64          `json:"ratio"` // toolCalls / max(1, interventions)
	FullyAutonomous bool             `json:"fullyAutonomous"`
}

// autonomyRatio is the Fusion formula (adapted): tool calls per human
// intervention. interventions = human-resolved permission requests + mid-run
// user prompts; a zero denominator means the range ran fully autonomously and
// the ratio degrades to the raw tool-call count (flagged, so the UI can badge
// it rather than print a misleading "N per 0"). Pure; unit-tested.
func autonomyRatio(toolCalls, interventions int64) (ratio float64, fullyAutonomous bool) {
	if interventions <= 0 {
		return float64(toolCalls), true
	}
	return float64(toolCalls) / float64(interventions), false
}

// GET /api/stats/autonomy?from&to&project
func (h *Handler) statsAutonomy(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	args := append([]any{dr.start, dr.end}, pargs...)

	out := autonomyDTO{From: dr.days[0], To: dr.days[len(dr.days)-1]}

	// Tool calls in range.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.type = 'tool_call' AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		args...).Scan(&out.ToolCalls); err != nil {
		writeErr(w, err)
		return
	}

	// Interventions #1: permission_requests a HUMAN resolved (dashboard /
	// terminal / mobile). 'rule' is an auto-approve — not a human in the loop,
	// so it is excluded from the denominator.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM permission_requests pr
		JOIN sessions s ON s.id = pr.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE pr.resolved_via IN ('dashboard','terminal','mobile')
		  AND pr.requested_at >= ? AND pr.requested_at < ? AND p.archived = 0`+pf,
		args...).Scan(&out.Interventions.Approvals); err != nil {
		writeErr(w, err)
		return
	}

	// Interventions #2: mid-session user_prompt events (a human steering).
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.type = 'user_prompt' AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		args...).Scan(&out.Interventions.UserPrompts); err != nil {
		writeErr(w, err)
		return
	}

	out.Interventions.Total = out.Interventions.Approvals + out.Interventions.UserPrompts
	out.Ratio, out.FullyAutonomous = autonomyRatio(out.ToolCalls, out.Interventions.Total)
	writeJSON(w, out, nil)
}

// ── /api/stats/productivity ────────────────────────────────────────────────

type languageStatDTO struct {
	Ext   string `json:"ext"`
	Files int64  `json:"files"`
	LOC   int64  `json:"loc"`
}

type taskDurationsDTO struct {
	Completed     int64    `json:"completed"`
	AvgSec        *float64 `json:"avgSec"`
	MedianSec     *float64 `json:"medianSec"`
	P90Sec        *float64 `json:"p90Sec"`
	TotalActiveMs int64    `json:"totalActiveMs"`
}

type hoursSavedDTO struct {
	Value    float64 `json:"value"`
	Formula  string  `json:"formula"`
	Estimate bool    `json:"estimate"`
}

type productivityDTO struct {
	From            string            `json:"from"`
	To              string            `json:"to"`
	Commits         int64             `json:"commits"`
	FilesModified   int64             `json:"filesModified"`
	LOC             int64             `json:"loc"`
	Languages       []languageStatDTO `json:"languages"`
	TaskDurations   taskDurationsDTO  `json:"taskDurations"`
	HumanHoursSaved hoursSavedDTO     `json:"humanHoursSaved"`
}

// langExtOf folds a file path to its analytics language bucket. Compound
// suffixes collapse to the outermost real extension ("foo.test.ts" → "ts",
// "styles.module.css" → "css"); a dotfile or an extensionless path → "other".
// Pure; unit-tested — the mapping is the source of the languages chart.
func langExtOf(filePath string) string {
	base := path.Base(filePath)
	// A leading dot is a dotfile, not an extension (".gitignore" → other).
	trimmed := strings.TrimLeft(base, ".")
	if !strings.Contains(trimmed, ".") {
		return "other"
	}
	ext := trimmed[strings.LastIndex(trimmed, ".")+1:]
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return "other"
	}
	return ext
}

// nearestRankPercentile returns the p-th percentile (0..1) of xs using the
// nearest-rank method (Fusion's choice). xs MUST be sorted ascending. Empty →
// nil. rank = ceil(p × n), clamped to [1, n]. Pure; unit-tested.
func nearestRankPercentile(sorted []float64, p float64) *float64 {
	n := len(sorted)
	if n == 0 {
		return nil
	}
	if p <= 0 {
		v := sorted[0]
		return &v
	}
	if p >= 1 {
		v := sorted[n-1]
		return &v
	}
	// ceil(p*n) via integer arithmetic (no math import).
	rank := int(p * float64(n))
	if float64(rank) < p*float64(n) {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	v := sorted[rank-1]
	return &v
}

// humanHoursSaved applies Fusion's LOC/15 constant. ALWAYS an estimate — the
// DTO says so and carries the formula string, so the UI labels it and never
// implies measured precision. Pure; unit-tested.
func humanHoursSaved(loc int64) hoursSavedDTO {
	return hoursSavedDTO{Value: float64(loc) / 15.0, Formula: "loc/15", Estimate: true}
}

// GET /api/stats/productivity?from&to&project
func (h *Handler) statsProductivity(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	args := append([]any{dr.start, dr.end}, pargs...)

	out := productivityDTO{From: dr.days[0], To: dr.days[len(dr.days)-1], Languages: []languageStatDTO{}}

	// Commits: commit events in range.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		JOIN sessions s ON s.id = e.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.type = 'commit' AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		args...).Scan(&out.Commits); err != nil {
		writeErr(w, err)
		return
	}

	// LOC + languages + distinct files: fold file_changes rows (joined to their
	// event for the ts filter) in Go — the language bucketing needs Go's path
	// logic anyway. out_of_scope rows still count as work done.
	rows, err := h.DB.Query(`
		SELECT fc.file_path, COALESCE(fc.additions,0), COALESCE(fc.deletions,0)
		FROM file_changes fc
		JOIN events e ON e.id = fc.event_id
		JOIN sessions s ON s.id = fc.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	type langAcc struct {
		files map[string]struct{}
		loc   int64
	}
	langs := map[string]*langAcc{}
	distinctFiles := map[string]struct{}{}
	var totalLOC int64
	for rows.Next() {
		var fp string
		var add, del int64
		if err := rows.Scan(&fp, &add, &del); err != nil {
			writeErr(w, err)
			return
		}
		loc := add + del
		totalLOC += loc
		distinctFiles[fp] = struct{}{}
		ext := langExtOf(fp)
		la := langs[ext]
		if la == nil {
			la = &langAcc{files: map[string]struct{}{}}
			langs[ext] = la
		}
		la.files[fp] = struct{}{}
		la.loc += loc
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	out.LOC = totalLOC
	out.FilesModified = int64(len(distinctFiles))
	for ext, la := range langs {
		out.Languages = append(out.Languages, languageStatDTO{Ext: ext, Files: int64(len(la.files)), LOC: la.loc})
	}
	// Rank by LOC desc, then ext asc; keep the top 12 (the chart's cap).
	sort.Slice(out.Languages, func(i, j int) bool {
		if out.Languages[i].LOC != out.Languages[j].LOC {
			return out.Languages[i].LOC > out.Languages[j].LOC
		}
		return out.Languages[i].Ext < out.Languages[j].Ext
	})
	if len(out.Languages) > 12 {
		out.Languages = out.Languages[:12]
	}

	// Task durations: completed board tasks with both timestamps; spans in Go
	// (nearest-rank percentiles need the sorted set anyway).
	spans, err := h.querySpans(`
		SELECT t.started_at, t.finished_at
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE t.source = 'queue' AND t.status = 'done'
		  AND t.started_at IS NOT NULL AND t.finished_at IS NOT NULL
		  AND t.finished_at >= ? AND t.finished_at < ? AND p.archived = 0`+pf, args)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.TaskDurations.Completed = int64(len(spans))
	if n := len(spans); n > 0 {
		sort.Float64s(spans)
		var sum float64
		for _, s := range spans {
			sum += s
		}
		avg := sum / float64(n)
		out.TaskDurations.AvgSec = &avg
		out.TaskDurations.MedianSec = nearestRankPercentile(spans, 0.5)
		out.TaskDurations.P90Sec = nearestRankPercentile(spans, 0.9)
		out.TaskDurations.TotalActiveMs = int64(sum * 1000)
	}

	out.HumanHoursSaved = humanHoursSaved(totalLOC)
	writeJSON(w, out, nil)
}

// ── /api/stats/funnel ──────────────────────────────────────────────────────
//
// SDLC funnel over the board. HONESTY NOTE: the board retains only
// column_moved_at (the LAST move), not per-transition history, so this is a
// current-state SNAPSHOT, not a full historical flow:
//   - `count`   = tasks CURRENTLY in the column (source='queue', in scope).
//   - `entered` for the terminal columns (done/archived) = tasks that reached
//     them in range (column_moved_at in range); for the intake columns it is
//     the current count (they have no "reached in range" proxy without
//     transition events). This is documented, not silently blurred — a
//     transition-history table would be empty for every existing row and thus
//     strictly worse than the snapshot (see phase-14 notes: no migration 0032).
// completionRate/perDay use the done column against tasks created in range.

type funnelColumnDTO struct {
	Column  string `json:"column"`
	Count   int64  `json:"count"`   // current occupancy
	Entered int64  `json:"entered"` // reached in range (terminal cols) / current (intake)
}

type funnelDTO struct {
	From           string            `json:"from"`
	To             string            `json:"to"`
	Columns        []funnelColumnDTO `json:"columns"`
	EnteredInRange int64             `json:"enteredInRange"` // tasks created in range
	DoneInRange    int64             `json:"doneInRange"`    // reached done/archived in range
	CompletionRate float64           `json:"completionRate"` // done / max(1, entered)
	PerDay         float64           `json:"perDay"`         // done / rangeDays
	Snapshot       bool              `json:"snapshot"`       // always true — honesty flag
}

// funnelOrder is the closed board column order (matches tasks_board.go).
var funnelOrder = []string{"triage", "todo", "in_progress", "in_review", "done", "archived"}

// completionRate = done/entered, guarding a zero denominator. Pure; unit-tested.
func completionRate(done, entered int64) float64 {
	if entered <= 0 {
		return 0
	}
	return float64(done) / float64(entered)
}

// GET /api/stats/funnel?from&to&project
func (h *Handler) statsFunnel(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)

	out := funnelDTO{From: dr.days[0], To: dr.days[len(dr.days)-1], Snapshot: true}

	// Current occupancy per column.
	occ := map[string]int64{}
	rows, err := h.DB.Query(`
		SELECT t.board_column, COUNT(*)
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE t.source = 'queue' AND p.archived = 0`+pf+`
		GROUP BY t.board_column`, pargs...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		var n int64
		if err := rows.Scan(&col, &n); err != nil {
			writeErr(w, err)
			return
		}
		occ[col] = n
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Reached terminal columns in range (column_moved_at in range).
	reached := map[string]int64{}
	rrows, err := h.DB.Query(`
		SELECT t.board_column, COUNT(*)
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE t.source = 'queue' AND t.board_column IN ('done','archived')
		  AND t.column_moved_at >= ? AND t.column_moved_at < ? AND p.archived = 0`+pf+`
		GROUP BY t.board_column`, append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rrows.Close()
	for rrows.Next() {
		var col string
		var n int64
		if err := rrows.Scan(&col, &n); err != nil {
			writeErr(w, err)
			return
		}
		reached[col] = n
	}
	if err := rrows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	terminal := map[string]bool{"done": true, "archived": true}
	for _, col := range funnelOrder {
		c := funnelColumnDTO{Column: col, Count: occ[col]}
		if terminal[col] {
			c.Entered = reached[col]
		} else {
			c.Entered = occ[col]
		}
		out.Columns = append(out.Columns, c)
	}

	// Tasks created in range (board intake).
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE t.source = 'queue'
		  AND t.created_at >= ? AND t.created_at < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...)...).Scan(&out.EnteredInRange); err != nil {
		writeErr(w, err)
		return
	}
	out.DoneInRange = reached["done"] + reached["archived"]
	out.CompletionRate = completionRate(out.DoneInRange, out.EnteredInRange)
	if days := len(dr.days); days > 0 {
		out.PerDay = float64(out.DoneInRange) / float64(days)
	}
	writeJSON(w, out, nil)
}

// ── /api/stats/playbooks ───────────────────────────────────────────────────
//
// Per-playbook rollup (Fusion's Workflows tab). Phase 13 (playbooks) may not be
// merged — the `playbook` column may be absent. This handler DEGRADES
// GRACEFULLY: it probes the column and returns an empty list (never an error)
// when it does not exist, so the tab renders "no playbook data yet" pre-13.

type playbookRollupDTO struct {
	Playbook   string   `json:"playbook"`
	TasksDone  int64    `json:"tasksDone"`
	InProgress int64    `json:"inProgress"`
	CostUSD    *float64 `json:"costUsd"`
	Tokens     int64    `json:"tokens"`
}

// hasTasksColumn reports whether the tasks table has a column of the given name
// (PRAGMA table_info). Used to degrade the playbook rollup gracefully before
// Phase 13 adds the `playbook` column.
func (h *Handler) hasTasksColumn(name string) (bool, error) {
	rows, err := h.DB.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if cname == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// GET /api/stats/playbooks?from&to&project
func (h *Handler) statsPlaybooks(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)

	out := []playbookRollupDTO{}

	has, err := h.hasTasksColumn("playbook")
	if err != nil {
		writeErr(w, err)
		return
	}
	if !has {
		// Pre-Phase-13: no playbook dimension exists yet. Empty list, not an
		// error — the UI shows an explanatory empty state.
		writeJSON(w, out, nil)
		return
	}

	// tokens/cost per playbook: join tasks → their linked session → turns.
	// Only board tasks (source='queue') with a non-empty playbook participate.
	// tasksDone counts terminal moves in range; inProgress is current occupancy.
	rows, err := h.DB.Query(`
		SELECT t.playbook AS pb,
		       SUM(CASE WHEN t.board_column IN ('done','archived')
		                 AND t.column_moved_at >= ? AND t.column_moved_at < ? THEN 1 ELSE 0 END) AS done,
		       SUM(CASE WHEN t.board_column = 'in_progress' THEN 1 ELSE 0 END) AS inprog,
		       COALESCE(SUM(tr.cost_usd), 0) AS cost,
		       COUNT(tr.cost_usd) AS priced,
		       COALESCE(SUM(COALESCE(tr.tokens_in,0) + COALESCE(tr.tokens_out,0)), 0) AS tokens
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		LEFT JOIN turns tr ON tr.session_id = t.session_id
		WHERE t.source = 'queue' AND t.playbook IS NOT NULL AND t.playbook <> ''
		  AND p.archived = 0`+pf+`
		GROUP BY pb
		ORDER BY tokens DESC, pb`, append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d playbookRollupDTO
		var cost float64
		var priced int64
		if err := rows.Scan(&d.Playbook, &d.TasksDone, &d.InProgress, &cost, &priced, &d.Tokens); err != nil {
			writeErr(w, err)
			return
		}
		if priced > 0 {
			c := cost
			d.CostUSD = &c
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}
