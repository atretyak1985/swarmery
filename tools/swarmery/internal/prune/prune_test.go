package prune

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const cutoff = "2026-05-01T00:00:00.000Z"

// oldDay/oldTS: well before the cutoff, mid-day UTC. The expected rollup day
// is computed via Go time.Local so the test is TZ-independent (the SQL uses
// date(x,'localtime'), which follows the same host zone).
const oldTS = "2026-03-10T12:00:00.000Z"

func localDayOf(t *testing.T, utc string) string {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		t.Fatal(err)
	}
	return ts.Local().Format("2006-01-02")
}

// seedDB: project 1 with
//
//	session 1 — ended long before the cutoff (prunable): 2 turns, 3 events
//	            (2 tool_calls, 1 error), 1 file_change, 1 permission_request
//	            referencing an event
//	session 2 — recent (kept intact)
//	session 3 — old but still open (ended_at NULL → never pruned)
func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/r', '-work-r', 'R', ?)`, oldTS)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at) VALUES
		(1, 1, 'r1', 'completed', ?, ?),
		(2, 1, 'r2', 'completed', '2026-06-20T12:00:00.000Z', '2026-06-20T13:00:00.000Z'),
		(3, 1, 'r3', 'active',    ?, NULL)`, oldTS, oldTS, oldTS)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd) VALUES
		(1, 0, 'assistant', ?, 100, 50, 0.5),
		(1, 1, 'assistant', ?, 30,  20, NULL),
		(2, 0, 'assistant', '2026-06-20T12:00:01.000Z', 10, 5, 0.1)`, oldTS, oldTS)
	mustExec(`INSERT INTO events (id, session_id, ts, type, status, dedup_key) VALUES
		(1, 1, ?, 'tool_call', 'ok',    'e1'),
		(2, 1, ?, 'tool_call', 'error', 'e2'),
		(3, 1, ?, 'error',     NULL,    'e3'),
		(4, 2, '2026-06-20T12:00:02.000Z', 'tool_call', 'ok', 'e4')`, oldTS, oldTS, oldTS)
	mustExec(`INSERT INTO file_changes (event_id, session_id, file_path, change_type) VALUES
		(1, 1, '/work/r/a.go', 'edit')`)
	mustExec(`INSERT INTO permission_requests (session_id, event_id, tool_name, request_json, status, requested_at) VALUES
		(1, 1, 'Bash', '{}', 'approved', ?)`, oldTS)
	return db
}

func TestPruneDryRun(t *testing.T) {
	db := seedDB(t)
	st, err := Run(db, cutoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 1 || st.Turns != 2 || st.Events != 3 || st.FileChanges != 1 {
		t.Errorf("dry-run counts = %+v, want sessions 1 / turns 2 / events 3 / fc 1", st)
	}
	var turns int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 3 {
		t.Errorf("dry-run must not delete: turns = %d, want 3", turns)
	}
}

func TestPruneRun(t *testing.T) {
	db := seedDB(t)
	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 1 || st.RollupRows == 0 {
		t.Fatalf("stats = %+v, want 1 session pruned with rollup rows", st)
	}
	// Non-dry-run counts come from RowsAffected of the in-tx deletes.
	if st.Turns != 2 || st.Events != 3 || st.FileChanges != 1 {
		t.Errorf("deleted counts = turns %d / events %d / fc %d, want 2/3/1",
			st.Turns, st.Events, st.FileChanges)
	}
	if st.VacuumErr != nil {
		t.Errorf("VacuumErr = %v, want nil", st.VacuumErr)
	}

	// The rollup carries the pruned session's aggregates on its local day.
	day := localDayOf(t, oldTS)
	var sessions, toolCalls, errors, tin, tout int64
	var cost float64
	if err := db.QueryRow(`
		SELECT SUM(sessions), SUM(tool_calls), SUM(errors),
		       SUM(tokens_in), SUM(tokens_out), SUM(cost_usd)
		  FROM daily_rollups WHERE day = ? AND project_id = 1 AND agent_id IS NULL`,
		day).Scan(&sessions, &toolCalls, &errors, &tin, &tout, &cost); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || toolCalls != 2 || errors != 2 || tin != 130 || tout != 70 || cost != 0.5 {
		t.Errorf("rollup = sess %d, tools %d, errs %d, in %d, out %d, $%v; want 1/2/2/130/70/0.5",
			sessions, toolCalls, errors, tin, tout, cost)
	}

	// Raw rows of session 1 are gone; session 2 is intact; headers remain.
	for q, want := range map[string]int64{
		`SELECT COUNT(*) FROM turns WHERE session_id = 1`:           0,
		`SELECT COUNT(*) FROM events WHERE session_id = 1`:          0,
		`SELECT COUNT(*) FROM file_changes WHERE session_id = 1`:    0,
		`SELECT COUNT(*) FROM turns WHERE session_id = 2`:           1,
		`SELECT COUNT(*) FROM events WHERE session_id = 2`:          1,
		`SELECT COUNT(*) FROM sessions`:                             3,
		`SELECT COUNT(*) FROM sessions WHERE pruned = 1`:            1,
		`SELECT COUNT(*) FROM sessions WHERE id = 3 AND pruned = 0`: 1,
	} {
		var got int64
		if err := db.QueryRow(q).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}

	// The approval row survives with its event edge cleared (FK safety).
	var evID sql.NullInt64
	if err := db.QueryRow(`SELECT event_id FROM permission_requests WHERE session_id = 1`).Scan(&evID); err != nil {
		t.Fatal(err)
	}
	if evID.Valid {
		t.Errorf("permission_requests.event_id = %v, want NULL", evID.Int64)
	}
}

// A post-commit VACUUM failure (e.g. SQLITE_BUSY from a live daemon) must not
// make a committed prune look failed: Run returns the full stats with err nil
// and carries the vacuum error separately on Stats.VacuumErr.
func TestPruneVacuumFailureIsWarning(t *testing.T) {
	db := seedDB(t)
	orig := vacuum
	t.Cleanup(func() { vacuum = orig })
	vacuum = func(*sql.DB) error { return errors.New("database is locked (SQLITE_BUSY)") }

	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("Run must succeed when only VACUUM fails, got %v", err)
	}
	if st.VacuumErr == nil {
		t.Fatal("st.VacuumErr = nil, want the injected vacuum error")
	}
	if st.Sessions != 1 || st.Turns != 2 || st.Events != 3 || st.FileChanges != 1 || st.RollupRows == 0 {
		t.Errorf("stats = %+v, want full prune counts despite vacuum failure", st)
	}
	// The prune really committed: session 1's raw rows are gone.
	var turns int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns WHERE session_id = 1`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 0 {
		t.Errorf("turns for pruned session = %d, want 0", turns)
	}
}

func TestPruneIdempotent(t *testing.T) {
	db := seedDB(t)
	if _, err := Run(db, cutoff, false); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM daily_rollups`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 0 || st.Turns != 0 {
		t.Errorf("second run = %+v, want zero candidates", st)
	}
	var after int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM daily_rollups`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("rollup rows grew on a no-op re-run: %d → %d", before, after)
	}
}
