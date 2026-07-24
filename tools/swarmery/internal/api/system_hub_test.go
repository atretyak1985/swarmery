package api

// fusion phase 18: System Hub — aggregation endpoints + the one write
// (template copy-to-project). These tests build a hermetic DB world plus an
// on-disk plugin-cache templates dir and a project .claude/templates dir, then
// exercise the summary counts, the skill usage rollup (statsSkills parity), the
// hook profile lint join, the command approximate flag, the template resolution
// badges, and the copy path (201 → 409 → traversal fail-closed).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// hubWorld seeds a project + a skill (with skill_use events) + a hook (with a
// lint finding) + a command (with a matching slash-prompt), and builds an
// on-disk plugin cache templates dir. projectRoot is the on-disk project path so
// the template copy has a real destination. Returns the server + project id +
// the resolved skill/hook/command ids + the claudeDir.
func hubWorld(t *testing.T, projectRoot string) (*httptest.Server, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	exec(`INSERT INTO projects (id, path, slug, name, first_seen)
	      VALUES (1, ?, 'alpha', 'Alpha', '2026-07-01T00:00:00Z')`, projectRoot)
	exec(`INSERT INTO sessions (id, project_id, session_uuid, title, status, started_at) VALUES
	      (1, 1, 'aaaaaaaa-0000-4000-8000-000000000001', 'Sess one', 'completed', '2026-07-10T10:00:00Z'),
	      (2, 1, 'bbbbbbbb-0000-4000-8000-000000000002', 'Sess two', 'completed', '2026-07-10T12:00:00Z')`)

	// A skill with 3 skill_use events across 2 sessions (one error), attributed
	// by payload.input.skill — the statsSkills grain (bare skill name, the way
	// skills are actually invoked; unlike subagent_type they carry no prefix).
	exec(`INSERT INTO skills (id, name, scope, project_id, dir_path, description,
	                          current_version_id, origin, plugin_name)
	      VALUES (1, 'deploy-helper', 'global', NULL, '/u/.claude/skills/deploy-helper',
	              'Deploys safely', NULL, 'local', NULL)`)
	exec(`INSERT INTO events (session_id, ts, type, status, payload) VALUES
	      (1, ?, 'skill_use', 'ok',    json_object('input', json_object('skill', 'deploy-helper'))),
	      (2, ?, 'skill_use', 'error', json_object('input', json_object('skill', 'deploy-helper'))),
	      (2, ?, 'skill_use', 'ok',    json_object('input', json_object('skill', 'deploy-helper')))`,
		sysTS(1), sysTS(2), sysTS(3))

	// A hook with a hook_no_timeout lint finding on it (target hook:1).
	exec(`INSERT INTO hooks (id, scope, project_id, event, matcher, command, timeout,
	                         status_message, source_file, seq, enabled, managed, content_hash)
	      VALUES (1, 'global', NULL, 'PreToolUse', 'Bash', 'echo hi', NULL,
	              NULL, '/u/.claude/settings.json', 0, 1, NULL, 'hook-hash')`)
	exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at, resolved_at)
	      VALUES ('hook:1', 'hook_no_timeout', 'warn', 'no timeout set', '2026-07-10T00:00:00Z', NULL)`)

	// A command + two user prompts that start with "/deploy" (approximate usage).
	exec(`INSERT INTO commands (id, name, scope, project_id, file_path, description,
	                            origin, plugin_name, content_hash)
	      VALUES (1, 'deploy', 'global', NULL, '/nonexistent/deploy.md', 'Deploy the app',
	              'local', NULL, 'cmd-hash')`)
	exec(`INSERT INTO events (session_id, ts, type, payload) VALUES
	      (1, ?, 'user_prompt', json_object('prompt', '/deploy staging')),
	      (1, ?, 'user_prompt', json_object('prompt', 'please /deploy')),
	      (2, ?, 'user_prompt', json_object('prompt', '/deploy'))`,
		sysTS(1), sysTS(2), sysTS(3))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, 1
}

// seedPluginTemplates builds <claudeDir>/plugins/cache/swarmery/core/2.2.0/templates
// with the given built-in template files and attaches the claudeDir to the
// System Hub. Restores the package var on cleanup.
func seedPluginTemplates(t *testing.T, claudeDir string, files map[string]string) {
	t.Helper()
	verDir := filepath.Join(claudeDir, "plugins", "cache", "swarmery", "core", "2.2.0")
	if err := os.MkdirAll(filepath.Join(verDir, ".in_use"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(verDir, "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := systemHubClaudeDir
	AttachSystemHubDir(claudeDir)
	t.Cleanup(func() { systemHubClaudeDir = prev })
}

func TestSystemHubSummary_Counts(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{
		"adr-template.md": "# ADR\n",
		"pr-template.md":  "# PR\n",
	})
	srv, _ := hubWorld(t, t.TempDir())

	var s map[string]any
	getJSON(t, srv.URL+"/api/system/hub/summary", &s)
	for field, want := range map[string]float64{
		"skills": 1, "hooks": 1, "commands": 1, "templates": 2, "lintFindings": 1,
	} {
		if got, _ := s[field].(float64); got != want {
			t.Errorf("summary.%s = %v, want %v", field, s[field], want)
		}
	}
}

func TestSkillHub_UsageRollupParity(t *testing.T) {
	srv, _ := hubWorld(t, t.TempDir())

	// The skill hub rollup must agree with /api/stats/skills for the same skill
	// over the same window: 3 invocations, 2 sessions, 1 error.
	var hub struct {
		Name  string `json:"name"`
		Usage struct {
			Invocations int64 `json:"invocations"`
			Sessions    int64 `json:"sessions"`
			Errors      int64 `json:"errors"`
			WindowDays  int   `json:"windowDays"`
		} `json:"usage"`
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/system/skills/1/hub", &hub)
	if hub.Name != "deploy-helper" {
		t.Fatalf("skill name = %q", hub.Name)
	}
	if hub.Usage.Invocations != 3 || hub.Usage.Sessions != 2 || hub.Usage.Errors != 1 {
		t.Errorf("rollup = %+v, want inv=3 sess=2 err=1", hub.Usage)
	}
	if hub.Usage.WindowDays != 30 {
		t.Errorf("windowDays = %d, want 30", hub.Usage.WindowDays)
	}
	if len(hub.Sessions) == 0 {
		t.Error("recent sessions must not be empty")
	}

	// Cross-check against the Analytics skills endpoint. The seeded skill_use
	// events are 1–3 days old, inside the statsSkills default 14-day window, so
	// its default range covers the same invocations the hub rollup counts.
	var stats struct {
		Skills []struct {
			Skill string `json:"skill"`
			Calls int64  `json:"calls"`
		} `json:"skills"`
	}
	getJSON(t, srv.URL+"/api/stats/skills", &stats)
	var statsCalls int64
	for _, s := range stats.Skills {
		if s.Skill == "deploy-helper" {
			statsCalls = s.Calls
		}
	}
	if statsCalls != hub.Usage.Invocations {
		t.Errorf("parity: stats calls=%d vs hub invocations=%d", statsCalls, hub.Usage.Invocations)
	}
}

func TestSkillHub_NotFound(t *testing.T) {
	srv, _ := hubWorld(t, t.TempDir())
	resp, err := http.Get(srv.URL + "/api/system/skills/999/hub")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown skill = %d, want 404", resp.StatusCode)
	}
}

func TestHookHub_LintJoinAndHonestNote(t *testing.T) {
	srv, _ := hubWorld(t, t.TempDir())
	var hub struct {
		Event           string `json:"event"`
		FiringTelemetry bool   `json:"firingTelemetry"`
		Lint            []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
		} `json:"lint"`
	}
	getJSON(t, srv.URL+"/api/system/hooks/1/hub", &hub)
	if hub.Event != "PreToolUse" {
		t.Errorf("event = %q", hub.Event)
	}
	// Honest note: firing telemetry is NOT tracked → the flag stays false (no
	// fabricated firing counts).
	if hub.FiringTelemetry {
		t.Error("firingTelemetry must be false — hook firings are not tracked")
	}
	if len(hub.Lint) != 1 || hub.Lint[0].Rule != "hook_no_timeout" {
		t.Fatalf("lint join = %+v, want one hook_no_timeout", hub.Lint)
	}
}

func TestCommandHub_ApproximateFlag(t *testing.T) {
	srv, _ := hubWorld(t, t.TempDir())
	var hub struct {
		Name  string `json:"name"`
		Usage struct {
			Invocations int64 `json:"invocations"`
			Approximate bool  `json:"approximate"`
		} `json:"usage"`
	}
	getJSON(t, srv.URL+"/api/system/commands/1/hub", &hub)
	if hub.Name != "deploy" {
		t.Errorf("name = %q", hub.Name)
	}
	// Usage is ALWAYS approximate; the two "/deploy" prompts count, "please
	// /deploy" (not a leading slash-command) does not → 2.
	if !hub.Usage.Approximate {
		t.Error("command usage must be flagged approximate")
	}
	if hub.Usage.Invocations != 2 {
		t.Errorf("approx invocations = %d, want 2 (leading /deploy only)", hub.Usage.Invocations)
	}
}

func TestSystemTemplates_ResolutionBadges(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{
		"adr-template.md": "# core ADR\n",
		"pr-template.md":  "# core PR\n",
	})
	// The project overrides adr-template.
	projTmpl := filepath.Join(projRoot, ".claude", "templates")
	if err := os.MkdirAll(projTmpl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projTmpl, "adr-template.md"), []byte("# project ADR\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, pid := hubWorld(t, projRoot)

	// Fleet mode: built-ins only, badge "core".
	var fleet []systemTemplateDTO
	getJSON(t, srv.URL+"/api/system/templates", &fleet)
	if len(fleet) != 2 {
		t.Fatalf("fleet templates = %d, want 2", len(fleet))
	}
	for _, tm := range fleet {
		if tm.Resolution != "core" || tm.Source != "plugin" {
			t.Errorf("fleet %s: resolution=%q source=%q, want core/plugin", tm.Name, tm.Resolution, tm.Source)
		}
	}

	// Project mode: adr-template flips to "project override", pr-template stays "core".
	var scoped []systemTemplateDTO
	getJSON(t, srv.URL+"/api/system/templates?projectId="+itoa64(pid), &scoped)
	byName := map[string]systemTemplateDTO{}
	for _, tm := range scoped {
		byName[tm.Name] = tm
	}
	adr, ok := byName["adr-template"]
	if !ok || adr.Resolution != "project override" || adr.Source != "project" {
		t.Errorf("adr-template = %+v, want project override/project", adr)
	}
	pr, ok := byName["pr-template"]
	if !ok || pr.Resolution != "core" {
		t.Errorf("pr-template = %+v, want core", pr)
	}
	// The built-in adr must NOT also appear as a separate "core" row (folded).
	if len(scoped) != 2 {
		t.Errorf("scoped templates = %d, want 2 (override folds the built-in)", len(scoped))
	}
}

func TestSystemTemplate_ContentReadOnly(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR body\n"})
	srv, _ := hubWorld(t, t.TempDir())

	var tc systemTemplateContentDTO
	getJSON(t, srv.URL+"/api/system/templates/adr-template", &tc)
	if tc.Name != "adr-template" || tc.Content != "# ADR body\n" {
		t.Errorf("template content = %+v", tc)
	}

	// Unknown template → 404.
	resp, err := http.Get(srv.URL + "/api/system/templates/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown template = %d, want 404", resp.StatusCode)
	}
}

func TestCopyTemplate_WritesOnceThen409(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR to copy\n"})
	srv, pid := hubWorld(t, projRoot)
	url := srv.URL + "/api/system/templates/adr-template/copy?projectId=" + itoa64(pid)

	// First copy → 201, writes into <project>/.claude/templates/adr-template.md.
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first copy = %d, want 201", resp.StatusCode)
	}
	var body struct{ Name, Path, Hint string }
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	// The fence de-symlinks the destination (macOS /var → /private/var), so the
	// returned path is the canonical resolved one — resolve the expected parent
	// the same way before comparing.
	resolvedParent, err := filepath.EvalSymlinks(projRoot)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(resolvedParent, ".claude", "templates", "adr-template.md")
	if body.Path != dest {
		t.Errorf("copy path = %q, want %q", body.Path, dest)
	}
	written, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if !bytes.Equal(written, []byte("# ADR to copy\n")) {
		t.Errorf("copied content = %q", written)
	}

	// Second copy → 409 (O_EXCL; never overwrite a customization). The project
	// now resolves adr-template locally, so the handler 409s before the write.
	resp2, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("repeat copy = %d, want 409", resp2.StatusCode)
	}
}

// A path-traversal name is rejected fail-closed BEFORE any filesystem write.
// The router will not match a raw slash to {name}, so an encoded ".." reaches
// the handler as a single path value and is refused 400 by safeTemplateName.
func TestCopyTemplate_RejectsTraversalName(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, pid := hubWorld(t, projRoot)

	resp, err := http.Post(
		srv.URL+"/api/system/templates/..%2f..%2fevil/copy?projectId="+itoa64(pid),
		"application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Errorf("traversal name = %d, want 400/404 (fail-closed)", resp.StatusCode)
	}
	// Nothing must have escaped: no file created above the templates dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(projRoot), "evil")); err == nil {
		t.Fatal("traversal wrote a file outside the project templates dir")
	}
	// And the templates dir itself must not contain an "evil" artifact.
	if entries, _ := os.ReadDir(filepath.Join(projRoot, ".claude", "templates")); len(entries) > 0 {
		t.Errorf("templates dir should be empty after a rejected copy, got %d entries", len(entries))
	}
}

// fencedTemplateDest must refuse a name whose resolved destination escapes the
// templates dir even if safeTemplateName were bypassed (defense in depth).
func TestFencedTemplateDest_RejectsEscape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".claude", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A clean name resolves inside.
	if _, err := fencedTemplateDest(dir, "adr-template"); err != nil {
		t.Errorf("clean name rejected: %v", err)
	}
	// A traversal segment must be refused with a descriptive path error.
	_, err := fencedTemplateDest(dir, "../escape")
	if err == nil {
		t.Fatal("traversal name accepted by fencedTemplateDest")
	}
	if err.Error() == "" {
		t.Error("fence rejection must carry a message")
	}
}

// A closed DB makes every read handler take its writeErr branch (500) — this
// covers the defensive DB-error paths that are otherwise unreachable, the same
// technique sibling handler tests use.
func TestSystemHub_DBErrorPaths(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "err.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Seed one project + skill + hook + command so path/id resolution reaches the
	// query that then fails on the closed DB.
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', '2026-01-01T00:00:00Z')`,
		`INSERT INTO skills (id, name, scope, dir_path, origin) VALUES (1, 's', 'global', '/d', 'local')`,
		`INSERT INTO hooks (id, scope, event, command, source_file, seq, enabled, content_hash) VALUES (1, 'global', 'Stop', 'x', '/s', 0, 1, 'h')`,
		`INSERT INTO commands (id, name, scope, file_path, origin, content_hash) VALUES (1, 'c', 'global', '/c', 'local', 'h')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	db.Close() // every subsequent query now errors

	for _, path := range []string{
		"/api/system/hub/summary",
		"/api/system/skills/1/hub",
		"/api/system/hooks/1/hub",
		"/api/system/commands/1/hub",
		"/api/system/templates",
		"/api/system/templates/adr-template",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError && resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on closed DB = %d, want 500 (or 404)", path, resp.StatusCode)
		}
	}
	// The copy write on a closed DB also surfaces an error (not a silent write).
	resp, err := http.Post(srv.URL+"/api/system/templates/adr-template/copy?projectId=1", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("copy on closed DB = %d, want an error status", resp.StatusCode)
	}
}

// Copying a name with no matching template for the project is a 404.
func TestCopyTemplate_UnknownTemplateName(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, pid := hubWorld(t, t.TempDir())
	resp, err := http.Post(srv.URL+"/api/system/templates/does-not-exist/copy?projectId="+itoa64(pid), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("copy of unknown template = %d, want 404", resp.StatusCode)
	}
}

func TestCopyTemplate_UnknownProject(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, _ := hubWorld(t, t.TempDir())
	resp, err := http.Post(srv.URL+"/api/system/templates/adr-template/copy?projectId=9999", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", resp.StatusCode)
	}
}

func TestCopyTemplate_MissingProjectId(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, _ := hubWorld(t, t.TempDir())
	resp, err := http.Post(srv.URL+"/api/system/templates/adr-template/copy", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing projectId = %d, want 400", resp.StatusCode)
	}
}

func TestCopyTemplate_ReadonlyMode(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, pid := hubWorld(t, t.TempDir())
	t.Setenv("SWARMERY_SYSTEM_READONLY", "1")
	resp, err := http.Post(srv.URL+"/api/system/templates/adr-template/copy?projectId="+itoa64(pid), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("readonly copy = %d, want 403", resp.StatusCode)
	}
}

// Copying a template that already resolves from the project (a project override)
// is a clean 409 — there is nothing to copy.
func TestCopyTemplate_AlreadyOverride409(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# core ADR\n"})
	projTmpl := filepath.Join(projRoot, ".claude", "templates")
	if err := os.MkdirAll(projTmpl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projTmpl, "adr-template.md"), []byte("# already local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, pid := hubWorld(t, projRoot)
	resp, err := http.Post(srv.URL+"/api/system/templates/adr-template/copy?projectId="+itoa64(pid), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("copy of an existing override = %d, want 409", resp.StatusCode)
	}
}

func TestSystemHubSummary_ProjectScope(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{
		"adr-template.md": "# ADR\n",
		"pr-template.md":  "# PR\n",
	})
	srv, pid := hubWorld(t, projRoot)
	// Project mode: core built-ins are always effective → templates count = 2.
	var s map[string]any
	getJSON(t, srv.URL+"/api/system/hub/summary?projectId="+itoa64(pid), &s)
	if got, _ := s["templates"].(float64); got != 2 {
		t.Errorf("scoped summary.templates = %v, want 2", s["templates"])
	}
}

// The seeded command 1 points at /nonexistent — its content degrades to "".
func TestCommandHub_VanishedFileDegrades(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, _ := hubWorld(t, t.TempDir())
	var hub struct {
		Content string `json:"content"`
	}
	getJSON(t, srv.URL+"/api/system/commands/1/hub", &hub)
	if hub.Content != "" {
		t.Errorf("vanished command file should degrade to empty content, got %q", hub.Content)
	}
}

// A command whose file exists serves its (redacted) frontmatter + body content.
func TestCommandHub_ContentFromDisk(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	db, err := store.Open(filepath.Join(t.TempDir(), "cmd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cmdPath := filepath.Join(t.TempDir(), "release.md")
	if err := os.WriteFile(cmdPath, []byte("---\nname: release\n---\n\nRelease body text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commands (id, name, scope, file_path, description, origin, content_hash)
	                      VALUES (1, 'release', 'global', ?, 'Release', 'local', 'h')`, cmdPath); err != nil {
		t.Fatal(err)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var hub struct {
		Frontmatter string `json:"frontmatter"`
		Content     string `json:"content"`
	}
	getJSON(t, srv.URL+"/api/system/commands/1/hub", &hub)
	if !bytes.Contains([]byte(hub.Content), []byte("Release body text")) {
		t.Errorf("command content = %q, want the body", hub.Content)
	}
	if !bytes.Contains([]byte(hub.Frontmatter), []byte("name: release")) {
		t.Errorf("command frontmatter = %q, want the frontmatter", hub.Frontmatter)
	}
}

// A project override's content is served through GET /api/system/templates/{name}.
func TestGetSystemTemplate_ProjectOverrideContent(t *testing.T) {
	claudeDir := t.TempDir()
	projRoot := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# core\n"})
	projTmpl := filepath.Join(projRoot, ".claude", "templates")
	if err := os.MkdirAll(projTmpl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projTmpl, "adr-template.md"), []byte("# PROJECT VERSION\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, pid := hubWorld(t, projRoot)
	var tc systemTemplateContentDTO
	getJSON(t, srv.URL+"/api/system/templates/adr-template?projectId="+itoa64(pid), &tc)
	if tc.Content != "# PROJECT VERSION\n" || tc.Resolution != "project override" {
		t.Errorf("override content = %+v, want the project version", tc)
	}
}

// A pack-shipped template resolves with a "pack:<name>" badge only when the pack
// is enabled for the project; a fleet listing always shows built-ins by pack.
func TestSystemTemplates_PackBadge(t *testing.T) {
	claudeDir := t.TempDir()
	// A non-core pack ships a template.
	verDir := filepath.Join(claudeDir, "plugins", "cache", "swarmery", "web-pack", "1.0.0")
	if err := os.MkdirAll(filepath.Join(verDir, ".in_use"), 0o755); err != nil {
		t.Fatal(err)
	}
	tdir := filepath.Join(verDir, "templates")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "web-scaffold.md"), []byte("# web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := systemHubClaudeDir
	AttachSystemHubDir(claudeDir)
	t.Cleanup(func() { systemHubClaudeDir = prev })

	srv, _ := hubWorld(t, t.TempDir())
	var fleet []systemTemplateDTO
	getJSON(t, srv.URL+"/api/system/templates", &fleet)
	if len(fleet) != 1 || fleet[0].Resolution != "pack:web-pack" {
		t.Fatalf("fleet pack template = %+v, want one pack:web-pack", fleet)
	}
}

func TestGetSystemTemplate_InvalidName(t *testing.T) {
	claudeDir := t.TempDir()
	seedPluginTemplates(t, claudeDir, map[string]string{"adr-template.md": "# ADR\n"})
	srv, _ := hubWorld(t, t.TempDir())
	// An encoded ".." reaches the handler as one path value → 400 safeTemplateName.
	resp, err := http.Get(srv.URL + "/api/system/templates/..%2fescape")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Errorf("invalid template name = %d, want 400/404", resp.StatusCode)
	}
}
