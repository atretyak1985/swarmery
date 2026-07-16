package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func hideTestServer(t *testing.T) (*httptest.Server, *sql.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hide.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen, last_activity)
		 VALUES ('/tmp/hp', '-tmp-hp', 'hp', '2026-07-13T00:00:00Z', '2026-07-13T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	res, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		 VALUES (1, 'u-hide-1', 'completed', '2026-07-13T00:00:00Z', 'jsonl')`)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id, _ := res.LastInsertId()

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, id
}

// sessionsListLen GETs /api/sessions and returns the envelope's list length.
func sessionsListLen(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(base + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	return len(page.Sessions)
}

func TestHideSessionRemovesFromListKeepsById(t *testing.T) {
	srv, _, id := hideTestServer(t)

	if n := sessionsListLen(t, srv.URL); n != 1 {
		t.Fatalf("before hide: list len = %d, want 1", n)
	}

	doJSON(t, http.MethodDelete, srv.URL+"/api/sessions/1", nil, http.StatusOK)

	if n := sessionsListLen(t, srv.URL); n != 0 {
		t.Errorf("after hide: list len = %d, want 0", n)
	}
	// Still reachable by id — the hide is reversible, not a destroy.
	resp, err := http.Get(srv.URL + "/api/sessions/" + strconv.FormatInt(id, 10))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("getSession by id after hide = %d, want 200", resp.StatusCode)
	}
}

func TestHideSessionNotFound(t *testing.T) {
	srv, _, _ := hideTestServer(t)
	doJSON(t, http.MethodDelete, srv.URL+"/api/sessions/9999", nil, http.StatusNotFound)
}
