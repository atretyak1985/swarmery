package api

// phase 4: system (step-05) — /api/system/* endpoints, redaction, diff.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Secrets seeded into the fixture — they must NEVER appear in any response.
const (
	seededHookSecret = "abc123"
	seededBodySecret = "sk-ant-secret123"
)

const agentV1 = `---
name: reviewer
model: claude-opus
---

# Reviewer

Old body line
`

var agentV2 = `---
name: reviewer
model: claude-opus
---

# Reviewer

New body line
ANTHROPIC_API_KEY=` + seededBodySecret + `
`

const skillV1 = `---
name: deploy-helper
description: Deploys things safely with checks
---

Skill body text
`

// sysTS renders an RFC3339 timestamp daysAgo days in the past.
func sysTS(daysAgo int) string {
	return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.RFC3339)
}

// systemServer seeds the full system-registry world directly in SQL: one
// project, two agents (2 versions on the first), one skill, one hook with a
// token in its command, one command, lint findings (active error, resolved
// warn, active agent_dead), usage events, and an overlays dir on disk.
func systemServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "system.db"))
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
	      VALUES (1, '/work/alpha', 'alpha', 'Alpha', '2026-07-01T00:00:00Z')`)
	exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
	      (1, 1, 'aaaaaaaa-0000-4000-8000-000000000001', 'completed', '2026-07-10T10:00:00Z'),
	      (2, 1, 'bbbbbbbb-0000-4000-8000-000000000002', 'completed', '2026-07-10T12:00:00Z'),
	      (3, 1, 'cccccccc-0000-4000-8000-000000000003', 'completed', '2026-07-10T14:00:00Z')`)

	exec(`INSERT INTO agents (id, name, scope, project_id, file_path, model, description,
	                          current_version_id, origin, plugin_name)
	      VALUES (1, 'reviewer', 'global', NULL, '/u/.claude/agents/reviewer.md', 'claude-opus',
	              'Reviews diffs', 2, 'local', NULL),
	             (2, 'proj-agent', 'project', 1, '/work/alpha/.claude/agents/proj-agent.md', NULL,
	              'Project helper', 3, 'plugin', 'toolpack')`)
	exec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at, change_note) VALUES
	      (1, 1, 'hash-v1', ?, '2026-07-01T00:00:00Z', 'initial'),
	      (2, 1, 'hash-v2', ?, '2026-07-05T00:00:00Z', NULL),
	      (3, 2, 'hash-p1', '---
name: proj-agent
---

proj body
', '2026-07-02T00:00:00Z', 'initial')`, agentV1, agentV2)

	exec(`INSERT INTO skills (id, name, scope, project_id, dir_path, description,
	                          current_version_id, origin, plugin_name)
	      VALUES (1, 'deploy-helper', 'global', NULL, '/u/.claude/skills/deploy-helper',
	              'Deploys things safely with checks', 1, 'local', NULL)`)
	exec(`INSERT INTO skill_versions (id, skill_id, content_hash, content, created_at, change_note)
	      VALUES (1, 1, 'skill-hash-v1', ?, '2026-07-03T00:00:00Z', 'initial')`, skillV1)

	exec(`INSERT INTO hooks (id, scope, project_id, event, matcher, command, timeout,
	                         status_message, source_file, seq, enabled, managed, content_hash)
	      VALUES (1, 'global', NULL, 'Stop', NULL,
	              'SWARMERY_TOKEN=` + seededHookSecret + ` swarmery hook stop', 10,
	              NULL, '/u/.claude/settings.json', 0, 1, 'swarmery', 'hook-hash')`)

	exec(`INSERT INTO commands (id, name, scope, project_id, file_path, description,
	                            origin, plugin_name, content_hash)
	      VALUES (1, 'deploy', 'global', NULL, '/u/.claude/commands/deploy.md',
	              'Deploy the app', 'local', NULL, 'cmd-hash')`)

	// Lint: active error on agent 1, RESOLVED warn on agent 1 (must not
	// count), active agent_dead info on agent 2.
	exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at, resolved_at) VALUES
	      ('agent:1', 'agent_no_boundaries', 'error', 'no boundaries section', '2026-07-10T00:00:00Z', NULL),
	      ('agent:1', 'agent_oversized', 'warn', 'was too big', '2026-07-01T00:00:00Z', '2026-07-05T00:00:00Z'),
	      ('agent:2', 'agent_dead', 'info', '0 mentions in 30 days', '2026-07-10T00:00:00Z', NULL)`)

	// Usage: agent 1 — 2 distinct sessions inside 30d + 1 outside the
	// window (counts for last_used ordering, not for tasks_30d); the skill
	// gets one skill_use event (metrics mirror via events.skill_id).
	exec(`INSERT INTO events (session_id, ts, type, agent_id) VALUES
	      (1, ?, 'subagent_start', 1),
	      (2, ?, 'subagent_start', 1),
	      (1, ?, 'subagent_start', 1)`, sysTS(1), sysTS(2), sysTS(40))
	exec(`INSERT INTO events (session_id, ts, type, skill_id) VALUES (3, ?, 'skill_use', 1)`, sysTS(3))

	// Overlays on disk: a valid one, a broken one, plus the schema file.
	overlays := t.TempDir()
	writeFile(t, filepath.Join(overlays, "_schema", "project.schema.json"), `{}`)
	writeFile(t, filepath.Join(overlays, "example", "project.json"),
		`{"name":"example","displayName":"Example","mainApp":"web-app",`+
			`"repos":["apps/web-app"],"enabledPacks":["web-pack"]}`)
	writeFile(t, filepath.Join(overlays, "broken", "project.json"), `{nope`)
	AttachOverlaysDir(overlays)
	t.Cleanup(func() { AttachOverlaysDir("") })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSystemSummary(t *testing.T) {
	srv := systemServer(t)

	var s map[string]any
	getJSON(t, srv.URL+"/api/system/summary", &s)
	for field, want := range map[string]float64{
		"agents": 2, "skills": 1, "hooks": 1, "commands": 1, "overlays": 2,
	} {
		if s[field].(float64) != want {
			t.Errorf("summary.%s = %v, want %v", field, s[field], want)
		}
	}
	lint := s["lint"].(map[string]any)
	if lint["error"].(float64) != 1 || lint["warn"].(float64) != 0 || lint["info"].(float64) != 1 {
		t.Errorf("summary.lint = %v, want error=1 warn=0 info=1 (resolved warn must not count)", lint)
	}
}

func TestSystemAgentsList(t *testing.T) {
	srv := systemServer(t)

	var agents []map[string]any
	getJSON(t, srv.URL+"/api/system/agents", &agents)
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
	byName := map[string]map[string]any{}
	for _, a := range agents {
		byName[a["name"].(string)] = a
	}

	rev := byName["reviewer"]
	if rev["scope"] != "global" || rev["origin"] != "local" || rev["model"] != "claude-opus" {
		t.Errorf("reviewer = %v", rev)
	}
	if rev["lintMax"] != "error" {
		t.Errorf("reviewer.lintMax = %v, want error (resolved warn must not raise it)", rev["lintMax"])
	}
	if rev["dead"] != false {
		t.Errorf("reviewer.dead = %v, want false", rev["dead"])
	}
	if rev["tasks30d"].(float64) != 2 {
		t.Errorf("reviewer.tasks30d = %v, want 2 (distinct sessions in window; 40d-old event excluded)", rev["tasks30d"])
	}
	if rev["lastUsed"] == nil {
		t.Error("reviewer.lastUsed = nil, want MAX(events.ts)")
	}

	pa := byName["proj-agent"]
	if pa["scope"] != "project" || pa["projectSlug"] != "alpha" ||
		pa["origin"] != "plugin" || pa["pluginName"] != "toolpack" {
		t.Errorf("proj-agent = %v", pa)
	}
	if pa["lintMax"] != "info" || pa["dead"] != true {
		t.Errorf("proj-agent lint = %v/%v, want info/dead=true (active agent_dead)", pa["lintMax"], pa["dead"])
	}
	if pa["lastUsed"] != nil || pa["tasks30d"].(float64) != 0 {
		t.Errorf("proj-agent usage = %v/%v, want nil/0 (no events)", pa["lastUsed"], pa["tasks30d"])
	}

	// Filters.
	getJSON(t, srv.URL+"/api/system/agents?scope=global", &agents)
	if len(agents) != 1 || agents[0]["name"] != "reviewer" {
		t.Errorf("scope=global: %v, want only reviewer", agents)
	}
	getJSON(t, srv.URL+"/api/system/agents?project=alpha", &agents)
	if len(agents) != 1 || agents[0]["name"] != "proj-agent" {
		t.Errorf("project=alpha: %v, want only proj-agent", agents)
	}
}

func TestSystemSkillsList(t *testing.T) {
	srv := systemServer(t)

	var skills []map[string]any
	getJSON(t, srv.URL+"/api/system/skills", &skills)
	if len(skills) != 1 {
		t.Fatalf("skills = %d, want 1", len(skills))
	}
	sk := skills[0]
	if sk["name"] != "deploy-helper" || sk["scope"] != "global" || sk["model"] != nil {
		t.Errorf("skill = %v", sk)
	}
	if sk["tasks30d"].(float64) != 1 || sk["lastUsed"] == nil {
		t.Errorf("skill usage = %v/%v, want 1 task + lastUsed (events.skill_id mirror)",
			sk["tasks30d"], sk["lastUsed"])
	}
}

func TestSystemHooksList(t *testing.T) {
	srv := systemServer(t)

	var hooks []map[string]any
	getJSON(t, srv.URL+"/api/system/hooks", &hooks)
	if len(hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(hooks))
	}
	hk := hooks[0]
	if hk["event"] != "Stop" || hk["seq"].(float64) != 0 || hk["enabled"] != true ||
		hk["managed"] != "swarmery" || hk["sourceFile"] != "/u/.claude/settings.json" ||
		hk["timeout"].(float64) != 10 {
		t.Errorf("hook = %v", hk)
	}
	cmd := hk["command"].(string)
	if strings.Contains(cmd, seededHookSecret) {
		t.Errorf("hook command leaked the token: %q", cmd)
	}
	if !strings.Contains(cmd, "SWARMERY_TOKEN=•••") {
		t.Errorf("hook command = %q, want value-side redaction keeping the key name", cmd)
	}
}

func TestSystemCommandsList(t *testing.T) {
	srv := systemServer(t)

	var cmds []map[string]any
	getJSON(t, srv.URL+"/api/system/commands", &cmds)
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	if cmds[0]["name"] != "deploy" || cmds[0]["origin"] != "local" {
		t.Errorf("command = %v", cmds[0])
	}
}

func TestSystemOverlays(t *testing.T) {
	srv := systemServer(t)

	var resp map[string]any
	getJSON(t, srv.URL+"/api/system/overlays", &resp)
	if resp["schemaPresent"] != true {
		t.Errorf("schemaPresent = %v, want true", resp["schemaPresent"])
	}
	overlays := resp["overlays"].([]any)
	if len(overlays) != 2 {
		t.Fatalf("overlays = %d, want 2 (broken one must not fail the list)", len(overlays))
	}
	broken := overlays[0].(map[string]any)
	if broken["dir"] != "broken" || broken["parseError"] != true {
		t.Errorf("broken overlay = %v, want parseError=true", broken)
	}
	example := overlays[1].(map[string]any)
	if example["parseError"] != false || example["name"] != "example" ||
		example["mainApp"] != "web-app" {
		t.Errorf("example overlay = %v", example)
	}
	if packs := example["enabledPacks"].([]any); len(packs) != 1 || packs[0] != "web-pack" {
		t.Errorf("example.enabledPacks = %v", example["enabledPacks"])
	}
}

func TestSystemAgentDetail(t *testing.T) {
	srv := systemServer(t)

	var d map[string]any
	getJSON(t, srv.URL+"/api/system/agents/1", &d)
	if d["name"] != "reviewer" || d["deleted"] != false ||
		d["currentVersionId"].(float64) != 2 {
		t.Errorf("detail meta = %v", d)
	}
	fm := d["frontmatter"].(string)
	if !strings.Contains(fm, "name: reviewer") || !strings.Contains(fm, "model: claude-opus") {
		t.Errorf("frontmatter = %q", fm)
	}
	body := d["body"].(string)
	if !strings.Contains(body, "New body line") || strings.Contains(body, "---") {
		t.Errorf("body = %q, want markdown body without frontmatter delimiters", body)
	}
	if strings.Contains(body, seededBodySecret) || strings.Contains(body, "secret123") {
		t.Errorf("body leaked the seeded key: %q", body)
	}
	versions := d["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	newest := versions[0].(map[string]any)
	if newest["id"].(float64) != 2 || newest["contentHash"] != "hash-v2" {
		t.Errorf("versions[0] = %v, want the newest first", newest)
	}

	resp, err := http.Get(srv.URL + "/api/system/agents/999")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing agent: status %d, want 404", resp.StatusCode)
	}
}

func TestSystemSkillDetail(t *testing.T) {
	srv := systemServer(t)

	var d map[string]any
	getJSON(t, srv.URL+"/api/system/skills/1", &d)
	if d["name"] != "deploy-helper" || d["currentVersionId"].(float64) != 1 {
		t.Errorf("skill detail = %v", d)
	}
	if !strings.Contains(d["frontmatter"].(string), "deploy-helper") ||
		!strings.Contains(d["body"].(string), "Skill body text") {
		t.Errorf("skill split = %q / %q", d["frontmatter"], d["body"])
	}
	if len(d["versions"].([]any)) != 1 {
		t.Errorf("skill versions = %v, want 1", d["versions"])
	}
}

func TestSystemVersionContent(t *testing.T) {
	srv := systemServer(t)

	var v map[string]any
	getJSON(t, srv.URL+"/api/system/agents/1/versions/2", &v)
	if v["contentHash"] != "hash-v2" {
		t.Errorf("version = %v", v)
	}
	content := v["content"].(string)
	if !strings.Contains(content, "New body line") {
		t.Errorf("content = %q", content)
	}
	if strings.Contains(content, seededBodySecret) {
		t.Errorf("version content leaked the seeded key: %q", content)
	}

	// A version of ANOTHER agent must not be addressable through this one.
	resp, err := http.Get(srv.URL + "/api/system/agents/1/versions/3")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign version: status %d, want 404", resp.StatusCode)
	}
}

// TestSystemAgentDiff: the endpoint returns a unified diff that actually
// applies — patching the redacted v1 content must yield the redacted v2.
func TestSystemAgentDiff(t *testing.T) {
	srv := systemServer(t)

	var d map[string]any
	getJSON(t, srv.URL+"/api/system/agents/1/diff?from=1&to=2", &d)
	if d["from"].(float64) != 1 || d["to"].(float64) != 2 {
		t.Errorf("diff meta = %v", d)
	}
	diff := d["diff"].(string)
	if !strings.Contains(diff, "-Old body line") || !strings.Contains(diff, "+New body line") {
		t.Errorf("diff = %q", diff)
	}
	if strings.Contains(diff, "secret123") {
		t.Errorf("diff leaked the seeded key: %q", diff)
	}

	var v1, v2 map[string]any
	getJSON(t, srv.URL+"/api/system/agents/1/versions/1", &v1)
	getJSON(t, srv.URL+"/api/system/agents/1/versions/2", &v2)
	got := applyUnified(t, v1["content"].(string), diff)
	want := strings.TrimSuffix(v2["content"].(string), "\n")
	if got != want {
		t.Errorf("patched v1 != v2:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Bad params.
	for path, wantStatus := range map[string]int{
		"/api/system/agents/1/diff?from=1":        http.StatusBadRequest,
		"/api/system/agents/1/diff?from=1&to=999": http.StatusNotFound,
		"/api/system/agents/2/diff?from=1&to=2":   http.StatusNotFound, // foreign versions
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, wantStatus)
		}
	}
}

// TestSystemRedactionSweep: the seeded secrets never appear in ANY /api/system
// response body (success criterion, step-05).
func TestSystemRedactionSweep(t *testing.T) {
	srv := systemServer(t)

	for _, path := range []string{
		"/api/system/summary",
		"/api/system/agents",
		"/api/system/agents/1",
		"/api/system/agents/1/versions/1",
		"/api/system/agents/1/versions/2",
		"/api/system/agents/1/diff?from=1&to=2",
		"/api/system/skills",
		"/api/system/skills/1",
		"/api/system/skills/1/versions/1",
		"/api/system/hooks",
		"/api/system/commands",
		"/api/system/overlays",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: read: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", path, resp.StatusCode)
		}
		for _, secret := range []string{seededHookSecret, "secret123"} {
			if strings.Contains(string(body), secret) {
				t.Errorf("GET %s leaked %q", path, secret)
			}
		}
	}
}

func TestRedactPatterns(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"anthropic", "key sk-ant-api03-xyzXYZ_09 end", "key ••• end"},
		{"github", "ghp_abcDEF123456 and github_pat_11ABC_def", "••• and •••"},
		{"gitlab", "glpat-abc-DEF_123", "•••"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "•••"},
		{"slack", "xoxb-1234-abcd-efgh", "•••"},
		{"google", "AIzaSyA1234567890abcdefghijklmnopqrstuv", "•••"},
		{"npm", "npm_abcdefghijklmnopqrstuvwxyz0123456789", "•••"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sflKxwRJ", "•••"},
		{"bearer", "Authorization: Bearer abc.def-123", "Authorization: Bearer •••"},
		{"generic-token", "MY_TOKEN=hunter2", "MY_TOKEN=•••"},
		{"generic-password", "password: hunter2", "password: •••"},
		{"generic-apikey", "api_key=hunter2", "api_key=•••"},
		{"url-userinfo", "https://user:hunter2@host/path", "https://user:•••@host/path"},
		{"clean", "swarmery hook stop --db /tmp/x.db", "swarmery hook stop --db /tmp/x.db"},
	}
	for _, c := range cases {
		if got := redact(c.in); got != c.want {
			t.Errorf("%s: redact(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if n := len(redactRules); n != 11 {
		t.Errorf("redact pattern classes = %d, want 11 (format doc §8)", n)
	}
}

func TestUnifiedDiff(t *testing.T) {
	if d := UnifiedDiff("a", "b", "same\ntext\n", "same\ntext\n"); d != "" {
		t.Errorf("identical texts: diff = %q, want empty", d)
	}

	cases := []struct{ name, a, b string }{
		{"replace-middle", "1\n2\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\nX\n5\n6\n7\n8\n"},
		{"prepend", "b\nc\n", "a\nb\nc\n"},
		{"append", "a\nb\n", "a\nb\nc\n"},
		{"delete-all", "a\nb\n", ""},
		{"create-all", "", "a\nb\n"},
		{"two-hunks", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n",
			"1\nX\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\nY\n15\n"},
	}
	for _, c := range cases {
		diff := UnifiedDiff("a", "b", c.a, c.b)
		got := applyUnified(t, c.a, diff)
		want := strings.TrimSuffix(c.b, "\n")
		if got != want {
			t.Errorf("%s: apply(a, diff) = %q, want %q\ndiff:\n%s", c.name, got, want, diff)
		}
	}
}

// applyUnified is a strict unified-diff applier: context and deleted lines
// must match the source exactly (a stand-in for `patch`, hermetic in CI).
func applyUnified(t *testing.T, src, diff string) string {
	t.Helper()
	var srcLines []string
	if src != "" {
		srcLines = strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	}
	var out []string
	pos := 0 // next unconsumed src line (0-based)

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "@@ "):
			var aStart, aCount, bStart, bCount int
			if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &aStart, &aCount, &bStart, &bCount); err != nil {
				t.Fatalf("bad hunk header %q: %v", line, err)
			}
			hunkFrom := aStart - 1
			if aCount == 0 {
				hunkFrom = aStart // empty a-range points BEFORE the insertion
			}
			for pos < hunkFrom {
				out = append(out, srcLines[pos])
				pos++
			}
		case line[0] == ' ' || line[0] == '-':
			if pos >= len(srcLines) || srcLines[pos] != line[1:] {
				t.Fatalf("diff does not apply: src line %d = %q, hunk wants %q",
					pos+1, safeLine(srcLines, pos), line[1:])
			}
			if line[0] == ' ' {
				out = append(out, srcLines[pos])
			}
			pos++
		case line[0] == '+':
			out = append(out, line[1:])
		default:
			t.Fatalf("unexpected diff line %q", line)
		}
	}
	out = append(out, srcLines[pos:]...)
	return strings.Join(out, "\n")
}

func safeLine(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<EOF>"
}
