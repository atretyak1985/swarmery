package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// archiveScopeServer seeds a KEPT project (id 1: session + priced turn +
// a 'debugger' subagent_start today) and an ARCHIVED project (id 2: session +
// a bigger priced turn + a 'tech-lead' subagent_start today). Archiving a
// project must drop it out of every session-data surface, so project 2's
// session, its $, and its 'tech-lead' runs must be invisible everywhere.
func archiveScopeServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const tsFmt = "2006-01-02T15:04:05.000Z"
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today := todayStart.Add(12 * time.Hour).UTC().Format(tsFmt)

	ex := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	ex(`INSERT INTO projects (id, path, slug, name, first_seen, archived) VALUES
		(1, '/work/keep', 'keep', 'Keep', ?, 0),
		(2, '/work/gone', 'gone', 'Gone', ?, 1)`, today, today)
	ex(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES
		(1, 1, 'u-keep', 'claude-fable-5', 'completed', ?),
		(2, 2, 'u-gone', 'claude-fable-5', 'completed', ?)`, today, today)
	ex(`INSERT INTO turns (session_id, seq, role, model, started_at, tokens_in, tokens_out, cost_usd) VALUES
		(1, 0, 'assistant', 'claude-fable-5', ?, 100, 50, 0.50),
		(2, 0, 'assistant', 'claude-fable-5', ?, 900, 900, 9.00)`, today, today)
	ex(`INSERT INTO events (session_id, ts, type, payload, dedup_key) VALUES
		(1, ?, 'subagent_start', '{"subagent_type":"debugger"}',  'e1'),
		(2, ?, 'subagent_start', '{"subagent_type":"tech-lead"}', 'e2')`, today, today)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getRaw(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func TestArchivedProjectExcludedFromSessionsList(t *testing.T) {
	srv := archiveScopeServer(t)
	var page struct {
		Sessions []map[string]any `json:"sessions"`
	}
	getRaw(t, srv.URL+"/api/sessions", &page)
	list := page.Sessions
	if len(list) != 1 {
		t.Fatalf("sessions list len = %d, want 1 (archived project's session hidden)", len(list))
	}
	if list[0]["projectSlug"] != "keep" {
		t.Errorf("remaining session project = %v, want keep", list[0]["projectSlug"])
	}
}

func TestArchivedProjectExcludedFromStatsToday(t *testing.T) {
	srv := archiveScopeServer(t)
	var today map[string]any
	getRaw(t, srv.URL+"/api/stats/today", &today)
	if today["sessions"].(float64) != 1 {
		t.Errorf("stats/today sessions = %v, want 1", today["sessions"])
	}
	// Only the kept project's $0.50 counts — not the archived project's $9.00.
	if c, ok := today["cost_usd"].(float64); !ok || c < 0.499 || c > 0.501 {
		t.Errorf("stats/today cost_usd = %v, want ~0.50 (archived $9.00 excluded)", today["cost_usd"])
	}
}

func TestArchivedProjectExcludedFromOverview(t *testing.T) {
	srv := archiveScopeServer(t)
	var o struct {
		Sessions int64 `json:"sessions"`
		Projects []struct {
			Slug string `json:"slug"`
		} `json:"projects"`
	}
	getRaw(t, srv.URL+"/api/stats/overview", &o)
	if o.Sessions != 1 {
		t.Errorf("overview sessions = %d, want 1", o.Sessions)
	}
	for _, p := range o.Projects {
		if p.Slug == "gone" {
			t.Error("archived project 'gone' must not appear in the overview projects rail")
		}
	}
}

func TestArchivedProjectExcludedFromTasksList(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ex := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	// project 1 kept, project 2 archived — each with a workspace and one task.
	ex(`INSERT INTO projects (id, path, slug, name, first_seen, archived) VALUES
		(1, '/work/keep', 'keep', 'Keep', '2026-07-01T00:00:00Z', 0),
		(2, '/work/gone', 'gone', 'Gone', '2026-07-01T00:00:00Z', 1)`)
	ex(`INSERT INTO workspaces (id, slug, root_path, code_path, project_id, display_name) VALUES
		(1, 'keep', '/ws/keep', '/work/keep', 1, 'Keep'),
		(2, 'gone', '/ws/gone', '/work/gone', 2, 'Gone')`)
	ex(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id, workspace_id) VALUES
		(10, 1, 'kept task', 'g', 'running', '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z', 'workspace', 'kept-task', 1),
		(20, 2, 'gone task', 'g', 'running', '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z', 'workspace', 'gone-task', 2)`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// List excludes the archived project's task…
	var tasks []map[string]any
	getRaw(t, srv.URL+"/api/tasks", &tasks)
	if len(tasks) != 1 || tasks[0]["externalId"] != "kept-task" {
		t.Fatalf("tasks list = %v, want only kept-task", tasks)
	}
	// …but it stays reachable by id (reversible).
	resp, err := http.Get(srv.URL + "/api/tasks/20")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/tasks/20 (archived project) = %d, want 200 (reachable by id)", resp.StatusCode)
	}
}

func TestArchivedProjectExcludedFromAnalytics(t *testing.T) {
	srv := archiveScopeServer(t)

	// breakdown by project → only the kept project.
	var byProject []map[string]any
	getRaw(t, srv.URL+"/api/stats/breakdown?by=project", &byProject)
	for _, row := range byProject {
		if row["key"] == "gone" {
			t.Error("breakdown by project must exclude the archived project")
		}
	}

	// breakdown by agent → 'debugger' (kept) present, 'tech-lead' (archived) gone.
	var byAgent []map[string]any
	getRaw(t, srv.URL+"/api/stats/breakdown?by=agent", &byAgent)
	seen := map[string]bool{}
	for _, row := range byAgent {
		if k, ok := row["key"].(string); ok {
			seen[k] = true
		}
	}
	if !seen["debugger"] {
		t.Error("kept project's 'debugger' runs should still appear")
	}
	if seen["tech-lead"] {
		t.Error("archived project's 'tech-lead' runs must be excluded from analytics")
	}
}
