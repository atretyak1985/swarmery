package sysscan

// Step-04 config linter: a post-pass over the registry the scanner (step-03)
// just converged. Its ONLY write surface is config_lint_findings.
//
// Target convention — extends the 0001_init.sql column comment
// ("agent:12 | skill:3 | claude_md:...") with the step-02/03 item kinds:
//
//	agent:<id> | skill:<id> | claude_md:<path> | hook:<id> | command:<id>
//
// (command:<id> and hooks:<source_file> are already emitted by the scanner's
// parse_error rule; the linter adds hook:<id> for per-entry rules.)
//
// Lifecycle per (target, rule): while a rule keeps firing the single active
// row (resolved_at IS NULL) is refreshed in place — no duplicate actives; a
// rule that stops firing gets resolved_at=now; a rule that fires again after
// a resolve INSERTs a NEW row, so history is preserved.
//
// The linter never touches the scanner-owned parse_error rule.

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
)

// Linter-owned rule names (design §3.5). parse_error is scanner-owned.
const (
	RuleAgentNoBoundaries  = "agent_no_boundaries"     // warn: agent body without a Boundaries section
	RuleAgentNoDescription = "agent_no_description"    // warn: empty description in agent frontmatter
	RuleSkillShortDesc     = "skill_short_description" // warn: skill description below the min length — poor trigger recall
	RuleClaudeMDOversized  = "claude_md_oversized"     // warn: project CLAUDE.md above the token estimate threshold
	RuleHookNoTimeout      = "hook_no_timeout"         // warn: hook command with neither timeout nor '|| true' — can stall Claude Code
	RuleAgentNameDuplicate = "agent_name_duplicate"    // warn: one name in global AND project scope — override confusion
	RuleAgentDead          = "agent_dead"              // info: no subagent run in 30 days (advisory, matched by name — skillaudit.go)

	// Session-start cost rules: what every session of a project loads before its
	// first prompt (the first-request card, internal/firstreq, shows the size).
	RuleClaudeMDOver200Lines = "claude_md_over_200_lines" // warn: project CLAUDE.md longer than MaxClaudeMDLines
	RuleMCPServerUnused30d   = "mcp_server_unused_30d"    // info: file-configured MCP server, no mcp__<server>__* call in 30 days

	// Usage-guide rules (docs/system-docs-format.md), all thin wrappers over
	// ParseDocs (docs.go) — the parse is never re-implemented here.
	RuleDocsMissing    = "docs_missing"         // warn: no `# How to use` block at all
	RuleDocsIncomplete = "docs_incomplete"      // warn: block present, a REQUIRED subsection absent or under the rune floor (§2)
	RuleDocsDuplicate  = "docs_duplicate_block" // warn: a second `# How to use` H1 (§5.4 — a violation, never a merge)
	RuleDocsStale      = "docs_stale"           // info: docs.source_sha no longer matches the body (§4)
	RuleDocsUnreviewed = "docs_unreviewed"      // info: docs.status is not `reviewed` (§3)
)

// DocsMissingSectionsMarker precedes the comma-separated list of absent
// required subsections in a docs_incomplete message. It is the ONE parse
// contract between the linter's message and the API's `undocumented` insight
// list (system_insights.go) — the message names the file first, so the marker
// is searched for from the RIGHT and a path containing ": " cannot fool it.
const DocsMissingSectionsMarker = "missing required section(s): "

// docsStatusReviewed is the one docs.status value that clears docs_unreviewed
// (§3: `generated` means machine-written and unread; an unknown value is kept
// verbatim by ParseDocs and reported here as-is rather than normalised away).
const docsStatusReviewed = "reviewed"

// linterRules is the full owned-rule set — every pass syncs each rule, so a
// rule with zero findings this pass resolves all of its previously active rows.
var linterRules = []string{
	RuleAgentNoBoundaries,
	RuleAgentNoDescription,
	RuleSkillShortDesc,
	RuleClaudeMDOversized,
	RuleClaudeMDOver200Lines,
	RuleHookNoTimeout,
	RuleAgentNameDuplicate,
	RuleAgentDead,
	RuleSkillDead,
	RuleSkillDescSimilar,
	RuleSessionSkillLoadHigh,
	RuleMCPServerUnused30d,
	RuleDocsMissing,
	RuleDocsIncomplete,
	RuleDocsDuplicate,
	RuleDocsStale,
	RuleDocsUnreviewed,
}

// Threshold defaults and their env overrides (precedence: explicit Config
// value > env > default — resolved in Config.withDefaults).
const (
	DefaultMinSkillDescription = 40   // runes
	DefaultMaxClaudeMDTokens   = 2500 // estimated tokens (len/4)

	// MaxClaudeMDLines is claude_md_over_200_lines' bound. Not configurable on
	// purpose: the number is in the rule's name.
	MaxClaudeMDLines = 200

	EnvMinSkillDescription = "SWARMERY_LINT_MIN_SKILL_DESC"
	EnvMaxClaudeMDTokens   = "SWARMERY_LINT_MAX_CLAUDE_MD_TOKENS"
)

// envInt reads an integer env override; unset, empty, or non-positive values
// fall back to def.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		log.Printf("warn: sysscan lint: %s=%q is not a positive integer — using %d", name, v, def)
		return def
	}
	return n
}

// boundariesHeading matches a markdown heading line whose text mentions
// "boundaries" ("# Boundaries", "## Boundaries & Scope", …) — the section the
// agent editor form treats as the boundaries field (design §3.5).
var boundariesHeading = regexp.MustCompile(`(?im)^#{1,6}[ \t][^\n]*\bboundaries\b`)

// LintStats reports one lint pass: active findings per rule plus how many
// previously active rows this pass resolved.
type LintStats struct {
	PerRule  map[string]int
	Resolved int
}

func (ls LintStats) String() string {
	parts := make([]string, 0, len(linterRules)+1)
	for _, rule := range linterRules {
		parts = append(parts, fmt.Sprintf("%s=%d", rule, ls.PerRule[rule]))
	}
	parts = append(parts, fmt.Sprintf("resolved=%d", ls.Resolved))
	return strings.Join(parts, " ")
}

// lintFinding is one detected violation, ready for its findings row.
type lintFinding struct {
	target   string
	severity string // info | warn (error is reserved for the scanner's parse_error)
	message  string
}

// Lint runs one linter pass with a throwaway Scanner — the package-level
// entrypoint for one-shot callers (cmd/swarmery sysscan).
func Lint(db *sql.DB, cfg Config) (LintStats, error) {
	return New(db, cfg, nil).Lint()
}

// Lint evaluates every linter-owned rule against the current registry state
// and syncs config_lint_findings (see the lifecycle contract in the package
// comment above). Like the scanner it is tolerant on disk: an unreadable
// CLAUDE.md warns and skips; only DB errors abort the pass.
func (s *Scanner) Lint() (LintStats, error) {
	st := LintStats{PerRule: map[string]int{}}
	byRule := map[string][]lintFinding{}
	// skipped rules were not evaluated this pass: their rows stay as they are
	// instead of being resolved by an empty sync.
	skipped := map[string]bool{}

	if err := s.lintAgentContent(byRule); err != nil {
		return st, fmt.Errorf("lint agents: %w", err)
	}
	if err := s.lintSkillDescriptions(byRule); err != nil {
		return st, fmt.Errorf("lint skills: %w", err)
	}
	if err := s.lintClaudeMD(byRule); err != nil {
		return st, fmt.Errorf("lint CLAUDE.md: %w", err)
	}
	if err := s.lintHookTimeouts(byRule); err != nil {
		return st, fmt.Errorf("lint hooks: %w", err)
	}
	if err := s.lintDuplicateNames(byRule); err != nil {
		return st, fmt.Errorf("lint duplicate names: %w", err)
	}
	if err := s.lintSkillAudit(byRule, skipped); err != nil {
		return st, fmt.Errorf("lint skill audit: %w", err)
	}
	if err := s.lintUnusedMCPServers(byRule); err != nil {
		return st, fmt.Errorf("lint unused MCP servers: %w", err)
	}
	if err := s.lintDocs(byRule); err != nil {
		return st, fmt.Errorf("lint docs: %w", err)
	}

	for _, rule := range linterRules {
		if skipped[rule] {
			continue
		}
		lf := byRule[rule]
		items := make([]findings.Item, 0, len(lf))
		for _, f := range lf {
			items = append(items, findings.Item{Target: f.target, Severity: f.severity, Message: f.message})
		}
		detected, resolved, err := findings.Sync(s.db, rule, items)
		if err != nil {
			return st, fmt.Errorf("lint %s: %w", rule, err)
		}
		st.PerRule[rule] = detected
		st.Resolved += resolved
	}
	return st, nil
}

// lintAgentContent covers the two content rules — agent_no_boundaries and
// agent_no_description — from the CURRENT stored version of each live agent
// (the linter never re-reads agent files from disk). Agents whose frontmatter
// does not parse are skipped: the scanner's parse_error finding owns those.
func (s *Scanner) lintAgentContent(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(
		`SELECT a.id, a.file_path, COALESCE(v.content, '')
		 FROM agents a LEFT JOIN agent_versions v ON v.id = a.current_version_id
		 WHERE a.deleted = 0 ORDER BY a.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var path, content string
		if err := rows.Scan(&id, &path, &content); err != nil {
			return err
		}
		_, body, err := splitFrontmatter([]byte(content))
		if err != nil {
			continue // unparseable — the parse_error finding owns this item
		}
		fm, err := parseFrontmatter([]byte(content))
		if err != nil {
			continue
		}
		target := fmt.Sprintf("agent:%d", id)
		if !boundariesHeading.Match(body) {
			byRule[RuleAgentNoBoundaries] = append(byRule[RuleAgentNoBoundaries], lintFinding{
				target:   target,
				severity: "warn",
				message:  fmt.Sprintf("%s: agent body has no Boundaries section", path),
			})
		}
		if strField(fm, "description") == "" {
			byRule[RuleAgentNoDescription] = append(byRule[RuleAgentNoDescription], lintFinding{
				target:   target,
				severity: "warn",
				message:  fmt.Sprintf("%s: empty description in frontmatter", path),
			})
		}
	}
	return rows.Err()
}

// lintSkillDescriptions flags skills whose description is missing or shorter
// than MinSkillDescription runes — short descriptions trigger poorly.
func (s *Scanner) lintSkillDescriptions(byRule map[string][]lintFinding) error {
	min := s.cfg.MinSkillDescription
	rows, err := s.db.Query(
		`SELECT id, name, COALESCE(description, '') FROM skills WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, desc string
		if err := rows.Scan(&id, &name, &desc); err != nil {
			return err
		}
		if n := utf8.RuneCountInString(desc); n < min {
			byRule[RuleSkillShortDesc] = append(byRule[RuleSkillShortDesc], lintFinding{
				target:   fmt.Sprintf("skill:%d", id),
				severity: "warn",
				message: fmt.Sprintf("skill %q: description is %d chars — below the %d-char minimum, it will trigger poorly",
					name, n, min),
			})
		}
	}
	return rows.Err()
}

// lintClaudeMD flags each project whose CLAUDE.md exceeds MaxClaudeMDTokens
// (claude_md_oversized) or MaxClaudeMDLines (claude_md_over_200_lines), from one
// read of the file. The token estimate is deliberately crude — len(bytes)/4; the
// precise context-waste detector is design §5.2 and is NOT built here. The line
// rule counts the project CLAUDE.md only, like the token rule: it is the file
// the project owns and can trim.
func (s *Scanner) lintClaudeMD(byRule map[string][]lintFinding) error {
	projects, err := s.loadProjects()
	if err != nil {
		return err
	}
	max := s.cfg.MaxClaudeMDTokens
	for _, pr := range projects {
		path := filepath.Join(pr.path, "CLAUDE.md")
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			log.Printf("warn: sysscan lint: %s: %v", path, err)
			continue
		}
		if tokens := len(raw) / 4; tokens > max {
			byRule[RuleClaudeMDOversized] = append(byRule[RuleClaudeMDOversized], lintFinding{
				target:   "claude_md:" + path,
				severity: "warn",
				message: fmt.Sprintf("%s: ~%d tokens (len/4 estimate) exceeds the %d-token threshold — trim it, it is loaded into every session",
					path, tokens, max),
			})
		}
		if lines := countLines(raw); lines > MaxClaudeMDLines {
			byRule[RuleClaudeMDOver200Lines] = append(byRule[RuleClaudeMDOver200Lines], lintFinding{
				target:   "claude_md:" + path,
				severity: "warn",
				message: fmt.Sprintf("%s: %d lines, over the %d-line guideline — every line is loaded into every session's first request; move detail into skills or .claude/rules",
					path, lines, MaxClaudeMDLines),
			})
		}
	}
	return nil
}

// countLines counts text lines: newline-terminated ones plus a final
// unterminated one.
func countLines(raw []byte) int {
	n := bytes.Count(raw, []byte{'\n'})
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		n++
	}
	return n
}

// lintHookTimeouts flags enabled hook entries that have neither a "timeout"
// nor a trailing '|| true' escape hatch — a hanging command stalls Claude Code.
func (s *Scanner) lintHookTimeouts(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(
		`SELECT id, event, command, source_file FROM hooks
		 WHERE enabled = 1 AND timeout IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var event, command, sourceFile string
		if err := rows.Scan(&id, &event, &command, &sourceFile); err != nil {
			return err
		}
		if strings.Contains(command, "|| true") {
			continue
		}
		byRule[RuleHookNoTimeout] = append(byRule[RuleHookNoTimeout], lintFinding{
			target:   fmt.Sprintf("hook:%d", id),
			severity: "warn",
			message: fmt.Sprintf("%s: %s hook command has no timeout and no '|| true' guard — a hang can stall Claude Code",
				sourceFile, event),
		})
	}
	return rows.Err()
}

// lintDuplicateNames flags one agent name defined in BOTH global and project
// scope — the project copy silently overrides the global one. Plugin agents
// never count: their names are already "plugin:name" composites (format doc
// §7). The finding anchors on the lowest involved agent id so its lifecycle
// target stays stable across rescans.
func (s *Scanner) lintDuplicateNames(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(
		`SELECT name, MIN(id) FROM agents
		 WHERE deleted = 0 AND origin <> 'plugin'
		 GROUP BY name HAVING COUNT(DISTINCT scope) > 1 ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var minID int64
		if err := rows.Scan(&name, &minID); err != nil {
			return err
		}
		byRule[RuleAgentNameDuplicate] = append(byRule[RuleAgentNameDuplicate], lintFinding{
			target:   fmt.Sprintf("agent:%d", minID),
			severity: "warn",
			message: fmt.Sprintf("agent name %q is defined in both global and project scope — the project copy overrides the global one",
				name),
		})
	}
	return rows.Err()
}

// lintSkillAudit files the skill audit (skillaudit.go): agent_dead, skill_dead,
// skill_desc_similar and session_skill_load_high. The two threshold rules read
// guards.json (Config overrides win, for tests); when the guards document does
// not load — a broken SWARMERY_GUARDS override — they are SKIPPED this pass,
// logged and left as they were, never evaluated against a guessed default.
func (s *Scanner) lintSkillAudit(byRule map[string][]lintFinding, skipped map[string]bool) error {
	opts := AuditOptions{
		SimilarityThreshold: s.cfg.SkillSimilarityThreshold,
		SessionSkillLoadP95: s.cfg.SessionSkillLoadP95,
	}
	if opts.SimilarityThreshold <= 0 || opts.SessionSkillLoadP95 <= 0 {
		g, err := guards.Load()
		if err != nil {
			log.Printf("warn: sysscan lint: %v — %s and %s are not evaluated this pass",
				err, RuleSkillDescSimilar, RuleSessionSkillLoadHigh)
		} else {
			if opts.SimilarityThreshold <= 0 {
				opts.SimilarityThreshold = g.SkillSimilarityThreshold
			}
			if opts.SessionSkillLoadP95 <= 0 {
				opts.SessionSkillLoadP95 = g.SessionSkillLoadP95
			}
		}
	}
	rep, err := ComputeSkillAudit(s.db, opts)
	if err != nil {
		return err
	}
	found := auditFindings(rep)
	for _, rule := range []string{RuleSkillDescSimilar, RuleSessionSkillLoadHigh} {
		if _, ok := found[rule]; !ok {
			skipped[rule] = true
		}
	}
	for rule, lf := range found {
		byRule[rule] = append(byRule[rule], lf...)
	}
	return nil
}

// lintUnusedMCPServers flags every MCP server a config FILE declares for a
// live project (mcpcfg.ReadConfigured — user, local and project scope; never
// `claude mcp list`, which would spawn each server to health-check it) that has
// no mcp__<server>__* tool call in the last 30 days. A user-scope server is
// machine-wide, so a call from any project counts as use and its finding is
// reported once; a local/project-scope server only counts calls from its own
// project's sessions. Info, like agent_dead: "unused by available telemetry".
//
// Tool names are matched by exact prefix in Go, not with LIKE: `_` is a LIKE
// wildcard and every MCP tool name is full of them.
func (s *Scanner) lintUnusedMCPServers(byRule map[string][]lintFinding) error {
	projects, err := s.loadProjects()
	if err != nil {
		return err
	}
	type call struct {
		tool      string
		projectID int64
	}
	var calls []call
	rows, err := s.db.Query(
		`SELECT DISTINCT e.tool_name, s.project_id
		 FROM events e JOIN sessions s ON s.id = e.session_id
		 WHERE e.ts > date('now','-30 day') AND e.tool_name LIKE 'mcp\_\_%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c call
		if err := rows.Scan(&c.tool, &c.projectID); err != nil {
			rows.Close()
			return err
		}
		calls = append(calls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	used := func(server string, projectID int64, anyProject bool) bool {
		prefix := mcpcfg.ToolPrefix(server)
		for _, c := range calls {
			if strings.HasPrefix(c.tool, prefix) && (anyProject || c.projectID == projectID) {
				return true
			}
		}
		return false
	}

	seen := map[string]bool{}
	report := func(target string, srv mcpcfg.ConfiguredServer, where string) {
		if seen[target] {
			return
		}
		seen[target] = true
		byRule[RuleMCPServerUnused30d] = append(byRule[RuleMCPServerUnused30d], lintFinding{
			target:   target,
			severity: "info",
			message: fmt.Sprintf("MCP server %q (%s scope, %s): no %s* tool call in the last 30 days by available telemetry — its tools are still offered to every session; consider removing it",
				srv.Name, srv.Scope, where, mcpcfg.ToolPrefix(srv.Name)),
		})
	}
	// The user tier is read even with no project rows: it is configured
	// machine-wide whether or not any project has been indexed yet.
	for _, srv := range mcpcfg.ReadConfigured(s.cfg.ClaudeDir, "") {
		if srv.Scope == mcpcfg.ScopeUser && !used(srv.Name, 0, true) {
			report("mcp:user:"+srv.Name, srv, srv.Source)
		}
	}
	for _, pr := range projects {
		for _, srv := range mcpcfg.ReadConfigured(s.cfg.ClaudeDir, pr.path) {
			if srv.Scope == mcpcfg.ScopeUser || used(srv.Name, pr.id, false) {
				continue
			}
			report(fmt.Sprintf("mcp:%s:%s|%s", srv.Scope, srv.Name, pr.path), srv, pr.path)
		}
	}
	return nil
}

// docsKinds are the item kinds the usage-guide rules cover. Agents and skills
// are versioned, so their content comes from the CURRENT stored version like
// every other content rule; commands are NOT versioned (no content column,
// registry.go), so their file is read from disk in the same pass.
var docsKinds = []struct {
	kind     string // lint target prefix: agent | skill
	table    string
	verTable string
	pathCol  string
}{
	{kind: "agent", table: "agents", verTable: "agent_versions", pathCol: "file_path"},
	{kind: "skill", table: "skills", verTable: "skill_versions", pathCol: "dir_path"},
}

// lintDocs covers the five usage-guide rules (docs/system-docs-format.md) over
// every live agent, skill and command.
//
// Each sub-pass runs its query to completion and closes it BEFORE the next one
// starts: the store caps the pool at a single connection (store.go), so holding
// one cursor open across another Query is a permanent deadlock, not a slow path.
func (s *Scanner) lintDocs(byRule map[string][]lintFinding) error {
	for _, k := range docsKinds {
		if err := s.lintDocsVersioned(byRule, k.kind, k.table, k.verTable, k.pathCol); err != nil {
			return err
		}
	}
	return s.lintDocsCommands(byRule)
}

// lintDocsVersioned evaluates one versioned kind from its stored content — the
// linter never re-reads agent/skill files from disk (same rule as
// lintAgentContent). Items whose frontmatter does not parse are skipped: the
// scanner's parse_error finding owns those.
func (s *Scanner) lintDocsVersioned(byRule map[string][]lintFinding, kind, table, verTable, pathCol string) error {
	rows, err := s.db.Query(
		`SELECT t.id, t.` + pathCol + `, COALESCE(v.content, '')
		 FROM ` + table + ` t LEFT JOIN ` + verTable + ` v ON v.id = t.current_version_id
		 WHERE t.deleted = 0 ORDER BY t.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var path, content string
		if err := rows.Scan(&id, &path, &content); err != nil {
			return err
		}
		if _, _, err := splitFrontmatter([]byte(content)); err != nil {
			continue // unparseable — the parse_error finding owns this item
		}
		s.docsFindings(byRule, fmt.Sprintf("%s:%d", kind, id), path, content)
	}
	return rows.Err()
}

// lintDocsCommands evaluates commands from disk. Tolerant exactly like
// lintClaudeMD: an unreadable file warns and is skipped, never aborts the pass —
// a command whose file vanished between the scan and the lint is not a docs
// violation, it is a race the next scan resolves.
func (s *Scanner) lintDocsCommands(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(`SELECT id, file_path FROM commands WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		return err
	}
	type cmd struct {
		id   int64
		path string
	}
	var cmds []cmd
	for rows.Next() {
		var c cmd
		if err := rows.Scan(&c.id, &c.path); err != nil {
			rows.Close()
			return err
		}
		cmds = append(cmds, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Disk IO happens only after the cursor is closed — see lintDocs.
	for _, c := range cmds {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			log.Printf("warn: sysscan lint: %s: %v", c.path, err)
			continue
		}
		if _, _, err := splitFrontmatter(raw); err != nil {
			continue // unparseable — the parse_error finding owns this item
		}
		s.docsFindings(byRule, fmt.Sprintf("command:%d", c.id), c.path, string(raw))
	}
	return nil
}

// docsFindings turns ONE parsed guide into its findings. Pure over the DB (no
// queries), so it is safe to call from inside a cursor loop.
//
// An absent guide reports docs_missing and STOPS: an item with no `# How to
// use` block is also, trivially, incomplete, unreviewed and of unknown
// staleness, and reporting all four would bury the one fact that matters — the
// same reason ParseDocs leaves Missing empty when Present is false (docs.go).
func (s *Scanner) docsFindings(byRule map[string][]lintFinding, target, path, content string) {
	d := ParseDocs([]byte(content), s.cfg.MinDocsSection)

	if !d.Present {
		msg := fmt.Sprintf("%s: no `# How to use` section — the dashboard shows this item with no usage guide", path)
		if d.Status != "" || d.SourceSHA != "" {
			// §3: provenance recorded for a guide that does not exist.
			msg = fmt.Sprintf("%s: frontmatter records `docs:` provenance but the file has no `# How to use` section", path)
		}
		byRule[RuleDocsMissing] = append(byRule[RuleDocsMissing], lintFinding{
			target: target, severity: "warn", message: msg,
		})
		return
	}

	if len(d.Missing) > 0 {
		byRule[RuleDocsIncomplete] = append(byRule[RuleDocsIncomplete], lintFinding{
			target:   target,
			severity: "warn",
			message: fmt.Sprintf("%s: `# How to use` is %s%s (each required subsection needs %d+ runes of body)",
				path, DocsMissingSectionsMarker, strings.Join(d.Missing, ", "), s.cfg.MinDocsSection),
		})
	}
	if d.Duplicate {
		byRule[RuleDocsDuplicate] = append(byRule[RuleDocsDuplicate], lintFinding{
			target:   target,
			severity: "warn",
			message: fmt.Sprintf("%s: more than one `# How to use` section — only the first is read, the rest is ordinary body text",
				path),
		})
	}
	if d.Stale {
		byRule[RuleDocsStale] = append(byRule[RuleDocsStale], lintFinding{
			target:   target,
			severity: "info",
			message: fmt.Sprintf("%s: the item changed since its guide was written (docs.source_sha %s, body is now %s)",
				path, d.SourceSHA, d.ComputedSHA),
		})
	}
	if d.Status != docsStatusReviewed {
		status := d.Status
		if status == "" {
			status = "unset"
		}
		byRule[RuleDocsUnreviewed] = append(byRule[RuleDocsUnreviewed], lintFinding{
			target:   target,
			severity: "info",
			message:  fmt.Sprintf("%s: docs.status is %q — this usage guide has not been reviewed", path, status),
		})
	}
}
