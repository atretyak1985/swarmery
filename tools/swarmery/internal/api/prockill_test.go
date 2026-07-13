package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openKillTestDB(t *testing.T) *api.Handler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp', 'p', ?)`,
		time.Now().UTC().Format(time.RFC3339))
	return &api.Handler{DB: db}
}

func TestKillSession_NotFound(t *testing.T) {
	h := openKillTestDB(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/9999/kill", strings.NewReader(`{}`))
	req.SetPathValue("id", "9999")
	w := httptest.NewRecorder()
	h.KillSession(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestKillSession_NoPID(t *testing.T) {
	h := openKillTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'test-uuid', 'active', '2024-01-01T00:00:00Z', 'jsonl')`)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/1/kill", strings.NewReader(`{}`))
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	h.KillSession(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestKillSession_InvalidState(t *testing.T) {
	h := openKillTestDB(t)
	// Session with a PID but proc_state='dead' — not killable
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source, pid, proc_state)
		VALUES (2, 1, 'dead-uuid', 'completed', '2024-01-01T00:00:00Z', 'jsonl', 1234, 'dead')`)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/2/kill", strings.NewReader(`{}`))
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	h.KillSession(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d; body: %s", w.Code, w.Body.String())
	}
}
