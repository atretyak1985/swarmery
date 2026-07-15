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

func openMessageTestDB(t *testing.T) *api.Handler {
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

func postMessage(t *testing.T, h *api.Handler, id, bodyJSON string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/message", strings.NewReader(bodyJSON))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.PostSessionMessage(w, req)
	return w
}

func TestPostSessionMessage_EmptyText(t *testing.T) {
	h := openMessageTestDB(t)
	w := postMessage(t, h, "1", `{"text":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty text, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestPostSessionMessage_NotFound(t *testing.T) {
	h := openMessageTestDB(t)
	w := postMessage(t, h, "9999", `{"text":"hello"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestPostSessionMessage_LiveProcRejected(t *testing.T) {
	h := openMessageTestDB(t)
	// A bound, running process — a real terminal owns the transcript.
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, cwd, proc_state, started_at, source)
		VALUES (1, 1, 'live-uuid', 'active', '/tmp', 'running', '2024-01-01T00:00:00Z', 'jsonl')`)
	w := postMessage(t, h, "1", `{"text":"hello"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for live process, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestPostSessionMessage_NoCwd(t *testing.T) {
	h := openMessageTestDB(t)
	// No live process (proc_state dead) but no cwd to resume in → 409 (cwd),
	// which also proves an "active" time-status alone no longer blocks a send.
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, proc_state, started_at, source)
		VALUES (2, 1, 'nocwd-uuid', 'active', 'dead', '2024-01-01T00:00:00Z', 'jsonl')`)
	w := postMessage(t, h, "2", `{"text":"hello"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for missing cwd, got %d; body: %s", w.Code, w.Body.String())
	}
}

func cancelMessage(t *testing.T, h *api.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/message/cancel", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.CancelSessionMessage(w, req)
	return w
}

func TestCancelSessionMessage_NotFound(t *testing.T) {
	h := openMessageTestDB(t)
	w := cancelMessage(t, h, "9999")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestCancelSessionMessage_NoneInFlight(t *testing.T) {
	h := openMessageTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (3, 1, 'idle-uuid', 'completed', '2024-01-01T00:00:00Z', 'jsonl')`)
	w := cancelMessage(t, h, "3")
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 when nothing in flight, got %d; body: %s", w.Code, w.Body.String())
	}
}
