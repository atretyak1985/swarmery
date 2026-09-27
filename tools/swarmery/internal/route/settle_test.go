package route

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

var settleT0 = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) sql.Result {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return res
}

func seedProject(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES (1, '/repo', 'p', 'now')`)
}

// card is one board task's columns as dispatch leaves them.
type card struct {
	status, column, dispatchErr, uuid string
	paused                            bool
}

func seedCard(t *testing.T, db *sql.DB, c card) int64 {
	t.Helper()
	seedProject(t, db)
	paused := 0
	if c.paused {
		paused = 1
	}
	res := mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, created_at, status, board_column,
		paused, dispatch_error, dispatch_session_uuid, source)
		VALUES (1, 't', 'p', 'now', ?, ?, ?, NULLIF(?, ''), ?, 'queue')`,
		c.status, c.column, paused, c.dispatchErr, c.uuid)
	id, _ := res.LastInsertId()
	return id
}

// seedSession ingests a session with one turn per cost (nil = an unpriced turn).
func seedSession(t *testing.T, db *sql.DB, uuid string, costs ...*float64) {
	t.Helper()
	seedSessionAt(t, db, uuid, settleT0, costs...)
}

// seedSessionAt is seedSession with an explicit start; it returns sessions.id.
func seedSessionAt(t *testing.T, db *sql.DB, uuid string, start time.Time, costs ...*float64) int64 {
	t.Helper()
	seedProject(t, db)
	at := start.UTC().Format(time.RFC3339)
	res := mustExec(t, db, `INSERT INTO sessions (project_id, session_uuid, status, started_at)
		VALUES (1, ?, 'completed', ?)`, uuid, at)
	sid, _ := res.LastInsertId()
	for i, c := range costs {
		mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, cost_usd)
			VALUES (?, ?, 'assistant', ?, ?)`, sid, i+1, at, c)
	}
	return sid
}

// linkStage links a session to a card the way dispatch links each stage.
func linkStage(t *testing.T, db *sql.DB, taskID, sessionID int64, source string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO task_sessions (task_id, session_id, link_source, confidence)
		VALUES (?, ?, ?, 1.0)`, taskID, sessionID, source)
}

func f(v float64) *float64 { return &v }

func recordAt(t *testing.T, db *sql.DB, surface Surface, subject, uuid string, at time.Time) {
	t.Helper()
	if err := Record(db, Row{Surface: surface, Subject: subject, SessionUUID: uuid, Mode: ModeShadow,
		Decision: Decision{Tier: TierM, Model: "sonnet", Effort: "medium"}, UsedModel: "claude-sonnet-5",
		CreatedAt: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

type outcomeCols struct {
	outcome, verify sql.NullString
	cost            sql.NullFloat64
	at              sql.NullString
}

func readCols(t *testing.T, db *sql.DB, subject, uuid string) outcomeCols {
	t.Helper()
	var c outcomeCols
	if err := db.QueryRow(`SELECT outcome, verify_status, cost_usd, outcome_at FROM route_decisions
		WHERE subject = ? AND session_uuid = ?`, subject, uuid).Scan(&c.outcome, &c.verify, &c.cost, &c.at); err != nil {
		t.Fatalf("read %s/%s: %v", subject, uuid, err)
	}
	return c
}

func TestSettle_DispatchTerminalStates(t *testing.T) {
	db := openStore(t)
	cases := []struct {
		name string
		card card
		want string // "" = stays NULL
	}{
		{"done", card{status: "done", column: "done"}, OutcomeDone},
		{"archived", card{status: "queued", column: "archived"}, OutcomeDone},
		{"review", card{status: "needs_review", column: "in_review"}, OutcomeReview},
		{"exit error is a failure", card{status: "needs_review", column: "in_review", dispatchErr: "session exited 1"}, OutcomeFailed},
		{"blocked sentinel", card{status: "queued", column: "todo", paused: true, dispatchErr: "BLOCKED: need creds"}, OutcomeBlocked},
		{"failed", card{status: "failed", column: "in_progress"}, OutcomeFailed},
		{"running", card{status: "running", column: "in_progress"}, ""},
		{"requeued after a dead process", card{status: "queued", column: "todo", dispatchErr: "dispatch process gone"}, ""},
	}
	ids := make([]int64, len(cases))
	for i, tc := range cases {
		uuid := fmt.Sprintf("uuid-%d", i)
		tc.card.uuid = uuid
		ids[i] = seedCard(t, db, tc.card)
		recordAt(t, db, SurfaceDispatch, SubjectTask(ids[i]), uuid, settleT0)
	}
	n, err := settleAt(db, settleT0.Add(time.Hour))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if want := 6; n != want {
		t.Errorf("settled %d rows, want %d", n, want)
	}
	for i, tc := range cases {
		c := readCols(t, db, SubjectTask(ids[i]), fmt.Sprintf("uuid-%d", i))
		if tc.want == "" {
			if c.outcome.Valid || c.at.Valid {
				t.Errorf("%s: outcome = %v, want NULL (not terminal)", tc.name, c.outcome)
			}
			continue
		}
		if c.outcome.String != tc.want || !c.at.Valid {
			t.Errorf("%s: outcome = %v at %v, want %q", tc.name, c.outcome, c.at, tc.want)
		}
	}
}

func TestSettle_DispatchCostAndVerdict(t *testing.T) {
	db := openStore(t)
	// Priced run with a verdict inside its window.
	priced := seedCard(t, db, card{status: "needs_review", column: "in_review", uuid: "u-priced"})
	seedSession(t, db, "u-priced", f(0.25), nil, f(0.5))
	recordAt(t, db, SurfaceDispatch, SubjectTask(priced), "u-priced", settleT0)
	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'error', '2026-09-27T11:00:00Z'), (?, ?, 'fail', '2026-09-27T10:30:00Z')`,
		SubjectTask(priced), priced, SubjectTask(priced), priced)

	// Transcript never ingested: cost stays NULL, not 0. A verdict from BEFORE
	// the run belongs to an earlier attempt and must not be copied.
	unpriced := seedCard(t, db, card{status: "done", column: "done", uuid: "u-none"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(unpriced), "u-none", settleT0)
	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'pass', '2026-09-26T10:00:00Z')`, SubjectTask(unpriced), unpriced)

	// Ingested session whose turns carry no price: SUM is NULL too.
	zero := seedCard(t, db, card{status: "done", column: "done", uuid: "u-nullturns"})
	seedSession(t, db, "u-nullturns", nil, nil)
	recordAt(t, db, SurfaceDispatch, SubjectTask(zero), "u-nullturns", settleT0)

	if _, err := settleAt(db, settleT0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := readCols(t, db, SubjectTask(priced), "u-priced")
	if !c.cost.Valid || c.cost.Float64 != 0.75 {
		t.Errorf("priced cost = %v, want 0.75", c.cost)
	}
	if c.verify.String != "fail" {
		t.Errorf("priced verdict = %v, want fail (error runs are not verdicts)", c.verify)
	}
	c = readCols(t, db, SubjectTask(unpriced), "u-none")
	if c.cost.Valid || c.verify.Valid {
		t.Errorf("unpriced: cost %v verify %v, want both NULL", c.cost, c.verify)
	}
	if c = readCols(t, db, SubjectTask(zero), "u-nullturns"); c.cost.Valid {
		t.Errorf("unpriced turns: cost %v, want NULL", c.cost)
	}
}

func TestSettle_SupersededAndDeleted(t *testing.T) {
	db := openStore(t)
	id := seedCard(t, db, card{status: "running", column: "in_progress", uuid: "u-second"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "u-first", settleT0)
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "u-second", settleT0.Add(time.Hour))
	// The first run's verdict falls in its own window; the second's does not
	// leak back into it.
	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'fail', '2026-09-27T10:30:00Z'), (?, ?, 'pass', '2026-09-27T11:30:00Z')`,
		SubjectTask(id), id, SubjectTask(id), id)
	recordAt(t, db, SurfaceDispatch, SubjectTask(9999), "u-gone", settleT0)
	recordAt(t, db, SurfacePhaseRun, SubjectPhase(8888), "u-phase-gone", settleT0)

	if _, err := settleAt(db, settleT0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c := readCols(t, db, SubjectTask(id), "u-first"); c.outcome.String != OutcomeSuperseded || c.verify.String != "fail" {
		t.Errorf("first run = %v/%v, want superseded/fail", c.outcome, c.verify)
	}
	if c := readCols(t, db, SubjectTask(id), "u-second"); c.outcome.Valid {
		t.Errorf("live run settled: %v", c.outcome)
	}
	if c := readCols(t, db, SubjectTask(9999), "u-gone"); c.outcome.String != OutcomeDeleted {
		t.Errorf("deleted card = %v", c.outcome)
	}
	if c := readCols(t, db, SubjectPhase(8888), "u-phase-gone"); c.outcome.String != OutcomeDeleted {
		t.Errorf("deleted phase = %v", c.outcome)
	}
}

func seedPhase(t *testing.T, db *sql.DB, runUUID string) int64 {
	t.Helper()
	res := mustExec(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, run_session_uuid)
		VALUES (1, 1, 'p', ?, ?)`, "/doc-"+runUUID, runUUID)
	id, _ := res.LastInsertId()
	return id
}

func TestSettle_PhaseRunFromActuals(t *testing.T) {
	db := openStore(t)
	done := seedPhase(t, db, "p-done")
	recordAt(t, db, SurfacePhaseRun, SubjectPhase(done), "p-done", settleT0)
	mustExec(t, db, `INSERT INTO phase_actuals (phase_id, session_uuid, run_state, outcome, verify_verdict, cost_usd, computed_at)
		VALUES (?, 'p-done', 'done', 'completed', 'pass', 1.5, 'now')`, done)

	// Cost unknown to the recorder stays unknown here; a blank outcome falls
	// back to the run state.
	part := seedPhase(t, db, "p-part")
	recordAt(t, db, SurfacePhaseRun, SubjectPhase(part), "p-part", settleT0)
	mustExec(t, db, `INSERT INTO phase_actuals (phase_id, session_uuid, run_state, outcome, computed_at)
		VALUES (?, 'p-part', 'partial', '', 'now')`, part)

	// Still running: no actuals, the phase's current run is this one.
	live := seedPhase(t, db, "p-live")
	recordAt(t, db, SurfacePhaseRun, SubjectPhase(live), "p-live", settleT0)

	// A later run replaced this one and no actuals were ever written for it.
	moved := seedPhase(t, db, "p-new")
	recordAt(t, db, SurfacePhaseRun, SubjectPhase(moved), "p-old", settleT0)

	n, err := settleAt(db, settleT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("settled %d, want 3", n)
	}
	c := readCols(t, db, SubjectPhase(done), "p-done")
	if c.outcome.String != "completed" || c.verify.String != "pass" || c.cost.Float64 != 1.5 {
		t.Errorf("done phase = %+v", c)
	}
	c = readCols(t, db, SubjectPhase(part), "p-part")
	if c.outcome.String != "partial" || c.verify.Valid || c.cost.Valid {
		t.Errorf("partial phase = %+v, want partial with NULL verdict and cost", c)
	}
	if c = readCols(t, db, SubjectPhase(live), "p-live"); c.outcome.Valid {
		t.Errorf("live phase settled: %+v", c)
	}
	if c = readCols(t, db, SubjectPhase(moved), "p-old"); c.outcome.String != OutcomeSuperseded {
		t.Errorf("replaced phase = %+v", c)
	}
}

func TestSettle_RefreshFillsLateVerdictThenStops(t *testing.T) {
	db := openStore(t)
	id := seedCard(t, db, card{status: "needs_review", column: "in_review", uuid: "u1"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "u1", settleT0)
	first := settleT0.Add(time.Hour)
	if n, err := settleAt(db, first); err != nil || n != 1 {
		t.Fatalf("first settle = %d, %v", n, err)
	}
	if n, _ := settleAt(db, first.Add(time.Minute)); n != 0 {
		t.Errorf("idempotent re-run changed %d rows", n)
	}
	before := readCols(t, db, SubjectTask(id), "u1")

	// Verification lands after the card reached review; cost is ingested; the
	// operator then deletes the card — which must not erase what was learned.
	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'pass', '2026-09-27T12:00:00Z')`, SubjectTask(id), id)
	seedSession(t, db, "u1", f(0.4))
	if n, err := settleAt(db, first.Add(24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("refresh = %d, %v", n, err)
	}
	mustExec(t, db, `DELETE FROM tasks WHERE id = ?`, id)
	if n, _ := settleAt(db, first.Add(25*time.Hour)); n != 0 {
		t.Errorf("deletion overwrote a known outcome (%d rows changed)", n)
	}
	c := readCols(t, db, SubjectTask(id), "u1")
	if c.outcome.String != OutcomeReview || c.verify.String != "pass" || c.cost.Float64 != 0.4 {
		t.Errorf("refreshed = %+v", c)
	}
	if c.at != before.at {
		t.Errorf("outcome_at moved on refresh: %v → %v", before.at, c.at)
	}

	// Past the window the row is no longer read at all.
	mustExec(t, db, `UPDATE route_decisions SET verify_status = NULL`)
	if n, _ := settleAt(db, first.Add(SettleRefresh+time.Hour)); n != 0 {
		t.Errorf("row re-read after the refresh window (%d changed)", n)
	}
}

func TestSettle_BatchBound(t *testing.T) {
	db := openStore(t)
	seedProject(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < SettleBatch+1; i++ {
		// No task row: every one of them settles as deleted.
		if _, err := tx.Exec(`INSERT INTO route_decisions (surface, subject, mode, signals_json, score, tier,
			pick_model, pick_effort, created_at) VALUES ('dispatch', ?, 'shadow', '{}', 0, 'S', 'haiku', 'low', ?)`,
			SubjectTask(int64(100000+i)), settleT0.Format(createdAtFormat)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := settleT0.Add(time.Hour)
	if n, err := settleAt(db, now); err != nil || n != SettleBatch {
		t.Fatalf("first call settled %d (%v), want %d", n, err, SettleBatch)
	}
	if n, err := settleAt(db, now); err != nil || n != 1 {
		t.Fatalf("second call settled %d (%v), want 1", n, err)
	}
}

// A full batch of rows that never settle must not starve the refresh window:
// a settled row's late verdict still lands.
func TestSettle_StuckRowsDoNotStarveRefresh(t *testing.T) {
	db := openStore(t)
	id := seedCard(t, db, card{status: "needs_review", column: "in_review", uuid: "u-review"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "u-review", settleT0)
	if n, err := settleAt(db, settleT0.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("first settle = %d, %v", n, err)
	}

	// SettleBatch phase runs still "running" forever: the phase's current run is
	// theirs and no actuals are ever recorded, so every one stays NULL.
	phase := seedPhase(t, db, "p-stuck")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < SettleBatch; i++ {
		if _, err := tx.Exec(`INSERT INTO route_decisions (surface, subject, session_uuid, mode, signals_json,
			score, tier, pick_model, pick_effort, created_at)
			VALUES ('phaserun', ?, 'p-stuck', 'shadow', '{}', 0, 'S', 'haiku', 'low', ?)`,
			SubjectPhase(phase), settleT0.Add(2*time.Hour).Format(createdAtFormat)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'fail', '2026-09-27T12:30:00Z')`, SubjectTask(id), id)
	if _, err := settleAt(db, settleT0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c := readCols(t, db, SubjectTask(id), "u-review"); c.verify.String != "fail" {
		t.Errorf("late verdict = %v, want fail (refresh starved by stuck rows)", c.verify)
	}
	var stuck int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_decisions WHERE outcome IS NULL`).Scan(&stuck); err != nil {
		t.Fatal(err)
	}
	if stuck != SettleBatch {
		t.Errorf("stuck rows = %d, want %d still NULL", stuck, SettleBatch)
	}
}

// A multi-stage run's cost is every stage's session, bounded by the next run.
func TestSettle_DispatchCostSumsEveryStage(t *testing.T) {
	db := openStore(t)
	id := seedCard(t, db, card{status: "needs_review", column: "in_review", uuid: "r2-s1"})

	// Run 1: two stages, then run 2 replaces it an hour later.
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "r1-s1", settleT0)
	linkStage(t, db, id, seedSessionAt(t, db, "r1-s1", settleT0.Add(time.Minute), f(0.25)), "explicit")
	linkStage(t, db, id, seedSessionAt(t, db, "r1-s2", settleT0.Add(30*time.Minute), f(0.5), nil), "explicit")

	// Run 2: two stages; a heuristic link in its window is a guess, not a stage.
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "r2-s1", settleT0.Add(time.Hour))
	linkStage(t, db, id, seedSessionAt(t, db, "r2-s1", settleT0.Add(61*time.Minute), f(2)), "explicit")
	linkStage(t, db, id, seedSessionAt(t, db, "r2-s2", settleT0.Add(90*time.Minute), f(1)), "explicit")
	linkStage(t, db, id, seedSessionAt(t, db, "guess", settleT0.Add(95*time.Minute), f(100)), "heuristic")

	// A card whose stage sessions carry no priced turn: unknown, not 0.
	empty := seedCard(t, db, card{status: "done", column: "done", uuid: "e-s1"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(empty), "e-s1", settleT0)
	linkStage(t, db, empty, seedSessionAt(t, db, "e-s1", settleT0.Add(time.Minute)), "explicit")
	linkStage(t, db, empty, seedSessionAt(t, db, "e-s2", settleT0.Add(time.Hour), nil), "explicit")

	if _, err := settleAt(db, settleT0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c := readCols(t, db, SubjectTask(id), "r1-s1"); c.outcome.String != OutcomeSuperseded || c.cost.Float64 != 0.75 {
		t.Errorf("run 1 = %v cost %v, want superseded 0.75 (both stages, not run 2's)", c.outcome, c.cost)
	}
	if c := readCols(t, db, SubjectTask(id), "r2-s1"); !c.cost.Valid || c.cost.Float64 != 3 {
		t.Errorf("run 2 cost = %v, want 3 (both stages, no heuristic link)", c.cost)
	}
	if c := readCols(t, db, SubjectTask(empty), "e-s1"); c.outcome.String != OutcomeDone || c.cost.Valid {
		t.Errorf("unpriced stages = %v cost %v, want done with NULL cost", c.outcome, c.cost)
	}
}

// A run replaced by one that left no route row (mode off) must not take the
// successor's verdict or sessions; until the successor is ingested, both stay
// unknown.
func TestSettle_SupersededWithoutNextRow(t *testing.T) {
	db := openStore(t)
	id := seedCard(t, db, card{status: "needs_review", column: "in_review", uuid: "later"})
	recordAt(t, db, SurfaceDispatch, SubjectTask(id), "early", settleT0)
	linkStage(t, db, id, seedSessionAt(t, db, "early", settleT0.Add(time.Minute), f(0.3)), "explicit")
	mustExec(t, db, `INSERT INTO verification_runs (target_key, task_id, status, started_at)
		VALUES (?, ?, 'pass', '2026-09-27T11:30:00Z')`, SubjectTask(id), id)

	if _, err := settleAt(db, settleT0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := readCols(t, db, SubjectTask(id), "early")
	if c.outcome.String != OutcomeSuperseded || c.verify.Valid || c.cost.Valid {
		t.Errorf("successor not ingested = %v/%v/%v, want superseded with NULL verdict and cost",
			c.outcome, c.verify, c.cost)
	}

	// The successor lands: its start closes this run's window.
	linkStage(t, db, id, seedSessionAt(t, db, "later", settleT0.Add(time.Hour), f(5)), "explicit")
	if _, err := settleAt(db, settleT0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c = readCols(t, db, SubjectTask(id), "early")
	if c.verify.Valid || !c.cost.Valid || c.cost.Float64 != 0.3 {
		t.Errorf("successor ingested = verdict %v cost %v, want NULL verdict and 0.3", c.verify, c.cost)
	}
}

func TestSettle_SkipsMalformedSubjects(t *testing.T) {
	db := openStore(t)
	mustExec(t, db, `INSERT INTO route_decisions (surface, subject, mode, signals_json, score, tier,
		pick_model, pick_effort, created_at) VALUES
		('dispatch', 'phase:1', 'shadow', '{}', 0, 'S', 'haiku', 'low', 'x'),
		('phaserun', 'task:1', 'shadow', '{}', 0, 'S', 'haiku', 'low', 'x'),
		('other', 'task:1', 'shadow', '{}', 0, 'S', 'haiku', 'low', 'x')`)
	if n, err := Settle(db); err != nil || n != 0 {
		t.Fatalf("Settle = %d, %v; want 0, nil", n, err)
	}
}

func TestSettle_ReportsDBError(t *testing.T) {
	db := openStore(t)
	db.Close()
	if _, err := Settle(db); err == nil {
		t.Fatal("Settle on a closed DB returned nil")
	}
}
