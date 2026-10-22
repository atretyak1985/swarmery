package api

// phase 4+: promotion & drift detector — GET /api/system/insights, a
// read-only analysis over the sysscan registry tables (NO new tables, NO
// migration):
//
//   a. promotion candidates — one component name, scope=project origin=local,
//      not deleted, present in ≥2 different projects. Graduation rule
//      (docs/EXTENDING.md): when a second project needs a component, promote
//      it to a pack. similarity=identical when every copy shares one
//      content_hash; diverged otherwise, with a line-churn stat and the
//      redacted unified diff of the two most-diverged copies.
//   b. stale local overrides — a local item whose name collides with a
//      plugin-shipped item. Plugin rows carry composite names
//      ("<plugin>:<name>", sysscan/registry.go §7), so the join is
//      g.name = g.plugin_name || ':' || l.name. An identical local copy is a
//      pointless override (safe to delete); a diverged one is intentional or
//      drift — the diff is served so a human can judge.
//   c. dead components — the linter's ACTIVE agent_dead and skill_dead
//      findings (sysscan/skillaudit.go, 30-day window, matched by name,
//      advisory), reframed into the insights payload so the UI reads one
//      endpoint.
//
// Content sources: agents/skills diff from *_versions.content (plugin cache
// items ARE versioned by the scanner, so plugin-side content comes from the
// DB too). Commands store only content_hash — their contents are read live
// from file_path, tolerantly: an unreadable file degrades that item to
// hash-only similarity (diff "", diffStat null), never an error.
//
// Every served diff is computed over redact()ed contents — a secret can
// never leak through hunks (same discipline as diffSystemItem).

import (
	"database/sql"
	"net/http"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plugindrift"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// ---- DTOs (mirrored in web/src/api/types.ts, "phase 4+: insights") ---------

// insightCopyDTO is one concrete copy of a component inside an insight.
type insightCopyDTO struct {
	ItemID      int64   `json:"itemId"`
	ProjectSlug *string `json:"projectSlug"` // null for global-tier rows
	Scope       string  `json:"scope"`       // global | project
	Path        string  `json:"path"`        // file_path / dir_path
	ContentHash *string `json:"contentHash"` // null when no version is stored yet
}

// insightDiffStatDTO is the line churn between two copies.
type insightDiffStatDTO struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

type promotionCandidateDTO struct {
	Kind       string              `json:"kind"` // agent | skill | command
	Name       string              `json:"name"`
	Copies     []insightCopyDTO    `json:"copies"`     // slug-ordered, one per row
	Similarity string              `json:"similarity"` // identical | diverged
	DiffStat   *insightDiffStatDTO `json:"diffStat"`   // diverged only; null when contents unavailable
	Diff       string              `json:"diff"`       // redacted unified diff of the two most-diverged copies; "" when identical/unavailable
	Hint       string              `json:"hint"`       // copyable next-step recipe (display-only)
}

type staleOverrideDTO struct {
	Kind       string              `json:"kind"`
	Name       string              `json:"name"` // base name (the plugin row is "plugin:name")
	PluginName string              `json:"pluginName"`
	Local      insightCopyDTO      `json:"local"`
	Plugin     insightCopyDTO      `json:"plugin"`
	Identical  bool                `json:"identical"` // true → pointless override, safe to delete
	DiffStat   *insightDiffStatDTO `json:"diffStat"`
	Diff       string              `json:"diff"`
	Hint       string              `json:"hint"`
}

type deadComponentDTO struct {
	Kind        string  `json:"kind"` // agent (agent_dead) | skill (skill_dead)
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"`
	ProjectSlug *string `json:"projectSlug"`
	Message     string  `json:"message"` // the active finding's message
	Hint        string  `json:"hint"`
}

// undocumentedItemDTO is one item the linter reports as having no usage guide
// (docs_missing) or an incomplete one (docs_incomplete). Read-only and
// display-only, exactly like every other insight list: the fix is an edit to
// the item's own file, never an API call.
type undocumentedItemDTO struct {
	Kind        string   `json:"kind"` // agent | skill | command
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`
	ProjectSlug *string  `json:"projectSlug"`
	PluginName  *string  `json:"pluginName"`
	Path        string   `json:"path"`
	Rule        string   `json:"rule"`    // docs_missing | docs_incomplete
	Missing     []string `json:"missing"` // required subsection names, never null
	Hint        string   `json:"hint"`
}

// systemInsightsDTO is GET /api/system/insights.
type systemInsightsDTO struct {
	PromotionCandidates []promotionCandidateDTO `json:"promotionCandidates"`
	StaleOverrides      []staleOverrideDTO      `json:"staleOverrides"`
	Dead                []deadComponentDTO      `json:"dead"`
	Undocumented        []undocumentedItemDTO   `json:"undocumented"`
	// PluginDrift is the ONLY cross-project view of plugin drift. Nothing else
	// renders it for free: every other consumer of config_lint_findings joins on
	// a kind-specific target expression ('agent:' || a.id and the like), and a
	// plugin: target matches none of them.
	PluginDrift []pluginDriftDTO `json:"pluginDrift"`
	// SkillAudit is the active skill_desc_similar / session_skill_load_high
	// findings, and SkillAuditReport the newest weekly report (null before the
	// first one) — system_skill_audit.go.
	SkillAudit       []skillAuditFindingDTO `json:"skillAudit"`
	SkillAuditReport *skillAuditReportDTO   `json:"skillAuditReport"`
}

// pluginDriftDTO is one active plugin_* finding, resolved to the project it
// belongs to. Rule rides along so the UI can label the kind of problem.
type pluginDriftDTO struct {
	PluginID    string  `json:"pluginId"` // "<name>@<marketplace>"
	Rule        string  `json:"rule"`
	Severity    string  `json:"severity"`
	Message     string  `json:"message"`
	ProjectSlug *string `json:"projectSlug"` // null when the path matches no project row
	ProjectPath string  `json:"projectPath"`
}

// Next-step hints — display-only text the UI offers for copying. Promotion
// itself deliberately stays a manual flow (docs/EXTENDING.md).
const (
	promotionHint      = "promote: de-flavor → move to a domain pack (2 projects) or core (all projects) → bump that plugin's semver → delete the donor copies (docs/EXTENDING.md)"
	staleIdenticalHint = "identical to the plugin copy — the local override is pointless; delete the local file and rely on the plugin"
	staleDivergedHint  = "diverged from the plugin copy — intentional override or drift; review the diff, then re-sync or delete the local copy"
	deadHint           = "no use in 30 days, matched by name (advisory — a component used only outside recorded sessions looks dead too); before deleting or archiving, check it is not the one a new agent should extend"
	undocumentedHint   = "add the missing `# How to use` subsections to the item's own file (docs/system-docs-format.md §2), then re-run the scan"
)

// Shared SQL predicates — spliced into BOTH the full compute queries
// (projectLocalGroups / staleOverrideList) and the insightCounts summary
// queries, so the badge counts equal len(promotionCandidates) /
// len(staleOverrides) BY CONSTRUCTION (byte-identical fragments, not
// hand-kept copies).
const (
	// localProjectPred filters live project-local rows. The item table must
	// be aliased `t` wherever this is spliced.
	localProjectPred = `t.deleted = 0 AND t.scope = 'project' AND t.origin = 'local'`
	// localOverridePred filters live local rows on the override side (any
	// scope). The local item table must be aliased `l`.
	localOverridePred = `l.deleted = 0 AND l.origin = 'local'`
	// pluginCollisionJoin joins a local row (alias `l`) to the plugin row
	// (alias `g`) it name-collides with — plugin rows carry composite names
	// ("<plugin>:<name>", sysscan/registry.go §7).
	pluginCollisionJoin = `g.deleted = 0 AND g.origin = 'plugin'
	                       AND g.name = g.plugin_name || ':' || l.name`
)

// ---- project scope ------------------------------------------------------------
//
// Insights are cross-project ANALYSIS (a promotion candidate is by definition a
// component several projects copied), so a project scope cannot simply drop the
// other projects' rows — it selects the insights THIS project participates in,
// and the payload still shows every copy. Both fragments below are spliced into
// the compute queries AND into insightCounts, so the badge equals the list
// length by construction (the discipline the predicates above already follow).

// promotionScopeHaving keeps only names the scoped project itself has a local
// copy of. Empty (and nil-safe) in fleet mode. Must be appended to a HAVING over
// the item table aliased `t`.
func (e *effectiveScope) promotionScopeHaving() (string, []any) {
	if e == nil {
		return "", nil
	}
	return ` AND SUM(CASE WHEN t.project_id = ? THEN 1 ELSE 0 END) > 0`, []any{e.projectID}
}

// staleOverrideScope keeps the overrides that are EFFECTIVE in the scoped
// project: a local copy that is either the project's own or the user's global
// one, shadowing a pack the project actually enables. Empty in fleet mode.
func (e *effectiveScope) staleOverrideScope() (string, []any) {
	if e == nil {
		return "", nil
	}
	holes, packArgs := e.packHoles()
	args := append([]any{e.projectID}, packArgs...)
	return ` AND (l.project_id = ? OR l.scope = 'global') AND g.plugin_name IN (` + holes + `)`, args
}

// insightKind parameterizes the per-kind queries. verTable=="" marks the
// unversioned kind (commands): contents are read from disk instead.
type insightKind struct {
	kind     string // agent | skill | command
	table    string
	verTable string
	pathCol  string
}

// Fixed order — makes the payload (and the tests) deterministic.
var insightKinds = []insightKind{
	{kind: "agent", table: "agents", verTable: "agent_versions", pathCol: "file_path"},
	{kind: "skill", table: "skills", verTable: "skill_versions", pathCol: "dir_path"},
	{kind: "command", table: "commands", verTable: "", pathCol: "file_path"},
}

// insightCopy pairs the wire DTO with the (not-served) raw content.
type insightCopy struct {
	dto        insightCopyDTO
	content    string
	hasContent bool
}

// ---- compute ----------------------------------------------------------------

// computeInsights builds the whole payload. esc is the optional project scope
// (nil = fleet-wide, the unchanged behaviour).
func computeInsights(db *sql.DB, esc *effectiveScope) (systemInsightsDTO, error) {
	out := systemInsightsDTO{
		PromotionCandidates: []promotionCandidateDTO{},
		StaleOverrides:      []staleOverrideDTO{},
		Dead:                []deadComponentDTO{},
		Undocumented:        []undocumentedItemDTO{},
		PluginDrift:         []pluginDriftDTO{},
		SkillAudit:          []skillAuditFindingDTO{},
	}
	var err error
	if out.PromotionCandidates, err = promotionCandidates(db, esc); err != nil {
		return out, err
	}
	if out.StaleOverrides, err = staleOverrideList(db, esc); err != nil {
		return out, err
	}
	if out.Dead, err = deadComponents(db, esc); err != nil {
		return out, err
	}
	if out.Undocumented, err = undocumentedItems(db, esc); err != nil {
		return out, err
	}
	// Best effort, like the fields above: a drift query failure must not take
	// the whole insights page down.
	if drift, derr := pluginDriftFindings(db, esc); derr == nil {
		out.PluginDrift = drift
	}
	if out.SkillAudit, err = skillAuditFindings(db, esc); err != nil {
		return out, err
	}
	out.SkillAuditReport = latestSkillAuditReport()
	return out, nil
}

// pluginDriftFindings lists active plugin_* findings with the project resolved
// by path. plugin:detector (no "|" in the target) is machine-wide and carries a
// null slug and an empty path.
// A project scope keeps only the findings that resolve to THAT project (the
// machine-wide plugin:detector row has no project and is dropped there).
func pluginDriftFindings(db *sql.DB, esc *effectiveScope) ([]pluginDriftDTO, error) {
	rows, err := db.Query(`
		SELECT f.target, f.rule, f.severity, f.message
		FROM config_lint_findings f
		WHERE f.resolved_at IS NULL AND f.rule LIKE 'plugin\_%' ESCAPE '\'
		ORDER BY f.severity DESC, f.target`)
	if err != nil {
		return nil, err
	}
	out := []pluginDriftDTO{}
	for rows.Next() {
		var target, rule, severity, message string
		if err := rows.Scan(&target, &rule, &severity, &message); err != nil {
			rows.Close()
			return nil, err
		}
		d := pluginDriftDTO{Rule: rule, Severity: severity, Message: message}
		if id, path, ok := plugindrift.ParseTarget(target); ok {
			d.PluginID, d.ProjectPath = id, path
		} else {
			d.PluginID = strings.TrimPrefix(target, "plugin:")
		}
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Slugs are resolved in a SECOND pass, after the cursor above is closed.
	// The store caps the pool at one connection (store.go:38), so a per-row
	// lookup inside the loop would wait for a connection this very loop holds —
	// a permanent deadlock, not a slow path.
	slugs, err := projectSlugsByPath(db)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if slug, ok := slugs[out[i].ProjectPath]; ok && out[i].ProjectPath != "" {
			out[i].ProjectSlug = &slug
		}
	}
	if !esc.scoped() {
		return out, nil
	}
	scopedOut := []pluginDriftDTO{}
	for _, d := range out {
		if d.ProjectSlug != nil && *d.ProjectSlug == esc.slug() {
			scopedOut = append(scopedOut, d)
		}
	}
	return scopedOut, nil
}

// projectSlugsByPath is the path→slug map used to link a finding to its project.
func projectSlugsByPath(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT path, slug FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, slug string
		if err := rows.Scan(&path, &slug); err != nil {
			return nil, err
		}
		out[path] = slug
	}
	return out, rows.Err()
}

// promotionCandidates groups project-local components by name and keeps the
// names present in ≥2 distinct projects.
func promotionCandidates(db *sql.DB, esc *effectiveScope) ([]promotionCandidateDTO, error) {
	out := []promotionCandidateDTO{}
	for _, k := range insightKinds {
		groups, names, err := projectLocalGroups(db, k, esc)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			copies := groups[name]
			if distinctProjects(copies) < 2 {
				continue
			}
			cand := promotionCandidateDTO{Kind: k.kind, Name: name, Hint: promotionHint}
			for _, c := range copies {
				cand.Copies = append(cand.Copies, c.dto)
			}
			if identicalHashes(copies) {
				cand.Similarity = "identical"
			} else {
				cand.Similarity = "diverged"
				if k.verTable == "" {
					fillDiskContents(copies) // commands: content lives on disk only
				}
				cand.Diff, cand.DiffStat = mostDivergedDiff(copies)
			}
			out = append(out, cand)
		}
	}
	return out, nil
}

// projectLocalGroups loads every live scope=project origin=local row of one
// kind, grouped by name (names returned in first-seen = SQL name order).
func projectLocalGroups(db *sql.DB, k insightKind, esc *effectiveScope) (map[string][]insightCopy, []string, error) {
	// The ≥2-distinct-projects name gate is repeated in SQL so version CONTENT
	// is only ever joined/loaded for actual candidate names — a lone local
	// component never drags its full body off disk pages into the result set.
	// In project mode the gate also requires the scoped project to be one of
	// those copies; the group itself still carries EVERY project's copy, which
	// is what makes the insight actionable.
	gate, gateArgs := esc.promotionScopeHaving()
	nameGate := ` AND t.name IN (SELECT t.name FROM ` + k.table + ` t
	                             WHERE ` + localProjectPred + `
	                             GROUP BY t.name HAVING COUNT(DISTINCT t.project_id) >= 2` + gate + `)`
	var q string
	if k.verTable != "" {
		q = `SELECT t.id, t.name, p.slug, t.` + k.pathCol + `, v.content_hash, v.content
		     FROM ` + k.table + ` t
		     JOIN projects p ON p.id = t.project_id
		     LEFT JOIN ` + k.verTable + ` v ON v.id = t.current_version_id
		     WHERE ` + localProjectPred + nameGate + `
		     ORDER BY t.name, p.slug, t.id`
	} else {
		q = `SELECT t.id, t.name, p.slug, t.file_path, t.content_hash, NULL
		     FROM commands t
		     JOIN projects p ON p.id = t.project_id
		     WHERE ` + localProjectPred + nameGate + `
		     ORDER BY t.name, p.slug, t.id`
	}
	rows, err := db.Query(q, gateArgs...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	groups := map[string][]insightCopy{}
	var names []string
	for rows.Next() {
		var c insightCopy
		var name string
		var slug, hash, content sql.NullString
		if err := rows.Scan(&c.dto.ItemID, &name, &slug, &c.dto.Path, &hash, &content); err != nil {
			return nil, nil, err
		}
		c.dto.Scope = "project"
		if slug.Valid {
			s := slug.String
			c.dto.ProjectSlug = &s
		}
		if hash.Valid {
			hs := hash.String
			c.dto.ContentHash = &hs
		}
		if content.Valid {
			c.content, c.hasContent = content.String, true
		}
		if _, seen := groups[name]; !seen {
			names = append(names, name)
		}
		groups[name] = append(groups[name], c)
	}
	return groups, names, rows.Err()
}

// staleOverrideList pairs each local item with the plugin item it
// name-collides with (composite plugin names, registry.go §7).
func staleOverrideList(db *sql.DB, esc *effectiveScope) ([]staleOverrideDTO, error) {
	out := []staleOverrideDTO{}
	scope, scopeArgs := esc.staleOverrideScope()
	for _, k := range insightKinds {
		var q string
		if k.verTable != "" {
			q = `SELECT l.id, l.name, l.scope, lp.slug, l.` + k.pathCol + `, lv.content_hash, lv.content,
			            g.id, g.plugin_name, g.` + k.pathCol + `, gv.content_hash, gv.content
			     FROM ` + k.table + ` l
			     JOIN ` + k.table + ` g ON ` + pluginCollisionJoin + `
			     LEFT JOIN projects lp ON lp.id = l.project_id
			     LEFT JOIN ` + k.verTable + ` lv ON lv.id = l.current_version_id
			     LEFT JOIN ` + k.verTable + ` gv ON gv.id = g.current_version_id
			     WHERE ` + localOverridePred + scope + `
			     ORDER BY l.name, lp.slug, l.id`
		} else {
			q = `SELECT l.id, l.name, l.scope, lp.slug, l.file_path, l.content_hash, NULL,
			            g.id, g.plugin_name, g.file_path, g.content_hash, NULL
			     FROM commands l
			     JOIN commands g ON ` + pluginCollisionJoin + `
			     LEFT JOIN projects lp ON lp.id = l.project_id
			     WHERE ` + localOverridePred + scope + `
			     ORDER BY l.name, lp.slug, l.id`
		}
		rows, err := db.Query(q, scopeArgs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			var local, plugin insightCopy
			var lSlug, lHash, lContent, gPlugin, gHash, gContent sql.NullString
			if err := rows.Scan(&local.dto.ItemID, &name, &local.dto.Scope, &lSlug,
				&local.dto.Path, &lHash, &lContent,
				&plugin.dto.ItemID, &gPlugin, &plugin.dto.Path, &gHash, &gContent); err != nil {
				rows.Close()
				return nil, err
			}
			// Plugin cache rows are always global-tier (sysscan/sources.go).
			plugin.dto.Scope = "global"
			if lSlug.Valid {
				s := lSlug.String
				local.dto.ProjectSlug = &s
			}
			if lHash.Valid {
				h := lHash.String
				local.dto.ContentHash = &h
			}
			if gHash.Valid {
				h := gHash.String
				plugin.dto.ContentHash = &h
			}
			if lContent.Valid {
				local.content, local.hasContent = lContent.String, true
			}
			if gContent.Valid {
				plugin.content, plugin.hasContent = gContent.String, true
			}

			so := staleOverrideDTO{Kind: k.kind, Name: name, PluginName: gPlugin.String}
			// Baseline first: the diff reads plugin → local ("what did the
			// local override change on top of the shipped copy").
			pair := []insightCopy{plugin, local}
			if identicalHashes(pair) {
				so.Identical, so.Hint = true, staleIdenticalHint
			} else {
				so.Hint = staleDivergedHint
				if k.verTable == "" {
					fillDiskContents(pair)
				}
				so.Diff, so.DiffStat = mostDivergedDiff(pair)
			}
			so.Local, so.Plugin = local.dto, plugin.dto
			out = append(out, so)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// deadKinds are the item kinds a dead rule covers, in payload order.
var deadKinds = []struct{ kind, table, rule string }{
	{kind: "agent", table: "agents", rule: sysscan.RuleAgentDead},
	{kind: "skill", table: "skills", rule: sysscan.RuleSkillDead},
}

// deadComponents reframes the linter's ACTIVE agent_dead and skill_dead
// findings — one source of truth, no second telemetry query. Agents first, then
// skills, each by name.
// A project scope keeps only the items EFFECTIVE there (effectiveScope) — a
// dead component of another project is that project's problem.
func deadComponents(db *sql.DB, esc *effectiveScope) ([]deadComponentDTO, error) {
	pred, args := esc.predicate("t.", true)
	out := []deadComponentDTO{}
	for _, k := range deadKinds {
		// One cursor at a time: the store runs a single connection.
		rows, err := db.Query(`
			SELECT t.id, t.name, t.scope, p.slug, f.message
			FROM config_lint_findings f
			JOIN `+k.table+` t ON f.target = '`+k.kind+`:' || t.id
			LEFT JOIN projects p ON p.id = t.project_id
			WHERE f.rule = ? AND f.resolved_at IS NULL AND t.deleted = 0`+pred+`
			ORDER BY t.name, t.id`, append([]any{k.rule}, args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := deadComponentDTO{Kind: k.kind, Hint: deadHint}
			if err := rows.Scan(&d.ID, &d.Name, &d.Scope, &d.ProjectSlug, &d.Message); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// undocumentedItems reframes the linter's ACTIVE docs_missing / docs_incomplete
// findings (sysscan/lint.go) into the insights payload — one source of truth,
// no second parse of any file here. Every registrable kind participates, so the
// list joins per insightKind exactly as the other insight queries do.
// A project scope keeps only the items EFFECTIVE there (effectiveScope): another
// project's undocumented component is that project's problem.
func undocumentedItems(db *sql.DB, esc *effectiveScope) ([]undocumentedItemDTO, error) {
	out := []undocumentedItemDTO{}
	pred, args := esc.predicate("t.", true)
	for _, k := range insightKinds {
		// One cursor at a time: the store caps the pool at a single connection
		// (store.go), so the rows of one kind are drained and closed before the
		// next kind is queried (same discipline as staleOverrideList).
		rows, err := db.Query(`
			SELECT t.id, t.name, t.scope, p.slug, t.plugin_name, t.`+k.pathCol+`, f.rule, f.message
			FROM config_lint_findings f
			JOIN `+k.table+` t ON f.target = '`+k.kind+`:' || t.id
			LEFT JOIN projects p ON p.id = t.project_id
			WHERE f.resolved_at IS NULL AND f.rule IN ('`+sysscan.RuleDocsMissing+`', '`+sysscan.RuleDocsIncomplete+`')
			  AND t.deleted = 0`+pred+`
			ORDER BY t.name, t.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := undocumentedItemDTO{Kind: k.kind, Hint: undocumentedHint}
			var message string
			if err := rows.Scan(&d.ID, &d.Name, &d.Scope, &d.ProjectSlug, &d.PluginName,
				&d.Path, &d.Rule, &message); err != nil {
				rows.Close()
				return nil, err
			}
			d.Missing = missingSections(d.Rule, message)
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// missingSections resolves the required subsections an item lacks.
//
// docs_missing means there is no guide at all, so ALL four required subsections
// are missing — the linter deliberately does not enumerate them in that message
// (sysscan/docs.go keeps Docs.Missing empty when the block is absent, so the two
// findings never double-report), and the useful answer for a reader is still the
// full list. docs_incomplete carries its gaps after DocsMissingSectionsMarker;
// the marker is searched from the RIGHT because the message names the file first
// and a path may itself contain ": ".
func missingSections(rule, message string) []string {
	if rule == sysscan.RuleDocsMissing {
		return append([]string{}, sysscan.RequiredDocSections...)
	}
	out := []string{}
	i := strings.LastIndex(message, sysscan.DocsMissingSectionsMarker)
	if i < 0 {
		return out
	}
	tail := message[i+len(sysscan.DocsMissingSectionsMarker):]
	// The linter appends a parenthetical rune-floor note after the list.
	if j := strings.Index(tail, " ("); j >= 0 {
		tail = tail[:j]
	}
	for _, s := range strings.Split(tail, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---- helpers ------------------------------------------------------------------

// distinctProjects counts distinct project slugs among the copies.
func distinctProjects(copies []insightCopy) int {
	set := map[string]bool{}
	for _, c := range copies {
		if c.dto.ProjectSlug != nil {
			set[*c.dto.ProjectSlug] = true
		}
	}
	return len(set)
}

// identicalHashes reports whether every copy carries the SAME known hash —
// an unknown hash is never "identical" (conservative: unknowns read as drift).
func identicalHashes(copies []insightCopy) bool {
	first := copies[0].dto.ContentHash
	if first == nil {
		return false
	}
	for _, c := range copies[1:] {
		if c.dto.ContentHash == nil || *c.dto.ContentHash != *first {
			return false
		}
	}
	return true
}

// fillDiskContents loads missing contents from file_path — the commands path
// (no version table). Tolerant: an unreadable file just stays content-less.
func fillDiskContents(copies []insightCopy) {
	for i := range copies {
		if copies[i].hasContent {
			continue
		}
		if b, err := os.ReadFile(copies[i].dto.Path); err == nil {
			copies[i].content, copies[i].hasContent = string(b), true
		}
	}
}

// maxInsightDiffBytes caps every diff served by the insights endpoint —
// advisory cards must never balloon the JSON payload on a pathological
// divergence (e.g. a generated file registered as a component). The stat is
// computed on the FULL diff first, so churn numbers stay exact; only the
// served text is cut, at a line boundary, with an explicit trailing marker.
const maxInsightDiffBytes = 64 * 1024

// diffTruncatedMarker is appended to a diff cut at maxInsightDiffBytes.
const diffTruncatedMarker = "\n… (diff truncated)"

// mostDivergedDiff picks the copy pair with the largest line churn and returns
// its REDACTED unified diff plus the added/removed stat. Copies without
// available content are skipped; fewer than two available → ("", nil).
// The returned diff is truncated at maxInsightDiffBytes.
func mostDivergedDiff(copies []insightCopy) (string, *insightDiffStatDTO) {
	var bestDiff string
	var best *insightDiffStatDTO
	for i := 0; i < len(copies); i++ {
		if !copies[i].hasContent {
			continue
		}
		for j := i + 1; j < len(copies); j++ {
			if !copies[j].hasContent {
				continue
			}
			// Redact BEFORE diffing — a secret can never leak through hunks
			// (same rule as diffSystemItem).
			d := UnifiedDiff(copies[i].dto.Path, copies[j].dto.Path,
				redact(copies[i].content), redact(copies[j].content))
			stat := statOf(d)
			if best == nil || stat.Added+stat.Removed > best.Added+best.Removed {
				bestDiff, best = d, &stat
			}
		}
	}
	if len(bestDiff) > maxInsightDiffBytes {
		cut := bestDiff[:maxInsightDiffBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i] // whole lines only — never a torn diff line or rune
		}
		bestDiff = cut + diffTruncatedMarker
	}
	return bestDiff, best
}

// statOf counts +/- lines of a unified diff (headers excluded).
// textdiff.UnifiedDiff emits the ---/+++ header pair ONLY as the first two
// lines, so headers are skipped by POSITION, never by content — a removed
// markdown `---` frontmatter fence renders as the content line `----` and
// must count as a removal.
func statOf(diff string) insightDiffStatDTO {
	var st insightDiffStatDTO
	lines := splitLines(diff)
	if len(lines) > 2 {
		lines = lines[2:] // the "--- a\n+++ b" header pair
	}
	for _, line := range lines {
		switch {
		case len(line) >= 1 && line[0] == '+':
			st.Added++
		case len(line) >= 1 && line[0] == '-':
			st.Removed++
		}
	}
	return st
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ---- summary counters ---------------------------------------------------------

// insightCounts serves the summary badge: cheap COUNT queries only — no disk
// IO, no diffing. Predicates are the SAME spliced constants the full compute
// uses (localProjectPred / localOverridePred / pluginCollisionJoin), so the
// counts equal len(promotionCandidates) / len(staleOverrides) by construction.
// esc scopes the counts to one project exactly as it scopes the lists (nil =
// fleet-wide).
func insightCounts(db *sql.DB, esc *effectiveScope) (promotions, staleOverrides int64, err error) {
	gate, gateArgs := esc.promotionScopeHaving()
	scope, scopeArgs := esc.staleOverrideScope()
	for _, k := range insightKinds {
		var n int64
		if err = db.QueryRow(`SELECT COUNT(*) FROM (
			SELECT t.name FROM `+k.table+` t
			WHERE `+localProjectPred+`
			GROUP BY t.name HAVING COUNT(DISTINCT t.project_id) >= 2`+gate+`)`, gateArgs...).Scan(&n); err != nil {
			return
		}
		promotions += n
		if err = db.QueryRow(`SELECT COUNT(*) FROM `+k.table+` l
			JOIN `+k.table+` g ON `+pluginCollisionJoin+`
			WHERE `+localOverridePred+scope, scopeArgs...).Scan(&n); err != nil {
			return
		}
		staleOverrides += n
	}
	return
}

// ---- handler (route registered in routes.go) -----------------------------------

// GET /api/system/insights?projectId=<slug|id> — fleet-wide, or narrowed to the
// insights one project participates in.
func (h *Handler) systemInsights(w http.ResponseWriter, r *http.Request) {
	esc, err := h.resolveEffectiveScope(r.URL.Query().Get("projectId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := computeInsights(h.DB, esc)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out, nil)
}
