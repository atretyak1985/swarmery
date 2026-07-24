package routines

import (
	"database/sql"
	"sync"
	"testing"
	"time"
)

// forceNextRun stamps next_run_at directly (bypassing the cron recompute) so a
// test can put a routine into a precise due/overdue state.
func forceNextRun(t *testing.T, s *Service, id string, at time.Time) {
	t.Helper()
	if _, err := s.DB.Exec(`UPDATE routines SET next_run_at=? WHERE id=?`, at.UTC().Format(tsFormat), id); err != nil {
		t.Fatal(err)
	}
}

func countRuns(t *testing.T, s *Service, id string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM routine_runs WHERE routine_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCatchUpRunOneWith3Missed: a run_one routine whose slot was missed 3 times
// (next_run_at 3 minutes in the past on an every-minute cron) fires EXACTLY once
// on the next tick, and next_run_at advances past now.
func TestCatchUpRunOneWith3Missed(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "ro", CronExpr: "* * * * *", Enabled: true,
		CatchUp: "run_one", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	// 3 slots missed: next_run_at = now - 3m.
	forceNextRun(t, s, r.ID, clk.now().Add(-3*time.Minute))

	s.tick()

	if got := countRuns(t, s, r.ID); got != 1 {
		t.Errorf("run_one with 3 missed slots ran %d times, want exactly 1", got)
	}
	after, _ := s.Get(r.ID)
	if !after.NextRunAt.Valid || !parseTS(after.NextRunAt.String).After(clk.now()) {
		t.Errorf("next_run_at did not advance past now: %v", after.NextRunAt)
	}
	if !after.LastRunAt.Valid {
		t.Error("last_run_at not stamped after a run")
	}
}

// TestCatchUpSkipWith3Missed: a skip routine with 3 missed slots does NOT run —
// it only advances next_run_at to the next future slot; last_run_at stays unset.
func TestCatchUpSkipWith3Missed(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "sk", CronExpr: "* * * * *", Enabled: true,
		CatchUp: "skip", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	forceNextRun(t, s, r.ID, clk.now().Add(-3*time.Minute))

	s.tick()

	if got := countRuns(t, s, r.ID); got != 0 {
		t.Errorf("skip with 3 missed slots ran %d times, want 0", got)
	}
	after, _ := s.Get(r.ID)
	if !after.NextRunAt.Valid || !parseTS(after.NextRunAt.String).After(clk.now()) {
		t.Errorf("skip did not advance next_run_at past now: %v", after.NextRunAt)
	}
	if after.LastRunAt.Valid {
		t.Errorf("skip must not stamp last_run_at, got %s", after.LastRunAt.String)
	}
}

// TestSkipSingleSlotRuns: a skip routine that is due for exactly ONE slot (not a
// backlog) runs normally — skip only drops MULTIPLE missed slots.
func TestSkipSingleSlotRuns(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "sk1", CronExpr: "* * * * *", Enabled: true,
		CatchUp: "skip", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	// Exactly one slot due: next_run_at = now (the current minute). The NEXT slot
	// (now+1m) is in the future, so this is not a backlog.
	forceNextRun(t, s, r.ID, clk.now())

	s.tick()

	if got := countRuns(t, s, r.ID); got != 1 {
		t.Errorf("skip with a single due slot ran %d times, want 1", got)
	}
}

// TestKillSwitchDisablesTick: with Enabled=false the tick admits nothing.
func TestKillSwitchDisablesTick(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	s.Enabled = false
	r := mustCreate(t, s, CreateParams{Name: "off", CronExpr: "* * * * *", Enabled: true,
		TimeoutSec: 30, Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	forceNextRun(t, s, r.ID, clk.now().Add(-1*time.Minute))

	s.tick()

	if got := countRuns(t, s, r.ID); got != 0 {
		t.Errorf("kill-switch off: ran %d times, want 0", got)
	}
}

// TestManualTriggerBypassesCron: Trigger runs a manual/webhook routine even with
// no cron and no due slot.
func TestManualTriggerBypassesCron(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "man", Enabled: true, TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	started, err := s.Trigger(r.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("Trigger reported not started")
	}
	if got := countRuns(t, s, r.ID); got != 1 {
		t.Errorf("manual trigger ran %d times, want 1", got)
	}
	runs, _ := s.Runs(r.ID)
	if runs[0].Trigger != "manual" {
		t.Errorf("trigger recorded as %q, want manual", runs[0].Trigger)
	}
}

// TestTriggerNotFound: Trigger on an unknown routine returns ErrNotFound.
func TestTriggerNotFound(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	if _, err := s.Trigger("R-ghost0", "manual"); err != ErrNotFound {
		t.Errorf("Trigger unknown: want ErrNotFound, got %v", err)
	}
}

// TestSingleFlight: while a run for a routine is live, a second Trigger is
// rejected (started=false). Uses a blocking runner + the REAL goroutine seam so
// two Triggers genuinely overlap.
func TestSingleFlight(t *testing.T) {
	block := make(chan struct{})
	sr := &stubRunner{blockCh: block}
	db := migratedTestDB(t)
	seedProject(t, db, 1, "/tmp/p")
	s := NewService(db, sr, &stubTasks{}, true) // real `go` seam
	r := mustCreate(t, s, CreateParams{
		ProjectID: sql.NullInt64{Int64: 1, Valid: true},
		Name:      "sf", Enabled: true, TimeoutSec: 60,
		Steps: mkSteps(Step{Type: StepAIPrompt, Name: "hold", Prompt: "wait"})})

	// First trigger acquires the single-flight slot and blocks inside the runner.
	started1, err := s.Trigger(r.ID, "manual")
	if err != nil || !started1 {
		t.Fatalf("first trigger: started=%v err=%v", started1, err)
	}
	// Wait until the runner is actually executing (slot held).
	waitUntil(t, func() bool { return sr.callCount() == 1 })

	// Second trigger must be rejected while the first is live.
	started2, err := s.Trigger(r.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if started2 {
		t.Error("second trigger started while first is live — single-flight breached")
	}

	close(block) // let the first run finish
	waitUntil(t, func() bool { return s.activeCount() == 0 })

	// Now a fresh trigger succeeds.
	started3, err := s.Trigger(r.ID, "manual")
	if err != nil || !started3 {
		t.Fatalf("third trigger after completion: started=%v err=%v", started3, err)
	}
	waitUntil(t, func() bool { return s.activeCount() == 0 })
}

// TestGlobalCap: with MaxConcurrent lanes busy, a further Trigger is rejected.
func TestGlobalCap(t *testing.T) {
	block := make(chan struct{})
	sr := &stubRunner{blockCh: block}
	db := migratedTestDB(t)
	for i := int64(1); i <= 4; i++ {
		seedProject(t, db, i, "/tmp/p"+itoa(i))
	}
	s := NewService(db, sr, &stubTasks{}, true)

	ids := make([]string, 0, 3)
	for i := int64(1); i <= 3; i++ {
		r := mustCreate(t, s, CreateParams{
			ProjectID: sql.NullInt64{Int64: i, Valid: true},
			Name:      "r", Enabled: true, TimeoutSec: 60,
			Steps: mkSteps(Step{Type: StepAIPrompt, Name: "hold", Prompt: "wait"})})
		ids = append(ids, r.ID)
	}

	// Fill both lanes (MaxConcurrent=2).
	if st, _ := s.Trigger(ids[0], "manual"); !st {
		t.Fatal("trigger 1 should start")
	}
	if st, _ := s.Trigger(ids[1], "manual"); !st {
		t.Fatal("trigger 2 should start")
	}
	waitUntil(t, func() bool { return sr.callCount() == 2 })

	// Third trigger is capped out.
	if st, _ := s.Trigger(ids[2], "manual"); st {
		t.Error("third trigger started beyond the global cap")
	}

	close(block)
	waitUntil(t, func() bool { return s.activeCount() == 0 })
}

// TestReentranceGuard: overlapping tick passes coalesce (the guard makes a
// concurrent tick a no-op). Hard to observe directly; we assert that a single
// due routine fires once even if tick is called twice in a row with the same
// clock (the schedule advances after the first, so the second sees nothing).
func TestTickAdvancePreventsDoubleFire(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "once", CronExpr: "* * * * *", Enabled: true,
		CatchUp: "run_one", TimeoutSec: 30,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	forceNextRun(t, s, r.ID, clk.now())

	s.tick()
	s.tick() // same clock: next_run_at already advanced past now → no second run

	if got := countRuns(t, s, r.ID); got != 1 {
		t.Errorf("routine fired %d times across two ticks at the same instant, want 1", got)
	}
}

// waitUntil polls cond up to 2s.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

var _ = sync.Mutex{} // keep sync imported if future tests need it
