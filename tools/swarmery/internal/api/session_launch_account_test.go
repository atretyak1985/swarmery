package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func readLaunchAccount(t *testing.T, h *Handler, uuid string) string {
	t.Helper()
	var v string
	if err := h.DB.QueryRow(`SELECT launch_account FROM sessions WHERE session_uuid = ?`, uuid).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The shim's launchAccount lands in sessions.launch_account on the same UPDATE
// as the terminal identity; an older shim that sends none leaves it alone.
func TestHookSessionStartWritesLaunchAccount(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/proj', 'proj', '2026-09-29T00:00:00Z')`)
	execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source, account)
		VALUES (1, 1, 'sid-launch', 'active', '2026-09-29T00:00:00Z', 'jsonl', 'work')`)
	fakeClaudeProcInfo(t, "")
	h := &Handler{DB: db}

	postSessionStart(t, h, `{"session_id":"sid-launch","pid":4242,"cwd":"/tmp/proj","launchAccount":"default"}`)
	if got := readLaunchAccount(t, h, "sid-launch"); got != "default" {
		t.Fatalf("launch_account = %q, want default", got)
	}
	postSessionStart(t, h, `{"session_id":"sid-launch","pid":4242,"cwd":"/tmp/proj"}`)
	if got := readLaunchAccount(t, h, "sid-launch"); got != "default" {
		t.Errorf("an older shim's payload overwrote launch_account with %q", got)
	}
	rows, err := store.AccountDrift(db, 10)
	if err != nil || len(rows) != 1 || rows[0].SessionUUID != "sid-launch" {
		t.Errorf("AccountDrift = %+v %v, want the drifted session", rows, err)
	}
}

// Hook before ingest: the launch account is parked with the terminal identity
// and applied when the tail mints the row.
func TestHookSessionStartParksLaunchAccount(t *testing.T) {
	db := openTerminalTestDB(t)
	fakeClaudeProcInfo(t, "")
	const uuid = "sid-launch-parked"
	h := &Handler{DB: db}
	postSessionStart(t, h, `{"session_id":"`+uuid+`","pid":4343,"cwd":"/tmp/proj3","launchAccount":"work"}`)

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"type":"user","parentUuid":null,"isSidechain":false,"promptId":"p-1","promptSource":"typed",` +
		`"message":{"role":"user","content":"hello"},"uuid":"ev-1","timestamp":"2026-09-29T10:00:00.000Z",` +
		`"cwd":"/tmp/proj3","sessionId":"` + uuid + `","version":"2.1.170","gitBranch":"main"}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.TailFile(db, transcript, "", ingest.DefaultThresholds()); err != nil {
		t.Fatal(err)
	}
	if got := readLaunchAccount(t, h, uuid); got != "work" {
		t.Errorf("launch_account = %q, want the parked work", got)
	}
}
