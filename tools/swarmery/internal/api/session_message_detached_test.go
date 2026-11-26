package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const detachedStartedAt = "Mon Jan  2 15:04:05 2006"

// fakeDetachedKillHooks swaps killProcInfo and killSignal for the duration of
// the test: killProcInfo reports the process alive (matching command/start
// time) for the identity check, then reports it gone from the Nth call
// onward, simulating a SIGKILL that takes effect after disappearAfter polls.
// killSignal is swapped purely to avoid signalling a PID that does not exist
// in the test process — it records whether it was invoked with SIGKILL.
func fakeDetachedKillHooks(t *testing.T, disappearAfter int) (sigCalled *bool) {
	t.Helper()
	prevInfo := killProcInfo
	prevSignal := killSignal
	calls := 0
	called := false
	killProcInfo = func(pid int) (*procwatch.ProcInfo, error) {
		calls++
		if calls > disappearAfter {
			return nil, nil // gone
		}
		return &procwatch.ProcInfo{PID: pid, StartTime: detachedStartedAt, Command: "claude"}, nil
	}
	killSignal = func(pid int, sig syscall.Signal) error {
		called = true
		if sig != syscall.SIGKILL {
			t.Errorf("expected SIGKILL, got %v", sig)
		}
		return nil
	}
	t.Cleanup(func() {
		killProcInfo = prevInfo
		killSignal = prevSignal
	})
	return &called
}

// fakeStubClaudeBin points SWARMERY_CLAUDE_BIN at a trivial shell script so a
// resume that falls through to startResume does not depend on a real `claude`
// being installed (or do anything observable).
func fakeStubClaudeBin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", bin)
}

func openDetachedTestDB(t *testing.T) *Handler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp', 'p', ?)`,
		time.Now().UTC().Format(time.RFC3339))
	return &Handler{DB: db}
}

func insertDetachedSession(t *testing.T, db *sql.DB, id int64, pid int, cwd string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, status, cwd, pid, proc_started_at, proc_state, started_at, source)
		 VALUES (?, 1, ?, 'idle', ?, ?, ?, 'detached', ?, 'jsonl')`,
		id, "detached-uuid-"+strconv.FormatInt(id, 10), cwd, pid, detachedStartedAt,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func postDetachedMessage(t *testing.T, h *Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/message", strings.NewReader(`{"text":"hello"}`))
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.PostSessionMessage(w, req)
	return w
}

// Branch 1: detached + identity match → SIGKILL, the process disappears
// within the poll window, and the message proceeds to the ordinary
// headless-resume path (started).
func TestPostSessionMessage_DetachedTakeoverStartsResume(t *testing.T) {
	fakeStubClaudeBin(t)
	called := fakeDetachedKillHooks(t, 1) // gone on the 2nd poll
	h := openDetachedTestDB(t)
	cwd := t.TempDir()
	insertDetachedSession(t, h.DB, 1, 4242, cwd)

	w := postDetachedMessage(t, h, "1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", w.Code, w.Body.String())
	}
	if !*called {
		t.Error("expected the detached process to be signalled")
	}
	var status, procState string
	if err := h.DB.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = 1`).Scan(&status, &procState); err != nil {
		t.Fatal(err)
	}
	if procState != procwatch.StateDead {
		t.Errorf("proc_state = %q, want dead", procState)
	}
	if status != "completed" {
		t.Errorf("status = %q, want completed (not 'killed' — ingest must keep tracking the resumed transcript)", status)
	}
}

// Branch 2: detached but the recorded start time no longer matches (PID was
// recycled) → 409, and the process must NOT be signalled.
func TestPostSessionMessage_DetachedIdentityMismatchRefuses(t *testing.T) {
	fakeStubClaudeBin(t)
	prevInfo := killProcInfo
	prevSignal := killSignal
	signalled := false
	killProcInfo = func(pid int) (*procwatch.ProcInfo, error) {
		return &procwatch.ProcInfo{PID: pid, StartTime: "Tue Nov 25 00:00:00 2026", Command: "claude"}, nil
	}
	killSignal = func(pid int, sig syscall.Signal) error {
		signalled = true
		return nil
	}
	t.Cleanup(func() { killProcInfo = prevInfo; killSignal = prevSignal })

	h := openDetachedTestDB(t)
	cwd := t.TempDir()
	insertDetachedSession(t, h.DB, 2, 4343, cwd)

	w := postDetachedMessage(t, h, "2")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a mismatched start time, got %d; body: %s", w.Code, w.Body.String())
	}
	if signalled {
		t.Error("a PID-reuse mismatch must never be signalled")
	}
	var procState string
	if err := h.DB.QueryRow(`SELECT proc_state FROM sessions WHERE id = 2`).Scan(&procState); err != nil {
		t.Fatal(err)
	}
	if procState != procwatch.StateDetached {
		t.Errorf("proc_state = %q, want unchanged detached", procState)
	}
}

// Branch 3: identity matches and the process is signalled, but it never
// actually exits within the poll window → 409, and the resume never starts.
func TestPostSessionMessage_DetachedNeverExitsRefuses(t *testing.T) {
	fakeStubClaudeBin(t)
	prevInfo := killProcInfo
	prevSignal := killSignal
	prevTimeout := detachedTakeoverTimeout
	prevPoll := detachedTakeoverPoll
	killProcInfo = func(pid int) (*procwatch.ProcInfo, error) {
		// Always alive, however many times it's polled.
		return &procwatch.ProcInfo{PID: pid, StartTime: detachedStartedAt, Command: "claude"}, nil
	}
	killSignal = func(pid int, sig syscall.Signal) error { return nil }
	detachedTakeoverTimeout = 50 * time.Millisecond
	detachedTakeoverPoll = 10 * time.Millisecond
	t.Cleanup(func() {
		killProcInfo = prevInfo
		killSignal = prevSignal
		detachedTakeoverTimeout = prevTimeout
		detachedTakeoverPoll = prevPoll
	})

	h := openDetachedTestDB(t)
	cwd := t.TempDir()
	insertDetachedSession(t, h.DB, 3, 4444, cwd)

	w := postDetachedMessage(t, h, "3")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when the process never exits, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "still alive") {
		t.Errorf("expected a 'still alive' explanation, got: %s", w.Body.String())
	}
	var procState string
	if err := h.DB.QueryRow(`SELECT proc_state FROM sessions WHERE id = 3`).Scan(&procState); err != nil {
		t.Fatal(err)
	}
	if procState != procwatch.StateDetached {
		t.Errorf("proc_state = %q, want unchanged detached — nothing to resume if the kill never took", procState)
	}
}
