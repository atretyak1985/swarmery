package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
)

func postStop(t *testing.T, h *api.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/stop", strings.NewReader(``))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.StopSession(w, req)
	return w
}

func TestStopSession_NotFound(t *testing.T) {
	h := openKillTestDB(t)
	if w := postStop(t, h, "9999"); w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestStopSession_AlreadyFinished(t *testing.T) {
	h := openKillTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'done-uuid', 'completed', '2024-01-01T00:00:00Z', 'jsonl')`)
	if w := postStop(t, h, "1"); w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d; body: %s", w.Code, w.Body.String())
	}

	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (2, 1, 'killed-uuid', 'killed', '2024-01-01T00:00:00Z', 'jsonl')`)
	if w := postStop(t, h, "2"); w.Code != http.StatusConflict {
		t.Errorf("expected 409 for killed, got %d", w.Code)
	}
}

// No PID at all — Stop must still close the row (this is the case Kill 409s on).
func TestStopSession_NoPID_MarksCompleted(t *testing.T) {
	h := openKillTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (3, 1, 'zombie-uuid', 'idle', '2024-01-01T00:00:00Z', 'jsonl')`)

	if w := postStop(t, h, "3"); w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", w.Code, w.Body.String())
	}
	var status, procState string
	var endedAt string
	if err := h.DB.QueryRow(
		`SELECT status, COALESCE(proc_state,''), COALESCE(ended_at,'') FROM sessions WHERE id = 3`,
	).Scan(&status, &procState, &endedAt); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || procState != "dead" || endedAt == "" {
		t.Errorf("row = status %q proc_state %q ended_at %q, want completed/dead/non-empty", status, procState, endedAt)
	}
}

// PID known but process already dead — no signal path, still 202 + completed.
func TestStopSession_DeadProc_MarksCompleted(t *testing.T) {
	h := openKillTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source, pid, proc_state, ended_at)
		VALUES (4, 1, 'deadproc-uuid', 'active', '2024-01-01T00:00:00Z', 'jsonl', 1234, 'dead', '2024-01-01T05:00:00Z')`)

	if w := postStop(t, h, "4"); w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", w.Code, w.Body.String())
	}
	var status, endedAt string
	h.DB.QueryRow(`SELECT status, ended_at FROM sessions WHERE id = 4`).Scan(&status, &endedAt)
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	// ended_at is "last activity" — Stop must NOT overwrite an existing value.
	if endedAt != "2024-01-01T05:00:00Z" {
		t.Errorf("ended_at = %q, want preserved 2024-01-01T05:00:00Z", endedAt)
	}
}

func TestStopSession_InvalidID(t *testing.T) {
	h := openKillTestDB(t)
	if w := postStop(t, h, "abc"); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// proc_state says running but the PID no longer exists — the identity guard
// must downgrade to mark-only and still 202. (PID 2147483000 is outside any
// realistic pid_max; Info returns nil for a nonexistent process.)
func TestStopSession_VanishedProc_MarksCompleted(t *testing.T) {
	h := openKillTestDB(t)
	h.DB.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source, pid, proc_state)
		VALUES (5, 1, 'vanished-uuid', 'active', '2024-01-01T00:00:00Z', 'jsonl', 2147483000, 'running')`)

	if w := postStop(t, h, "5"); w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", w.Code, w.Body.String())
	}
	var status string
	h.DB.QueryRow(`SELECT status FROM sessions WHERE id = 5`).Scan(&status)
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
}
