package ingest

import (
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// TestStatusRecompute_LivenessOverride verifies that RecomputeStatuses never
// fast-forwards a session to "completed" while procwatch believes its process
// is still alive (running/orphaned). A live-but-silent session must cap at
// "idle" so the dashboard stops reporting "Done" for a process that is still
// running. Untracked or dead sessions keep the pure time-based fallback.
func TestStatusRecompute_LivenessOverride(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, first_seen) VALUES (?, ?, ?)
		 ON CONFLICT(path) DO NOTHING`, "/tmp/live-proj", "-tmp-live-proj", "2026-07-12T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var projID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path='/tmp/live-proj'`).Scan(&projID); err != nil {
		t.Fatal(err)
	}

	// All four have been silent for 45 min (past the 30-min idle→completed line).
	insert := func(uuid, status, procState string) int64 {
		var ps interface{}
		if procState != "" {
			ps = procState
		}
		r, err := db.Exec(
			`INSERT INTO sessions (project_id, session_uuid, status, started_at, ended_at, proc_state)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			projID, uuid, status,
			now.Add(-2*time.Hour).Format(time.RFC3339),
			now.Add(-45*time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"),
			ps)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}

	running := insert("s-running", "idle", procwatch.StateRunning)   // alive → stays idle
	orphaned := insert("s-orphaned", "active", procwatch.StateOrphaned) // alive → capped at idle
	dead := insert("s-dead", "idle", procwatch.StateDead)            // not alive → completed
	untracked := insert("s-untracked", "idle", "")                  // no liveness info → completed

	if _, err := RecomputeStatuses(db, Thresholds{}, now); err != nil {
		t.Fatal(err)
	}

	want := map[int64]string{
		running:   "idle",
		orphaned:  "idle",
		dead:      "completed",
		untracked: "completed",
	}
	for id, w := range want {
		var got string
		if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("session %d status = %q, want %q", id, got, w)
		}
	}
}
