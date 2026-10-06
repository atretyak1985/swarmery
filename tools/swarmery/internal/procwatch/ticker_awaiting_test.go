package procwatch_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// TestCheckAll_AwaitingReplyDeadPidCompletes: awaiting_reply is a live status,
// so the liveness pass must scan it — a dead pid flips it to completed exactly
// like an active/idle row.
func TestCheckAll_AwaitingReplyDeadPidCompletes(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 4242, "running", "/tmp/proj")
	if _, err := db.Exec(`UPDATE sessions SET status = 'awaiting_reply' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	ticker := &procwatch.Ticker{DB: db, Provider: &procwatch.FakeProvider{}} // pid 4242 is gone
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("expected [%d] changed, got %v", id, changed)
	}
	var status, state string
	if err := db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = ?`, id).Scan(&status, &state); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || state != "dead" {
		t.Errorf("status/proc_state = %q/%q, want completed/dead", status, state)
	}
}

// TestCheckAll_HeuristicMatch_BindsAwaitingReply: a pid-less awaiting_reply
// row is still eligible for the cwd-based pid binding.
func TestCheckAll_HeuristicMatch_BindsAwaitingReply(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	res, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source, cwd)
		VALUES (1, 'await-unbound', 'awaiting_reply', ?, 'jsonl', '/tmp/await')`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 777, StartTime: "Mon Jan  1 00:00:00 2024", Command: "claude", CWD: "/tmp/await"},
	}}
	if _, err := (&procwatch.Ticker{DB: db, Provider: fake}).CheckAll(time.Now()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	var pid sql.NullInt64
	if err := db.QueryRow(`SELECT pid FROM sessions WHERE id = ?`, id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if !pid.Valid || pid.Int64 != 777 {
		t.Errorf("pid = %v, want 777", pid)
	}
}
