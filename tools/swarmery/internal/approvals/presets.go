package approvals

// Permission presets (fusion phase 11 — DESIGN.md §2 item 11): a human-readable
// policy layer COMPILED into the low-level approval_rules (rules.go). One preset
// per project + per-category overrides → a managed set of auto-approve rules
// (source='preset'). Hand-written manual rules (source='manual') are never
// touched by Compile.
//
// SECURITY — fail CLOSED (least privilege):
//   - The default preset is 'approval-required' and a project with NO preset row
//     is treated identically (EffectivePolicy returns the default). An absent /
//     unknown / misconfigured preset therefore compiles NO auto-approve rules —
//     it can never silently widen access.
//   - Compile only ever deletes+inserts source='preset' rows for ITS project id.
//   - 'unrestricted' auto-approves every category whose effective policy is
//     'allow'; the default allow-set is every category EXCEPT git_push, and
//     command_exec (the broadest, Bash(*)) is always compiled LAST so a narrower
//     rule is evaluated first.
//   - Escalating to 'unrestricted', or overriding command_exec/git_push to
//     'allow', is a privileged move gated behind an explicit confirm (R13).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Preset values (mirrors the CHECK in migration 0029).
const (
	PresetUnrestricted      = "unrestricted"
	PresetApprovalRequired  = "approval-required"
	PresetLockedDown        = "locked-down"
	DefaultPreset           = PresetApprovalRequired // fail-closed default
)

// Per-category override policy. Two states only — "block" is out of scope (the
// hook protocol has no auto-deny; honesty over parity).
const (
	PolicyAllow = "allow" // compile auto-approve rules for this category
	PolicyAsk   = "ask"   // no rules — requests queue for a human
)

// ruleSourcePreset marks managed rows written by Compile (vs 'manual').
const ruleSourcePreset = "preset"

// categoryPatterns maps a permission category to its Claude Code tool patterns
// (the single source of truth; rules.go parses each pattern). Order within a
// category is preserved on compile, and categories are compiled in
// categoryOrder so command_exec (broadest) lands LAST.
var categoryPatterns = map[string][]string{
	"read_only":    {"Read", "Grep", "Glob", "Bash(git status*)", "Bash(git log*)", "Bash(git diff*)"},
	"file_write":   {"Edit", "Write", "NotebookEdit"},
	"git_write":    {"Bash(git add*)", "Bash(git commit*)", "Bash(git checkout*)", "Bash(git worktree*)"},
	"git_push":     {"Bash(git push*)", "Bash(gh *)"}, // never auto-approved by unrestricted default
	"command_exec": {"Bash(*)"},                       // broadest — ordered LAST at compile
	"network":      {"WebFetch", "WebSearch"},
}

// categoryOrder is the deterministic compile order. command_exec is LAST so its
// broad Bash(*) rule is evaluated after any narrower category rule (matchRuleLocked
// walks rules by id ascending, i.e. insert order).
var categoryOrder = []string{"read_only", "file_write", "git_write", "git_push", "network", "command_exec"}

// unrestrictedAllowDefault is the baseline allow-set for the 'unrestricted'
// preset: every category EXCEPT git_push (pushing / gh must be an explicit
// opt-in). approval-required and locked-down compile NOTHING regardless.
var unrestrictedAllowDefault = map[string]bool{
	"read_only":    true,
	"file_write":   true,
	"git_write":    true,
	"git_push":     false,
	"command_exec": true,
	"network":      true,
}

// escalationCategories are the categories whose promotion to 'allow' is a
// privileged move requiring explicit confirm (R13).
var escalationCategories = map[string]bool{
	"command_exec": true,
	"git_push":     true,
}

// KnownCategory reports whether name is a compilable category.
func KnownCategory(name string) bool {
	_, ok := categoryPatterns[name]
	return ok
}

// KnownPreset reports whether p is a valid preset value.
func KnownPreset(p string) bool {
	return p == PresetUnrestricted || p == PresetApprovalRequired || p == PresetLockedDown
}

// ValidateOverrides checks an overrides map: every key must be a known category
// and every value must be "allow" or "ask". Returns a client-facing message on
// the first problem, "" when clean.
func ValidateOverrides(overrides map[string]string) string {
	for cat, pol := range overrides {
		if !KnownCategory(cat) {
			return fmt.Sprintf("unknown category %q", cat)
		}
		if pol != PolicyAllow && pol != PolicyAsk {
			return fmt.Sprintf("category %q policy must be %q or %q, got %q", cat, PolicyAllow, PolicyAsk, pol)
		}
	}
	return ""
}

// effectivePolicyFor computes the effective allow/ask policy for one category
// under a preset + overrides. approval-required and locked-down are always
// 'ask' (no managed rules); unrestricted starts from the default allow-set and
// applies any explicit override.
func effectivePolicyFor(preset, category string, overrides map[string]string) string {
	if preset != PresetUnrestricted {
		return PolicyAsk
	}
	if pol, ok := overrides[category]; ok {
		return pol
	}
	if unrestrictedAllowDefault[category] {
		return PolicyAllow
	}
	return PolicyAsk
}

// CategoryPolicy is one row of EffectivePolicy: the category, its patterns, and
// the resolved allow/ask policy under the current preset+overrides.
type CategoryPolicy struct {
	Category string   `json:"category"`
	Patterns []string `json:"patterns"`
	Policy   string   `json:"policy"` // allow | ask
}

// PolicyView is the display model returned by EffectivePolicy — the stored
// preset, overrides, whether the project is locked down, and the per-category
// resolution (in categoryOrder).
type PolicyView struct {
	ProjectID  int64             `json:"projectId"`
	Preset     string            `json:"preset"`
	Overrides  map[string]string `json:"overrides"`
	LockedDown bool              `json:"lockedDown"`
	Categories []CategoryPolicy  `json:"categories"`
}

// GetPreset reads a project's stored preset + overrides, returning the
// fail-closed default (approval-required, no overrides) when no row exists.
func GetPreset(db *sql.DB, projectID int64) (preset string, overrides map[string]string, err error) {
	var overridesJSON string
	row := db.QueryRow(
		`SELECT preset, overrides FROM project_permission_presets WHERE project_id = ?`, projectID)
	switch err = row.Scan(&preset, &overridesJSON); {
	case err == sql.ErrNoRows:
		return DefaultPreset, map[string]string{}, nil
	case err != nil:
		return "", nil, err
	}
	overrides = map[string]string{}
	if overridesJSON != "" {
		if err := json.Unmarshal([]byte(overridesJSON), &overrides); err != nil {
			// A corrupt overrides blob must not fail OPEN — degrade to no
			// overrides (fail closed) rather than surfacing a wider policy.
			overrides = map[string]string{}
		}
	}
	return preset, overrides, nil
}

// EffectivePolicy builds the display model for a project (fail-closed default
// when unset).
func EffectivePolicy(db *sql.DB, projectID int64) (PolicyView, error) {
	preset, overrides, err := GetPreset(db, projectID)
	if err != nil {
		return PolicyView{}, err
	}
	view := PolicyView{
		ProjectID:  projectID,
		Preset:     preset,
		Overrides:  overrides,
		LockedDown: preset == PresetLockedDown,
		Categories: make([]CategoryPolicy, 0, len(categoryOrder)),
	}
	for _, cat := range categoryOrder {
		view.Categories = append(view.Categories, CategoryPolicy{
			Category: cat,
			Patterns: categoryPatterns[cat],
			Policy:   effectivePolicyFor(preset, cat, overrides),
		})
	}
	return view, nil
}

// Escalations returns the escalation reasons for a proposed preset+overrides —
// non-empty means the write is privileged and requires explicit confirm (R13):
//   - switching TO unrestricted, and/or
//   - promoting command_exec / git_push to allow.
// Deterministically ordered for a stable UI payload.
func Escalations(preset string, overrides map[string]string) []string {
	var out []string
	if preset == PresetUnrestricted {
		out = append(out, "switch to unrestricted (auto-approves file writes, git, and shell by default)")
	}
	esc := make([]string, 0, len(escalationCategories))
	for cat := range escalationCategories {
		if effectivePolicyFor(preset, cat, overrides) == PolicyAllow {
			esc = append(esc, cat)
		}
	}
	sort.Strings(esc)
	for _, cat := range esc {
		out = append(out, fmt.Sprintf("auto-approve %s (%s)", cat, describeCategory(cat)))
	}
	return out
}

func describeCategory(cat string) string {
	switch cat {
	case "command_exec":
		return "any shell command"
	case "git_push":
		return "pushing to remotes and gh"
	default:
		return cat
	}
}

// compiledPatterns returns the ordered auto-approve patterns for a
// preset+overrides: category patterns concatenated in categoryOrder for every
// category whose effective policy is 'allow'. Empty for approval-required /
// locked-down (fail closed).
func compiledPatterns(preset string, overrides map[string]string) []string {
	if preset != PresetUnrestricted {
		return nil
	}
	var out []string
	for _, cat := range categoryOrder {
		if effectivePolicyFor(preset, cat, overrides) != PolicyAllow {
			continue
		}
		out = append(out, categoryPatterns[cat]...)
	}
	return out
}

// Compile replaces a project's managed (source='preset') auto-approve rules
// with the set implied by preset+overrides, and upserts the stored preset row —
// all in ONE transaction so a display read never sees a half-applied policy.
// Manual rules (source='manual') are untouched. Returns the number of preset
// rules written.
//
// Callers MUST have already gated escalations (Escalations + confirm, R13) and
// validated overrides (ValidateOverrides) — Compile trusts its inputs are safe.
func Compile(db *sql.DB, projectID int64, preset string, overrides map[string]string) (int, error) {
	if !KnownPreset(preset) {
		return 0, fmt.Errorf("unknown preset %q", preset)
	}
	if overrides == nil {
		overrides = map[string]string{}
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return 0, fmt.Errorf("marshal overrides: %w", err)
	}
	now := time.Now().UTC().Format(tsFormat)

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Upsert the stored preset (the audit + display source of truth).
	if _, err := tx.Exec(`
		INSERT INTO project_permission_presets (project_id, preset, overrides, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
		  preset = excluded.preset, overrides = excluded.overrides, updated_at = excluded.updated_at`,
		projectID, preset, string(overridesJSON), now); err != nil {
		return 0, fmt.Errorf("upsert preset: %w", err)
	}

	// Replace ONLY this project's managed rules — manual rules are invisible here.
	if _, err := tx.Exec(
		`DELETE FROM approval_rules WHERE project_id = ? AND source = ?`,
		projectID, ruleSourcePreset); err != nil {
		return 0, fmt.Errorf("clear preset rules: %w", err)
	}

	patterns := compiledPatterns(preset, overrides)
	note := "managed by preset " + preset
	written := 0
	for _, pattern := range patterns {
		// Defensive: every compiled pattern must parse under the rules matcher
		// (a typo in categoryPatterns would otherwise write a dead rule). Skip +
		// keep going rather than abort — but categoryPatterns is test-covered.
		if _, err := ParseRulePattern(pattern); err != nil {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO approval_rules (project_id, tool_pattern, action, enabled, source, note, created_at)
			VALUES (?, ?, 'approve', 1, ?, ?, ?)`,
			projectID, pattern, ruleSourcePreset, note, now); err != nil {
			return 0, fmt.Errorf("insert preset rule %q: %w", pattern, err)
		}
		written++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return written, nil
}

// IsLockedDown reports whether a project's preset is locked-down. Used by the
// dispatcher's admission gate. A missing row is NOT locked down (the default is
// approval-required) — but a locked-down project also has zero compiled
// auto-approve rules, so even a missed block cannot auto-approve anything
// (defense in depth).
func IsLockedDown(db *sql.DB, projectID int64) (bool, error) {
	var preset string
	err := db.QueryRow(
		`SELECT preset FROM project_permission_presets WHERE project_id = ?`, projectID).Scan(&preset)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return preset == PresetLockedDown, nil
}
