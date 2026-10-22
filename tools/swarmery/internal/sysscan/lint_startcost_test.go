package sysscan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLintClaudeMDOver200Lines: the line rule fires above 200 lines of the
// project CLAUDE.md and is silent at exactly 200 (and on the 122-line fixture).
func TestLintClaudeMDOver200Lines(t *testing.T) {
	db, cfg, root := setup(t)
	s := New(db, cfg, nil)
	path := filepath.Join(root, "project", "CLAUDE.md")
	target := "claude_md:" + path

	if _, err := s.Lint(); err != nil {
		t.Fatalf("lint: %v", err)
	}
	if n := activeCount(t, db, target, RuleClaudeMDOver200Lines); n != 0 {
		t.Fatalf("122-line fixture flagged by %s", RuleClaudeMDOver200Lines)
	}

	for _, tc := range []struct {
		lines int
		want  int
		tail  string
	}{
		{lines: 200, want: 0, tail: "\n"},
		{lines: 201, want: 1, tail: "\n"},
		{lines: 201, want: 1, tail: ""}, // an unterminated last line still counts
	} {
		body := strings.Repeat("x\n", tc.lines-1) + "x" + tc.tail
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := s.Lint()
		if err != nil {
			t.Fatalf("lint: %v", err)
		}
		if n := count(t, db,
			`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND severity = 'warn' AND resolved_at IS NULL`,
			target, RuleClaudeMDOver200Lines); n != tc.want {
			t.Errorf("%d lines (tail %q): active %s = %d, want %d", tc.lines, tc.tail, RuleClaudeMDOver200Lines, n, tc.want)
		}
		if st.PerRule[RuleClaudeMDOver200Lines] != tc.want {
			t.Errorf("%d lines: LintStats = %d, want %d", tc.lines, st.PerRule[RuleClaudeMDOver200Lines], tc.want)
		}
	}
}

// TestLintMCPServerUnused30d: a configured server with a recent mcp__<server>__*
// call stays silent; one without gets an info finding. User-scope servers count
// a call from any project; local/project-scope ones only their own project's.
func TestLintMCPServerUnused30d(t *testing.T) {
	db, cfg, root := setup(t)
	projectPath := filepath.Join(root, "project")
	pid := intQuery(t, db, `SELECT id FROM projects WHERE slug = 'project'`)
	mustExec(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES ('/elsewhere', 'elsewhere', 'elsewhere', '2026-07-13T00:00:00Z')`)
	other := intQuery(t, db, `SELECT id FROM projects WHERE slug = 'elsewhere'`)

	// cfg.ClaudeDir is <root>/claude, so the global config is <root>/.claude.json.
	writeTestFile(t, filepath.Join(root, ".claude.json"), `{
	  "mcpServers": {"used-user": {}, "idle-user": {}},
	  "projects": {"`+projectPath+`": {"mcpServers": {"used-local": {}, "idle-local": {}, "elsewhere-local": {}}}}
	}`)
	writeTestFile(t, filepath.Join(projectPath, ".mcp.json"), `{"mcpServers": {"Repo Tools": {}, "idle-json": {}}}`)

	now := time.Now().UTC()
	recent := now.Add(-time.Hour).Format(time.RFC3339Nano)
	stale := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	session := func(projectID int64, uuid string) int64 {
		mustExec(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, ?, ?)`, projectID, uuid, recent)
		return intQuery(t, db, `SELECT id FROM sessions WHERE session_uuid = ?`, uuid)
	}
	own, foreign := session(pid, "mcp-own"), session(other, "mcp-foreign")
	for _, e := range []struct {
		sid  int64
		ts   string
		tool string
	}{
		{foreign, recent, "mcp__used-user__go"},       // user scope: any project counts
		{own, recent, "mcp__used-local__go"},          // local scope, own project
		{own, recent, "mcp__Repo_Tools__go"},          // name folded the way Claude Code folds it
		{foreign, recent, "mcp__elsewhere-local__go"}, // local scope, WRONG project → still unused
		{own, stale, "mcp__idle-local__go"},           // outside the window
		{own, recent, "mcp__idle-json-v2__go"},        // a longer name is not this server
		{own, recent, "mcpXXidle-userXXgo"},           // LIKE wildcards would match this
	} {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name) VALUES (?, ?, 'tool_call', ?)`, e.sid, e.ts, e.tool)
	}

	st, err := New(db, cfg, nil).Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	wantFlagged := []string{
		"mcp:user:idle-user",
		"mcp:local:idle-local|" + projectPath,
		"mcp:local:elsewhere-local|" + projectPath,
		"mcp:project:idle-json|" + projectPath,
	}
	for _, target := range wantFlagged {
		if n := count(t, db,
			`SELECT COUNT(*) FROM config_lint_findings WHERE target = ? AND rule = ? AND severity = 'info' AND resolved_at IS NULL`,
			target, RuleMCPServerUnused30d); n != 1 {
			t.Errorf("%s: active info findings = %d, want 1", target, n)
		}
	}
	if got := count(t, db, `SELECT COUNT(*) FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`,
		RuleMCPServerUnused30d); got != len(wantFlagged) {
		t.Errorf("active %s = %d, want %d (used servers must stay silent)", RuleMCPServerUnused30d, got, len(wantFlagged))
	}
	if st.PerRule[RuleMCPServerUnused30d] != len(wantFlagged) {
		t.Errorf("LintStats = %d, want %d", st.PerRule[RuleMCPServerUnused30d], len(wantFlagged))
	}

	// The server gets used → its finding resolves on the next pass.
	mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name) VALUES (?, ?, 'tool_call', 'mcp__idle-user__go')`, own, recent)
	if _, err := New(db, cfg, nil).Lint(); err != nil {
		t.Fatalf("re-lint: %v", err)
	}
	if n := activeCount(t, db, "mcp:user:idle-user", RuleMCPServerUnused30d); n != 0 {
		t.Errorf("idle-user still flagged after a recent call")
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
