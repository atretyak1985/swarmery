package procwatch_test

import (
	"database/sql"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertProject(t *testing.T, db *sql.DB) {
	t.Helper()
	db.Exec(`INSERT OR IGNORE INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp', 'test', ?)`,
		time.Now().UTC().Format(time.RFC3339))
}

func insertSession(t *testing.T, db *sql.DB, pid int, state, cwd string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO sessions
		(project_id, session_uuid, status, started_at, source, pid, proc_started_at, proc_state, cwd)
		VALUES (1, ?, 'active', ?, 'jsonl', ?, 'Mon Jan  1 00:00:00 2024', ?, ?)`,
		t.Name()+"-"+strconv.Itoa(pid), time.Now().UTC().Format(time.RFC3339), pid, state, cwd)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCheckAll_RunningToDeadFastForwards(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 9999, "running", "/tmp/proj")

	ticker := &procwatch.Ticker{
		DB:       db,
		Provider: &procwatch.FakeProvider{}, // no procs — pid 9999 is gone
	}
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("expected [%d] changed, got %v", id, changed)
	}

	var status, state string
	db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = ?`, id).Scan(&status, &state)
	if status != "completed" {
		t.Errorf("expected status=completed, got %q", status)
	}
	if state != "dead" {
		t.Errorf("expected proc_state=dead, got %q", state)
	}
}

func TestCheckAll_OrphanedDetected(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 1234, "running", "/tmp/proj")

	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 1234, StartTime: "Mon Jan  1 00:00:00 2024", Command: "claude", Orphaned: true},
	}}
	ticker := &procwatch.Ticker{DB: db, Provider: fake}
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("expected [%d] changed, got %v", id, changed)
	}

	var state string
	db.QueryRow(`SELECT proc_state FROM sessions WHERE id = ?`, id).Scan(&state)
	if state != "orphaned" {
		t.Errorf("expected orphaned, got %q", state)
	}
}

func TestCheckAll_PIDReuseGuard(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 5678, "running", "/tmp/proj")
	// PID 5678 exists but with a different start time → PID was reused
	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 5678, StartTime: "Tue Feb  2 12:00:00 2025", Command: "claude"},
	}}
	ticker := &procwatch.Ticker{DB: db, Provider: fake}
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed, got %d", len(changed))
	}
	var state string
	db.QueryRow(`SELECT proc_state FROM sessions WHERE id = ?`, id).Scan(&state)
	if state != "dead" {
		t.Errorf("PID reuse: expected dead, got %q", state)
	}
}

func TestCheckAll_HeuristicMatch_UnambiguousBindsPID(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	// Insert session without PID
	res, _ := db.Exec(`INSERT INTO sessions
		(project_id, session_uuid, status, started_at, source, cwd)
		VALUES (1, 'heuristic-test', 'active', ?, 'jsonl', '/tmp/proj2')`,
		time.Now().UTC().Format(time.RFC3339))
	id, _ := res.LastInsertId()

	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 7777, StartTime: "Mon Jan  1 00:00:00 2024", Command: "claude", CWD: "/tmp/proj2"},
	}}
	ticker := &procwatch.Ticker{DB: db, Provider: fake}
	if _, err := ticker.CheckAll(time.Now()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	var pid sql.NullInt64
	db.QueryRow(`SELECT pid FROM sessions WHERE id = ?`, id).Scan(&pid)
	if !pid.Valid || pid.Int64 != 7777 {
		t.Errorf("expected pid=7777 after heuristic match, got %v", pid)
	}
}

func TestCheckAll_HeuristicMatch_AmbiguousSkipped(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	res, _ := db.Exec(`INSERT INTO sessions
		(project_id, session_uuid, status, started_at, source, cwd)
		VALUES (1, 'ambiguous-test', 'active', ?, 'jsonl', '/tmp/proj3')`,
		time.Now().UTC().Format(time.RFC3339))
	id, _ := res.LastInsertId()

	// Two claude processes in same dir — ambiguous
	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 100, StartTime: "A", Command: "claude", CWD: "/tmp/proj3"},
		{PID: 101, StartTime: "B", Command: "claude", CWD: "/tmp/proj3"},
	}}
	ticker := &procwatch.Ticker{DB: db, Provider: fake}
	if _, err := ticker.CheckAll(time.Now()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	var pid sql.NullInt64
	db.QueryRow(`SELECT pid FROM sessions WHERE id = ?`, id).Scan(&pid)
	if pid.Valid {
		t.Errorf("ambiguous match should not bind PID, got %d", pid.Int64)
	}
}

func TestCheckAll_NonClaudeCommandTreatedAsDead(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 4321, "running", "/tmp")

	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 4321, StartTime: "Mon Jan  1 00:00:00 2024", Command: "bash"},
	}}
	ticker := &procwatch.Ticker{DB: db, Provider: fake}
	if _, err := ticker.CheckAll(time.Now()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	var state string
	db.QueryRow(`SELECT proc_state FROM sessions WHERE id = ?`, id).Scan(&state)
	if state != "dead" {
		t.Errorf("non-claude command should become dead, got %q", state)
	}
}

// TestCheckAll_SteadyDeadReconcilesLiveStatus: a session left as active/idle
// while its process is already dead (ingest reactivated it via a post-death
// JSONL flush) is a "green dot next to a dead badge" inconsistency. procwatch
// must re-assert status='completed' even when proc_state does not transition —
// otherwise nothing self-heals the stuck row until it ages out 30 min later.
func TestCheckAll_SteadyDeadReconcilesLiveStatus(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 8888, "dead", "/tmp/proj") // already dead, status='active'

	ticker := &procwatch.Ticker{
		DB:       db,
		Provider: &procwatch.FakeProvider{}, // pid 8888 gone → stays dead
	}
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("expected [%d] reconciled, got %v", id, changed)
	}

	var status, state string
	db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = ?`, id).Scan(&status, &state)
	if status != "completed" {
		t.Errorf("expected status=completed for a dead process, got %q", status)
	}
	if state != "dead" {
		t.Errorf("expected proc_state=dead, got %q", state)
	}
}

// A session that finished while its process still looked alive (a headless run
// reads "orphaned": no tty) used to keep that proc_state forever — the live pass
// never revisits a finished row — so the dashboard offered to Stop a process that
// was long gone. The ticker re-checks such rows and corrects proc_state only.
func TestCheckAll_FinishedSessionWithDeadProcBecomesDead(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	completed := insertSession(t, db, 4242, "orphaned", "/tmp/proj")
	killed := insertSession(t, db, 4343, "running", "/tmp/proj")
	db.Exec(`UPDATE sessions SET status = 'completed' WHERE id = ?`, completed)
	db.Exec(`UPDATE sessions SET status = 'killed' WHERE id = ?`, killed)

	ticker := &procwatch.Ticker{DB: db, Provider: &procwatch.FakeProvider{}} // both pids gone
	changed, err := ticker.CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("expected both sessions changed, got %v", changed)
	}
	for id, wantStatus := range map[int64]string{completed: "completed", killed: "killed"} {
		var status, state string
		db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = ?`, id).Scan(&status, &state)
		if state != "dead" {
			t.Errorf("session %d: expected proc_state=dead, got %q", id, state)
		}
		if status != wantStatus {
			t.Errorf("session %d: status must stay %q, got %q", id, wantStatus, status)
		}
	}
}

func TestCheckAll_FinishedSessionWithLiveProcIsLeftAlone(t *testing.T) {
	db := openTestDB(t)
	insertProject(t, db)
	id := insertSession(t, db, 5151, "running", "/tmp/proj")
	db.Exec(`UPDATE sessions SET status = 'completed' WHERE id = ?`, id)

	fake := &procwatch.FakeProvider{Procs: []procwatch.FakeProcess{
		{PID: 5151, StartTime: "Mon Jan  1 00:00:00 2024", Command: "claude"},
	}}
	changed, err := (&procwatch.Ticker{DB: db, Provider: fake}).CheckAll(time.Now())
	if err != nil {
		t.Fatalf("CheckAll: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("expected no change, got %v", changed)
	}
	var status, state string
	db.QueryRow(`SELECT status, proc_state FROM sessions WHERE id = ?`, id).Scan(&status, &state)
	if status != "completed" || state != "running" {
		t.Errorf("expected completed/running untouched, got %s/%s", status, state)
	}
}
