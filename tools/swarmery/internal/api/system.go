package api

// phase 4: system (step-05) — read-only registry surface over the sysscan
// tables (agents/skills/hooks/commands, *_versions, config_lint_findings)
// plus the on-demand overlays listing. GET only — every write flow is
// Stage 2. All served content (hook commands, frontmatter, bodies, version
// contents) passes the response-layer redaction filter (redact.go); the DB
// keeps ground truth.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// systemOverlaysDir is the overlays/ root served by GET /api/system/overlays;
// read live from disk on every request (pattern: AttachBus/AttachApprovals).
var systemOverlaysDir string

// AttachOverlaysDir wires the sysscan overlays dir into the overlays endpoint.
func AttachOverlaysDir(dir string) { systemOverlaysDir = dir }

// usageWindowDays is the tasks_30d metric window.
const usageWindowDays = 30

// docsRuleLike is the SQL literal matching every usage-guide rule
// (sysscan.RuleDocs* — docs_missing, docs_incomplete, docs_duplicate_block,
// docs_stale, docs_unreviewed). It is spliced into `rule NOT LIKE ` + this, so
// every severity ROLL-UP can exclude the docs axis from ONE place.
//
// The `_` is escaped: unescaped it is a LIKE wildcard, which would silently
// swallow a future rule named `docsomething`. The docs findings themselves are
// never filtered out of the finding LISTS (insights, hub) — only out of the
// aggregates that answer "how bad is this row/fleet", which the guide rules
// would otherwise saturate.
const docsRuleLike = `'docs\_%' ESCAPE '\'`

// systemKind parameterizes the agents/skills twin endpoints — same shape,
// different tables. Usage metrics are DIRECT event-column queries only (no
// heuristics): events.agent_id for agents, its mirror events.skill_id for
// skills; rows never referenced by events serve null/0.
type systemKind struct {
	kind     string // "agent" | "skill" — lint target prefix
	table    string // agents | skills
	verTable string // agent_versions | skill_versions
	fkCol    string // *_versions FK: agent_id | skill_id
	pathCol  string // file_path | dir_path
	hasModel bool   // agents carry a model column
	// Usage attribution: the ingester leaves events.agent_id / skill_id
	// unpopulated (see system_history.go), so usage is folded by the component
	// NAME pulled from the event payload and normalised via normAgentType.
	usageType     string // event type carrying a run: subagent_start | skill_use
	usageNameExpr string // SQL: extract the raw (possibly qualified) name
}

var (
	agentKind = systemKind{kind: "agent", table: "agents", verTable: "agent_versions",
		fkCol: "agent_id", pathCol: "file_path", hasModel: true,
		usageType: "subagent_start", usageNameExpr: `json_extract(payload, '$.subagent_type')`}
	skillKind = systemKind{kind: "skill", table: "skills", verTable: "skill_versions",
		fkCol: "skill_id", pathCol: "dir_path",
		usageType: "skill_use", usageNameExpr: `json_extract(payload, '$.input.skill')`}
)

// ---- DTOs (mirrored in web/src/api/types.ts, "phase 4: system") -----------

// systemLintCountsDTO is the active-finding severity headline. Usage-guide
// findings (docs_*) are excluded — they are counted in systemDocsCoverageDTO
// instead, and mixing the two would make the badges disagree with the ?lint=
// list they filter (see systemSummary).
type systemLintCountsDTO struct {
	Error int64 `json:"error"`
	Warn  int64 `json:"warn"`
	Info  int64 `json:"info"`
}

// systemInsightCountsDTO carries the promotion/drift badge counters
// (system_insights.go — insightCounts).
type systemInsightCountsDTO struct {
	Promotions     int64 `json:"promotions"`
	StaleOverrides int64 `json:"staleOverrides"`
}

// systemDocsCoverageDTO is the usage-guide coverage headline
// (docs/system-docs-format.md), derived entirely from the linter's active docs
// findings — there is no second parse of any file here.
type systemDocsCoverageDTO struct {
	Total      int64 `json:"total"`      // live agents + skills + commands
	Documented int64 `json:"documented"` // total minus distinct targets with docs_missing or docs_incomplete active
	Reviewed   int64 `json:"reviewed"`   // documented minus distinct targets with docs_unreviewed active
}

type systemSummaryDTO struct {
	Agents   int64                  `json:"agents"`
	Skills   int64                  `json:"skills"`
	Hooks    int64                  `json:"hooks"`
	Commands int64                  `json:"commands"`
	Overlays int64                  `json:"overlays"`
	Lint     systemLintCountsDTO    `json:"lint"`
	Insights systemInsightCountsDTO `json:"insights"`
	Docs     systemDocsCoverageDTO  `json:"docs"`
}

// systemItemDTO is one agents/skills list row.
type systemItemDTO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"` // global | project
	ProjectSlug *string `json:"projectSlug"`
	Origin      string  `json:"origin"` // local | plugin
	PluginName  *string `json:"pluginName"`
	Model       *string `json:"model"` // agents only; always null for skills
	Description *string `json:"description"`
	Path        string  `json:"path"` // agents.file_path / skills.dir_path
	// Worst ACTIVE lint finding severity (resolved_at IS NULL): error beats
	// warn beats info; null = clean. Usage-guide findings (docs_*) are NOT
	// folded in — they ride Documented instead (see systemItemSelect).
	LintMax *string `json:"lintMax"`
	// Active agent_dead finding (agents only; advisory).
	Dead bool `json:"dead"`
	// Documented is false while a docs_missing or docs_incomplete finding is
	// active (sysscan/lint.go). It rides the SAME aggregate lint JOIN as
	// LintMax/Dead — never a per-row subquery.
	Documented bool `json:"documented"`
	// Usage metrics folded by normalised component name (usageByName), since
	// events carry no populated agent_id/skill_id: lastUsed = newest run,
	// tasks30d = distinct sessions in the last 30 days. Never-run items serve
	// null/0. Name-grain means same-named rows across projects share totals.
	LastUsed *string `json:"lastUsed"`
	Tasks30d int64   `json:"tasks30d"`
}

type systemVersionDTO struct {
	ID          int64   `json:"id"`
	CreatedAt   string  `json:"createdAt"`
	ChangeNote  *string `json:"changeNote"`
	ContentHash string  `json:"contentHash"`
}

// systemDocsDTO is the item's parsed `# How to use` usage guide
// (docs/system-docs-format.md), served on the detail endpoints.
type systemDocsDTO struct {
	Present   bool     `json:"present"`
	Duplicate bool     `json:"duplicate"`
	Markdown  string   `json:"markdown"` // redacted
	Sections  []string `json:"sections"`
	Missing   []string `json:"missing"`
	Status    string   `json:"status"` // generated | reviewed | ""
	Stale     bool     `json:"stale"`
}

// emptyDocsDTO is the no-guide shape. Sections/Missing are non-nil so they
// serialize as [] and never null — the client treats the guide as a list-shaped
// object unconditionally.
func emptyDocsDTO() systemDocsDTO {
	return systemDocsDTO{Sections: []string{}, Missing: []string{}}
}

// docsDTO parses a component file into the wire guide, redacting it exactly as
// splitRedacted does for the body.
//
// EVERY author-controlled string in the guide goes through redact(), not just
// the markdown: a `## Bearer sk-ant-…` heading is author content that reaches
// the wire as a section TITLE, and `docs.status` is stored verbatim by §3
// precisely so an unknown value survives to the UI. `missing` is a fixed
// vocabulary (sysscan.RequiredDocSections) rather than author text, but it is
// filtered too so the rule is "the whole docs object is redacted" with no
// exception a future field can quietly fall through.
//
// The rune floor comes from sysscan.MinDocsSection() rather than the bare
// default so the `missing` list the UI renders can never disagree with the
// coverage findings the linter writes under the same SWARMERY_LINT_* override.
func docsDTO(content string) systemDocsDTO {
	d := sysscan.ParseDocs([]byte(content), sysscan.MinDocsSection())
	out := systemDocsDTO{
		Present:   d.Present,
		Duplicate: d.Duplicate,
		Markdown:  redact(d.Markdown),
		Sections:  redactEach(d.Sections),
		Missing:   redactEach(d.Missing),
		Status:    redact(d.Status),
		Stale:     d.Stale,
	}
	return out
}

// redactEach redacts every element of a string slice, always returning a
// non-nil slice so the field serializes as [] and never null — the client
// treats sections/missing as list-shaped unconditionally.
func redactEach(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, redact(s))
	}
	return out
}

// systemItemDetailDTO is GET /api/system/{agents|skills}/{id}.
type systemItemDetailDTO struct {
	systemItemDTO
	Deleted          bool               `json:"deleted"`
	CurrentVersionID *int64             `json:"currentVersionId"`
	Frontmatter      string             `json:"frontmatter"` // raw YAML block (redacted)
	Body             string             `json:"body"`        // markdown body (redacted)
	Docs             systemDocsDTO      `json:"docs"`        // parsed usage guide
	Versions         []systemVersionDTO `json:"versions"`    // newest first
}

// systemVersionContentDTO is GET .../{id}/versions/{v}: one full snapshot.
type systemVersionContentDTO struct {
	systemVersionDTO
	Content string `json:"content"` // redacted in the response, original in DB
}

// systemDiffDTO is GET .../{id}/diff?from=&to= — the backend-computed
// unified diff between two version snapshots (redacted before diffing).
type systemDiffDTO struct {
	From int64  `json:"from"`
	To   int64  `json:"to"`
	Diff string `json:"diff"` // "" when the contents are identical
}

type systemHookDTO struct {
	ID            int64   `json:"id"`
	Scope         string  `json:"scope"`
	ProjectSlug   *string `json:"projectSlug"`
	Event         string  `json:"event"`
	Matcher       *string `json:"matcher"`
	Command       string  `json:"command"` // redacted
	Timeout       *int64  `json:"timeout"` // seconds
	StatusMessage *string `json:"statusMessage"`
	SourceFile    string  `json:"sourceFile"`
	Seq           int64   `json:"seq"`
	Enabled       bool    `json:"enabled"`
	Managed       *string `json:"managed"` // 'swarmery' for installer-owned rows
	// ContentHash is the write-guard token: every step-10 toggle/edit call
	// must echo it as base_hash. It hashes the raw entry (pre-redaction).
	ContentHash string `json:"contentHash"`
}

type systemCommandDTO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"`
	ProjectSlug *string `json:"projectSlug"`
	Origin      string  `json:"origin"`
	PluginName  *string `json:"pluginName"`
	Description *string `json:"description"`
	Path        string  `json:"path"`
}

// systemOverlayDTO is one overlays/<dir>/ entry, read live from its
// project.json — safe identity fields only.
type systemOverlayDTO struct {
	Dir        string   `json:"dir"`  // overlay directory name
	Path       string   `json:"path"` // absolute overlay dir path
	ParseError bool     `json:"parseError"`
	Name       *string  `json:"name"`
	DispName   *string  `json:"displayName"`
	CodePath   *string  `json:"codePath"`
	MainApp    *string  `json:"mainApp"`
	Repos      []string `json:"repos"`
	Packs      []string `json:"enabledPacks"`
}

// systemOverlaysDTO wraps the list with the _schema presence check
// (project.schema.json is validated for PRESENCE only — a broken overlay
// never fails the list, it is marked parseError instead).
type systemOverlaysDTO struct {
	SchemaPresent bool               `json:"schemaPresent"`
	Overlays      []systemOverlayDTO `json:"overlays"`
}

// ---- summary ---------------------------------------------------------------

// GET /api/system/summary
func (h *Handler) systemSummary(w http.ResponseWriter, r *http.Request) {
	var s systemSummaryDTO
	// Docs coverage is COUNT(DISTINCT target) over the linter's guide rules —
	// no disk IO and no second parse: the summary is refetched on every WS
	// system_item_updated, so it must stay a handful of index-backed counts.
	var undocumented, unreviewed int64
	for _, c := range []struct {
		dst   *int64
		query string
	}{
		{&s.Agents, `SELECT COUNT(*) FROM agents WHERE deleted = 0`},
		{&s.Skills, `SELECT COUNT(*) FROM skills WHERE deleted = 0`},
		{&s.Hooks, `SELECT COUNT(*) FROM hooks`},
		{&s.Commands, `SELECT COUNT(*) FROM commands WHERE deleted = 0`},
		{&undocumented, `SELECT COUNT(DISTINCT target) FROM config_lint_findings
		                 WHERE resolved_at IS NULL AND rule IN ('docs_missing', 'docs_incomplete')`},
		// An undocumented item is never counted as unreviewed on top: the linter
		// stops at docs_missing for an absent guide, and excluding the
		// undocumented targets here keeps `reviewed` correct even if a stale row
		// from an older rule set is still open.
		{&unreviewed, `SELECT COUNT(DISTINCT target) FROM config_lint_findings
		               WHERE resolved_at IS NULL AND rule = 'docs_unreviewed'
		                 AND target NOT IN (SELECT target FROM config_lint_findings
		                                    WHERE resolved_at IS NULL
		                                      AND rule IN ('docs_missing', 'docs_incomplete'))`},
	} {
		if err := h.DB.QueryRow(c.query).Scan(c.dst); err != nil {
			writeErr(w, err)
			return
		}
	}
	// Clamped: findings are keyed by target, so a finding left open for an item
	// deleted between two lint passes could otherwise drive the headline
	// negative. Coverage is a display figure — it degrades to 0, never below.
	s.Docs.Total = s.Agents + s.Skills + s.Commands
	s.Docs.Documented = max(s.Docs.Total-undocumented, 0)
	s.Docs.Reviewed = max(s.Docs.Documented-unreviewed, 0)

	// The severity headline shares the row-level convention of systemItemSelect:
	// docs_* rules are excluded. The badges are click-to-filter over `lintMax`
	// (System.tsx), so a count that included the guide rules would advertise ~one
	// warn per component and then filter to a list where none of them show —
	// the badge and the list would disagree. Guide coverage has its own headline
	// in s.Docs right above. Alerts (AlertRules) are left out for the same
	// reason: they are stored as findings, but no component carries them.
	notAlert, alertArgs := notAlertRule()
	rows, err := h.DB.Query(`
		SELECT severity, COUNT(*) FROM config_lint_findings
		WHERE resolved_at IS NULL AND rule NOT LIKE `+docsRuleLike+notAlert+`
		GROUP BY severity`, alertArgs...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var n int64
		if err := rows.Scan(&sev, &n); err != nil {
			writeErr(w, err)
			return
		}
		switch sev {
		case "error":
			s.Lint.Error = n
		case "warn":
			s.Lint.Warn = n
		case "info":
			s.Lint.Info = n
		}
	}

	// Insight badge counters — cheap COUNT queries (system_insights.go), no
	// disk IO: the summary is refetched on every WS system_item_updated.
	promos, stales, err := insightCounts(h.DB, nil) // /api/system/summary is fleet-wide
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Insights = systemInsightCountsDTO{Promotions: promos, StaleOverrides: stales}

	overlays, _ := listOverlays(systemOverlaysDir)
	s.Overlays = int64(len(overlays))
	writeJSON(w, s, rows.Err())
}

// ---- agents / skills lists -------------------------------------------------

// GET /api/system/agents?scope=&project=
func (h *Handler) listSystemAgents(w http.ResponseWriter, r *http.Request) {
	h.listSystemItems(w, r, agentKind)
}

// GET /api/system/skills?scope=&project=
func (h *Handler) listSystemSkills(w http.ResponseWriter, r *http.Request) {
	h.listSystemItems(w, r, skillKind)
}

// systemItemSelect builds the shared agents/skills projection: entity columns
// plus the worst-active-lint / dead flags in ONE aggregate JOIN — never per-row
// subqueries (no N+1; pattern: sessionSelect). Usage metrics (lastUsed /
// tasks30d) are NOT joined here: events carry no populated agent_id/skill_id, so
// they are folded by normalised name in Go and overlaid post-query (usageByName).
//
// `sev` deliberately EXCLUDES the docs_* rules. They are real findings at their
// stated severities (docs_missing is a warn — sysscan/lint.go), but a docs rule
// saturates: before the backfill every item carried docs_missing, and after it
// every item carries docs_unreviewed until a human reads the guide. Either way,
// folding them into the row-level severity would paint one uniform LintDot
// across essentially every row, make ?lint= return the whole list, and drown the
// pre-existing lint signal. The docs signal has its own dedicated carrier on the same JOIN —
// `undoc` → the `documented` field — plus the insights `undocumented` list, so
// nothing is hidden by keeping the two axes separate.
func systemItemSelect(k systemKind) string {
	model := `NULL`
	if k.hasModel {
		model = `t.model`
	}
	return `
		SELECT t.id, t.name, t.scope, p.slug, t.origin, t.plugin_name, ` + model + `,
		       t.description, t.` + k.pathCol + `, lf.sev, COALESCE(lf.dead, 0),
		       COALESCE(lf.undoc, 0)
		FROM ` + k.table + ` t
		LEFT JOIN projects p ON p.id = t.project_id
		LEFT JOIN (
			SELECT target,
			       MAX(CASE WHEN rule NOT LIKE ` + docsRuleLike + `
			                THEN CASE severity WHEN 'error' THEN 3 WHEN 'warn' THEN 2 ELSE 1 END
			           END) AS sev,
			       MAX(CASE WHEN rule = 'agent_dead' THEN 1 ELSE 0 END) AS dead,
			       MAX(CASE WHEN rule IN ('docs_missing', 'docs_incomplete') THEN 1 ELSE 0 END) AS undoc
			FROM config_lint_findings WHERE resolved_at IS NULL GROUP BY target
		) lf ON lf.target = '` + k.kind + `:' || t.id`
}

// nameUsage is the folded per-normalised-name usage overlay.
type nameUsage struct {
	lastUsed string
	tasks30d int64
}

// usageByName folds run events into per-normalised-name usage, mirroring the
// name-grain attribution of the agent-history endpoint. events.<fk> is never
// populated by the ingester (see system_history.go), so usage is keyed off the
// component NAME in the event payload, normalised via normAgentType — folding
// every notation ("core:x", "x") of one component together. tasks30d counts
// distinct sessions within the window; lastUsed is the newest run overall.
func (h *Handler) usageByName(k systemKind, cutoff string) (map[string]nameUsage, error) {
	if k.usageType == "" {
		return map[string]nameUsage{}, nil
	}
	rows, err := h.DB.Query(
		`SELECT `+k.usageNameExpr+` AS n, ts, session_id
		   FROM events
		  WHERE type = ? AND `+k.usageNameExpr+` IS NOT NULL`, k.usageType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		lastUsed string
		sessions map[int64]struct{}
	}
	acc := map[string]*agg{}
	for rows.Next() {
		var name, ts sql.NullString
		var sessID sql.NullInt64
		if err := rows.Scan(&name, &ts, &sessID); err != nil {
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
		if ts.Valid && ts.String > a.lastUsed {
			a.lastUsed = ts.String
		}
		if sessID.Valid && ts.Valid && ts.String >= cutoff {
			a.sessions[sessID.Int64] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]nameUsage, len(acc))
	for key, a := range acc {
		out[key] = nameUsage{lastUsed: a.lastUsed, tasks30d: int64(len(a.sessions))}
	}
	return out, nil
}

// severityName maps the numeric lint rank back to its wire word.
func severityName(rank int64) string {
	switch rank {
	case 3:
		return "error"
	case 2:
		return "warn"
	default:
		return "info"
	}
}

func usageCutoff() string {
	return time.Now().UTC().AddDate(0, 0, -usageWindowDays).Format(time.RFC3339)
}

func (h *Handler) listSystemItems(w http.ResponseWriter, r *http.Request, k systemKind) {
	query := systemItemSelect(k) + ` WHERE t.deleted = 0`
	args := []any{}
	query, args, err := h.systemFilters(query, args, r, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	query += ` ORDER BY t.name, t.scope, t.id`

	rows, qerr := h.DB.Query(query, args...)
	if qerr != nil {
		writeErr(w, qerr)
		return
	}
	defer rows.Close()

	items := []systemItemDTO{}
	for rows.Next() {
		var it systemItemDTO
		var sev sql.NullInt64
		var dead, undoc int64
		if err := rows.Scan(&it.ID, &it.Name, &it.Scope, &it.ProjectSlug, &it.Origin,
			&it.PluginName, &it.Model, &it.Description, &it.Path,
			&sev, &dead, &undoc); err != nil {
			writeErr(w, err)
			return
		}
		if sev.Valid {
			name := severityName(sev.Int64)
			it.LintMax = &name
		}
		it.Dead = dead != 0
		it.Documented = undoc == 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	usage, err := h.usageByName(k, usageCutoff())
	if err != nil {
		writeErr(w, err)
		return
	}
	for i := range items {
		if u, ok := usage[normAgentType(items[i].Name)]; ok {
			if u.lastUsed != "" {
				lu := u.lastUsed
				items[i].LastUsed = &lu
			}
			items[i].Tasks30d = u.tasks30d
		}
	}
	writeJSON(w, items, nil)
}

// systemFilters appends the shared ?scope=&project= WHERE clauses.
//
// ?project= selects the components EFFECTIVE for that project — its own rows
// PLUS the global-local ones PLUS the built-ins of the packs it enables (see
// effectiveScope). It is deliberately NOT a plain project_id match: the registry
// is machine-wide, so a project_id match would hide everything a project
// inherits while an unfiltered list leaks other projects' local components.
// ?scope= still narrows independently, so ?scope=project&project=X keeps the
// old "only X's own rows" view.
//
// hasOrigin tells whether the item table carries origin/plugin_name (agents,
// skills, commands do; hooks do not).
func (h *Handler) systemFilters(query string, args []any, r *http.Request, hasOrigin bool) (string, []any, error) {
	if scope := r.URL.Query().Get("scope"); scope != "" {
		query += ` AND t.scope = ?`
		args = append(args, scope)
	}
	esc, err := h.resolveEffectiveScope(r.URL.Query().Get("project"))
	if err != nil {
		return "", nil, err
	}
	pred, pargs := esc.predicate("t.", hasOrigin)
	return query + pred, append(args, pargs...), nil
}

// ---- agents / skills detail ------------------------------------------------

// GET /api/system/agents/{id}
func (h *Handler) getSystemAgent(w http.ResponseWriter, r *http.Request) {
	h.getSystemItem(w, r, agentKind)
}

// GET /api/system/skills/{id}
func (h *Handler) getSystemSkill(w http.ResponseWriter, r *http.Request) {
	h.getSystemItem(w, r, skillKind)
}

func (h *Handler) getSystemItem(w http.ResponseWriter, r *http.Request, k systemKind) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}

	// The detail projection = list projection + deleted + current_version_id;
	// one query so the lint/usage flags can never diverge from the list.
	var d systemItemDetailDTO
	var deleted int64
	sel := systemItemSelectDetail(k)

	var sev sql.NullInt64
	var dead, undoc int64
	err := h.DB.QueryRow(sel, id).Scan(
		&d.ID, &d.Name, &d.Scope, &d.ProjectSlug, &d.Origin, &d.PluginName,
		&d.Model, &d.Description, &d.Path, &sev, &dead, &undoc,
		&deleted, &d.CurrentVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"`+k.kind+` not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if sev.Valid {
		name := severityName(sev.Int64)
		d.LintMax = &name
	}
	d.Dead = dead != 0
	d.Documented = undoc == 0
	d.Deleted = deleted != 0

	usage, err := h.usageByName(k, usageCutoff())
	if err != nil {
		writeErr(w, err)
		return
	}
	if u, ok := usage[normAgentType(d.Name)]; ok {
		if u.lastUsed != "" {
			lu := u.lastUsed
			d.LastUsed = &lu
		}
		d.Tasks30d = u.tasks30d
	}

	// Current content → redacted frontmatter/body split + parsed usage guide.
	// An item with no current version still serves the empty guide shape.
	d.Docs = emptyDocsDTO()
	if d.CurrentVersionID != nil {
		var content string
		err := h.DB.QueryRow(`SELECT content FROM `+k.verTable+` WHERE id = ?`,
			*d.CurrentVersionID).Scan(&content)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeErr(w, err)
			return
		}
		d.Frontmatter, d.Body = splitRedacted(content)
		d.Docs = docsDTO(content)
	}

	// Version history, newest first.
	d.Versions = []systemVersionDTO{}
	rows, err := h.DB.Query(`
		SELECT id, created_at, change_note, content_hash
		FROM `+k.verTable+` WHERE `+k.fkCol+` = ?
		ORDER BY created_at DESC, id DESC`, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v systemVersionDTO
		if err := rows.Scan(&v.ID, &v.CreatedAt, &v.ChangeNote, &v.ContentHash); err != nil {
			writeErr(w, err)
			return
		}
		d.Versions = append(d.Versions, v)
	}
	writeJSON(w, d, rows.Err())
}

// systemItemSelectDetail extends the list projection with the detail-only
// columns (deleted, current_version_id).
func systemItemSelectDetail(k systemKind) string {
	sel := systemItemSelect(k)
	// Splice the extra columns before ` FROM ` — the projection is built in
	// one place (systemItemSelect) so list and detail can never diverge.
	const marker = "\n\t\tFROM "
	i := strings.Index(sel, marker)
	return sel[:i] + `, t.deleted, t.current_version_id` + sel[i:] + ` WHERE t.id = ?`
}

// splitRedacted splits a component file into (frontmatter, body) and redacts
// both halves. Files without valid frontmatter serve the whole (redacted)
// content as body.
func splitRedacted(content string) (frontmatter, body string) {
	fm, b, err := sysscan.SplitFrontmatter([]byte(content))
	if err != nil {
		return "", redact(content)
	}
	return redact(string(fm)), redact(string(b))
}

// systemItemID parses the numeric {id} path segment (row id only — names
// collide across scopes).
func systemItemID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id, want the numeric row id"}`, http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// ---- versions & diff --------------------------------------------------------

// GET /api/system/agents/{id}/versions/{v}
func (h *Handler) getSystemAgentVersion(w http.ResponseWriter, r *http.Request) {
	h.getSystemVersion(w, r, agentKind)
}

// GET /api/system/skills/{id}/versions/{v}
func (h *Handler) getSystemSkillVersion(w http.ResponseWriter, r *http.Request) {
	h.getSystemVersion(w, r, skillKind)
}

func (h *Handler) getSystemVersion(w http.ResponseWriter, r *http.Request, k systemKind) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	vid, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid version id"}`, http.StatusBadRequest)
		return
	}

	var v systemVersionContentDTO
	err = h.DB.QueryRow(`
		SELECT id, created_at, change_note, content_hash, content
		FROM `+k.verTable+` WHERE id = ? AND `+k.fkCol+` = ?`, vid, id).
		Scan(&v.ID, &v.CreatedAt, &v.ChangeNote, &v.ContentHash, &v.Content)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"version not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	v.Content = redact(v.Content)
	writeJSON(w, v, nil)
}

// GET /api/system/agents/{id}/diff?from=&to=
func (h *Handler) diffSystemAgent(w http.ResponseWriter, r *http.Request) {
	h.diffSystemItem(w, r, agentKind)
}

// GET /api/system/skills/{id}/diff?from=&to=
func (h *Handler) diffSystemSkill(w http.ResponseWriter, r *http.Request) {
	h.diffSystemItem(w, r, skillKind)
}

func (h *Handler) diffSystemItem(w http.ResponseWriter, r *http.Request, k systemKind) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, errF := strconv.ParseInt(q.Get("from"), 10, 64)
	to, errT := strconv.ParseInt(q.Get("to"), 10, 64)
	if errF != nil || errT != nil {
		http.Error(w, `{"error":"want numeric from= and to= version ids"}`, http.StatusBadRequest)
		return
	}

	load := func(vid int64) (string, error) {
		var content string
		err := h.DB.QueryRow(`SELECT content FROM `+k.verTable+` WHERE id = ? AND `+k.fkCol+` = ?`,
			vid, id).Scan(&content)
		return content, err
	}
	aText, err := load(from)
	if err == nil {
		var bText string
		bText, err = load(to)
		if err == nil {
			// Redact BEFORE diffing so a secret can never leak through hunks.
			d := systemDiffDTO{From: from, To: to,
				Diff: UnifiedDiff(
					k.kind+"/"+strconv.FormatInt(id, 10)+"/versions/"+strconv.FormatInt(from, 10),
					k.kind+"/"+strconv.FormatInt(id, 10)+"/versions/"+strconv.FormatInt(to, 10),
					redact(aText), redact(bText))}
			writeJSON(w, d, nil)
			return
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"version not found"}`, http.StatusNotFound)
		return
	}
	writeErr(w, err)
}

// ---- hooks / commands lists --------------------------------------------------

// GET /api/system/hooks?scope=&project=
func (h *Handler) listSystemHooks(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT t.id, t.scope, p.slug, t.event, t.matcher, t.command, t.timeout,
		       t.status_message, t.source_file, t.seq, t.enabled, t.managed,
		       t.content_hash
		FROM hooks t
		LEFT JOIN projects p ON p.id = t.project_id
		WHERE 1=1`
	args := []any{}
	// Hooks carry no origin/plugin_name: they are parsed out of settings.json
	// files, never shipped by a pack, so "effective" is global ∪ the project's own.
	query, args, err := h.systemFilters(query, args, r, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	query += ` ORDER BY t.source_file, t.event, t.seq`

	rows, qerr := h.DB.Query(query, args...)
	if qerr != nil {
		writeErr(w, qerr)
		return
	}
	defer rows.Close()

	hooks := []systemHookDTO{}
	for rows.Next() {
		var hk systemHookDTO
		var enabled int64
		if err := rows.Scan(&hk.ID, &hk.Scope, &hk.ProjectSlug, &hk.Event, &hk.Matcher,
			&hk.Command, &hk.Timeout, &hk.StatusMessage, &hk.SourceFile, &hk.Seq,
			&enabled, &hk.Managed, &hk.ContentHash); err != nil {
			writeErr(w, err)
			return
		}
		hk.Enabled = enabled != 0
		hk.Command = redact(hk.Command)
		hooks = append(hooks, hk)
	}
	writeJSON(w, hooks, rows.Err())
}

// GET /api/system/commands?scope=&project=
func (h *Handler) listSystemCommands(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT t.id, t.name, t.scope, p.slug, t.origin, t.plugin_name, t.description, t.file_path
		FROM commands t
		LEFT JOIN projects p ON p.id = t.project_id
		WHERE t.deleted = 0`
	args := []any{}
	query, args, err := h.systemFilters(query, args, r, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	query += ` ORDER BY t.name, t.scope, t.id`

	rows, qerr := h.DB.Query(query, args...)
	if qerr != nil {
		writeErr(w, qerr)
		return
	}
	defer rows.Close()

	cmds := []systemCommandDTO{}
	for rows.Next() {
		var c systemCommandDTO
		if err := rows.Scan(&c.ID, &c.Name, &c.Scope, &c.ProjectSlug, &c.Origin,
			&c.PluginName, &c.Description, &c.Path); err != nil {
			writeErr(w, err)
			return
		}
		cmds = append(cmds, c)
	}
	writeJSON(w, cmds, rows.Err())
}

// ---- overlays ----------------------------------------------------------------

// GET /api/system/overlays — reads overlays/*/project.json live from disk
// (the scanner only reports paths, step-03). A broken overlay never fails
// the list: it is marked parseError and its parsed fields stay null.
func (h *Handler) listSystemOverlays(w http.ResponseWriter, r *http.Request) {
	overlays, schemaPresent := listOverlays(systemOverlaysDir)
	writeJSON(w, systemOverlaysDTO{SchemaPresent: schemaPresent, Overlays: overlays}, nil)
}

// overlayManifest is the safe-field subset of overlays/*/project.json
// (overlays/_schema/project.schema.json).
type overlayManifest struct {
	Name         *string  `json:"name"`
	DisplayName  *string  `json:"displayName"`
	CodePath     *string  `json:"codePath"`
	MainApp      *string  `json:"mainApp"`
	Repos        []string `json:"repos"`
	EnabledPacks []string `json:"enabledPacks"`
}

// listOverlays lists every overlays/<dir>/ holding a project.json. Shared by
// the overlays endpoint and the summary counter.
func listOverlays(dir string) ([]systemOverlayDTO, bool) {
	out := []systemOverlayDTO{}
	if dir == "" {
		return out, false
	}
	schemaPresent := false
	if _, err := os.Stat(filepath.Join(dir, "_schema", "project.schema.json")); err == nil {
		schemaPresent = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, schemaPresent
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "_schema" || e.Name()[0] == '.' {
			continue
		}
		manifest := filepath.Join(dir, e.Name(), "project.json")
		raw, err := os.ReadFile(manifest)
		if err != nil {
			continue // no project.json → not an overlay
		}
		item := systemOverlayDTO{Dir: e.Name(), Path: filepath.Join(dir, e.Name()),
			Repos: []string{}, Packs: []string{}}
		var m overlayManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			item.ParseError = true
		} else {
			item.Name, item.DispName, item.CodePath, item.MainApp = m.Name, m.DisplayName, m.CodePath, m.MainApp
			if m.Repos != nil {
				item.Repos = m.Repos
			}
			if m.EnabledPacks != nil {
				item.Packs = m.EnabledPacks
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, schemaPresent
}
