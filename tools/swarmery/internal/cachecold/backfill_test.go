package cachecold

import (
	"database/sql"
	"testing"
	"time"
)

// insertUsage adds a main-thread assistant turn with explicit usage.
func insertUsage(t *testing.T, db *sql.DB, sessionID int64, seq int, start time.Time, tin, tcr, tcw int64, cost float64) int64 {
	t.Helper()
	ts := start.UTC().Format(time.RFC3339Nano)
	r, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read,
		tokens_cache_write, cache_write_1h_tokens, cost_usd) VALUES (?, ?, 'assistant', ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, seq, ts, ts, tin, tcr, tcw, tcw, cost)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func coldOf(t *testing.T, db *sql.DB, id int64) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT cold_restart FROM turns WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBackfillIdempotentWithFactorCounts(t *testing.T) {
	db := openDB(t)
	cfg := testConfig()
	id := addSession(t, db, sess{uuid: "s"})
	since := t0.Add(-30 * 24 * time.Hour)

	// The predecessor lies BEFORE the window; the cold turn inside it.
	insertUsage(t, db, id, 0, since.Add(-time.Hour), 10, 190_000, 10_000, 0.2)
	// 2 h later: re-wrote 0.85 of the previous 200k, read nothing — marked at
	// 0.7 and 0.8, not at 0.9.
	cold := insertUsage(t, db, id, 1, since.Add(time.Hour), 10, 0, 170_000, 2.5)
	// A warm continuation.
	warm := insertUsage(t, db, id, 2, since.Add(time.Hour+time.Minute), 10, 170_000, 1_000, 0.1)
	// A stale mark the rule no longer supports is cleared.
	if _, err := db.Exec(`UPDATE turns SET cold_restart = 1 WHERE id = ?`, warm); err != nil {
		t.Fatal(err)
	}

	res, err := Backfill(db, since, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evaluated != 2 || res.Marked != 1 || res.Changed != 2 || res.MarkedCostUSD != 2.5 {
		t.Fatalf("first run = %+v", res)
	}
	byF := map[float64]int{}
	for _, f := range res.ByFactor {
		byF[f.Factor] = f.Marked
	}
	if byF[0.7] != 1 || byF[0.8] != 1 || byF[0.9] != 0 {
		t.Errorf("ByFactor = %v", res.ByFactor)
	}
	if coldOf(t, db, cold) != 1 || coldOf(t, db, warm) != 0 {
		t.Errorf("marks: cold=%d warm=%d", coldOf(t, db, cold), coldOf(t, db, warm))
	}

	again, err := Backfill(db, since, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed != 0 || again.Marked != 1 {
		t.Errorf("re-run = %+v, want Changed 0 and the same mark", again)
	}
}
