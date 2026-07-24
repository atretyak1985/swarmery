package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// serverWithDispatch builds an httptest server whose api layer has a dispatcher
// attached (package-var), reset to nil on cleanup. No Todo tasks are created, so
// pause/status never trigger a real spawn. The worktree Manager is real but
// unused on these paths.
func serverWithDispatch(t *testing.T) (*httptest.Server, *sql.DB, *dispatch.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "dispatch_api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	svc := dispatch.NewService(db, dispatch.Config{
		MaxConcurrent: 2, MaxWorktrees: 4, PollInterval: time.Hour, RunTimeout: time.Minute, Enabled: true,
	}, dispatch.ClaudeRunner{}, &worktree.Manager{Git: worktree.ExecGit{}})
	AttachDispatch(svc)
	t.Cleanup(func() { AttachDispatch(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

func TestGetDispatch503WhenUnattached(t *testing.T) {
	// The default testServer attaches no dispatcher.
	AttachDispatch(nil)
	srv := testServer(t)
	resp, err := http.Get(srv.URL + "/api/dispatch")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /api/dispatch (unattached) = %d, want 503", resp.StatusCode)
	}
}

func TestGetDispatchStatus(t *testing.T) {
	srv, _, _ := serverWithDispatch(t)
	resp, err := http.Get(srv.URL + "/api/dispatch")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var st dispatch.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.MaxConcurrent != 2 || st.FreeSlots != 2 {
		t.Errorf("status = %+v", st)
	}
}

func TestPauseDispatchGlobalAndProject(t *testing.T) {
	srv, db, _ := serverWithDispatch(t)

	// Global pause.
	resp := postBody(t, srv.URL+"/api/dispatch/pause", `{"scope":"global","paused":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("global pause status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	var paused int
	if err := db.QueryRow(`SELECT paused FROM dispatch_pause WHERE scope='global'`).Scan(&paused); err != nil || paused != 1 {
		t.Errorf("global pause not persisted (paused=%d, err=%v)", paused, err)
	}

	// Project pause.
	resp = postBody(t, srv.URL+"/api/dispatch/pause", `{"scope":"project","projectId":1,"paused":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("project pause status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if err := db.QueryRow(`SELECT paused FROM dispatch_pause WHERE scope='project:1'`).Scan(&paused); err != nil || paused != 1 {
		t.Errorf("project pause not persisted (paused=%d, err=%v)", paused, err)
	}
}

func TestPauseDispatchValidation(t *testing.T) {
	srv, _, _ := serverWithDispatch(t)
	cases := []struct {
		body string
		code int
	}{
		{`{"scope":"bogus","paused":true}`, http.StatusBadRequest},
		{`{"scope":"project","paused":true}`, http.StatusBadRequest},        // missing projectId
		{`{"scope":"project","projectId":999,"paused":true}`, http.StatusBadRequest}, // unknown project
		{`not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp := postBody(t, srv.URL+"/api/dispatch/pause", tc.body)
		if resp.StatusCode != tc.code {
			t.Errorf("pause %q = %d, want %d", tc.body, resp.StatusCode, tc.code)
		}
		resp.Body.Close()
	}
}

func TestPauseDispatch503WhenUnattached(t *testing.T) {
	AttachDispatch(nil)
	srv := testServer(t)
	resp := postBody(t, srv.URL+"/api/dispatch/pause", `{"scope":"global","paused":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("pause (unattached) = %d, want 503", resp.StatusCode)
	}
}

// postBody posts a JSON string body with no Origin header (mirrors the swarmery
// hook shim / curl — passes requireLocalOrigin).
func postBody(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
