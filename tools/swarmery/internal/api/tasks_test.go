package api

// phase 3.5: workspaces — tasks endpoints + session task attribution.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// taskServer seeds a minimal workspace-linked world directly in SQL:
// one project, one workspace, one card-task, two sessions (one explicit
// link, one heuristic), and a priced turn on each session.
func taskServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
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
	exec(`INSERT INTO workspaces (id, slug, root_path, code_path, project_id, display_name)
	      VALUES (1, 'alpha', '/ws/alpha', '/work/alpha', 1, 'Alpha')`)
	exec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at,
	                         source, external_id, workspace_id)
	      VALUES (7, 1, 'ship the feature', 'the goal line', 'running',
	              '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z', 'workspace',
	              '2026-07-10-ship-the-feature', 1)`)
	exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at) VALUES
	      (1, 1, 'aaaaaaaa-0000-4000-8000-000000000001', 'completed', '2026-07-10T10:00:00Z', '2026-07-10T11:00:00Z'),
	      (2, 1, 'bbbbbbbb-0000-4000-8000-000000000002', 'completed', '2026-07-10T12:00:00Z', '2026-07-10T13:00:00Z'),
	      (3, 1, 'cccccccc-0000-4000-8000-000000000003', 'completed', '2026-07-10T14:00:00Z', '2026-07-10T15:00:00Z')`)
	exec(`INSERT INTO turns (session_id, seq, role, started_at, cost_usd) VALUES
	      (1, 1, 'assistant', '2026-07-10T10:00:00Z', 1.25),
	      (2, 1, 'assistant', '2026-07-10T12:00:00Z', 0.75)`)
	exec(`INSERT INTO task_sessions (task_id, session_id, link_source, confidence) VALUES
	      (7, 1, 'explicit', NULL),
	      (7, 2, 'heuristic', 0.8)`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestListTasks(t *testing.T) {
	srv := taskServer(t)

	var tasks []map[string]any
	getJSON(t, srv.URL+"/api/tasks", &tasks)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	tk := tasks[0]
	if tk["externalId"] != "2026-07-10-ship-the-feature" || tk["workspaceSlug"] != "alpha" ||
		tk["projectSlug"] != "alpha" || tk["outcome"] != "active" {
		t.Errorf("task summary = %v", tk)
	}
	if tk["sessions"].(float64) != 2 {
		t.Errorf("sessions = %v, want 2", tk["sessions"])
	}
	if tk["costUsd"].(float64) != 2.0 {
		t.Errorf("costUsd = %v, want 2.0 (Σ linked sessions)", tk["costUsd"])
	}
}

func TestGetTask(t *testing.T) {
	srv := taskServer(t)

	for _, id := range []string{"7", "2026-07-10-ship-the-feature"} {
		var d map[string]any
		getJSON(t, srv.URL+"/api/tasks/"+id, &d)
		if d["title"] != "ship the feature" || d["goal"] != "the goal line" {
			t.Errorf("task %s: detail = %v", id, d)
		}
		links := d["sessionLinks"].([]any)
		if len(links) != 2 {
			t.Fatalf("task %s: sessionLinks = %d, want 2", id, len(links))
		}
		first := links[0].(map[string]any)
		if first["linkSource"] != "explicit" || first["costUsd"].(float64) != 1.25 {
			t.Errorf("task %s: first link = %v", id, first)
		}
		second := links[1].(map[string]any)
		if second["linkSource"] != "heuristic" || second["confidence"].(float64) != 0.8 {
			t.Errorf("task %s: second link = %v", id, second)
		}
	}

	resp, err := http.Get(srv.URL + "/api/tasks/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing task: status %d, want 404", resp.StatusCode)
	}
}

func TestSessionTaskAttribution(t *testing.T) {
	srv := taskServer(t)

	var page struct {
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/sessions", &page)
	sessions := page.Sessions
	byID := map[float64]map[string]any{}
	for _, s := range sessions {
		byID[s["id"].(float64)] = s
	}
	if s := byID[1]; s["taskExternalId"] != "2026-07-10-ship-the-feature" || s["taskLinkSource"] != "explicit" {
		t.Errorf("session 1 attribution = %v/%v", s["taskExternalId"], s["taskLinkSource"])
	}
	if s := byID[2]; s["taskLinkSource"] != "heuristic" || s["taskConfidence"].(float64) != 0.8 {
		t.Errorf("session 2 attribution = %v/%v", s["taskLinkSource"], s["taskConfidence"])
	}
	if s := byID[3]; s["taskId"] != nil {
		t.Errorf("session 3 must stay unlinked, got taskId=%v", s["taskId"])
	}
}
