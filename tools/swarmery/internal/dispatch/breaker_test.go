package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// projectAccount is the account key the fixture project runs under — resolved
// the way admit() resolves it, never assumed.
func projectAccount(t *testing.T, db *sql.DB) string {
	t.Helper()
	var path string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id=1`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	return runcore.QuotaAccountKey(claudeacct.Resolve(path))
}

// TestDispatchLeavesCardInTodo goes through the REAL gate: the card's account
// has an open breaker in the store. The card is held like a full pool — it stays
// in Todo, nothing is spawned, no slot and no worktree are taken — with the
// reason stamped on the row (a breakerWaitPrefix dispatch_error the board
// renders) and recorded as ONE account_breaker run event for the opening,
// however many scheduling passes re-evaluate it. When the breaker closes, the
// same card is admitted and the admission CAS clears the stamp.
func TestDispatchLeavesCardInTodo(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-paused", taskOpts{})
	account := projectAccount(t, db)
	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := runcore.OpenBreaker(db, account, store.BreakerKindAuth, claudeprobe.ReasonAccessRefused,
		store.BreakerSourceRun, opened); err != nil {
		t.Fatal(err)
	}

	// Several scheduling passes against the same opening.
	s.Schedule()
	s.Schedule()
	s.Schedule()

	if r.count() != 0 {
		t.Errorf("runner spawned %d times, want 0 — the account is paused", r.count())
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo — a paused account defers the card, it does not fail it", got)
	}
	wantMsg := "account " + account + " is paused (auth): " + claudeprobe.ReasonAccessRefused +
		", since 2026-09-30T12:00:00Z"
	if e := taskField(t, db, id, "dispatch_error"); e.String != breakerWaitPrefix+wantMsg {
		t.Errorf("dispatch_error = %q, want %q — the board must say why the card waits", e.String, breakerWaitPrefix+wantMsg)
	}
	if n := s.Slots.Count(); n != 0 {
		t.Errorf("slots held = %d, want 0 — the gate runs before the slot", n)
	}
	if n := wt.acquiredCount(); n != 0 {
		t.Errorf("worktrees acquired = %d, want 0", n)
	}
	evs := runcore.RunEvents(db, Engine, id)
	if len(evs) != 1 || evs[0].Kind != runcore.EventAccountBreaker || evs[0].Detail != wantMsg {
		t.Fatalf("run events = %+v, want exactly one %q carrying the refusal", evs, runcore.EventAccountBreaker)
	}

	// The breaker closes: the very same card is admitted on the next pass and the
	// admission CAS clears the stamp. The spawn is captured, not run, so what is
	// asserted is the admission's write and not whatever the run later records.
	if _, err := runcore.CloseBreaker(db, account, store.BreakerClosedByProbe, opened.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var spawned func()
	s.Go = func(fn func()) { spawned = fn }
	s.Schedule()
	if got := column(t, db, id); got != "in_progress" {
		t.Fatalf("column = %q after the breaker closed, want in_progress", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.Valid {
		t.Errorf("dispatch_error = %q after admission, want NULL — the CAS must clear the stamp", e.String)
	}
	if spawned == nil {
		t.Fatal("no run spawned after the breaker closed")
	}
	spawned()
	if r.count() != 1 {
		t.Errorf("runner spawned %d times after the breaker closed, want 1", r.count())
	}
}

// A NEW opening of the same account is news: the card records a second event.
func TestDispatchRecordsOneEventPerOpening(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := insertTask(t, db, "T-reopened", taskOpts{})
	account := projectAccount(t, db)
	first := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	open := func(at time.Time) {
		t.Helper()
		if err := runcore.OpenBreaker(db, account, store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
			store.BreakerSourceRun, at); err != nil {
			t.Fatal(err)
		}
	}
	open(first)
	s.Schedule()
	s.Schedule()
	if _, err := runcore.CloseBreaker(db, account, store.BreakerClosedByOperator, first.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	open(first.Add(2 * time.Hour))
	s.Schedule()
	s.Schedule()

	if n := len(runcore.RunEvents(db, Engine, id)); n != 2 {
		t.Errorf("run events = %d, want 2 — one per opening", n)
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo", got)
	}
}

// The stamp only ever overwrites nothing, its own message or the other account
// hold: a real failure on the card survives the gate, and a stale quota-wait
// stamp gives way to the account pause (and back).
func TestDispatchBreakerStampRules(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	refusal := &runcore.AccountBreakerError{Account: "work", Kind: "auth", Reason: claudeprobe.ReasonNoLogin,
		OpenedAt: "2026-09-30T12:00:00Z"}
	s.AccountCheck = func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error { return refusal }

	broken := insertTask(t, db, "T-broken", taskOpts{})
	if _, err := db.Exec(`UPDATE tasks SET dispatch_error='worktree acquire: boom' WHERE id=?`, broken); err != nil {
		t.Fatal(err)
	}
	waiting := insertTask(t, db, "T-waiting", taskOpts{})
	if _, err := db.Exec(`UPDATE tasks SET dispatch_error=? WHERE id=?`, quotaWaitPrefix+"account work: Weekly at 2% left", waiting); err != nil {
		t.Fatal(err)
	}

	s.Schedule()
	if e := taskField(t, db, broken, "dispatch_error"); e.String != "worktree acquire: boom" {
		t.Errorf("dispatch_error = %q, want the real error untouched", e.String)
	}
	if n := len(runcore.RunEvents(db, Engine, broken)); n != 1 {
		t.Errorf("run events = %d, want 1 — the history is recorded even when the stamp is not", n)
	}
	if e := taskField(t, db, waiting, "dispatch_error"); e.String != breakerWaitPrefix+refusal.Error() {
		t.Errorf("dispatch_error = %q, want the account pause over the stale quota wait", e.String)
	}

	// The breaker is gone and the quota gate refuses instead: its stamp replaces
	// the stale account pause.
	s.AccountCheck = func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error { return nil }
	low := &runcore.LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z",
		PercentLeft: 1, Floor: 10}
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error { return low }
	s.Schedule()
	if e := taskField(t, db, waiting, "dispatch_error"); e.String != quotaWaitPrefix+low.Error() {
		t.Errorf("dispatch_error = %q, want the quota wait over the stale account pause", e.String)
	}
}

// The gate is checked against the PROJECT's account, and a check that fails for
// any OTHER reason is unknown — which admits.
func TestDispatchAccountCheckSeam(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-seam", taskOpts{})
	var projectPath string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id=1`).Scan(&projectPath); err != nil {
		t.Fatal(err)
	}

	var got claudeacct.Resolution
	s.AccountCheck = func(_ context.Context, _ *sql.DB, res claudeacct.Resolution, _ time.Time) error {
		got = res
		return runcore.ErrAccountBreaker // a bare sentinel is still a refusal
	}
	s.Schedule()
	if want := claudeacct.Resolve(projectPath); got != want {
		t.Errorf("account check saw %+v, want the project's resolution %+v", got, want)
	}
	if column(t, db, id) != "todo" || r.count() != 0 {
		t.Errorf("a bare sentinel did not hold the card (column=%s spawned=%d)", column(t, db, id), r.count())
	}

	s.AccountCheck = func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error {
		return errors.New("db locked")
	}
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })
	if r.count() != 1 {
		t.Errorf("runner spawned %d times, want 1 — a failed check must not freeze the board", r.count())
	}
}

// The dispatched run's exit is read with the tail-aware classifier: an account
// the API refuses reaches the verdict hook as no-login (so run-truth opens the
// breaker), while a zero exit over the same line stays ready.
func TestAccountVerdictHookAccessRefusedExit(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	spec := RunSpec{Prompt: "p", SessionUUID: "verdict-org", Resolution: claudeacct.Resolution{Account: "nabu-org"}}

	fakeClaude(t, `echo 'working'; echo 'Your organization has disabled Claude subscription access'; exit 1`)
	_, calls, account, result := startWithVerdictHook(t, spec)
	want := claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonAccessRefused}
	if calls != 1 || account != "nabu-org" || result != want {
		t.Errorf("hook = %d calls, %q, %+v; want 1, nabu-org, %+v", calls, account, result, want)
	}

	fakeClaude(t, `echo 'Your organization has disabled Claude subscription access'; exit 0`)
	if _, _, _, result := startWithVerdictHook(t, spec); result.Status != claudeprobe.StatusReady {
		t.Errorf("zero exit: hook status = %q, want ready — a run that succeeded never trips", result.Status)
	}

	fakeClaude(t, `echo 'API Error: 529 Overloaded'; exit 1`)
	if _, _, _, result := startWithVerdictHook(t, spec); result.Status != claudeprobe.StatusUnknown {
		t.Errorf("API error: hook status = %q, want unknown — it says nothing about the account", result.Status)
	}
}
