package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// toolsServer seeds tool_call events across two projects: Bash calls on the
// main transcript and one inside a debugger sidechain (parented to its
// subagent_start — the attribution the ingester actually stores), a denied
// call on beta, and a Read call outside the default 14-day range.
func toolsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today := todayStart.Add(12 * time.Hour).UTC().Format(tsFmt)
	day20 := todayStart.AddDate(0, 0, -20).Add(12 * time.Hour).UTC().Format(tsFmt)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  NULL,    ?)`, day20, day20)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES
		(1, 1, 'u1', 'claude-fable-5', 'active',    ?),
		(2, 2, 'u2', 'claude-fable-5', 'completed', ?)`, today, today)

	// The debugger's subagent_start: sidechain tool events are parented to it.
	mustExec(`INSERT INTO events (id, session_id, ts, type, tool_name, status, duration_ms, payload, dedup_key) VALUES
		(10, 1, ?, 'subagent_start', 'Agent', 'ok', 60000, '{"subagent_type":"core:debugger"}', 'sub1')`, today)

	// Bash: 3 ok on main, 1 error inside the debugger, 1 denied on beta (no duration).
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, duration_ms, parent_event_id, dedup_key) VALUES
		(1, ?, 'tool_call', 'Bash', 'ok',     100,  NULL, 'b1'),
		(1, ?, 'tool_call', 'Bash', 'ok',     200,  NULL, 'b2'),
		(1, ?, 'tool_call', 'Bash', 'ok',     1000, NULL, 'b3'),
		(1, ?, 'tool_call', 'Bash', 'error',  400,  10,   'b4'),
		(2, ?, 'tool_call', 'Bash', 'denied', NULL, NULL, 'b5')`,
		today, today, today, today, today)

	// Read: one in range, one 20 days back (excluded).
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, duration_ms, dedup_key) VALUES
		(1, ?, 'tool_call', 'Read', 'ok', 50, 'r1'),
		(1, ?, 'tool_call', 'Read', 'ok', 70, 'r2')`, today, day20)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestStatsTools(t *testing.T) {
	srv := toolsServer(t)

	var out toolsDTO
	getJSON(t, srv.URL+"/api/stats/tools", &out)
	byTool := map[string]toolStatDTO{}
	for _, tl := range out.Tools {
		byTool[tl.Tool] = tl
	}

	bash := byTool["Bash"]
	if bash.Calls != 5 || bash.Errors != 1 || bash.Denied != 1 {
		t.Fatalf("Bash = %+v, want 5 calls / 1 error / 1 denied", bash)
	}
	// durations [100,200,400,1000] (denied carried none): avg 425, p95 = ceil(0.95×4)=4th → 1000
	if bash.AvgMs == nil || *bash.AvgMs != 425 {
		t.Errorf("Bash avg = %v, want 425", bash.AvgMs)
	}
	if bash.P95Ms == nil || *bash.P95Ms != 1000 {
		t.Errorf("Bash p95 = %v, want 1000", bash.P95Ms)
	}
	agents := map[string]toolAgentDTO{}
	for _, a := range bash.Agents {
		agents[a.Agent] = a
	}
	if a := agents["main"]; a.Calls != 4 || a.Errors != 0 {
		t.Errorf("main split = %+v, want 4 calls 0 errors", a)
	}
	if a := agents["debugger"]; a.Calls != 1 || a.Errors != 1 { // "core:debugger" folded
		t.Errorf("debugger split = %+v, want 1 call 1 error", a)
	}

	if byTool["Read"].Calls != 1 { // the day20 row is out of range
		t.Errorf("Read calls = %d, want 1", byTool["Read"].Calls)
	}
	if byTool["Agent"].Calls != 1 { // subagent_start counts as an Agent tool call
		t.Errorf("Agent calls = %d, want 1", byTool["Agent"].Calls)
	}
	if out.Tools[0].Tool != "Bash" { // ranked by calls desc
		t.Errorf("first tool = %q, want Bash", out.Tools[0].Tool)
	}

	// ?project= filters by slug (global scope predicate): beta's denied Bash
	// call disappears.
	var alpha toolsDTO
	getJSON(t, srv.URL+"/api/stats/tools?project=-work-alpha", &alpha)
	for _, tl := range alpha.Tools {
		if tl.Tool == "Bash" && (tl.Calls != 4 || tl.Denied != 0) {
			t.Errorf("filtered Bash = %+v, want 4 calls 0 denied", tl)
		}
	}
}
