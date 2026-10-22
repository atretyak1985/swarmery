package firstreq

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/work/a', 'a', '2026-01-01T00:00:00Z'), (2, '/work/b', 'b', '2026-01-01T00:00:00Z')`)
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// turn is one seeded turns row; nil pointers stay NULL.
type turn struct {
	seq           int
	role          string
	model         string
	agent         string
	in, read      int64
	flat          *int64
	w5m, w1h      *int64
	startedOffset time.Duration
}

func i64(v int64) *int64 { return &v }

var sessionSeq int

// seedSession inserts a session of project pid that started at `started` with
// the given turns, and returns its id.
func seedSession(t *testing.T, db *sql.DB, pid int64, started time.Time, turns ...turn) int64 {
	t.Helper()
	sessionSeq++
	res, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, ?, ?)`,
		pid, fmt.Sprintf("s-%d", sessionSeq), started.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	for _, tr := range turns {
		var agent, model any
		if tr.agent != "" {
			agent = tr.agent
		}
		if tr.model != "" {
			model = tr.model
		}
		mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, model, agent_name,
		                    tokens_in, tokens_cache_read, tokens_cache_write, cache_write_5m_tokens, cache_write_1h_tokens)
		                  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sid, tr.seq, tr.role, started.Add(tr.startedOffset).UTC().Format(time.RFC3339Nano), model, agent,
			tr.in, tr.read, tr.flat, tr.w5m, tr.w1h)
	}
	return sid
}

// first returns a normal two-turn session opening: user prompt, then the first
// assistant request with the given cache write split.
func first(w1h, read int64) []turn {
	return []turn{
		{seq: 0, role: "user"},
		{seq: 1, role: "assistant", model: "claude-opus-5-5", in: 2, read: read, flat: i64(w1h), w1h: i64(w1h), w5m: i64(0)},
	}
}

func TestMeasureMedianOfFirstAssistantTurn(t *testing.T) {
	db := openStore(t)
	now := time.Now()
	since := now.Add(-30 * 24 * time.Hour)

	// Three in-window sessions of project 1: first-request writes 10k, 30k, 20k.
	seedSession(t, db, 1, now.Add(-time.Hour), first(10_000, 5_000)...)
	seedSession(t, db, 1, now.Add(-2*time.Hour), append(first(30_000, 0),
		// A LATER assistant turn must never be read as the first request.
		turn{seq: 2, role: "assistant", model: "claude-opus-5-5", w1h: i64(900_000), w5m: i64(0)})...)
	seedSession(t, db, 1, now.Add(-3*time.Hour),
		// A synthetic stand-in turn first, a subagent turn with a lower seq: both skipped.
		turn{seq: 0, role: "user"},
		turn{seq: 1, role: "assistant", model: syntheticModel},
		turn{seq: 2, role: "assistant", model: "claude-haiku-5-5", agent: "explorer", w1h: i64(777_777), w5m: i64(0)},
		turn{seq: 3, role: "assistant", model: "claude-opus-5-5", in: 1, read: 1_000, w1h: i64(15_000), w5m: i64(5_000)},
	)
	// Pre-0075 row: no TTL split → the flat cache-write total is the write.
	seedSession(t, db, 1, now.Add(-4*time.Hour),
		turn{seq: 0, role: "user"},
		turn{seq: 1, role: "assistant", model: "claude-opus-5", flat: i64(40_000)},
	)

	// Excluded: a one-turn session, an out-of-window session, another project.
	seedSession(t, db, 1, now.Add(-time.Hour), turn{seq: 0, role: "assistant", model: "claude-opus-5-5", w1h: i64(1), w5m: i64(0)})
	seedSession(t, db, 1, now.Add(-40*24*time.Hour), first(999_999, 0)...)
	seedSession(t, db, 2, now.Add(-time.Hour), first(888_888, 0)...)

	m, err := Measure(db, 1, since)
	if err != nil {
		t.Fatal(err)
	}
	// writes: 10k, 30k, 20k, 40k → median (20k+30k)/2 = 25k.
	// prompts: 15_002, 30_002, 21_001, 40_000 → (21_001+30_002)/2 = 25_501.
	want := Measured{MedianTokens: 25_000, MedianPromptTokens: 25_501, Sessions: 4}
	if m != want {
		t.Fatalf("Measure = %+v, want %+v", m, want)
	}

	empty, err := Measure(db, 1, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if empty != (Measured{}) {
		t.Fatalf("empty window = %+v, want zero", empty)
	}
}

func TestMedianOdd(t *testing.T) {
	if got := median([]int64{9, 1, 5}); got != 5 {
		t.Fatalf("median = %d, want 5", got)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateSumsPartsAndOtherIsTheRemainder(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, "home", ".claude")
	root := filepath.Join(tmp, "work", "repo")
	writeFile(t, filepath.Join(claudeDir, "CLAUDE.md"), strings.Repeat("u", 400))                         // 100 tok
	writeFile(t, filepath.Join(claudeDir, "rules", "style.md"), strings.Repeat("r", 40))                  // 10 tok
	writeFile(t, filepath.Join(tmp, "work", "CLAUDE.md"), strings.Repeat("a", 80))                        // 20 tok (ancestor)
	writeFile(t, filepath.Join(root, "CLAUDE.md"), strings.Repeat("p", 800))                              // 200 tok
	writeFile(t, filepath.Join(root, ".claude", "rules", "nested", "always.md"), strings.Repeat("x", 40)) // 10 tok
	writeFile(t, filepath.Join(root, ".claude", "rules", "scoped.md"), "---\npaths:\n  - \"src/**\"\n---\n"+strings.Repeat("s", 4000))

	mcp := []MCPTool{
		{Server: "playwright", Name: "mcp__playwright__click"},                           // 22 bytes
		{Server: "docs", Name: "mcp__docs__q", Schema: []byte(strings.Repeat("j", 102))}, // 12+102 bytes
	}
	skills := []Skill{{Name: "deploy", Description: strings.Repeat("d", 30)}} // 6+30+4 = 40 bytes

	b := Estimate(Sources{ClaudeDir: claudeDir, ProjectRoot: root}, mcp, skills, 1_000)

	if b.ClaudeMD != 340 {
		t.Errorf("ClaudeMD = %d, want 340 (100+10+20+200+10; the paths-scoped rule excluded)", b.ClaudeMD)
	}
	if b.MCP != (22+114)/4 {
		t.Errorf("MCP = %d, want %d", b.MCP, (22+114)/4)
	}
	if b.Skills != 10 {
		t.Errorf("Skills = %d, want 10", b.Skills)
	}
	if want := int64(1_000) - (b.ClaudeMD + b.MCP + b.Skills); b.Other != want {
		t.Errorf("Other = %d, want %d", b.Other, want)
	}
	if b.MCPTools != 2 || b.MCPSchemas != 1 || b.SkillCount != 1 {
		t.Errorf("counts = tools %d schemas %d skills %d, want 2/1/1", b.MCPTools, b.MCPSchemas, b.SkillCount)
	}
	if !strings.Contains(b.Note, "estimate (~bytes/4)") || !strings.Contains(b.Note, "1 of 2 MCP tools are counted by name only") {
		t.Errorf("Note = %q, missing the estimate caveat or the name-only MCP count", b.Note)
	}
	wantFiles := []string{
		filepath.Join(claudeDir, "CLAUDE.md"),
		filepath.Join(claudeDir, "rules", "style.md"),
		filepath.Join(tmp, "work", "CLAUDE.md"),
		filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, ".claude", "rules", "nested", "always.md"),
	}
	if !reflect.DeepEqual(b.ClaudeMDFiles, wantFiles) {
		t.Errorf("ClaudeMDFiles =\n%v\nwant\n%v", b.ClaudeMDFiles, wantFiles)
	}

	// Parts larger than the measured size: Other floors at zero, never negative.
	if small := Estimate(Sources{ClaudeDir: claudeDir, ProjectRoot: root}, mcp, skills, 10); small.Other != 0 {
		t.Errorf("Other with measured < parts = %d, want 0", small.Other)
	}
}

func TestEnabledPluginsLayersUserProjectLocal(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, ".claude")
	root := filepath.Join(tmp, "repo")
	writeFile(t, filepath.Join(claudeDir, "settings.json"),
		`{"enabledPlugins": {"superpowers@official": true, "figma@official": true, "core@swarmery": true}}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"enabledPlugins": {"figma@official": false, "web-pack@swarmery": true}}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"enabledPlugins": {"web-pack@swarmery": false, "jira-pack@swarmery": true}}`)

	got := EnabledPlugins(claudeDir, root)
	want := map[string]bool{"superpowers": true, "core": true, "jira-pack": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnabledPlugins = %v, want %v", got, want)
	}
}

func TestVisibleSkills(t *testing.T) {
	db := openStore(t)
	mustExec(t, db, `INSERT INTO skills (name, scope, project_id, dir_path, description, origin, plugin_name) VALUES
		('mine', 'global', NULL, '/h/.claude/skills/mine', 'user skill', 'local', NULL),
		('repo-only', 'project', 1, '/work/a/.claude/skills/repo-only', 'project skill', 'local', NULL),
		('other-repo', 'project', 2, '/work/b/.claude/skills/other-repo', 'not visible', 'local', NULL),
		('core:plan', 'global', NULL, '/cache/core/skills/plan', 'enabled plugin', 'plugin', 'core'),
		('figma:x', 'global', NULL, '/cache/figma/skills/x', 'disabled plugin', 'plugin', 'figma')`)
	mustExec(t, db, `INSERT INTO skills (name, scope, project_id, dir_path, description, origin, deleted)
		VALUES ('gone', 'global', NULL, '/h/.claude/skills/gone', 'deleted', 'local', 1)`)

	got, err := VisibleSkills(db, 1, map[string]bool{"core": true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if want := []string{"core:plan", "mine", "repo-only"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("VisibleSkills = %v, want %v", names, want)
	}
}

func TestObservedMCPTools(t *testing.T) {
	db := openStore(t)
	now := time.Now()
	own := seedSession(t, db, 1, now.Add(-time.Hour), first(1, 0)...)
	foreign := seedSession(t, db, 2, now.Add(-time.Hour), first(1, 0)...)
	ts := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	old := now.Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	for _, e := range []struct {
		sid  int64
		ts   string
		tool string
	}{
		{own, ts, "mcp__claude_ai_Docs__read"},  // own session, unconfigured server → kept
		{own, ts, "mcp__claude_ai_Docs__read"},  // duplicate call → one tool
		{foreign, ts, "mcp__playwright__click"}, // configured server, used elsewhere → kept
		{foreign, ts, "mcp__other__thing"},      // neither → dropped
		{own, old, "mcp__playwright__stale"},    // out of window → dropped
		{own, ts, "Bash"},                       // not MCP
		{own, ts, "mcpXXplaywright"},            // LIKE wildcard trap: not an mcp__ name
	} {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name) VALUES (?, ?, 'tool_call', ?)`, e.sid, e.ts, e.tool)
	}
	servers := []mcpcfg.ConfiguredServer{{Name: "playwright", Scope: mcpcfg.ScopeLocal}}

	got, err := ObservedMCPTools(db, 1, servers, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPTool{
		{Server: "", Name: "mcp__claude_ai_Docs__read"},
		{Server: "playwright", Name: "mcp__playwright__click"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ObservedMCPTools =\n%+v\nwant\n%+v", got, want)
	}
}
