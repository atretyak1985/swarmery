package approvals

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// A daemon restart, in this package's terms: the row survives in the DB, the
// waiter map does not. A fresh Service over the same DB is exactly that.

// TestHealStaleExpiresPreBootPending: the boot heal expires a request that
// was pending before the process started, the same way a sweeper expiry does
// (permission_resolved event, session leaves waiting_approval, WS
// permission_resolved) but stamped resolved_via 'restart' with the restart
// reason — and leaves a request minted AFTER boot alone, since that one has
// a live waiter somewhere.
func TestHealStaleExpiresPreBootPending(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-heal")
	bus := ingest.NewBus()
	notes, cancel := bus.Subscribe(32)
	defer cancel()

	// Controllable clock: orphan requested at t0, daemon restarts at t0+1m,
	// a fresh request arrives at t0+2m.
	t0 := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	now := t0
	clock := func() time.Time { return now }

	previous := New(db, nil, Options{Now: clock})
	orphanID, _, _, err := previous.Open(hookInput(t, "uuid-heal", "Bash", "git push origin main"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionStatus(t, db, sid); got != "waiting_approval" {
		t.Fatalf("session status before restart = %q, want waiting_approval", got)
	}

	bootAt := t0.Add(time.Minute)
	now = bootAt
	svc := New(db, bus, Options{Now: clock}) // the restart: empty waiter map

	healed, err := svc.HealStale(bootAt)
	if err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	if healed != 1 {
		t.Fatalf("healed = %d, want 1", healed)
	}

	var status, reason string
	var via *string
	var resolvedAt *string
	if err := db.QueryRow(
		`SELECT status, resolved_via, COALESCE(reason, ''), resolved_at FROM permission_requests WHERE id = ?`,
		orphanID).Scan(&status, &via, &reason, &resolvedAt); err != nil {
		t.Fatal(err)
	}
	if status != StatusExpired || via == nil || *via != ViaRestart || reason != RestartReason {
		t.Errorf("row = %s/%v/%q, want expired/%s/%q", status, via, reason, ViaRestart, RestartReason)
	}
	if resolvedAt == nil || *resolvedAt != bootAt.Format(tsFormat) {
		t.Errorf("resolved_at = %v, want the boot instant %s", resolvedAt, bootAt.Format(tsFormat))
	}

	// Audit trail: the same permission_resolved event a timeout writes.
	var evStatus, evPayload string
	if err := db.QueryRow(
		`SELECT COALESCE(status, ''), payload FROM events WHERE session_id = ? AND type = 'permission_resolved'`,
		sid).Scan(&evStatus, &evPayload); err != nil {
		t.Fatalf("permission_resolved event: %v", err)
	}
	if evStatus != "timeout" {
		t.Errorf("event status = %q, want timeout", evStatus)
	}
	for _, want := range []string{`"decision":"expired"`, `"via":"restart"`} {
		if !strings.Contains(evPayload, want) {
			t.Errorf("event payload %s lacks %s", evPayload, want)
		}
	}

	// waiting_approval is never sticky: the session is recomputed.
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Errorf("session still waiting_approval after the heal")
	}

	// The WS update an open dashboard needs to drop the card.
	sawResolved := false
	deadline := time.After(2 * time.Second)
	for i := 0; i < 3 && !sawResolved; i++ {
		select {
		case n := <-notes:
			if n.Type == ingest.NotePermissionResolved && n.RequestID == orphanID {
				sawResolved = true
			}
		case <-deadline:
			t.Fatal("no bus notification from the heal")
		}
	}
	if !sawResolved {
		t.Error("heal did not publish permission_resolved for the orphan")
	}

	// Idempotent: nothing is left to heal.
	if healed, err := svc.HealStale(bootAt); err != nil || healed != 0 {
		t.Errorf("second HealStale = %d, %v; want 0, nil", healed, err)
	}

	// A request minted after boot belongs to a live waiter — untouched even
	// when the heal is (wrongly) run again later.
	now = t0.Add(2 * time.Minute)
	liveID, liveCh, _, err := svc.Open(hookInput(t, "uuid-heal", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	if healed, err := svc.HealStale(bootAt); err != nil || healed != 0 {
		t.Errorf("HealStale over a post-boot row = %d, %v; want 0, nil", healed, err)
	}
	if got := requestStatus(t, db, liveID); got != StatusPending {
		t.Errorf("post-boot row status = %q, want pending", got)
	}
	if err := svc.Decide(liveID, StatusDenied, "dashboard", "cleanup"); err != nil {
		t.Fatal(err)
	}
	<-liveCh
}

// TestHealStaleSkipsResolvedRows: terminal rows older than boot are history,
// not orphans.
func TestHealStaleSkipsResolvedRows(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-heal-resolved")
	t0 := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	previous := New(db, nil, Options{Now: func() time.Time { return t0 }})
	id, ch, _, err := previous.Open(hookInput(t, "uuid-heal-resolved", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Resolve(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	<-ch

	svc := New(db, nil, Options{})
	if healed, err := svc.HealStale(t0.Add(time.Hour)); err != nil || healed != 0 {
		t.Errorf("HealStale = %d, %v; want 0, nil", healed, err)
	}
	if got := requestStatus(t, db, id); got != StatusApproved {
		t.Errorf("approved row became %q", got)
	}
}

// TestDecideRequiresLiveWaiter: a dashboard decision must reach a hook. On a
// pending row nobody is polling on (its waiter died with a previous daemon)
// Decide and Answer refuse with ErrNoWaiter and leave the row pending for the
// sweeper; the error precedence (404 → 409 → 410) is preserved, the sweeper's
// own Expire path keeps working without a waiter, and a live waiter still
// receives the decision.
func TestDecideRequiresLiveWaiter(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-decide")

	previous := New(db, nil, Options{})
	deadID, _, _, err := previous.Open(hookInput(t, "uuid-decide", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	deadAskID, _, _, err := previous.Open(askHookInput(t, "uuid-decide"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(db, nil, Options{}) // the restart

	for _, status := range []string{StatusApproved, StatusDenied, StatusResolvedElsewhere} {
		if err := svc.Decide(deadID, status, "dashboard", "x"); !errors.Is(err, ErrNoWaiter) {
			t.Errorf("Decide(%s) on a dead row: err = %v, want ErrNoWaiter", status, err)
		}
	}
	if got := requestStatus(t, db, deadID); got != StatusPending {
		t.Errorf("dead row after refused decisions: status = %q, want pending (left for the sweeper)", got)
	}

	// Answer is guarded the same way, before any answer validation runs.
	if err := svc.Answer(deadAskID, answersOf(t, `{"Pick a color":"Red","Pick fruits":["Apple"]}`)); !errors.Is(err, ErrNoWaiter) {
		t.Errorf("Answer on a dead row: err = %v, want ErrNoWaiter", err)
	}
	if got := requestStatus(t, db, deadAskID); got != StatusPending {
		t.Errorf("dead AskUserQuestion row: status = %q, want pending", got)
	}

	// Precedence: unknown id and terminal rows keep their own sentinels.
	if err := svc.Decide(999, StatusApproved, "dashboard", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	if err := svc.Expire(deadID); err != nil { // the sweeper's path needs no waiter
		t.Fatalf("Expire without a waiter: %v", err)
	}
	if got := requestStatus(t, db, deadID); got != StatusExpired {
		t.Errorf("after Expire: status = %q, want expired", got)
	}
	if err := svc.Decide(deadID, StatusApproved, "dashboard", ""); !errors.Is(err, ErrAlreadyResolved) {
		t.Errorf("Decide on an expired row: err = %v, want ErrAlreadyResolved", err)
	}

	// A live waiter: Decide resolves and wakes it.
	liveID, ch, _, err := svc.Open(hookInput(t, "uuid-decide", "Bash", "pwd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Decide(liveID, StatusApproved, "dashboard", "go"); err != nil {
		t.Fatalf("Decide with a live waiter: %v", err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusApproved || d.Reason != "go" {
			t.Errorf("decision = %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live waiter never woke")
	}
}
