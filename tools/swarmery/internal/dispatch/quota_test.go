package dispatch

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// A card whose account is below the quota floor is held like a full pool: it
// stays in Todo, nothing is spawned and no slot is taken. The reason is surfaced
// on the card (a quotaWaitPrefix dispatch_error the board renders) and recorded
// once per reset window as a quota_wait run event. When headroom returns, the
// same card is admitted and admission's CAS clears the stamp.
func TestSchedule_LowQuotaHoldsCardInTodo(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-quota", taskOpts{})

	calls := 0
	s.QuotaCheck = func(_ *sql.DB, _ claudeacct.Resolution, _ time.Time) error {
		calls++
		return &runcore.LowQuotaError{Account: "work", Window: "five_hour", Label: "Session (5h)",
			ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 4, Floor: 10}
	}

	// Several scheduling passes in one reset window.
	s.Schedule()
	s.Schedule()
	s.Schedule()

	if calls != 3 {
		t.Errorf("quota check ran %d times, want 3 (once per pass)", calls)
	}
	if r.count() != 0 {
		t.Errorf("runner spawned %d times, want 0 — the account is below the floor", r.count())
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo — low quota defers, it does not fail the row", got)
	}
	const wantMsg = "account work: Session (5h) at 4% left (floor 10%), resets 2027-01-01T00:00:00Z"
	if e := taskField(t, db, id, "dispatch_error"); e.String != quotaWaitPrefix+wantMsg {
		t.Errorf("dispatch_error = %q, want %q — the board must say why the card waits", e.String, quotaWaitPrefix+wantMsg)
	}
	if n := s.Slots.Count(); n != 0 {
		t.Errorf("slots held = %d, want 0 — the gate runs before the slot", n)
	}
	evs := runcore.RunEvents(db, Engine, id)
	if len(evs) != 1 || evs[0].Kind != runcore.EventQuotaWait {
		t.Fatalf("run events = %+v, want exactly one %q", evs, runcore.EventQuotaWait)
	}
	if evs[0].Detail != wantMsg {
		t.Errorf("event detail = %q, want %q", evs[0].Detail, wantMsg)
	}

	// A later pass in the same window with a moved reading refreshes the stamp
	// (its own message) but adds no second event.
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.LowQuotaError{Account: "work", Window: "five_hour", Label: "Session (5h)",
			ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 2, Floor: 10}
	}
	s.Schedule()
	if e := taskField(t, db, id, "dispatch_error"); e.String != quotaWaitPrefix+"account work: Session (5h) at 2% left (floor 10%), resets 2027-01-01T00:00:00Z" {
		t.Errorf("dispatch_error not refreshed: %q", e.String)
	}
	if n := len(runcore.RunEvents(db, Engine, id)); n != 1 {
		t.Errorf("run events = %d, want still 1 in the same reset window", n)
	}

	// Headroom is back: the very same card is admitted on the next pass, and the
	// admission CAS clears the stamp. The spawn is captured, not run, so what is
	// asserted is the admission's write and not whatever the run later records.
	var spawned func()
	s.Go = func(fn func()) { spawned = fn }
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error { return nil }
	s.Schedule()
	if got := column(t, db, id); got != "in_progress" {
		t.Fatalf("column = %q after headroom returned, want in_progress", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.Valid {
		t.Errorf("dispatch_error = %q after admission, want NULL — the CAS must clear the quota stamp", e.String)
	}
	if spawned == nil {
		t.Fatal("no run spawned after headroom returned")
	}
	spawned()
	if r.count() != 1 {
		t.Errorf("runner spawned %d times after headroom returned, want 1", r.count())
	}
}

// The stamp only ever overwrites nothing or its own message: a real failure on
// the card must survive the quota gate, exactly as it survives the dependency gate.
func TestSchedule_LowQuotaNeverOverwritesARealError(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := insertTask(t, db, "T-broken", taskOpts{})
	if _, err := db.Exec(`UPDATE tasks SET dispatch_error='worktree acquire: boom' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z",
			PercentLeft: 1, Floor: 10}
	}
	s.Schedule()
	if e := taskField(t, db, id, "dispatch_error"); e.String != "worktree acquire: boom" {
		t.Errorf("dispatch_error = %q, want the real error untouched", e.String)
	}
	if n := len(runcore.RunEvents(db, Engine, id)); n != 1 {
		t.Errorf("run events = %d, want 1 — the history is recorded even when the stamp is not", n)
	}
}

// The gate is checked against the PROJECT's account — the same resolution the
// run's stages then execute under.
func TestSchedule_QuotaCheckSeesProjectResolution(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	insertTask(t, db, "T-res", taskOpts{})
	var projectPath string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id=1`).Scan(&projectPath); err != nil {
		t.Fatal(err)
	}

	var got claudeacct.Resolution
	s.QuotaCheck = func(_ *sql.DB, res claudeacct.Resolution, _ time.Time) error {
		got = res
		return runcore.ErrLowQuota // a bare sentinel is still a refusal
	}
	s.Schedule()

	if want := claudeacct.Resolve(projectPath); got != want {
		t.Errorf("quota check saw %+v, want the project's resolution %+v", got, want)
	}
}

// A check that fails for any OTHER reason is unknown, and unknown admits.
func TestSchedule_QuotaCheckErrorFailsOpen(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-open", taskOpts{})
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error { return errors.New("db locked") }

	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })
	if r.count() != 1 {
		t.Errorf("runner spawned %d times, want 1 — a failed check must not freeze the board", r.count())
	}
}
