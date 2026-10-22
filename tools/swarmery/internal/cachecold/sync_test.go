package cachecold

import (
	"database/sql"
	"testing"
	"time"
)

type coldRow struct {
	since, kind string
	footprint   int64
	cleared     sql.NullString
}

func readRow(t *testing.T, db *sql.DB, uuid string) (coldRow, bool) {
	t.Helper()
	var r coldRow
	err := db.QueryRow(`SELECT since, kind, footprint, cleared_at FROM session_cache_cold WHERE session_uuid = ?`, uuid).
		Scan(&r.since, &r.kind, &r.footprint, &r.cleared)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// TestSyncUpsertsAndClears drives the tick with a fake clock: no row while the
// cache is warm, a row once the TTL passes, no rewrite on an unchanged tick, a
// clear when a new turn lands, and a re-open on the next cold spell.
func TestSyncUpsertsAndClears(t *testing.T) {
	db := openDB(t)
	cfg := testConfig()
	id := addSession(t, db, sess{uuid: "s1", procState: "running"})
	lastEnd := t0
	addTurn(t, db, id, 0, "assistant", lastEnd, 200_000)

	// 30 min idle: still warm.
	res, err := Sync(db, t0.Add(30*time.Minute), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Opened)+len(res.Cleared) != 0 {
		t.Fatalf("warm tick changed rows: %+v", res)
	}
	if _, ok := readRow(t, db, "s1"); ok {
		t.Fatal("warm session got a row")
	}

	// 61 min idle: cold.
	res, err = Sync(db, t0.Add(61*time.Minute), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Opened) != 1 || res.Opened[0] != id {
		t.Fatalf("cold tick Opened = %v, want [%d]", res.Opened, id)
	}
	r, ok := readRow(t, db, "s1")
	if !ok || r.since != "2026-10-22T13:00:00Z" || r.kind != "subscription" || r.footprint != 200_000 || r.cleared.Valid {
		t.Fatalf("row after cold tick = %+v (ok=%v)", r, ok)
	}

	// Next tick, nothing changed: no rewrite, nothing reported.
	res, err = Sync(db, t0.Add(62*time.Minute), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Opened)+len(res.Cleared) != 0 {
		t.Fatalf("unchanged tick reported %+v", res)
	}

	// A new turn arrives: the tick clears the row.
	addTurn(t, db, id, 1, "user", t0.Add(63*time.Minute), 0)
	res, err = Sync(db, t0.Add(64*time.Minute), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cleared) != 1 || res.Cleared[0] != id || len(res.Opened) != 0 {
		t.Fatalf("new-turn tick = %+v", res)
	}
	if r, _ := readRow(t, db, "s1"); r.cleared.String != "2026-10-22T13:04:00Z" {
		t.Fatalf("cleared_at = %q", r.cleared.String)
	}

	// The reply lands and the session goes cold again: the row re-opens.
	addTurn(t, db, id, 2, "assistant", t0.Add(65*time.Minute), 210_000)
	res, err = Sync(db, t0.Add(3*time.Hour), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Opened) != 1 {
		t.Fatalf("second cold spell Opened = %v", res.Opened)
	}
	r, _ = readRow(t, db, "s1")
	if r.cleared.Valid || r.footprint != 210_000 || r.since != "2026-10-22T14:05:00Z" {
		t.Fatalf("re-opened row = %+v", r)
	}

	// The session ends: the row clears.
	if _, err := db.Exec(`UPDATE sessions SET status = 'completed', proc_state = 'dead' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	res, err = Sync(db, t0.Add(4*time.Hour), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cleared) != 1 {
		t.Fatalf("ended session not cleared: %+v", res)
	}
}
