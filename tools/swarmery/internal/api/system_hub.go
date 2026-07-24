package api

// System Hub (fusion phase 18): the catalog-wide extension of the Agent Hub
// pattern (phase 17), grouped by ROLE — Toolkit (Skills/Commands/Templates),
// Hooks, and an Insights inbox. Like agent_hub.go this is aggregation over data
// that ALREADY exists (the sysscan registry + the events telemetry + the
// config_lint_findings), with ONE new write: template copy-to-project.
//
// Endpoints:
//   - GET  /api/system/hub/summary?projectId=          — nav count badges
//   - GET  /api/system/skills/{id}/hub?projectId=      — skill profile: definition
//       meta + 30d usage rollup (skill_use events, the statsSkills grain, so the
//       numbers agree with Analytics) + recent invoking sessions.
//   - GET  /api/system/hooks/{id}/hub                  — hook profile: the settings
//       entry + its config_lint_findings. Hook FIRINGS are NOT tracked; the
//       profile says so honestly (no fabricated zeros) — firing telemetry is a
//       documented follow-up.
//   - GET  /api/system/commands/{id}/hub?projectId=    — command profile: frontmatter
//       + content + a best-effort invocation rollup (slash-command text match),
//       flagged approximate:true.
//   - GET  /api/system/templates?projectId=            — the effective template list
//       with a resolution badge (core|pack:<name>|project override).
//   - GET  /api/system/templates/{name}?projectId=     — one template's content (RO).
//   - POST /api/system/templates/{name}/copy?projectId= — copy a built-in into the
//       project's .claude/templates/ (O_EXCL → 409; requireLocalOrigin; fenced).
//
// No new tables. Templates live on disk (sysscan.ScanTemplates), so their id is
// the file stem (unique within a resolution scope) rather than a DB row id.

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// systemHubClaudeDir anchors the plugin-cache root the template scan reads
// (~/.claude by default). Attached at startup with the same resolved
// --claude-dir the scanner uses; the default keeps the endpoints working when
// nothing attaches it. Mirrors memoryClaudeDir / AttachOverlaysDir.
var systemHubClaudeDir = defaultMemoryClaudeDir()

// AttachSystemHubDir points the template scan at a claude dir (plugin cache
// root). Empty keeps the current value.
func AttachSystemHubDir(claudeDir string) {
	if claudeDir != "" {
		systemHubClaudeDir = claudeDir
	}
}

// hubUsageWindowDays is the skill/command usage rollup window (matches the
// System tasks30d + the Analytics default grain).
const hubUsageWindowDays = 30

// hubSkillSessionsCap bounds the skill profile's recent-sessions list.
const hubSkillSessionsCap = 20

// ── DTOs (mirrored in web/src/api/types.ts, "fusion phase 18: system hub") ──

// systemHubSummaryDTO carries the nav count badges. Templates counts the
// effective set for the scope; insights is the open-inbox count (promotion +
// stale-override, the same counters the System page badges); lintFindings is
// the active (unresolved) config-lint count.
type systemHubSummaryDTO struct {
	Agents       int64 `json:"agents"`
	Skills       int64 `json:"skills"`
	Hooks        int64 `json:"hooks"`
	Commands     int64 `json:"commands"`
	Templates    int64 `json:"templates"`
	Insights     int64 `json:"insights"`
	LintFindings int64 `json:"lintFindings"`
}

// skillUsageDTO is the skill profile's 30-day rollup (skill_use grain).
type skillUsageDTO struct {
	WindowDays  int     `json:"windowDays"`
	Invocations int64   `json:"invocations"`
	Sessions    int64   `json:"sessions"`
	Projects    int64   `json:"projects"`
	Errors      int64   `json:"errors"`
	LastUsed    *string `json:"lastUsed"`
	// Approximate is true when the window overlaps pruned (rolled-up) days —
	// rollups carry no per-skill events, so the counts undercount there (the
	// same honesty flag statsSkills sets).
	Approximate bool                `json:"approximate"`
	ByDay       []systemHubDayCount `json:"byDay"`
}

type systemHubDayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// skillSessionRow is one recent invoking session in the skill profile.
type skillSessionRow struct {
	Ts           string `json:"ts"`
	SessionUUID  string `json:"sessionUuid"`
	SessionTitle string `json:"sessionTitle"`
	ProjectSlug  string `json:"projectSlug"`
	Status       string `json:"status"`
}

// skillHubDTO is GET /api/system/skills/{id}/hub — definition meta + usage.
type skillHubDTO struct {
	systemItemDTO
	Usage    skillUsageDTO     `json:"usage"`
	Sessions []skillSessionRow `json:"sessions"`
}

// hookHubDTO is GET /api/system/hooks/{id}/hub — the settings entry + lint.
type hookHubDTO struct {
	systemHookDTO
	Lint []systemLintFindingDTO `json:"lint"`
	// FiringTelemetry is always false in v1 — hook firings are not tracked. The
	// UI renders an honest "not tracked yet" note instead of fake zeros.
	FiringTelemetry bool `json:"firingTelemetry"`
}

// systemLintFindingDTO is one config_lint_findings row for a hub profile.
type systemLintFindingDTO struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// commandHubDTO is GET /api/system/commands/{id}/hub — frontmatter + content +
// an APPROXIMATE usage rollup (slash-command text match is best-effort).
type commandHubDTO struct {
	systemCommandDTO
	Frontmatter string `json:"frontmatter"` // redacted
	Content     string `json:"content"`     // redacted body
	Usage       struct {
		WindowDays  int   `json:"windowDays"`
		Invocations int64 `json:"invocations"`
		// Approximate is ALWAYS true: slash-command invocations are inferred from
		// prompt text, never an authoritative event (the phase honesty rule).
		Approximate bool `json:"approximate"`
	} `json:"usage"`
}

// systemTemplateDTO is one row of GET /api/system/templates — an effective
// template with its resolution badge.
type systemTemplateDTO struct {
	// Name is the identity (file stem) — the {name} path handle.
	Name string `json:"name"`
	// FileName is the on-disk basename.
	FileName string `json:"fileName"`
	// Path is the absolute on-disk path (display only).
	Path string `json:"path"`
	// Resolution is the badge: "core" / "pack:<name>" / "project override".
	Resolution string `json:"resolution"`
	// Source is plugin | project (drives the copy affordance — only plugin
	// built-ins can be copied into the project).
	Source string `json:"source"`
	// PluginName is the pack for a built-in ("" for a project override).
	PluginName string `json:"pluginName"`
	// Overridden is true for a built-in that a project-local copy shadows (fleet
	// list only ever sees built-ins; project list folds them).
	Overridden bool `json:"overridden"`
}

// systemTemplateContentDTO is GET /api/system/templates/{name} — content (RO).
type systemTemplateContentDTO struct {
	systemTemplateDTO
	Content string `json:"content"` // redacted
}

// ── GET /api/system/hub/summary ──

// systemHubSummary — GET /api/system/hub/summary?projectId= : the nav count
// badges. Agents/skills/hooks/commands are the deleted=0 registry counts (the
// System summary grain); templates is the effective count for the scope;
// insights is the open promotion/stale-override inbox count; lintFindings is
// the active config-lint count.
func (h *Handler) systemHubSummary(w http.ResponseWriter, r *http.Request) {
	var s systemHubSummaryDTO
	for _, c := range []struct {
		dst   *int64
		query string
	}{
		{&s.Agents, `SELECT COUNT(*) FROM agents WHERE deleted = 0`},
		{&s.Skills, `SELECT COUNT(*) FROM skills WHERE deleted = 0`},
		{&s.Hooks, `SELECT COUNT(*) FROM hooks`},
		{&s.Commands, `SELECT COUNT(*) FROM commands WHERE deleted = 0`},
		{&s.LintFindings, `SELECT COUNT(*) FROM config_lint_findings WHERE resolved_at IS NULL`},
	} {
		if err := h.DB.QueryRow(c.query).Scan(c.dst); err != nil {
			writeErr(w, err)
			return
		}
	}

	// Insights inbox count — the same promotion + stale-override counters the
	// System nav badges (system_insights.go).
	promos, stales, err := insightCounts(h.DB)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Insights = promos + stales

	// Templates: the effective count for the scope.
	tmpls, err := h.effectiveTemplates(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Templates = int64(len(tmpls))

	writeJSON(w, s, nil)
}

// ── GET /api/system/skills/{id}/hub ──

func (h *Handler) skillHub(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	// Reuse the exact list projection so the definition meta (lint, path, scope,
	// usage) matches the Skills roster row by construction.
	item, ok, err := h.systemItemByID(skillKind, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeClientErr(w, http.StatusNotFound, "skill not found")
		return
	}

	out := skillHubDTO{systemItemDTO: item, Sessions: []skillSessionRow{}}
	out.Usage, out.Sessions, err = h.skillUsageRollup(item.Name, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}

// skillUsageRollup folds skill_use events for one skill into a 30-day rollup +
// recent sessions, over the ?project= scope. Grain is IDENTICAL to statsSkills
// (analytics.go): the raw payload skill name (json_extract(payload,
// '$.input.skill')) matched against the registry name — NO normalisation — so
// the numbers agree with the Analytics skills panel for the same window by
// construction. (Skills are invoked by their bare name; unlike subagent_type
// they carry no plugin: prefix, so a raw match is exact.)
func (h *Handler) skillUsageRollup(skillName string, r *http.Request) (skillUsageDTO, []skillSessionRow, error) {
	dr := hubRange() // last 30 local days (shared with the agent hub)
	pf, pargs := scopeFilter(r)

	usage := skillUsageDTO{WindowDays: hubUsageWindowDays, ByDay: []systemHubDayCount{}}
	sessions := []skillSessionRow{}

	rows, err := h.DB.Query(`
		SELECT e.ts, COALESCE(e.status, ''), json_extract(e.payload, '$.input.skill'),
		       s.session_uuid, s.title, COALESCE(p.slug, '')
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		  LEFT JOIN events pe ON pe.id = e.parent_event_id
		 WHERE e.type = 'skill_use'
		   AND json_extract(e.payload, '$.input.skill') IS NOT NULL
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf+`
		 ORDER BY e.ts DESC`,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return usage, sessions, err
	}
	defer rows.Close()

	byDay := map[string]int64{}
	sessSet := map[string]struct{}{}
	projSet := map[string]struct{}{}
	var lastUsed string
	for rows.Next() {
		var ts, status, skill, sessUUID, title, slug sql.NullString
		if err := rows.Scan(&ts, &status, &skill, &sessUUID, &title, &slug); err != nil {
			return usage, sessions, err
		}
		if skill.String != skillName {
			continue // exact-name match — the statsSkills grain (no normalisation)
		}
		usage.Invocations++
		if status.String == "error" {
			usage.Errors++
		}
		if sessUUID.String != "" {
			sessSet[sessUUID.String] = struct{}{}
		}
		if slug.String != "" {
			projSet[slug.String] = struct{}{}
		}
		if ts.String > lastUsed {
			lastUsed = ts.String
		}
		if day, ok := localDay(ts.String); ok {
			byDay[day]++
		}
		if len(sessions) < hubSkillSessionsCap {
			sessions = append(sessions, skillSessionRow{
				Ts: ts.String, SessionUUID: sessUUID.String, SessionTitle: title.String,
				ProjectSlug: slug.String, Status: status.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return usage, sessions, err
	}

	usage.Sessions = int64(len(sessSet))
	usage.Projects = int64(len(projSet))
	if lastUsed != "" {
		usage.LastUsed = &lastUsed
	}
	for _, day := range dr.days {
		usage.ByDay = append(usage.ByDay, systemHubDayCount{Day: day, Count: byDay[day]})
	}

	rolled, err := h.hasRolledUpDays(dr.days[0], dr.days[len(dr.days)-1], pf, pargs)
	if err != nil {
		return usage, sessions, err
	}
	usage.Approximate = rolled
	return usage, sessions, nil
}

// ── GET /api/system/hooks/{id}/hub ──

func (h *Handler) hookHub(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	var hk systemHookDTO
	var enabled int64
	err := h.DB.QueryRow(`
		SELECT t.id, t.scope, p.slug, t.event, t.matcher, t.command, t.timeout,
		       t.status_message, t.source_file, t.seq, t.enabled, t.managed, t.content_hash
		  FROM hooks t
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ?`, id).Scan(
		&hk.ID, &hk.Scope, &hk.ProjectSlug, &hk.Event, &hk.Matcher, &hk.Command, &hk.Timeout,
		&hk.StatusMessage, &hk.SourceFile, &hk.Seq, &enabled, &hk.Managed, &hk.ContentHash)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "hook not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	hk.Enabled = enabled != 0
	hk.Command = redact(hk.Command)

	out := hookHubDTO{systemHookDTO: hk, Lint: []systemLintFindingDTO{}, FiringTelemetry: false}
	// Lint findings for this hook (target = 'hook:<id>', the lint.go grain).
	lrows, err := h.DB.Query(`
		SELECT rule, severity, message
		  FROM config_lint_findings
		 WHERE target = ? AND resolved_at IS NULL
		 ORDER BY CASE severity WHEN 'error' THEN 3 WHEN 'warn' THEN 2 ELSE 1 END DESC, rule`,
		"hook:"+strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer lrows.Close()
	for lrows.Next() {
		var f systemLintFindingDTO
		if err := lrows.Scan(&f.Rule, &f.Severity, &f.Message); err != nil {
			writeErr(w, err)
			return
		}
		out.Lint = append(out.Lint, f)
	}
	writeJSON(w, out, lrows.Err())
}

// ── GET /api/system/commands/{id}/hub ──

func (h *Handler) commandHub(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	var c systemCommandDTO
	err := h.DB.QueryRow(`
		SELECT t.id, t.name, t.scope, p.slug, t.origin, t.plugin_name, t.description, t.file_path
		  FROM commands t
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ? AND t.deleted = 0`, id).Scan(
		&c.ID, &c.Name, &c.Scope, &c.ProjectSlug, &c.Origin, &c.PluginName, &c.Description, &c.Path)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "command not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	out := commandHubDTO{systemCommandDTO: c}
	// Content is read live off disk (commands are not versioned); a vanished
	// file degrades to empty content rather than failing the profile.
	if raw, rerr := os.ReadFile(c.Path); rerr == nil {
		out.Frontmatter, out.Content = splitRedacted(string(raw))
	}
	out.Usage.WindowDays = hubUsageWindowDays
	out.Usage.Approximate = true // slash-command usage is always best-effort
	inv, err := h.commandApproxInvocations(c.Name, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Usage.Invocations = inv
	writeJSON(w, out, nil)
}

// commandApproxInvocations counts user prompts that begin with the slash-command
// (`/<name>`) over the window — a best-effort signal (marked approximate), NOT an
// authoritative event. Folded over the ?project= scope.
func (h *Handler) commandApproxInvocations(name string, r *http.Request) (int64, error) {
	dr := hubRange()
	pf, pargs := scopeFilter(r)
	// user_prompt events whose payload text starts with "/<name>" (optionally
	// followed by whitespace/args). A missing text is not a match.
	like := "/" + name
	rows, err := h.DB.Query(`
		SELECT json_extract(e.payload, '$.prompt')
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.type = 'user_prompt'
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`+pf,
		append([]any{dr.start, dr.end}, pargs...)...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var prompt sql.NullString
		if err := rows.Scan(&prompt); err != nil {
			return 0, err
		}
		t := strings.TrimSpace(prompt.String)
		if t == like || strings.HasPrefix(t, like+" ") || strings.HasPrefix(t, like+"\n") {
			n++
		}
	}
	return n, rows.Err()
}

// ── templates ──

// GET /api/system/templates?projectId= — the effective template list.
func (h *Handler) listSystemTemplates(w http.ResponseWriter, r *http.Request) {
	tmpls, err := h.effectiveTemplates(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, tmpls, nil)
}

// GET /api/system/templates/{name}?projectId= — one template's content (RO).
func (h *Handler) getSystemTemplate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if !safeTemplateName(name) {
		writeClientErr(w, http.StatusBadRequest, "invalid template name")
		return
	}
	tmpls, err := h.effectiveTemplates(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	i := slices.IndexFunc(tmpls, func(t systemTemplateDTO) bool { return t.Name == name })
	if i < 0 {
		writeClientErr(w, http.StatusNotFound, "template not found")
		return
	}
	tmpl := tmpls[i]
	raw, err := os.ReadFile(tmpl.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeClientErr(w, http.StatusNotFound, "template file not found")
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, systemTemplateContentDTO{systemTemplateDTO: tmpl, Content: redact(string(raw))}, nil)
}

// effectiveTemplates resolves the discovered templates into the effective view
// for the ?projectId= scope, each carrying a resolution badge:
//   - fleet mode (no projectId): every installed pack's built-ins, badge "core"
//     or "pack:<name>". (Project overrides are project-scoped, so they never
//     appear in the fleet list.)
//   - project mode: the pack built-ins whose pack is ENABLED for the project
//     (core is always effective), overlaid by the project's own overrides. A
//     project-local template shadowing a built-in flips the badge to "project
//     override" and marks the built-in Overridden; a pack that is not enabled
//     contributes nothing.
func (h *Handler) effectiveTemplates(r *http.Request) ([]systemTemplateDTO, error) {
	projects, err := h.scannableProjects()
	if err != nil {
		return nil, err
	}
	raw := sysscan.ScanTemplates(systemHubClaudeDir, projects, nil)

	pid := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if pid == "" {
		return fleetTemplates(raw), nil
	}

	// Project mode: resolve the scoped project + its enabled packs.
	var proj sysscan.TemplateProject
	found := false
	for _, p := range projects {
		if p.Slug == pid || matchProjectID(p, pid) {
			proj = p
			found = true
			break
		}
	}
	if !found {
		return nil, errUnknownTemplateProject
	}
	enabledPacks := map[string]bool{}
	if st, serr := projectscan.ReadPluginState(proj.Path, nil); serr == nil && st != nil {
		for _, name := range st.Packs {
			enabledPacks[name] = true
		}
		// core is always effective when the project is a swarmery consumer; even
		// if Managed is false the built-ins are still installed, so we surface
		// core built-ins in project mode to keep the Toolkit useful.
	}
	return projectTemplates(raw, proj, enabledPacks), nil
}

// fleetTemplates keeps only plugin built-ins (project overrides are scoped) and
// tags each with a resolution badge.
func fleetTemplates(raw []sysscan.Template) []systemTemplateDTO {
	out := []systemTemplateDTO{}
	for _, t := range raw {
		if t.Source != sysscan.TemplateSourcePlugin {
			continue
		}
		out = append(out, systemTemplateDTO{
			Name: t.Name, FileName: t.FileName, Path: t.Path,
			Resolution: pluginResolution(t.PluginName), Source: string(t.Source),
			PluginName: t.PluginName,
		})
	}
	sortTemplates(out)
	return out
}

// projectTemplates folds the effective view for one project: enabled-pack
// built-ins overlaid by the project's own overrides (override wins + badge).
func projectTemplates(raw []sysscan.Template, proj sysscan.TemplateProject, enabledPacks map[string]bool) []systemTemplateDTO {
	// The project's own overrides (highest precedence), keyed by name.
	overrides := map[string]sysscan.Template{}
	for _, t := range raw {
		if t.Source == sysscan.TemplateSourceProject && t.ProjectID == proj.ID {
			overrides[t.Name] = t
		}
	}

	out := []systemTemplateDTO{}
	seen := map[string]bool{}

	// Project overrides first (they are the effective row for their name).
	for name, t := range overrides {
		out = append(out, systemTemplateDTO{
			Name: name, FileName: t.FileName, Path: t.Path,
			Resolution: "project override", Source: string(sysscan.TemplateSourceProject),
		})
		seen[name] = true
	}

	// Then enabled-pack built-ins not shadowed by an override. core is always
	// effective; a domain pack only when enabled for this project.
	for _, t := range raw {
		if t.Source != sysscan.TemplateSourcePlugin {
			continue
		}
		if t.PluginName != "core" && !enabledPacks[t.PluginName] {
			continue // pack disabled for this project — not effective
		}
		if seen[t.Name] {
			continue // duplicate built-in name already represented
		}
		out = append(out, systemTemplateDTO{
			Name: t.Name, FileName: t.FileName, Path: t.Path,
			Resolution: pluginResolution(t.PluginName), Source: string(t.Source),
			PluginName: t.PluginName,
			Overridden: overrides[t.Name].Name != "", // an override shadows this built-in
		})
		seen[t.Name] = true
	}
	sortTemplates(out)
	return out
}

func pluginResolution(pluginName string) string {
	if pluginName == "core" {
		return "core"
	}
	return "pack:" + pluginName
}

func sortTemplates(t []systemTemplateDTO) {
	sort.Slice(t, func(i, j int) bool { return t[i].Name < t[j].Name })
}

// ── POST /api/system/templates/{name}/copy — the ONE new write ──

// templateCopyResponse is the 201 body.
type templateCopyResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Hint string `json:"hint"`
}

// copySystemTemplate — POST /api/system/templates/{name}/copy?projectId= : copy a
// built-in into <project>/.claude/templates/<name>.md so a project can customise
// it (the graduation-rule override, mirroring the playbook duplicate idiom).
// requireLocalOrigin at the route; O_EXCL → 409 on repeat (never overwrite an
// existing customization); the destination is fenced STRICTLY inside the
// project's templates dir (fail-closed on path traversal).
func (h *Handler) copySystemTemplate(w http.ResponseWriter, r *http.Request) {
	if memoryReadOnly() {
		writeClientErr(w, http.StatusForbidden, "system is in readonly mode")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if !safeTemplateName(name) {
		writeClientErr(w, http.StatusBadRequest, "invalid template name")
		return
	}
	pid := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if pid == "" {
		writeClientErr(w, http.StatusBadRequest, "projectId is required")
		return
	}

	// Resolve the scoped project + confirm the source is a copyable built-in for
	// it (effectiveTemplates already gates by enabled pack).
	tmpls, err := h.effectiveTemplates(r)
	if err != nil {
		if errors.Is(err, errUnknownTemplateProject) {
			writeClientErr(w, http.StatusNotFound, "project not found")
			return
		}
		writeErr(w, err)
		return
	}
	i := slices.IndexFunc(tmpls, func(t systemTemplateDTO) bool { return t.Name == name })
	if i < 0 {
		writeClientErr(w, http.StatusNotFound, "no template named "+name+" for this project")
		return
	}
	src := tmpls[i]
	if src.Source != string(sysscan.TemplateSourcePlugin) {
		// Already a project override — nothing to copy.
		writeClientErr(w, http.StatusConflict, "template already resolves from the project")
		return
	}

	projectPath, ok, err := h.projectPathForTemplateScope(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeClientErr(w, http.StatusNotFound, "project not found")
		return
	}

	// Fence: the destination MUST resolve strictly inside <project>/.claude/templates.
	dir := filepath.Join(filepath.Clean(projectPath), ".claude", "templates")
	dest, ferr := fencedTemplateDest(dir, name)
	if ferr != nil {
		writeClientErr(w, http.StatusBadRequest, ferr.Error())
		return
	}

	content, rerr := os.ReadFile(src.Path)
	if rerr != nil {
		writeErr(w, rerr)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	// O_EXCL: a second copy of an already-customized file is a clean 409, never a
	// silent overwrite of the user's edits.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			writeClientErr(w, http.StatusConflict, "project template already exists: "+dest)
			return
		}
		writeErr(w, err)
		return
	}
	if _, werr := f.Write(content); werr != nil {
		f.Close()
		writeErr(w, werr)
		return
	}
	if cerr := f.Close(); cerr != nil {
		writeErr(w, cerr)
		return
	}
	writeJSONStatus(w, http.StatusCreated, templateCopyResponse{
		Name: name, Path: dest,
		Hint: "edit " + dest + " — project templates override built-ins",
	})
}

// fencedTemplateDest resolves <dir>/<name>.md and requires it to sit STRICTLY
// inside dir after cleaning — a name that walks out (despite safeTemplateName)
// is refused fail-closed. The dir's existing ancestry is symlink-resolved so a
// symlinked .claude cannot smuggle the write outside the project (the memory.go
// fence idiom, adapted for a not-yet-existing leaf).
func fencedTemplateDest(dir, name string) (string, error) {
	dest := filepath.Join(dir, name+".md")
	clean := filepath.Clean(dest)
	// De-symlink the deepest existing prefix of both the dir and the destination.
	rootResolved, err := evalExistingPath(filepath.Clean(dir))
	if err != nil {
		return "", errBadTemplatePath("cannot resolve templates dir")
	}
	destResolved, err := evalExistingPath(clean)
	if err != nil {
		return "", errBadTemplatePath("cannot resolve destination path")
	}
	// The destination must be a DIRECT child of the templates dir (flat) named
	// <name>.md — no nested traversal, no escape.
	if filepath.Dir(destResolved) != rootResolved || !strings.HasSuffix(destResolved, ".md") {
		return "", errBadTemplatePath("destination escapes the project templates dir")
	}
	return destResolved, nil
}

type templatePathError struct{ msg string }

func (e templatePathError) Error() string { return e.msg }

func errBadTemplatePath(msg string) error { return templatePathError{msg: msg} }

// errUnknownTemplateProject is returned by effectiveTemplates for an unresolvable
// projectId scope.
var errUnknownTemplateProject = errors.New("unknown template project")

// ── shared helpers ──

// scannableProjects lists the archived=0, absolute-path projects the template
// scan walks (the sysscan loadProjects universe, projected to TemplateProject).
func (h *Handler) scannableProjects() ([]sysscan.TemplateProject, error) {
	rows, err := h.DB.Query(`SELECT id, slug, path FROM projects WHERE archived = 0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sysscan.TemplateProject
	for rows.Next() {
		var id int64
		var slug, path sql.NullString
		if err := rows.Scan(&id, &slug, &path); err != nil {
			return nil, err
		}
		if !path.Valid || !filepath.IsAbs(path.String) {
			continue // '(unknown)' and friends
		}
		out = append(out, sysscan.TemplateProject{ID: id, Slug: slug.String, Path: path.String})
	}
	return out, rows.Err()
}

// projectPathForTemplateScope resolves the ?projectId= (slug or id) to a path.
func (h *Handler) projectPathForTemplateScope(pid string) (string, bool, error) {
	var path string
	err := h.DB.QueryRow(
		`SELECT path FROM projects WHERE (slug = ? OR CAST(id AS TEXT) = ?) AND archived = 0`,
		pid, pid).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

// matchProjectID reports whether a TemplateProject matches a numeric id string.
func matchProjectID(p sysscan.TemplateProject, pid string) bool {
	return pid != "" && strings.TrimSpace(pid) == strconv.FormatInt(p.ID, 10)
}

// systemItemByID runs the shared list projection for one id (agents/skills),
// returning the same systemItemDTO the roster serves (lint + usage folded). ok
// is false on a row miss.
func (h *Handler) systemItemByID(k systemKind, id int64) (systemItemDTO, bool, error) {
	var it systemItemDTO
	var sev sql.NullInt64
	var dead int64
	err := h.DB.QueryRow(systemItemSelect(k)+` WHERE t.id = ? AND t.deleted = 0`, id).Scan(
		&it.ID, &it.Name, &it.Scope, &it.ProjectSlug, &it.Origin, &it.PluginName,
		&it.Model, &it.Description, &it.Path, &sev, &dead)
	if errors.Is(err, sql.ErrNoRows) {
		return it, false, nil
	}
	if err != nil {
		return it, false, err
	}
	if sev.Valid {
		name := severityName(sev.Int64)
		it.LintMax = &name
	}
	it.Dead = dead != 0

	usage, err := h.usageByName(k, usageCutoff())
	if err != nil {
		return it, false, err
	}
	if u, ok := usage[normAgentType(it.Name)]; ok {
		if u.lastUsed != "" {
			lu := u.lastUsed
			it.LastUsed = &lu
		}
		it.Tasks30d = u.tasks30d
	}
	return it, true, nil
}

// safeTemplateName rejects a name that could escape the templates dir or embed a
// path separator — the {name} path segment is attacker-influenced. Template
// stems are lowercase-kebab file names, so this is strict (same rule as
// safePlaybookName, extended to allow the '.' some template stems use never —
// stems never carry a dot, the ".md" is added by the handler).
func safeTemplateName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
