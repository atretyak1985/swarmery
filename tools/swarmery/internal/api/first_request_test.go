package api

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// ── GET /api/projects/{id}/first-request ─────────────────────────────────────

func TestProjectFirstRequest(t *testing.T) {
	srv, db := projectsTestServer(t)
	root := projectPath(t, srv.URL, "1")

	prev := systemHubClaudeDir
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	systemHubClaudeDir = claudeDir
	t.Cleanup(func() { systemHubClaudeDir = prev })
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(strings.Repeat("u", 4_000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(strings.Repeat("p", 8_000)), 0o644); err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, `INSERT INTO skills (name, scope, project_id, dir_path, description, origin)
		VALUES ('mine', 'global', NULL, '/h/.claude/skills/mine', ?, 'local')`, strings.Repeat("d", 392))

	// Two in-window sessions: first-request writes 30k and 50k, cached prefix 10k.
	started := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for i, write := range []int64{30_000, 50_000} {
		sid := int64(500 + i)
		execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (?, 1, ?, ?)`,
			sid, "fr-"+started+string(rune('a'+i)), started)
		execSQL(t, db, `INSERT INTO turns (session_id, seq, role, started_at) VALUES (?, 0, 'user', ?)`, sid, started)
		execSQL(t, db, `INSERT INTO turns (session_id, seq, role, started_at, model, tokens_in, tokens_cache_read,
			tokens_cache_write, cache_write_5m_tokens, cache_write_1h_tokens)
			VALUES (?, 1, 'assistant', ?, 'claude-opus-5-5', 0, 10000, ?, 0, ?)`, sid, started, write, write)
		execSQL(t, db, `INSERT INTO events (session_id, ts, type, tool_name) VALUES (?, ?, 'tool_call', 'mcp__claude_ai_Docs__read')`,
			sid, started)
	}

	var got firstRequestDTO
	getJSON(t, srv.URL+"/api/projects/1/first-request", &got)
	if got.WindowDays != 30 {
		t.Errorf("windowDays = %d, want 30", got.WindowDays)
	}
	if got.Measured.Sessions != 2 || got.Measured.MedianTokens != 40_000 || got.Measured.MedianPromptTokens != 50_000 {
		t.Errorf("measured = %+v, want 2 sessions, median write 40000, median prompt 50000", got.Measured)
	}
	e := got.Estimate
	if e.ClaudeMD != 3_000 { // (4000 + 8000) / 4
		t.Errorf("estimate.claudeMd = %d, want 3000", e.ClaudeMD)
	}
	if e.Skills != 100 { // ("mine" + 392 + 4) / 4
		t.Errorf("estimate.skills = %d, want 100", e.Skills)
	}
	if e.MCPTools != 1 || e.MCP != int64(len("mcp__claude_ai_Docs__read"))/4 {
		t.Errorf("estimate mcp = %d over %d tools, want the one observed tool by name", e.MCP, e.MCPTools)
	}
	if want := int64(50_000) - e.ClaudeMD - e.Skills - e.MCP; e.Other != want {
		t.Errorf("estimate.other = %d, want %d (measured prompt minus the parts)", e.Other, want)
	}
	if !strings.Contains(e.Note, "estimate (~bytes/4)") {
		t.Errorf("estimate.note = %q, want the ~bytes/4 caveat", e.Note)
	}
	if got.MCPServers == nil {
		t.Error("mcpServers = null, want []")
	}

	for path, code := range map[string]int{
		"/api/projects/999/first-request": http.StatusNotFound,
		"/api/projects/abc/first-request": http.StatusBadRequest,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != code {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, code)
		}
	}
}

// ── GET /api/sessions — modelSwitched / models ───────────────────────────────

func TestSessionsModelSwitched(t *testing.T) {
	srv, db := projectsTestServer(t)
	started := "2026-07-15T00:00:00Z"
	seed := func(id int64, uuid string, turns ...[3]string) {
		execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (?, 1, ?, ?)`, id, uuid, started)
		for seq, tr := range turns {
			var agent any
			if tr[2] != "" {
				agent = tr[2]
			}
			execSQL(t, db, `INSERT INTO turns (session_id, seq, role, started_at, model, agent_name) VALUES (?, ?, ?, ?, ?, ?)`,
				id, seq, tr[0], started, tr[1], agent)
		}
	}
	// [role, model, agent_name]
	seed(601, "sw-real", [3]string{"assistant", "claude-fable-5-1", ""}, [3]string{"assistant", "claude-opus-5-5", ""},
		[3]string{"assistant", "claude-fable-5-1", ""})
	seed(602, "sw-subagent-synthetic", [3]string{"assistant", "claude-opus-5-5", ""},
		[3]string{"assistant", "claude-haiku-5-5", "Explore"}, [3]string{"assistant", "<synthetic>", ""})
	seed(603, "sw-context-marker", [3]string{"assistant", "claude-opus-5-5", ""}, [3]string{"assistant", "claude-opus-5-5[1m]", ""})

	var page sessionsPageDTO
	getJSON(t, srv.URL+"/api/sessions?project=1", &page)
	byUUID := map[string]sessionDTO{}
	for _, s := range page.Sessions {
		byUUID[s.SessionUUID] = s
	}
	for uuid, want := range map[string]struct {
		switched bool
		models   []string
	}{
		"sw-real":               {true, []string{"claude-fable-5-1", "claude-opus-5-5"}},
		"sw-subagent-synthetic": {false, []string{"claude-opus-5-5"}},
		"sw-context-marker":     {false, []string{"claude-opus-5-5"}},
		"u-managed-1":           {false, []string{}}, // no model on its turns
	} {
		s, ok := byUUID[uuid]
		if !ok {
			t.Fatalf("session %s missing from the list", uuid)
		}
		if s.ModelSwitched != want.switched || !reflect.DeepEqual(s.Models, want.models) {
			t.Errorf("%s: modelSwitched=%v models=%v, want %v %v", uuid, s.ModelSwitched, s.Models, want.switched, want.models)
		}
	}
	// The detail endpoint shares the projection.
	var d sessionDetailDTO
	getJSON(t, srv.URL+"/api/sessions/sw-real", &d)
	if !d.ModelSwitched {
		t.Errorf("detail modelSwitched = false, want true")
	}
}

// ── GET /api/system/insights — startCost ─────────────────────────────────────

func TestSystemInsightsStartCost(t *testing.T) {
	srv, db := projectsTestServer(t)
	p1 := projectPath(t, srv.URL, "1")
	execSQL(t, db, `INSERT INTO projects (id, path, slug, name, first_seen, archived)
		VALUES (77, '/Volumes/Work/other', 'other', 'Other', '2026-07-10T00:00:00Z', 0)`)

	seedFinding(t, db, "claude_md:"+filepath.Join(p1, "CLAUDE.md"), sysscan.RuleClaudeMDOver200Lines, "warn", "too long", "")
	seedFinding(t, db, "mcp:user:idle", sysscan.RuleMCPServerUnused30d, "info", "unused", "")
	seedFinding(t, db, "mcp:local:pw|/Volumes/Work/other", sysscan.RuleMCPServerUnused30d, "info", "unused there", "")
	seedFinding(t, db, "mcp:user:was-idle", sysscan.RuleMCPServerUnused30d, "info", "fixed", "2026-07-28T10:00:00Z")
	seedFinding(t, db, "agent:1", sysscan.RuleAgentDead, "info", "not a start-cost rule", "")

	var fleet systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights", &fleet)
	byTarget := map[string]startCostDTO{}
	for _, d := range fleet.StartCost {
		byTarget[d.Target] = d
	}
	if len(byTarget) != 3 {
		t.Fatalf("startCost has %d rows, want 3: %+v", len(fleet.StartCost), fleet.StartCost)
	}
	if md := byTarget["claude_md:"+filepath.Join(p1, "CLAUDE.md")]; md.ProjectSlug == nil || md.ProjectPath != p1 {
		t.Errorf("CLAUDE.md finding must resolve to project 1, got %+v", md)
	}
	if other := byTarget["mcp:local:pw|/Volumes/Work/other"]; other.ProjectSlug == nil || *other.ProjectSlug != "other" {
		t.Errorf("local MCP finding must resolve to 'other', got %+v", other)
	}
	if user := byTarget["mcp:user:idle"]; user.ProjectSlug != nil || user.ProjectPath != "" {
		t.Errorf("user-scope MCP finding must be project-less, got %+v", user)
	}

	// Project scope: its own CLAUDE.md finding + the machine-wide user server.
	var scoped systemInsightsDTO
	getJSON(t, srv.URL+"/api/system/insights?projectId=1", &scoped)
	var targets []string
	for _, d := range scoped.StartCost {
		targets = append(targets, d.Target)
	}
	want := []string{"claude_md:" + filepath.Join(p1, "CLAUDE.md"), "mcp:user:idle"}
	if !reflect.DeepEqual(targets, want) {
		t.Errorf("scoped startCost targets = %v, want %v", targets, want)
	}
}
