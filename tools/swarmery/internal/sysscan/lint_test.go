package sysscan

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// intQuery returns a single int64 result (ids, mins) — count() cousin.
func intQuery(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("query %s: %v", q, err)
	}
	return n
}

// activeCount counts unresolved findings for (target, rule).
func activeCount(t *testing.T, db *sql.DB, target, rule string) int {
	t.Helper()
	return count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		target, rule)
}

// markAlive inserts one freshly-timestamped event attributed to agentID so
// the agent_dead rule sees it as alive (plain agent_id join, no heuristics).
func markAlive(t *testing.T, db *sql.DB, agentID int64) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (1, 'lint-test-session', ?)`,
		time.Now().UTC().Format(time.RFC3339))
	sessionID := intQuery(t, db, `SELECT id FROM sessions WHERE session_uuid = 'lint-test-session'`)
	mustExec(t, db,
		`INSERT INTO events (session_id, ts, type, agent_id) VALUES (?, ?, 'subagent_start', ?)`,
		sessionID, time.Now().UTC().Format(time.RFC3339), agentID)
}

// TestLintRulesFireOnFixtures drives all 7 linter rules over the fixture
// tree: each rule fires on exactly its planted fixture and stays quiet on the
// clean ones.
func TestLintRulesFireOnFixtures(t *testing.T) {
	db, cfg, root := setup(t)
	s := New(db, cfg, nil)
	if _, err := s.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// global-agent gets recent telemetry — every other agent is "dead".
	aliveID := intQuery(t, db, `SELECT id FROM agents WHERE name = 'global-agent'`)
	markAlive(t, db, aliveID)

	st, err := s.Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	lintPoor := intQuery(t, db, `SELECT id FROM agents WHERE name = 'lint-poor'`)
	shortSkill := intQuery(t, db, `SELECT id FROM skills WHERE name = 'short-desc'`)
	slowHook := intQuery(t, db, `SELECT id FROM hooks WHERE event = 'PostToolUse'`)
	dupTarget := intQuery(t, db, `SELECT MIN(id) FROM agents WHERE name = 'x' AND origin <> 'plugin'`)
	claudeMD := filepath.Join(root, "project", "CLAUDE.md")

	tests := []struct {
		rule     string
		target   string
		severity string
		total    int // active findings for the rule across the whole pass
	}{
		{RuleAgentNoBoundaries, fmt.Sprintf("agent:%d", lintPoor), "warn", 1},
		{RuleAgentNoDescription, fmt.Sprintf("agent:%d", lintPoor), "warn", 1},
		{RuleSkillShortDesc, fmt.Sprintf("skill:%d", shortSkill), "warn", 1},
		{RuleClaudeMDOversized, "claude_md:" + claudeMD, "warn", 1},
		{RuleHookNoTimeout, fmt.Sprintf("hook:%d", slowHook), "warn", 1},
		{RuleAgentNameDuplicate, fmt.Sprintf("agent:%d", dupTarget), "warn", 1},
		// 8 live agents, 1 marked alive → 7 dead by available telemetry.
		{RuleAgentDead, fmt.Sprintf("agent:%d", lintPoor), "info", 7},
	}
	for _, tc := range tests {
		t.Run(tc.rule, func(t *testing.T) {
			if n := count(t, db,
				`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND severity = ? AND resolved_at IS NULL`,
				tc.target, tc.rule, tc.severity); n != 1 {
				t.Errorf("%s on %s: active findings = %d, want 1", tc.rule, tc.target, n)
			}
			if n := count(t, db,
				`SELECT COUNT(*) FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`,
				tc.rule); n != tc.total {
				t.Errorf("%s total active = %d, want %d", tc.rule, n, tc.total)
			}
			if st.PerRule[tc.rule] != tc.total {
				t.Errorf("LintStats[%s] = %d, want %d", tc.rule, st.PerRule[tc.rule], tc.total)
			}
		})
	}

	// Clean fixtures stay quiet.
	cleanAgent := intQuery(t, db, `SELECT id FROM agents WHERE name = 'global-agent'`)
	for _, rule := range []string{RuleAgentNoBoundaries, RuleAgentNoDescription, RuleAgentDead} {
		if n := activeCount(t, db, fmt.Sprintf("agent:%d", cleanAgent), rule); n != 0 {
			t.Errorf("clean global-agent flagged by %s", rule)
		}
	}
	// broken-agent is owned by the scanner's parse_error — the content rules
	// must skip it, and the linter must not touch the parse_error row itself.
	broken := intQuery(t, db, `SELECT id FROM agents WHERE name = 'broken-agent'`)
	for _, rule := range []string{RuleAgentNoBoundaries, RuleAgentNoDescription} {
		if n := activeCount(t, db, fmt.Sprintf("agent:%d", broken), rule); n != 0 {
			t.Errorf("unparseable broken-agent flagged by %s (parse_error owns it)", rule)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE rule = 'parse_error'`); n != 1 {
		t.Errorf("parse_error rows = %d, want 1 (linter must not touch the scanner's rule)", n)
	}
	cleanSkill := intQuery(t, db, `SELECT id FROM skills WHERE name = 'global-skill'`)
	if n := activeCount(t, db, fmt.Sprintf("skill:%d", cleanSkill), RuleSkillShortDesc); n != 0 {
		t.Errorf("clean global-skill flagged by %s", RuleSkillShortDesc)
	}
}

// TestLintLifecycle exercises the (target, rule) lifecycle: no duplicate
// actives on re-lint, resolved_at on fix, and a NEW history row on relapse.
func TestLintLifecycle(t *testing.T) {
	db, cfg, root := setup(t)
	s := New(db, cfg, nil)
	if _, err := s.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := s.Lint(); err != nil {
		t.Fatalf("lint: %v", err)
	}

	lintPoor := fmt.Sprintf("agent:%d", intQuery(t, db, `SELECT id FROM agents WHERE name = 'lint-poor'`))

	// Re-lint without changes: zero duplicate active (target, rule) pairs.
	if _, err := s.Lint(); err != nil {
		t.Fatalf("re-lint: %v", err)
	}
	if n := count(t, db,
		`SELECT COUNT(*) FROM (SELECT target, rule FROM config_lint_findings
		  WHERE resolved_at IS NULL GROUP BY target, rule HAVING COUNT(*) > 1)`); n != 0 {
		t.Fatalf("%d duplicate active (target, rule) pairs after double lint, want 0", n)
	}
	if n := activeCount(t, db, lintPoor, RuleAgentNoBoundaries); n != 1 {
		t.Fatalf("active agent_no_boundaries after double lint = %d, want 1", n)
	}

	// Fix the fixture: description added, Boundaries section added.
	agentPath := filepath.Join(root, "claude", "agents", "lint-poor.md")
	fixed := `---
name: lint-poor
description: Now with a description — the lint fixture got fixed.
model: claude-haiku-4-5
---

# lint-poor

Fixture body.

## Boundaries

- Now bounded.
`
	if err := os.WriteFile(agentPath, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	ls, err := s.Lint()
	if err != nil {
		t.Fatalf("lint after fix: %v", err)
	}
	if ls.Resolved != 2 {
		t.Errorf("Resolved after fix = %d, want 2 (boundaries + description)", ls.Resolved)
	}
	for _, rule := range []string{RuleAgentNoBoundaries, RuleAgentNoDescription} {
		if n := activeCount(t, db, lintPoor, rule); n != 0 {
			t.Errorf("%s still active after the fixture was fixed", rule)
		}
		if n := count(t, db,
			`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NOT NULL`,
			lintPoor, rule); n != 1 {
			t.Errorf("%s resolved rows = %d, want 1", rule, n)
		}
	}

	// Relapse (Boundaries removed again, description kept): a NEW row opens,
	// the resolved one stays — history is never rewritten.
	relapsed := `---
name: lint-poor
description: Now with a description — the lint fixture got fixed.
model: claude-haiku-4-5
---

# lint-poor

Fixture body without the section again.
`
	if err := os.WriteFile(agentPath, []byte(relapsed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if _, err := s.Lint(); err != nil {
		t.Fatalf("lint after relapse: %v", err)
	}
	if n := activeCount(t, db, lintPoor, RuleAgentNoBoundaries); n != 1 {
		t.Errorf("active agent_no_boundaries after relapse = %d, want 1", n)
	}
	if n := count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ?`,
		lintPoor, RuleAgentNoBoundaries); n != 2 {
		t.Errorf("agent_no_boundaries history rows = %d, want 2 (resolved + active)", n)
	}
	// The description stayed fixed — no new row for it.
	if n := count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ?`,
		lintPoor, RuleAgentNoDescription); n != 1 {
		t.Errorf("agent_no_description history rows = %d, want 1 (resolved only)", n)
	}
}

// TestLintThresholdEnvOverride: precedence is explicit Config > env > default
// (SWARMERY_LINT_MIN_SKILL_DESC drives skill_short_description here).
func TestLintThresholdEnvOverride(t *testing.T) {
	t.Setenv(EnvMinSkillDescription, "5")

	db, cfg, _ := setup(t)
	if _, err := New(db, cfg, nil).Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Env wins over the 40-char default: "Too short." is 10 runes ≥ 5.
	if _, err := Lint(db, cfg); err != nil {
		t.Fatalf("lint: %v", err)
	}
	if n := count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`,
		RuleSkillShortDesc); n != 0 {
		t.Errorf("active skill_short_description with env min=5: %d, want 0", n)
	}

	// Explicit config wins over env: min=100 flags all three fixture skills.
	strict := cfg
	strict.MinSkillDescription = 100
	if _, err := Lint(db, strict); err != nil {
		t.Fatalf("strict lint: %v", err)
	}
	if n := count(t, db,
		`SELECT COUNT(*) FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`,
		RuleSkillShortDesc); n != 3 {
		t.Errorf("active skill_short_description with config min=100: %d, want 3", n)
	}
}
