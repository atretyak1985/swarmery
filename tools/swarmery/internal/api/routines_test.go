package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/routines"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// serverWithRoutines builds an httptest server with a routines service attached
// (synchronous Go seam so triggered runs execute inline), reset to nil on
// cleanup. A project row (id 1) is seeded for project-scoped routines.
func serverWithRoutines(t *testing.T) (*httptest.Server, *sql.DB, *routines.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "routines_api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// A command-only routine needs no runner/tasks, but wire the real adapter so
	// create-task paths would work; use a stub runner that never spawns claude.
	svc := routines.NewService(db, stubAPIRunner{}, NewRoutinesTaskCreator(db), true)
	svc.Go = func(fn func()) { fn() } // synchronous
	AttachRoutines(svc)
	t.Cleanup(func() { AttachRoutines(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

// stubAPIRunner satisfies routines.Runner without spawning claude.
type stubAPIRunner struct{}

func (stubAPIRunner) Run(_ context.Context, _, _, _ string) (string, error) { return "ok", nil }

func doRoutineReq(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestRoutinesCRUDLifecycle(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)

	// Create a project-scoped command routine.
	create := map[string]any{
		"projectId": 1,
		"name":      "nightly",
		"cronExpr":  "0 3 * * *",
		"catchUp":   "run_one",
		"steps": []map[string]any{
			{"type": "command", "name": "echo", "command": "echo hi"},
		},
	}
	resp, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", create)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d body %s", resp.StatusCode, data)
	}
	var made map[string]any
	json.Unmarshal(data, &made)
	id, _ := made["id"].(string)
	if id == "" {
		t.Fatalf("no id in create response: %s", data)
	}
	if made["nextRunAt"] == nil {
		t.Error("enabled cron routine should have nextRunAt")
	}

	// List includes it, and never leaks a token.
	resp, data = doRoutineReq(t, http.MethodGet, srv.URL+"/api/routines", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var list []map[string]any
	json.Unmarshal(data, &list)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if _, leaked := list[0]["webhookToken"]; leaked {
		t.Error("list leaked webhookToken")
	}

	// Patch: disable it → nextRunAt clears.
	resp, data = doRoutineReq(t, http.MethodPatch, srv.URL+"/api/routines/"+id,
		map[string]any{"cronExpr": "0 3 * * *", "enabled": false})
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d body %s", resp.StatusCode, data)
	}
	var patched map[string]any
	json.Unmarshal(data, &patched)
	if patched["nextRunAt"] != nil {
		t.Errorf("disabled routine should have null nextRunAt, got %v", patched["nextRunAt"])
	}

	// Delete.
	resp, _ = doRoutineReq(t, http.MethodDelete, srv.URL+"/api/routines/"+id, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	// Second delete → 404.
	resp, _ = doRoutineReq(t, http.MethodDelete, srv.URL+"/api/routines/"+id, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", resp.StatusCode)
	}
}

func TestRoutineCreateValidation(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"no name", map[string]any{"steps": []map[string]any{{"type": "command", "name": "c", "command": "ls"}}}},
		{"no steps", map[string]any{"name": "x"}},
		{"bad cron", map[string]any{"name": "x", "cronExpr": "not cron",
			"steps": []map[string]any{{"type": "command", "name": "c", "command": "ls"}}}},
		{"bad catchUp", map[string]any{"name": "x", "catchUp": "bogus",
			"steps": []map[string]any{{"type": "command", "name": "c", "command": "ls"}}}},
		{"unknown step type", map[string]any{"name": "x",
			"steps": []map[string]any{{"type": "nope", "name": "c"}}}},
		{"unknown project", map[string]any{"name": "x", "projectId": 999,
			"steps": []map[string]any{{"type": "command", "name": "c", "command": "ls"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status %d body %s, want 400", tc.name, resp.StatusCode, data)
			}
		})
	}
}

func TestManualRunTrigger(t *testing.T) {
	srv, db, _ := serverWithRoutines(t)
	// A command routine that writes a marker via echo (status ok).
	create := map[string]any{
		"name":    "manual",
		"catchUp": "skip",
		"steps":   []map[string]any{{"type": "command", "name": "c", "command": "true"}},
	}
	_, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", create)
	var made map[string]any
	json.Unmarshal(data, &made)
	id := made["id"].(string)

	resp, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines/"+id+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run: status %d body %s", resp.StatusCode, data)
	}
	// The synchronous Go seam means the run already completed; a run row exists.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routine_runs WHERE routine_id=? AND status='ok'`, id).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 ok run, got %d", n)
	}

	// Run history endpoint returns it.
	resp, data = doRoutineReq(t, http.MethodGet, srv.URL+"/api/routines/"+id+"/runs", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("runs: %d", resp.StatusCode)
	}
	var runs []map[string]any
	json.Unmarshal(data, &runs)
	if len(runs) != 1 || runs[0]["trigger"] != "manual" {
		t.Errorf("runs = %s", data)
	}

	// Unknown routine → 404 on run + runs.
	resp, _ = doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines/R-ghost0/run", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("run unknown = %d, want 404", resp.StatusCode)
	}
	resp, _ = doRoutineReq(t, http.MethodGet, srv.URL+"/api/routines/R-ghost0/runs", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("runs unknown = %d, want 404", resp.StatusCode)
	}
}

func TestWebhookTrigger(t *testing.T) {
	srv, db, _ := serverWithRoutines(t)
	// Create with a webhook token.
	create := map[string]any{
		"name":    "hooked",
		"webhook": true,
		"steps":   []map[string]any{{"type": "command", "name": "c", "command": "true"}},
	}
	resp, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", create)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d body %s", resp.StatusCode, data)
	}
	var made map[string]any
	json.Unmarshal(data, &made)
	id := made["id"].(string)
	token, _ := made["webhookToken"].(string)
	if token == "" {
		t.Fatalf("create with webhook=true did not return a token: %s", data)
	}
	if made["hasWebhook"] != true {
		t.Error("hasWebhook should be true")
	}

	// Correct token → 202 + a run fires.
	resp, _ = doRoutineReq(t, http.MethodPost, srv.URL+"/api/hooks/routine/"+id+"/"+token, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook correct token = %d, want 202", resp.StatusCode)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routine_runs WHERE routine_id=? AND trigger='webhook'`, id).Scan(&n)
	if n != 1 {
		t.Errorf("webhook run count = %d, want 1", n)
	}

	// Wrong token → 404 (not 403, and no run).
	resp, _ = doRoutineReq(t, http.MethodPost, srv.URL+"/api/hooks/routine/"+id+"/deadbeefdeadbeefdeadbeefdeadbeef", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("webhook wrong token = %d, want 404", resp.StatusCode)
	}

	// Unknown routine id → 404 (same opaque response).
	resp, _ = doRoutineReq(t, http.MethodPost, srv.URL+"/api/hooks/routine/R-ghost0/"+token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("webhook unknown id = %d, want 404", resp.StatusCode)
	}

	// A routine WITHOUT a webhook token cannot be triggered → 404.
	c2 := map[string]any{"name": "noweb", "steps": []map[string]any{{"type": "command", "name": "c", "command": "true"}}}
	_, d2 := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", c2)
	var m2 map[string]any
	json.Unmarshal(d2, &m2)
	id2 := m2["id"].(string)
	resp, _ = doRoutineReq(t, http.MethodPost, srv.URL+"/api/hooks/routine/"+id2+"/anytoken", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("webhook on token-less routine = %d, want 404", resp.StatusCode)
	}
}

func TestCreateTaskStepLandsBoardCard(t *testing.T) {
	srv, db, _ := serverWithRoutines(t)
	// A project-scoped routine whose only step creates a Triage board task.
	create := map[string]any{
		"projectId": 1,
		"name":      "spawner",
		"steps": []map[string]any{
			{"type": "create-task", "name": "mk", "taskTitle": "Investigate flake",
				"taskPrompt": "Look into the flaky test", "boardColumn": "triage"},
		},
	}
	_, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", create)
	var made map[string]any
	json.Unmarshal(data, &made)
	id := made["id"].(string)

	resp, _ := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines/"+id+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run: %d", resp.StatusCode)
	}
	// A board task (source='queue') landed in triage.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE source='queue' AND board_column='triage' AND title='Investigate flake'`).Scan(&n)
	if n != 1 {
		t.Errorf("create-task board card count = %d, want 1", n)
	}
}

func TestRoutines503WhenUnattached(t *testing.T) {
	AttachRoutines(nil)
	srv := testServer(t)
	resp, err := http.Get(srv.URL + "/api/routines")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /api/routines (unattached) = %d, want 503", resp.StatusCode)
	}
}
