package link

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// fakeClock is a settable clock for the TTL / freshness rules.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// noteLog collects published notes; safe across goroutines.
type noteLog struct {
	mu    sync.Mutex
	notes []ingest.Notification
}

func (l *noteLog) publish(n ingest.Notification) {
	l.mu.Lock()
	l.notes = append(l.notes, n)
	l.mu.Unlock()
}

func (l *noteLog) states(commandID int64) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, n := range l.notes {
		if n.CommandID == commandID {
			out = append(out, n.State)
		}
	}
	return out
}

type cmdEnv struct {
	db    *sql.DB
	c     *Commands
	clk   *fakeClock
	notes *noteLog
	sid   int64
	uuid  string
}

// newCmdEnv mints one mod session (through Ingest, as the mod would) and a
// Commands over the same DB with a fake clock.
func newCmdEnv(t *testing.T) *cmdEnv {
	t.Helper()
	db := testDB(t)
	uuid := "c0ffee00-0000-4000-8000-000000000001"
	mustIngest(t, db, nil, []Event{{SessionID: uuid, Cwd: "/work/ctl", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"}})
	var sid int64
	if err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, uuid).Scan(&sid); err != nil {
		t.Fatalf("session row: %v", err)
	}
	e := &cmdEnv{db: db, clk: &fakeClock{t: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}, notes: &noteLog{}, sid: sid, uuid: uuid}
	e.c = e.newCommands()
	return e
}

func (e *cmdEnv) newCommands() *Commands {
	c := NewCommands(e.db)
	c.now = e.clk.Now
	c.SetPublish(e.notes.publish)
	return c
}

// poll is a non-blocking poll: it records the poll and claims what is due.
func (e *cmdEnv) poll(t *testing.T) *Command {
	t.Helper()
	cmd, err := e.c.WaitNext(context.Background(), e.uuid, 0)
	if err != nil {
		t.Fatalf("WaitNext: %v", err)
	}
	return cmd
}

func (e *cmdEnv) enqueue(t *testing.T, kind string, payload any) int64 {
	t.Helper()
	id, err := e.c.Enqueue(context.Background(), e.sid, kind, payload)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return id
}

type cmdRow struct {
	state       string
	attempts    int
	deliveredAt sql.NullString
	ackedAt     sql.NullString
	result      sql.NullString
	message     sql.NullString
	waitTurn    int
}

func (e *cmdEnv) row(t *testing.T, id int64) cmdRow {
	t.Helper()
	var r cmdRow
	if err := e.db.QueryRow(
		`SELECT state, attempts, delivered_at, acked_at, result, message, wait_turn FROM link_commands WHERE id = ?`, id).
		Scan(&r.state, &r.attempts, &r.deliveredAt, &r.ackedAt, &r.result, &r.message, &r.waitTurn); err != nil {
		t.Fatalf("row %d: %v", id, err)
	}
	return r
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestCommandsEnqueueWaitAck(t *testing.T) {
	e := newCmdEnv(t)
	if cmd := e.poll(t); cmd != nil {
		t.Fatalf("empty queue delivered %+v", cmd)
	}
	id := e.enqueue(t, CmdStop, nil)
	cmd := e.poll(t)
	if cmd == nil || cmd.ID != id || cmd.Kind != CmdStop || string(cmd.Payload) != "{}" || cmd.SessionID != e.sid {
		t.Fatalf("delivered %+v, want stop #%d with {}", cmd, id)
	}
	r := e.row(t, id)
	if r.state != StateDelivered || !r.deliveredAt.Valid {
		t.Fatalf("after delivery: %+v", r)
	}
	if cmd := e.poll(t); cmd != nil {
		t.Fatalf("a delivered command was handed out again before RedeliverAfter: %+v", cmd)
	}
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	r = e.row(t, id)
	if r.state != StateDone || !r.ackedAt.Valid || r.result.String != ResultDone || r.message.Valid {
		t.Fatalf("after ack: %+v", r)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateDelivered, StateDone}) {
		t.Errorf("notes = %v", got)
	}
	e.notes.mu.Lock()
	n := e.notes.notes[0]
	e.notes.mu.Unlock()
	if n.Type != ingest.NoteLinkCommand || n.SessionID != e.sid || n.Kind != CmdStop {
		t.Errorf("note = %+v", n)
	}
}

func TestCommandsPromptPayload(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "continue", AsUser: true})
	cmd := e.poll(t)
	if cmd == nil || cmd.ID != id || string(cmd.Payload) != `{"text":"continue","asUser":true}` {
		t.Fatalf("delivered %+v", cmd)
	}
}

func TestCommandsEnqueueRefusals(t *testing.T) {
	e := newCmdEnv(t)
	ctx := context.Background()
	if _, err := e.c.Enqueue(ctx, e.sid, CmdStop, nil); !errors.Is(err, ErrNotControllable) {
		t.Errorf("never polled: err = %v, want ErrNotControllable", err)
	}
	if _, err := e.c.Enqueue(ctx, 999999, CmdStop, nil); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("unknown session: err = %v, want ErrSessionNotFound", err)
	}
	if _, err := e.c.Enqueue(ctx, e.sid, "reboot", nil); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: err = %v, want ErrUnknownKind", err)
	}
	e.poll(t)
	if _, err := e.c.Enqueue(ctx, e.sid, CmdPrompt, func() {}); err == nil {
		t.Error("an unmarshalable payload was accepted")
	}
}

func TestCommandsControllableFreshness(t *testing.T) {
	e := newCmdEnv(t)
	if e.c.Controllable(e.uuid) {
		t.Fatal("controllable before any poll")
	}
	e.poll(t)
	e.clk.Add(PollFreshness - time.Second)
	if !e.c.Controllable(e.uuid) {
		t.Fatal("not controllable 59 s after a poll")
	}
	e.clk.Add(time.Second)
	if e.c.Controllable(e.uuid) {
		t.Fatal("still controllable 60 s after the last poll")
	}
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.c.mu.Lock()
	_, kept := e.c.lastPoll[e.uuid]
	e.c.mu.Unlock()
	if kept {
		t.Error("Sweep kept a stale poll time")
	}
}

func TestCommandsWaitTimesOut(t *testing.T) {
	e := newCmdEnv(t)
	start := time.Now()
	cmd, err := e.c.WaitNext(context.Background(), e.uuid, 60*time.Millisecond)
	if err != nil || cmd != nil {
		t.Fatalf("WaitNext = %+v, %v; want nil, nil", cmd, err)
	}
	if el := time.Since(start); el < 50*time.Millisecond {
		t.Errorf("returned after %v, before the wait elapsed", el)
	}
}

func TestCommandsWaitWakesOnEnqueue(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	got := make(chan *Command, 1)
	go func() {
		cmd, _ := e.c.WaitNext(context.Background(), e.uuid, 5*time.Second)
		got <- cmd
	}()
	time.Sleep(30 * time.Millisecond)
	id := e.enqueue(t, CmdStop, nil)
	select {
	case cmd := <-got:
		if cmd == nil || cmd.ID != id {
			t.Fatalf("woke with %+v, want #%d", cmd, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting poll was not woken by Enqueue")
	}
}

func TestCommandsWaitContextCancel(t *testing.T) {
	e := newCmdEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cmd, err := e.c.WaitNext(ctx, e.uuid, 5*time.Second)
		if cmd != nil || err != nil {
			t.Errorf("cancelled WaitNext = %+v, %v", cmd, err)
		}
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitNext ignored the cancelled context")
	}
	e.c.mu.Lock()
	left := len(e.c.slots)
	e.c.mu.Unlock()
	if left != 0 {
		t.Errorf("%d wait slots left after the waiter went away", left)
	}
}

func TestCommandsConcurrentWaitersGetOneCopy(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	const waiters = 4
	var wg sync.WaitGroup
	results := make(chan *Command, waiters)
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd, err := e.c.WaitNext(context.Background(), e.uuid, 300*time.Millisecond)
			if err != nil {
				t.Errorf("WaitNext: %v", err)
			}
			results <- cmd
		}()
	}
	time.Sleep(30 * time.Millisecond)
	id := e.enqueue(t, CmdStop, nil)
	wg.Wait()
	close(results)
	n := 0
	for cmd := range results {
		if cmd != nil {
			n++
			if cmd.ID != id {
				t.Errorf("delivered #%d, want #%d", cmd.ID, id)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d waiters got the command, want exactly 1", n)
	}
}

func TestCommandsAckIdempotentAndErrors(t *testing.T) {
	e := newCmdEnv(t)
	ctx := context.Background()
	e.poll(t)
	id := e.enqueue(t, CmdStop, nil)
	e.poll(t)
	if err := e.c.Ack(ctx, id, ResultFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	first := e.row(t, id)
	e.clk.Add(time.Minute)
	if err := e.c.Ack(ctx, id, ResultDone, ""); err != nil {
		t.Fatalf("second ack: %v", err)
	}
	if second := e.row(t, id); second != first {
		t.Errorf("second ack changed the row: %+v → %+v", first, second)
	}
	if first.state != StateFailed || first.message.String != "boom" {
		t.Errorf("failed ack row = %+v", first)
	}
	if err := e.c.Ack(ctx, 424242, ResultDone, ""); !errors.Is(err, ErrCommandNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if err := e.c.Ack(ctx, id, "maybe", ""); !errors.Is(err, ErrBadAck) {
		t.Errorf("bad result: %v", err)
	}
	if err := e.c.Ack(ctx, id, ResultFailed, strings.Repeat("é", MaxAckMessage+1)); !errors.Is(err, ErrBadAck) {
		t.Errorf("long message: %v", err)
	}
	if err := e.c.Ack(ctx, id, ResultFailed, strings.Repeat("é", MaxAckMessage)); err != nil {
		t.Errorf("a %d-character message was refused: %v", MaxAckMessage, err)
	}
	// An ack of a command that was never delivered changes nothing. (The
	// clock moved a minute: the mod polls again first.)
	e.poll(t)
	q := e.enqueue(t, CmdPrompt, PromptPayload{Text: "x"})
	if err := e.c.Ack(ctx, q, ResultDone, ""); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, q); r.state != StateQueued {
		t.Errorf("ack of a queued command moved it to %q", r.state)
	}
}

func TestCommandsRedeliveryAfterRedeliverAfter(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "go"})
	if cmd := e.poll(t); cmd == nil || cmd.ID != id {
		t.Fatalf("first delivery = %+v", cmd)
	}
	firstAt := e.row(t, id).deliveredAt.String
	e.clk.Add(RedeliverAfter - time.Second)
	if cmd := e.poll(t); cmd != nil {
		t.Fatalf("re-delivered after 29 s: %+v", cmd)
	}
	e.clk.Add(2 * time.Second)
	cmd := e.poll(t)
	if cmd == nil || cmd.ID != id {
		t.Fatalf("not re-delivered after 31 s: %+v", cmd)
	}
	if r := e.row(t, id); r.deliveredAt.String <= firstAt {
		t.Errorf("delivered_at not advanced on re-delivery: %s → %s", firstAt, r.deliveredAt.String)
	}
}

// The queue is the table: a fresh Commands over the same DB (a daemon
// restart) hands an unacked command out again once RedeliverAfter passed, and
// the ack of either delivery lands.
func TestCommandsRestartRedeliversUnacked(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "go on"})
	if cmd := e.poll(t); cmd == nil {
		t.Fatal("not delivered")
	}

	e.c = e.newCommands() // restart: poll times and waiters are gone
	if e.c.Controllable(e.uuid) {
		t.Fatal("controllable right after a restart, before any poll")
	}
	e.clk.Add(RedeliverAfter + time.Second)
	cmd := e.poll(t)
	if cmd == nil || cmd.ID != id {
		t.Fatalf("restart lost the unacked command: %+v", cmd)
	}
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.state != StateDone {
		t.Errorf("state = %q", r.state)
	}
}

func TestCommandsStopExpiresAfter30s(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdStop, nil)
	e.clk.Add(StopTTL - time.Millisecond)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.state != StateQueued {
		t.Fatalf("expired early: %q", r.state)
	}
	e.clk.Add(2 * time.Millisecond)
	if cmd := e.poll(t); cmd != nil {
		t.Fatalf("a late stop was delivered: %+v", cmd)
	}
	r := e.row(t, id)
	if r.state != StateExpired || r.deliveredAt.Valid {
		t.Fatalf("row = %+v, want expired, never delivered", r)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateExpired}) {
		t.Errorf("notes = %v", got)
	}
}

func TestCommandsPromptExpiresAfter10m(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	// A stop and a prompt queued together: at 9 minutes the stop is long
	// gone, the prompt still waits (it is held for a turn end, so no poll
	// takes it).
	stop := e.enqueue(t, CmdStop, nil)
	p := e.enqueue(t, CmdPrompt, PromptPayload{Text: "later"})
	if _, err := e.db.Exec(`UPDATE link_commands SET wait_turn = 1 WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	e.clk.Add(9 * time.Minute)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.row(t, stop).state != StateExpired || e.row(t, p).state != StateQueued {
		t.Fatalf("at 9 min: stop %q, prompt %q", e.row(t, stop).state, e.row(t, p).state)
	}
	e.clk.Add(time.Minute + time.Millisecond)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, p).state; got != StateExpired {
		t.Fatalf("prompt at 10 min: %q, want expired", got)
	}
}

// A prompt the mod could not submit because a turn was running goes back to
// the queue, is held until the session's next turn.complete is ingested, and
// gives up for good on the third bounce.
func TestCommandsTurnInProgressBouncesUntilTurnComplete(t *testing.T) {
	e := newCmdEnv(t)
	ctx := context.Background()
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "next phase"})

	// Each turn end falls between the bounce it releases and the next
	// delivery (the fake clock starts at 09:00:00 and moves 31 s a round), so
	// none of them counts as "the turn already ended" for the bounce after it.
	turnEnd := []string{"2026-10-14T09:00:20Z", "2026-10-14T09:00:50Z"}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		cmd := e.poll(t)
		if cmd == nil || cmd.ID != id {
			t.Fatalf("attempt %d: delivered %+v", attempt, cmd)
		}
		if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
			t.Fatal(err)
		}
		r := e.row(t, id)
		if r.attempts != attempt {
			t.Fatalf("attempt %d: attempts = %d", attempt, r.attempts)
		}
		if attempt == MaxAttempts {
			if r.state != StateFailed || !r.ackedAt.Valid {
				t.Fatalf("third bounce: %+v, want final failed", r)
			}
			break
		}
		if r.state != StateQueued || r.waitTurn != 1 || r.deliveredAt.Valid {
			t.Fatalf("bounce %d: %+v, want queued, held, undelivered", attempt, r)
		}
		// Held: neither a poll nor RedeliverAfter hands it out.
		e.clk.Add(RedeliverAfter + time.Second)
		if cmd := e.poll(t); cmd != nil {
			t.Fatalf("a held prompt was delivered before turn.complete: %+v", cmd)
		}
		// A resent turn.complete of an EARLIER turn is a duplicate and
		// releases nothing.
		if attempt == 2 {
			res := mustIngest(t, e.db, nil, []Event{{SessionID: e.uuid, Kind: KindTurnComplete, At: turnEnd[0]}})
			if res.Duplicates != 1 {
				t.Fatalf("resend: %+v", res)
			}
			if cmd := e.poll(t); cmd != nil {
				t.Fatalf("a duplicate turn.complete released the prompt: %+v", cmd)
			}
		}
		mustIngest(t, e.db, nil, []Event{{SessionID: e.uuid, Kind: KindTurnComplete, At: turnEnd[attempt-1]}})
		if r := e.row(t, id); r.waitTurn != 0 {
			t.Fatalf("turn.complete did not release: %+v", r)
		}
	}
	got := e.notes.states(id)
	want := []string{StateQueued, StateDelivered, StateQueued, StateDelivered, StateQueued, StateDelivered, StateFailed}
	if !equalStrings(got, want) {
		t.Errorf("notes = %v, want %v", got, want)
	}
}

// A stop is re-delivered only while it is younger than StopTTL: one the mod
// ran but whose reply was lost must not abort a later turn, so past StopTTL the
// poll that would have re-delivered it expires it instead.
func TestCommandsDeliveredStopExpiresInsteadOfRedelivery(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdStop, nil)
	if cmd := e.poll(t); cmd == nil || cmd.ID != id {
		t.Fatalf("first delivery = %+v", cmd)
	}
	e.clk.Add(RedeliverAfter + time.Second)
	if cmd := e.poll(t); cmd != nil {
		t.Fatalf("a stale stop was re-delivered: %+v", cmd)
	}
	r := e.row(t, id)
	if r.state != StateExpired || !r.deliveredAt.Valid {
		t.Fatalf("row = %+v, want expired after a delivery", r)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateDelivered, StateExpired}) {
		t.Errorf("notes = %v", got)
	}
	// The late ack of the lost reply changes nothing.
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, id).state; got != StateExpired {
		t.Errorf("late ack moved the row to %q", got)
	}
	// A delivered stop inside RedeliverAfter is not touched by a sweep.
	s := e.enqueue(t, CmdStop, nil)
	e.poll(t)
	e.clk.Add(RedeliverAfter - time.Second)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, s).state; got != StateDelivered {
		t.Errorf("in-flight stop = %q, want delivered", got)
	}
}

// A delivered prompt that is never acked and never re-polled (the mod went
// away) expires once it is past PromptTTL and due for re-delivery; while a
// poller keeps re-delivering it, or before PromptTTL, it stays delivered.
func TestCommandsDeliveredPromptWithoutPollerExpires(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "go on"})
	if cmd := e.poll(t); cmd == nil || cmd.ID != id {
		t.Fatalf("first delivery = %+v", cmd)
	}
	e.clk.Add(PromptTTL - time.Minute)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, id).state; got != StateDelivered {
		t.Fatalf("before PromptTTL = %q, want delivered", got)
	}
	e.clk.Add(2 * time.Minute)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, id).state; got != StateExpired {
		t.Fatalf("after PromptTTL with no poller = %q, want expired", got)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateDelivered, StateExpired}) {
		t.Errorf("notes = %v", got)
	}
}

// The mod's turn.complete and its "turn in progress" ack race. Whichever lands
// first, the bounced prompt is deliverable once that turn has ended.
func TestCommandsBounceAfterTurnCompleteReleasesAtOnce(t *testing.T) {
	ctx := context.Background()
	t.Run("turn.complete first", func(t *testing.T) {
		e := newCmdEnv(t)
		e.poll(t)
		id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "next"})
		e.poll(t) // delivered 09:00:00
		mustIngest(t, e.db, nil, []Event{{SessionID: e.uuid, Kind: KindTurnComplete, At: "2026-10-14T09:00:03Z"}})
		got := make(chan *Command, 1)
		go func() {
			cmd, _ := e.c.WaitNext(ctx, e.uuid, 5*time.Second)
			got <- cmd
		}()
		time.Sleep(30 * time.Millisecond)
		if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
			t.Fatal(err)
		}
		select {
		case cmd := <-got:
			if cmd == nil || cmd.ID != id {
				t.Fatalf("woke with %+v", cmd)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the bounce did not wake the waiting poll")
		}
		if r := e.row(t, id); r.attempts != 1 || r.state != StateDelivered {
			t.Errorf("row = %+v, want re-delivered with attempts 1", r)
		}
	})
	t.Run("ack first", func(t *testing.T) {
		e := newCmdEnv(t)
		e.poll(t)
		id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "next"})
		e.poll(t)
		if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
			t.Fatal(err)
		}
		if r := e.row(t, id); r.waitTurn != 1 {
			t.Fatalf("held = %d, want 1 before turn.complete", r.waitTurn)
		}
		mustIngest(t, e.db, nil, []Event{{SessionID: e.uuid, Kind: KindTurnComplete, At: "2026-10-14T09:00:03Z"}})
		if cmd := e.poll(t); cmd == nil || cmd.ID != id {
			t.Fatalf("not released by the turn.complete: %+v", cmd)
		}
	})
	t.Run("turn ended before the delivery", func(t *testing.T) {
		e := newCmdEnv(t)
		mustIngest(t, e.db, nil, []Event{{SessionID: e.uuid, Kind: KindTurnComplete, At: "2026-10-14T08:59:59Z"}})
		e.poll(t)
		id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "next"})
		e.poll(t)
		if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
			t.Fatal(err)
		}
		if r := e.row(t, id); r.waitTurn != 1 {
			t.Errorf("an older turn.complete released the prompt: %+v", r)
		}
	})
	t.Run("a newer turn already started", func(t *testing.T) {
		e := newCmdEnv(t)
		e.poll(t)
		id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "next"})
		e.poll(t)
		mustIngest(t, e.db, nil, []Event{
			{SessionID: e.uuid, Kind: KindTurnComplete, At: "2026-10-14T09:00:03Z"},
			{SessionID: e.uuid, Kind: KindTurnStart, At: "2026-10-14T09:00:04Z"},
		})
		if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
			t.Fatal(err)
		}
		if r := e.row(t, id); r.waitTurn != 1 {
			t.Errorf("released into a running turn: %+v", r)
		}
	})
}

// A non-"turn in progress" failure, and that message on a stop, are final.
func TestCommandsOtherFailuresAreFinal(t *testing.T) {
	e := newCmdEnv(t)
	ctx := context.Background()
	e.poll(t)
	p := e.enqueue(t, CmdPrompt, PromptPayload{Text: "x"})
	e.poll(t)
	if err := e.c.Ack(ctx, p, ResultFailed, "submit rejected"); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, p); r.state != StateFailed || r.attempts != 0 {
		t.Errorf("prompt: %+v", r)
	}
	s := e.enqueue(t, CmdStop, nil)
	e.poll(t)
	if err := e.c.Ack(ctx, s, ResultFailed, TurnInProgress); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, s); r.state != StateFailed {
		t.Errorf("stop: %+v", r)
	}
}

func TestCommandsReleaseWaitTurnWakesPoll(t *testing.T) {
	e := newCmdEnv(t)
	ctx := context.Background()
	e.poll(t)
	id := e.enqueue(t, CmdPrompt, PromptPayload{Text: "x"})
	e.poll(t)
	if err := e.c.Ack(ctx, id, ResultFailed, TurnInProgress); err != nil {
		t.Fatal(err)
	}
	got := make(chan *Command, 1)
	go func() {
		cmd, _ := e.c.WaitNext(ctx, e.uuid, 5*time.Second)
		got <- cmd
	}()
	time.Sleep(30 * time.Millisecond)
	n, err := e.c.ReleaseWaitTurn(ctx, e.sid)
	if err != nil || n != 1 {
		t.Fatalf("ReleaseWaitTurn = %d, %v", n, err)
	}
	select {
	case cmd := <-got:
		if cmd == nil || cmd.ID != id {
			t.Fatalf("woke with %+v", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReleaseWaitTurn did not wake the poll")
	}
	if n, err := e.c.ReleaseWaitTurn(ctx, e.sid); err != nil || n != 0 {
		t.Errorf("second release = %d, %v", n, err)
	}
}

// A wait beyond MaxWait, or a negative one, is legal; the request's context
// still ends a long one.
func TestCommandsWaitOutOfRange(t *testing.T) {
	e := newCmdEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if cmd, err := e.c.WaitNext(ctx, e.uuid, time.Hour); cmd != nil || err != nil {
		t.Fatalf("WaitNext(1h) = %+v, %v", cmd, err)
	}
	if cmd, err := e.c.WaitNext(context.Background(), e.uuid, -time.Second); cmd != nil || err != nil {
		t.Fatalf("WaitNext(-1s) = %+v, %v", cmd, err)
	}
}

func TestCommandsRunStopsWithContext(t *testing.T) {
	e := newCmdEnv(t)
	e.poll(t)
	id := e.enqueue(t, CmdStop, nil)
	e.clk.Add(StopTTL + time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.c.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for e.row(t, id).state != StateExpired {
		if time.Now().After(deadline) {
			t.Fatal("Run never expired the overdue stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestCommandsWithoutPublisher(t *testing.T) {
	e := newCmdEnv(t)
	e.c.SetPublish(nil)
	e.poll(t)
	if _, err := e.c.Enqueue(context.Background(), e.sid, CmdStop, nil); err != nil {
		t.Fatal(err)
	}
}
