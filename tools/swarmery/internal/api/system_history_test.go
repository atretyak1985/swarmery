package api

// phase 4: agent run history — GET /api/system/agents/{id}/history.
// Verifies the name-normalisation folding (core:x + x → one agent, built-ins
// excluded) and the per-project / duration / error aggregates.

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func historyServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
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

	exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
	      (1, '/work/alpha', 'alpha', 'Alpha', '2026-07-01T00:00:00Z'),
	      (2, '/work/beta',  'beta',  'Beta',  '2026-07-01T00:00:00Z')`)
	exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
	      (1, 1, 'aaaa-1', 'completed', '2026-07-10T10:00:00Z'),
	      (2, 1, 'aaaa-2', 'completed', '2026-07-11T10:00:00Z'),
	      (3, 2, 'bbbb-1', 'completed', '2026-07-12T10:00:00Z')`)
	exec(`INSERT INTO agents (id, name, scope, project_id, file_path, model, origin)
	      VALUES (1, 'tech-lead', 'global', NULL, '/u/.claude/agents/tech-lead.md', 'claude-opus', 'local')`)

	// tech-lead runs: two notations (core:tech-lead + tech-lead), 2 projects,
	// statuses ok/ok/error. Plus a built-in Explore and an unrelated agent that
	// must both be excluded by the name filter.
	seed := func(sess int, ts, st, status string, dur int64) {
		exec(`INSERT INTO events (session_id, ts, type, status, duration_ms, payload) VALUES
		      (?, ?, 'subagent_start', ?, ?, json_object('subagent_type', ?, 'description', 'task'))`,
			sess, ts, status, dur, st)
	}
	seed(1, "2026-07-10T10:05:00Z", "core:tech-lead", "ok", 100000)   // alpha
	seed(2, "2026-07-11T10:05:00Z", "tech-lead", "ok", 200000)        // alpha (bare form)
	seed(3, "2026-07-12T10:05:00Z", "core:tech-lead", "error", 60000) // beta, error
	seed(1, "2026-07-10T11:00:00Z", "Explore", "ok", 5000)            // built-in — excluded
	seed(1, "2026-07-10T11:30:00Z", "other-agent", "ok", 5000)        // unrelated — excluded

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentHistory(t *testing.T) {
	srv := historyServer(t)

	var hist map[string]any
	getJSON(t, srv.URL+"/api/system/agents/1/history?days=365", &hist)

	if hist["agentName"] != "tech-lead" {
		t.Errorf("agentName = %v, want tech-lead", hist["agentName"])
	}

	totals := hist["totals"].(map[string]any)
	if totals["runs"].(float64) != 3 {
		t.Errorf("totals.runs = %v, want 3 (Explore + other-agent excluded)", totals["runs"])
	}
	if totals["projects"].(float64) != 2 {
		t.Errorf("totals.projects = %v, want 2", totals["projects"])
	}
	if totals["sessions"].(float64) != 3 {
		t.Errorf("totals.sessions = %v, want 3", totals["sessions"])
	}
	if totals["okRuns"].(float64) != 2 || totals["errorRuns"].(float64) != 1 {
		t.Errorf("ok/err = %v/%v, want 2/1", totals["okRuns"], totals["errorRuns"])
	}
	if er := totals["errorRate"].(float64); er < 0.33 || er > 0.34 {
		t.Errorf("errorRate = %v, want ~0.333", er)
	}

	dur := hist["duration"].(map[string]any)
	if dur["totalMs"].(float64) != 360000 {
		t.Errorf("duration.totalMs = %v, want 360000", dur["totalMs"])
	}
	if dur["avgMs"].(float64) != 120000 {
		t.Errorf("duration.avgMs = %v, want 120000", dur["avgMs"])
	}
	if dur["p95Ms"].(float64) != 200000 {
		t.Errorf("duration.p95Ms = %v, want 200000", dur["p95Ms"])
	}

	byProject := hist["byProject"].([]any)
	if len(byProject) != 2 {
		t.Fatalf("byProject = %d entries, want 2", len(byProject))
	}
	// sorted by runs desc: alpha (2) before beta (1)
	top := byProject[0].(map[string]any)
	if top["slug"] != "alpha" || top["runs"].(float64) != 2 {
		t.Errorf("byProject[0] = %v, want alpha runs=2", top)
	}

	if runs := hist["recentRuns"].([]any); len(runs) != 3 {
		t.Errorf("recentRuns = %d, want 3", len(runs))
	}
}

func TestAgentHistoryNotFound(t *testing.T) {
	srv := historyServer(t)
	resp, err := srv.Client().Get(srv.URL + "/api/system/agents/999/history")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestNormAgentType(t *testing.T) {
	cases := map[string]string{
		"core:tech-lead": "tech-lead",
		"tech-lead":      "tech-lead",
		"Explore":        "Explore",
		"a:b:c":          "c",
		"":               "",
	}
	for in, want := range cases {
		if got := normAgentType(in); got != want {
			t.Errorf("normAgentType(%q) = %q, want %q", in, got, want)
		}
	}
}
