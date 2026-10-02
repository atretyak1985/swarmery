package approvals

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/notify"
)

// notifyTestSink: an httptest receiver + generic-template Notifier that
// delivers decoded Events into a channel.
func notifyTestSink(t *testing.T) (*notify.Notifier, <-chan notify.Event) {
	t.Helper()
	got := make(chan notify.Event, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e notify.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Errorf("webhook body: %v", err)
		}
		got <- e
	}))
	t.Cleanup(srv.Close)
	n, err := notify.New(notify.Config{URL: srv.URL, Events: notify.KnownEvents})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	return n, got
}

func waitNotifyEvent(t *testing.T, ch <-chan notify.Event, typ string) notify.Event {
	t.Helper()
	select {
	case e := <-ch:
		if e.Type != typ {
			t.Fatalf("event type = %s, want %s", e.Type, typ)
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s webhook within 3s", typ)
		return notify.Event{}
	}
}

func TestOpenEmitsApprovalRequested(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-notify-open")
	n, got := notifyTestSink(t)
	svc := New(db, nil, Options{Notifier: n})

	id, ch, isNew, err := svc.Open(hookInput(t, "uuid-notify-open", "Bash", "git push origin"))
	if err != nil || !isNew {
		t.Fatalf("Open: id=%d isNew=%v err=%v", id, isNew, err)
	}
	e := waitNotifyEvent(t, got, notify.EventApprovalRequested)
	if e.RequestID != id || e.SessionID != sid || e.Tool != "Bash" || e.Project != "proj" {
		t.Errorf("event = %+v", e)
	}
	if !strings.Contains(e.Body, "git push origin") {
		t.Errorf("body = %q, want the command", e.Body)
	}
	// Clean up the waiter: deny from the dashboard side.
	if err := svc.Resolve(id, StatusDenied, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	<-ch
}

func TestExpireEmitsApprovalExpired(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-notify-exp")
	n, got := notifyTestSink(t)
	svc := New(db, nil, Options{Notifier: n})

	id, ch, _, err := svc.Open(hookInput(t, "uuid-notify-exp", "Bash", "rm -rf ."))
	if err != nil {
		t.Fatal(err)
	}
	waitNotifyEvent(t, got, notify.EventApprovalRequested)
	if err := svc.Expire(id); err != nil {
		t.Fatal(err)
	}
	e := waitNotifyEvent(t, got, notify.EventApprovalExpired)
	if e.RequestID != id || !strings.Contains(e.Title, "expired") {
		t.Errorf("event = %+v", e)
	}
	<-ch // drain the expired decision
}

// TestDenyEmitsNothingExtra: a plain dashboard deny sends no webhook (only
// requested/expired are approval events).
func TestDenyEmitsNothingExtra(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-notify-deny")
	n, got := notifyTestSink(t)
	svc := New(db, nil, Options{Notifier: n})

	id, ch, _, err := svc.Open(hookInput(t, "uuid-notify-deny", "Read", "x"))
	if err != nil {
		t.Fatal(err)
	}
	waitNotifyEvent(t, got, notify.EventApprovalRequested)
	if err := svc.Resolve(id, StatusDenied, "dashboard", "no"); err != nil {
		t.Fatal(err)
	}
	<-ch
	select {
	case e := <-got:
		t.Fatalf("unexpected webhook after deny: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestHealStaleEmitsApprovalExpired: a request orphaned by a daemon restart
// fires the same approval_expired webhook a timeout does — the receiver that
// got approval_requested from the previous daemon gets its closing event —
// with the restart reason in the body so the operator knows to answer in the
// terminal rather than look for the card.
func TestHealStaleEmitsApprovalExpired(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-notify-heal")
	t0 := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

	// Previous daemon (no sink attached: only the post-restart events matter).
	previous := New(db, nil, Options{Now: func() time.Time { return t0 }})
	id, _, _, err := previous.Open(hookInput(t, "uuid-notify-heal", "Bash", "terraform apply"))
	if err != nil {
		t.Fatal(err)
	}

	n, got := notifyTestSink(t)
	bootAt := t0.Add(time.Minute)
	svc := New(db, nil, Options{Notifier: n, Now: func() time.Time { return bootAt }})
	if healed, err := svc.HealStale(bootAt); err != nil || healed != 1 {
		t.Fatalf("HealStale = %d, %v; want 1, nil", healed, err)
	}

	e := waitNotifyEvent(t, got, notify.EventApprovalExpired)
	if e.RequestID != id || e.SessionID != sid || e.Tool != "Bash" || e.Project != "proj" {
		t.Errorf("event = %+v", e)
	}
	if !strings.Contains(e.Title, "expired") {
		t.Errorf("title = %q, want an expiry title", e.Title)
	}
	if !strings.Contains(e.Body, RestartReason) {
		t.Errorf("body = %q, want the restart reason appended", e.Body)
	}
	select {
	case extra := <-got:
		t.Fatalf("unexpected second webhook: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
