package prune

import (
	"testing"
)

// The prune runs on the store's SINGLE SQLite connection (store.Open sets
// SetMaxOpenConns(1)), so an unbounded pass over a backlog is a multi-minute
// all-or-nothing transaction that every HTTP handler and every ingest write
// queues behind, and killing it rolls the whole thing back, so a restart loop
// makes no forward progress at all. candidateSet therefore caps one pass at
// MaxSessionsPerPass sessions.
//
// This pins BOTH halves of that contract: a pass over a too-large backlog
// processes exactly the cap (reporting Capped) and leaves the remainder
// intact, and the NEXT pass drains what is left — the cap bounds a pass, it
// does not strand rows.
func TestPruneCapsSessionsPerPass(t *testing.T) {
	db := seedDB(t) // session 1 is already prunable (ended_at = oldTS)

	// remainder extras beyond the cap, so pass 1 must leave work behind.
	const remainder = 5
	extras := MaxSessionsPerPass - 1 + remainder // + session 1 = cap + remainder

	// All extras end AFTER session 1 but before the cutoff, so candidateSet's
	// `ORDER BY ended_at, id` puts session 1 first and then the extras by id,
	// which makes "who survives pass 1" deterministic.
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at)
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		SELECT 1000 + i, 1, printf('cap-%d', i), 'completed', ?, '2026-04-01T00:00:00.000Z'
		  FROM n`, extras, oldTS); err != nil {
		t.Fatalf("seed extra sessions: %v", err)
	}
	// One turn each, so there is something for the pass to actually delete.
	if _, err := db.Exec(`
		INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd)
		SELECT id, 0, 'assistant', ?, 1, 1, 0.01 FROM sessions WHERE id > 1000`,
		oldTS); err != nil {
		t.Fatalf("seed extra turns: %v", err)
	}

	prunable := func() int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM sessions
			WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < ?`, cutoff).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got, want := prunable(), int64(MaxSessionsPerPass+remainder); got != want {
		t.Fatalf("fixture has %d prunable sessions, want %d", got, want)
	}

	// Pass 1: exactly the cap, and it says so.
	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if st.Sessions != MaxSessionsPerPass {
		t.Errorf("first pass marked %d sessions, want exactly the cap %d",
			st.Sessions, MaxSessionsPerPass)
	}
	if !st.Capped {
		t.Error("st.Capped = false on a pass that hit the cap; the operator has no signal the backlog remains")
	}
	var marked int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE pruned = 1`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != MaxSessionsPerPass {
		t.Errorf("pruned=1 rows = %d, want %d", marked, MaxSessionsPerPass)
	}

	// The remainder is untouched — headers AND their raw rows.
	if got := prunable(); got != remainder {
		t.Errorf("%d sessions still prunable after the capped pass, want %d", got, remainder)
	}
	var leftoverTurns int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns t
		JOIN sessions s ON s.id = t.session_id
		 WHERE s.pruned = 0 AND s.id > 1000`).Scan(&leftoverTurns); err != nil {
		t.Fatal(err)
	}
	if leftoverTurns != remainder {
		t.Errorf("turns of un-pruned sessions = %d, want %d (the cap must not delete beyond its slice)",
			leftoverTurns, remainder)
	}

	// Pass 2: the cap bounds a pass, it does not strand rows.
	st2, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if st2.Sessions != remainder {
		t.Errorf("second pass marked %d sessions, want the %d left over", st2.Sessions, remainder)
	}
	if st2.Capped {
		t.Error("st2.Capped = true on a pass that drained the backlog")
	}
	if got := prunable(); got != 0 {
		t.Errorf("%d sessions still prunable after two passes, want 0", got)
	}
}

// The exact-cap boundary: a backlog of EXACTLY MaxSessionsPerPass is fully
// drained by one pass, so it must NOT report a remainder.
//
// The old test was `Sessions >= MaxSessionsPerPass`, which cannot distinguish
// "took the cap and more is waiting" from "took the cap and that was all of
// it". At this boundary it sent the CLI operator back for a pass with nothing
// to do ("run again to continue the backlog") and made the daemon log "backlog
// remains, continuing next pass" over an empty candidate set — the Stats doc
// comment's promise that the NEXT pass still has work was literally false.
// Stats.Capped now comes from candidateProbe (cap+1 rows), which can tell the
// two apart.
func TestPruneExactCapReportsNoRemainder(t *testing.T) {
	db := seedDB(t) // session 1 is already prunable (ended_at = oldTS)

	// + session 1 = exactly MaxSessionsPerPass prunable sessions, not one more.
	extras := MaxSessionsPerPass - 1
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at)
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		SELECT 1000 + i, 1, printf('exact-%d', i), 'completed', ?, '2026-04-01T00:00:00.000Z'
		  FROM n`, extras, oldTS); err != nil {
		t.Fatalf("seed extra sessions: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd)
		SELECT id, 0, 'assistant', ?, 1, 1, 0.01 FROM sessions WHERE id > 1000`,
		oldTS); err != nil {
		t.Fatalf("seed extra turns: %v", err)
	}

	prunable := func() int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM sessions
			WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < ?`, cutoff).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got, want := prunable(), int64(MaxSessionsPerPass); got != want {
		t.Fatalf("fixture has %d prunable sessions, want exactly the cap %d", got, want)
	}

	st, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if st.Sessions != MaxSessionsPerPass {
		t.Errorf("pass marked %d sessions, want the full backlog of %d",
			st.Sessions, MaxSessionsPerPass)
	}
	if st.Capped {
		t.Error("st.Capped = true on a backlog of exactly MaxSessionsPerPass; " +
			"the pass drained it, so both callers would tell the operator to run again for nothing")
	}
	if got := prunable(); got != 0 {
		t.Errorf("%d sessions still prunable after the pass, want 0", got)
	}

	// And the corollary at cap+1: one more waiting row IS a remainder.
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at)
		VALUES (2001, 1, 'exact-plus-one', 'completed', ?, '2026-04-01T00:00:00.000Z')`,
		oldTS); err != nil {
		t.Fatalf("seed the cap+1 session: %v", err)
	}
	// Re-prune from a clean slate so the candidate set is cap+1, not 1.
	if _, err := db.Exec(`UPDATE sessions SET pruned = 0 WHERE ended_at < ?`, cutoff); err != nil {
		t.Fatalf("reset pruned flags: %v", err)
	}
	if got, want := prunable(), int64(MaxSessionsPerPass+1); got != want {
		t.Fatalf("fixture has %d prunable sessions, want %d", got, want)
	}
	st2, err := Run(db, cutoff, false)
	if err != nil {
		t.Fatalf("cap+1 pass: %v", err)
	}
	if st2.Sessions != MaxSessionsPerPass {
		t.Errorf("cap+1 pass marked %d sessions, want the cap %d", st2.Sessions, MaxSessionsPerPass)
	}
	if !st2.Capped {
		t.Error("st2.Capped = false with one session beyond the cap still waiting")
	}
}

// DryRun reports the same bounded slice AND the same remainder signal as the
// real pass would. This is the boundary behind the README's sizing warning: an
// operator must not read `sessions marked: 300` as "the backlog is 300" — the
// Capped flag is the only thing distinguishing a drained backlog from a slice
// of a huge one, so it has to be right under --dry-run too.
func TestPruneDryRunCapBoundary(t *testing.T) {
	db := seedDB(t)

	// Exactly the cap: the dry run must NOT claim a remainder.
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at)
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		SELECT 1000 + i, 1, printf('dry-%d', i), 'completed', ?, '2026-04-01T00:00:00.000Z'
		  FROM n`, MaxSessionsPerPass-1, oldTS); err != nil {
		t.Fatalf("seed extra sessions: %v", err)
	}
	st, err := Run(db, cutoff, true)
	if err != nil {
		t.Fatalf("dry run at the cap: %v", err)
	}
	if st.Sessions != MaxSessionsPerPass {
		t.Errorf("dry run counted %d sessions, want %d", st.Sessions, MaxSessionsPerPass)
	}
	if st.Capped {
		t.Error("dry run at exactly the cap reported a remainder that does not exist")
	}

	// One more waiting → the dry run must flag that its number is a slice.
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at)
		VALUES (2001, 1, 'dry-plus-one', 'completed', ?, '2026-04-01T00:00:00.000Z')`,
		oldTS); err != nil {
		t.Fatalf("seed the cap+1 session: %v", err)
	}
	st2, err := Run(db, cutoff, true)
	if err != nil {
		t.Fatalf("dry run past the cap: %v", err)
	}
	if st2.Sessions != MaxSessionsPerPass {
		t.Errorf("dry run counted %d sessions, want the capped %d", st2.Sessions, MaxSessionsPerPass)
	}
	if !st2.Capped {
		t.Error("dry run past the cap did not flag that its count is a bounded slice — " +
			"exactly how an operator mis-sizes a backlog")
	}

	// A dry run writes nothing, cap or no cap.
	var marked int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE pruned = 1`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Errorf("dry run marked %d sessions pruned, want 0", marked)
	}
}
