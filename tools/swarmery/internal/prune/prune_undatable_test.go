package prune

import (
	"testing"
)

// Regression: a pruned session carrying an event with an EMPTY ts used to abort
// the entire prune. SQLite's date(”) is NULL, daily_rollups.day is NOT NULL, so
// the rollup INSERT failed with "NOT NULL constraint failed: daily_rollups.day"
// and the transaction rolled back — nothing was ever pruned. Observed on a real
// 872MB store: 4 'unknown' events out of 43k had ts=”.
//
// The row must still be DELETED (retention is the point); only its contribution
// to the day's aggregate is dropped, because it cannot be attributed to a day.
//
// "Not failing" is only half the contract. The other half is that an undatable
// row must not be MISATTRIBUTED: a guard written as
// COALESCE(date(t.started_at,'localtime'), date('now')) would satisfy every
// "did not abort / no NULL day" assertion while silently folding these tokens
// into an arbitrary day. So this test also pins the datable aggregate to the
// exact numbers TestPruneRun asserts on a fixture WITHOUT undatable rows —
// 130 / 70 / 0.5 on localDayOf(oldTS) — which only holds if the undatable rows
// contributed to no day at all.
func TestPruneSkipsUndatableRowsInsteadOfFailing(t *testing.T) {
	db := seedDB(t)

	// Every shape of unparseable timestamp the claim covers, on the prunable
	// session (id 1). A literal NULL is deliberately absent: turns.started_at
	// and events.ts are both NOT NULL in 0001_init.sql, so a NULL row cannot
	// exist to be tested — SQLite's date() maps all of these to NULL anyway,
	// which is the predicate the rollup actually guards on.
	events := []struct{ id, ts string }{
		{"99", ""},
		{"98", "not-a-date"},
		{"97", "   "},
	}
	for _, e := range events {
		if _, err := db.Exec(`INSERT INTO events (id, session_id, ts, type, status, dedup_key)
			VALUES (?, 1, ?, 'unknown', NULL, ?)`, e.id, e.ts, "e"+e.id); err != nil {
			t.Fatal(err)
		}
	}
	// Same hazard on the token grain. Non-zero tokens/cost are the point: if a
	// guard ever bucketed them into some day, the aggregate assertion below
	// would move off 130/70/0.5.
	turns := []struct {
		seq int
		ts  string
	}{{99, ""}, {98, "not-a-date"}, {97, "   "}}
	for _, tr := range turns {
		if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd)
			VALUES (1, ?, 'assistant', ?, 7, 7, 0.07)`, tr.seq, tr.ts); err != nil {
			t.Fatal(err)
		}
	}

	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("Run with undatable rows: %v (the rollup INSERT must skip them, not abort)", err)
	}
	if st.Sessions != 1 {
		t.Errorf("st.Sessions = %d, want 1", st.Sessions)
	}
	// 3 original + 3 undatable events, 2 original + 3 undatable turns: all deleted.
	if st.Events != 6 {
		t.Errorf("st.Events = %d, want 6 (the undatable events are still deleted)", st.Events)
	}
	if st.Turns != 5 {
		t.Errorf("st.Turns = %d, want 5 (the undatable turns are still deleted)", st.Turns)
	}
	if st.RollupRows == 0 {
		t.Error("st.RollupRows = 0, want the datable rows still rolled up")
	}
	// The deleted-but-unrolled rows are COUNTED, not silent: without these an
	// ingest regression writing bad timestamps at volume would leave no trace
	// (the NOT NULL abort that used to announce it is gone).
	if st.UndatableEvents != 3 {
		t.Errorf("st.UndatableEvents = %d, want 3", st.UndatableEvents)
	}
	if st.UndatableTurns != 3 {
		t.Errorf("st.UndatableTurns = %d, want 3", st.UndatableTurns)
	}
	if st.UndatableSessions != 0 {
		t.Errorf("st.UndatableSessions = %d, want 0 (session 1's started_at parses)", st.UndatableSessions)
	}

	// No NULL-day row may reach the rollups table.
	var nullDays int
	if err := db.QueryRow(`SELECT COUNT(*) FROM daily_rollups WHERE day IS NULL`).Scan(&nullDays); err != nil {
		t.Fatal(err)
	}
	if nullDays != 0 {
		t.Errorf("daily_rollups has %d NULL-day rows, want 0", nullDays)
	}

	// The datable aggregate is byte-for-byte what it is without the undatable
	// rows: excluded, not folded into some fallback day.
	day := localDayOf(t, oldTS)
	var tin, tout int64
	var cost float64
	if err := db.QueryRow(`
		SELECT SUM(tokens_in), SUM(tokens_out), SUM(cost_usd)
		  FROM daily_rollups WHERE day = ? AND project_id = 1 AND agent_id IS NULL`,
		day).Scan(&tin, &tout, &cost); err != nil {
		t.Fatal(err)
	}
	if tin != 130 || tout != 70 || cost != 0.5 {
		t.Errorf("rollup on %s = in %d, out %d, $%v; want 130/70/0.5 — undatable tokens leaked into a day",
			day, tin, tout, cost)
	}
	// ...and nowhere else either: the whole table must hold the same totals,
	// so a fallback bucketing onto date('now') cannot hide on another row.
	var allIn, allOut int64
	if err := db.QueryRow(`SELECT SUM(tokens_in), SUM(tokens_out) FROM daily_rollups`).Scan(&allIn, &allOut); err != nil {
		t.Fatal(err)
	}
	if allIn != 130 || allOut != 70 {
		t.Errorf("daily_rollups totals = in %d, out %d; want 130/70 (an undatable turn was bucketed somewhere)",
			allIn, allOut)
	}

	// The undatable rows are gone from the live tables.
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE id IN (97, 98, 99)`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("undatable events survived the prune (%d rows left)", left)
	}
}
