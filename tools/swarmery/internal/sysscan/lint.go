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
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Linter-owned rule names (design §3.5). parse_error is scanner-owned.
const (
	RuleAgentNoBoundaries  = "agent_no_boundaries"     // warn: agent body without a Boundaries section
	RuleAgentNoDescription = "agent_no_description"    // warn: empty description in agent frontmatter
	RuleSkillShortDesc     = "skill_short_description" // warn: skill description below the min length — poor trigger recall
	RuleClaudeMDOversized  = "claude_md_oversized"     // warn: project CLAUDE.md above the token estimate threshold
	RuleHookNoTimeout      = "hook_no_timeout"         // warn: hook command with neither timeout nor '|| true' — can stall Claude Code
	RuleAgentNameDuplicate = "agent_name_duplicate"    // warn: one name in global AND project scope — override confusion
	RuleAgentDead          = "agent_dead"              // info: 0 event mentions in 30 days (advisory — sparse events.agent_id)
)

// linterRules is the full owned-rule set — every pass syncs each rule, so a
// rule with zero findings this pass resolves all of its previously active rows.
var linterRules = []string{
	RuleAgentNoBoundaries,
	RuleAgentNoDescription,
	RuleSkillShortDesc,
	RuleClaudeMDOversized,
	RuleHookNoTimeout,
	RuleAgentNameDuplicate,
	RuleAgentDead,
}

// Threshold defaults and their env overrides (precedence: explicit Config
// value > env > default — resolved in Config.withDefaults).
const (
	DefaultMinSkillDescription = 40   // runes
	DefaultMaxClaudeMDTokens   = 2500 // estimated tokens (len/4)

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
	if err := s.lintDeadAgents(byRule); err != nil {
		return st, fmt.Errorf("lint dead agents: %w", err)
	}

	for _, rule := range linterRules {
		findings := byRule[rule]
		keep := make(map[string]bool, len(findings))
		for _, f := range findings {
			if err := s.upsertFinding(f.target, rule, f.severity, f.message); err != nil {
				return st, fmt.Errorf("lint %s %s: %w", rule, f.target, err)
			}
			keep[f.target] = true
		}
		st.PerRule[rule] = len(findings)
		resolved, err := s.resolveStaleFindings(rule, keep)
		if err != nil {
			return st, fmt.Errorf("lint %s resolve: %w", rule, err)
		}
		st.Resolved += resolved
	}
	return st, nil
}

// resolveStaleFindings closes every active finding of one rule whose target
// was not re-detected this pass.
func (s *Scanner) resolveStaleFindings(rule string, keep map[string]bool) (int, error) {
	rows, err := s.db.Query(
		`SELECT id, target FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`, rule)
	if err != nil {
		return 0, err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var target string
		if err := rows.Scan(&id, &target); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[target] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := nowRFC3339()
	for _, id := range stale {
		if _, err := s.db.Exec(
			`UPDATE config_lint_findings SET resolved_at = ? WHERE id = ?`, now, id); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
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

// lintClaudeMD flags each project whose CLAUDE.md exceeds MaxClaudeMDTokens.
// The token estimate is deliberately crude — len(bytes)/4; the precise
// context-waste detector is design §5.2 and is NOT built here.
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
	}
	return nil
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

// lintDeadAgents flags agents with zero event mentions in the last 30 days.
// Severity is info, not warn: events.agent_id is only partially attributed
// (00-plan risk #4), so "dead by available telemetry" is advisory — the join
// is the plain agent_id FK, no extra attribution heuristics.
func (s *Scanner) lintDeadAgents(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(
		`SELECT a.id, a.name FROM agents a
		 LEFT JOIN events e ON e.agent_id = a.id AND e.ts > date('now','-30 day')
		 WHERE a.deleted = 0
		 GROUP BY a.id, a.name HAVING COUNT(e.id) = 0 ORDER BY a.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		byRule[RuleAgentDead] = append(byRule[RuleAgentDead], lintFinding{
			target:   fmt.Sprintf("agent:%d", id),
			severity: "info",
			message: fmt.Sprintf("agent %q: 0 event mentions in the last 30 days by available telemetry (events.agent_id is only partially attributed)",
				name),
		})
	}
	return rows.Err()
}
