package api

import (
	"testing"
)

// A session the operator stopped from the dashboard is 'killed' — terminal for
// ingest and procwatch alike. When the operator then resumes it
// (`claude --resume`), the new process's SessionStart is the one signal that
// the session lives again; without it the row keeps its red "killed" badge
// while the transcript grows and proc_state reads 'running'.
func TestHookSessionStartRevivesKilledSessionOnResume(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/proj', 'proj', '2026-11-17T00:00:00Z')`)
	execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source,
		pid, pid_source, proc_started_at, proc_state)
		VALUES (1, 1, 'sid-resumed', 'killed', '2026-11-17T13:17:49Z', 'both',
		30663, 'hook', 'Tue Nov 17 13:17:40 2026', 'dead')`)
	fakeClaudeProcInfo(t, "ttys003")

	h := &Handler{DB: db}
	postSessionStart(t, h, `{"session_id":"sid-resumed","pid":24402,"cwd":"/tmp/proj"}`)

	var status, procState string
	if err := db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = 1`).Scan(&status, &procState); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active — a new claude process resumed the killed session", status)
	}
	if procState != "running" {
		t.Errorf("proc_state = %q, want running", procState)
	}
}

// The SessionStart of the very process that was killed (SIGTERM ignored, then
// a /compact re-fires the hook) is not a resume: the row stays 'killed'.
func TestHookSessionStartKeepsKilledForTheSameProcess(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/proj', 'proj', '2026-11-17T00:00:00Z')`)
	// fakeClaudeProcInfo reports this exact start time for every pid.
	execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source,
		pid, pid_source, proc_started_at, proc_state)
		VALUES (1, 1, 'sid-same-proc', 'killed', '2026-11-17T13:17:49Z', 'both',
		4242, 'hook', 'Mon Jan  2 15:04:05 2006', 'dead')`)
	fakeClaudeProcInfo(t, "ttys003")

	h := &Handler{DB: db}
	postSessionStart(t, h, `{"session_id":"sid-same-proc","pid":4242,"cwd":"/tmp/proj"}`)

	var status string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "killed" {
		t.Errorf("status = %q, want killed — the same process is not a resume", status)
	}
}
