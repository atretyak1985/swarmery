package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const msgRecipient = "c0ffee00-0000-4000-8000-0000000000aa"

// newMsgEnv is a cmdEnv plus a recipient session.
func newMsgEnv(t *testing.T) *cmdEnv {
	t.Helper()
	e := newCmdEnv(t)
	mustIngest(t, e.db, nil, []Event{{SessionID: msgRecipient, Cwd: "/work/ctl", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"}})
	return e
}

type dashRow struct {
	from, to, addr, reason sql.NullString
	body                   string
	delivered, commandID   sql.NullInt64
}

func (e *cmdEnv) dashboardRow(t *testing.T, commandID int64) dashRow {
	t.Helper()
	var r dashRow
	if err := e.db.QueryRow(
		`SELECT from_session, to_session, to_address, reason, body, delivered, command_id
		   FROM link_messages WHERE kind = 'dashboard' AND command_id = ?`, commandID).
		Scan(&r.from, &r.to, &r.addr, &r.reason, &r.body, &r.delivered, &r.commandID); err != nil {
		t.Fatalf("dashboard row for command %d: %v", commandID, err)
	}
	return r
}

func (e *cmdEnv) swarmNotes() int {
	e.notes.mu.Lock()
	defer e.notes.mu.Unlock()
	return countType(e.notes.notes, ingest.NoteSwarmMessage)
}

// A message command is refused like a prompt (409 analogue) until the sender
// polls; bad input is ErrBadMessage and writes nothing.
func TestCommandsMessageRefusals(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	if _, _, err := e.c.EnqueueMessage(ctx, e.sid, msgRecipient, "hi"); !errors.Is(err, ErrNotControllable) {
		t.Errorf("never polled: err = %v, want ErrNotControllable", err)
	}
	if _, _, err := e.c.EnqueueMessage(ctx, 999999, msgRecipient, "hi"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("unknown sender: err = %v, want ErrSessionNotFound", err)
	}
	e.poll(t)
	for name, tc := range map[string]struct{ to, text string }{
		"empty text":   {msgRecipient, "  "},
		"empty to":     {" ", "hi"},
		"to is sender": {e.uuid, "hi"},
		"too long":     {msgRecipient, strings.Repeat("x", MaxMessageBody+1)},
	} {
		if _, _, err := e.c.EnqueueMessage(ctx, e.sid, tc.to, tc.text); !errors.Is(err, ErrBadMessage) {
			t.Errorf("%s: err = %v, want ErrBadMessage", name, err)
		}
	}
	if _, err := e.c.Enqueue(ctx, e.sid, CmdMessage, func() {}); !errors.Is(err, ErrBadMessage) {
		t.Errorf("unmarshalable payload: err = %v, want ErrBadMessage", err)
	}
	var n int
	if err := e.db.QueryRow(`SELECT (SELECT COUNT(*) FROM link_commands) + (SELECT COUNT(*) FROM link_messages)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows after refusals = %d (%v), want 0", n, err)
	}
}

// queued → delivered with {to,text} → ack done: the dashboard row is written
// with the command and settles delivered = 1 on the ack.
func TestCommandsMessageDeliveredAckedDone(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	id, err := e.c.Enqueue(context.Background(), e.sid, CmdMessage, MessagePayload{To: msgRecipient, Text: "rebase on main"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	r := e.dashboardRow(t, id)
	if r.from.String != e.uuid || r.to.String != msgRecipient || r.addr.String != msgRecipient || r.body != "rebase on main" || r.delivered.Valid {
		t.Fatalf("dashboard row = %+v", r)
	}
	if e.swarmNotes() != 1 {
		t.Errorf("swarm_message notes after enqueue = %d, want 1", e.swarmNotes())
	}
	cmd := e.poll(t)
	if cmd == nil || cmd.ID != id || cmd.Kind != CmdMessage {
		t.Fatalf("delivered %+v", cmd)
	}
	var p MessagePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p != (MessagePayload{To: msgRecipient, Text: "rebase on main"}) {
		t.Fatalf("payload = %s (%v)", cmd.Payload, err)
	}
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, id); got.state != StateDone {
		t.Errorf("command state = %s", got.state)
	}
	if r := e.dashboardRow(t, id); r.delivered.Int64 != 1 || !r.delivered.Valid || r.reason.Valid {
		t.Errorf("after done: %+v", r)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateDelivered, StateDone}) {
		t.Errorf("link_command states = %v", got)
	}
	if e.swarmNotes() != 2 {
		t.Errorf("swarm_message notes after ack = %d, want 2", e.swarmNotes())
	}
}

// A failed ack is final for a message — "turn in progress" included (no
// bounce: a send needs no idle session) — and lands on the row as the reason.
func TestCommandsMessageFailedNoBounce(t *testing.T) {
	for _, msg := range []string{"undeliverable: no such session", TurnInProgress} {
		t.Run(msg, func(t *testing.T) {
			e := newMsgEnv(t)
			e.poll(t)
			id, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "ping")
			if err != nil {
				t.Fatal(err)
			}
			e.poll(t)
			if err := e.c.Ack(context.Background(), id, ResultFailed, msg); err != nil {
				t.Fatal(err)
			}
			row := e.row(t, id)
			if row.state != StateFailed || row.waitTurn != 0 || row.attempts != 0 {
				t.Errorf("command = %+v, want a final failed with no bounce", row)
			}
			if r := e.dashboardRow(t, id); !r.delivered.Valid || r.delivered.Int64 != 0 || r.reason.String != msg {
				t.Errorf("dashboard row = %+v", r)
			}
			if cmd := e.poll(t); cmd != nil {
				t.Errorf("failed message delivered again: %+v", cmd)
			}
		})
	}
}

// An undelivered message expires on the prompt TTL, and so does a delivered
// one whose mod stopped polling; the row records "expired".
func TestCommandsMessageExpires(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	queued, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "a")
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Add(PromptTTL + time.Second)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, queued); got.state != StateExpired {
		t.Errorf("queued message state = %s, want expired", got.state)
	}
	if r := e.dashboardRow(t, queued); r.delivered.Int64 != 0 || !r.delivered.Valid || r.reason.String != expiredReason {
		t.Errorf("expired row = %+v", r)
	}

	e.poll(t)
	delivered, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "b")
	if err != nil {
		t.Fatal(err)
	}
	if cmd := e.poll(t); cmd == nil || cmd.ID != delivered {
		t.Fatalf("not delivered: %+v", cmd)
	}
	e.clk.Add(PromptTTL + time.Second)
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, delivered); got.state != StateExpired {
		t.Errorf("delivered message state = %s, want expired", got.state)
	}
}

// The sender's mod reports the send it ran for a dashboard command (commandId):
// it folds into the dashboard row instead of adding a second message, and the
// recipient's receive pairs with it without touching the ack-owned delivered.
func TestCommandsMessageSendEventFolds(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	id, msgID, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "status?")
	if err != nil {
		t.Fatal(err)
	}
	at := e.clk.Now().Add(time.Second).Format(time.RFC3339Nano)
	send := Event{SessionID: e.uuid, Kind: KindSessionSend, At: at, Payload: json.RawMessage(
		fmt.Sprintf(`{"to":%q,"text":"status?","isDelivered":true,"origin":"plugin","commandId":%d}`, msgRecipient, id))}
	recv := Event{SessionID: msgRecipient, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"text":"status?","originKind":"peer"}`)}
	mustIngest(t, e.db, nil, []Event{send, recv})

	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_messages WHERE kind IN ('send', 'dashboard')`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("send-side rows = %d (%v), want 1 (folded)", n, err)
	}
	var (
		origin    sql.NullString
		delivered sql.NullInt64
		matched   sql.NullInt64
		from      sql.NullString
	)
	if err := e.db.QueryRow(`SELECT origin, delivered, matched_id FROM link_messages WHERE id = ?`, msgID).Scan(&origin, &delivered, &matched); err != nil {
		t.Fatal(err)
	}
	if origin.String != "plugin" || delivered.Valid || !matched.Valid {
		t.Errorf("folded row origin=%v delivered=%v matched=%v", origin, delivered, matched)
	}
	if err := e.db.QueryRow(`SELECT from_session FROM link_messages WHERE kind = 'receive'`).Scan(&from); err != nil || from.String != e.uuid {
		t.Errorf("receive from_session = %v (%v), want the sender", from, err)
	}

	// A commandId with no dashboard row behind it is stored as a plain send.
	stray := Event{SessionID: e.uuid, Kind: KindSessionSend, At: e.clk.Now().Add(2 * time.Second).Format(time.RFC3339Nano),
		Payload: json.RawMessage(fmt.Sprintf(`{"to":%q,"text":"x","commandId":%d}`, msgRecipient, id+100))}
	mustIngest(t, e.db, nil, []Event{stray})
	var cmd sql.NullInt64
	if err := e.db.QueryRow(`SELECT command_id FROM link_messages WHERE kind = 'send'`).Scan(&cmd); err != nil || cmd.Int64 != id+100 {
		t.Errorf("stray send command_id = %v (%v)", cmd, err)
	}
}

// The phase-6 live check's dashboard relay: the recipient's receive arrives in
// the engine's cross-session envelope and still pairs with the dashboard row.
func TestCommandsMessageEnvelopedReceivePairs(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	_, msgID, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "hello from the dashboard via A")
	if err != nil {
		t.Fatal(err)
	}
	at := e.clk.Now().Add(time.Second).Format(time.RFC3339Nano)
	recv := Event{SessionID: msgRecipient, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(fmt.Sprintf(`{"text":%q,"originKind":"peer"}`,
		envelope(98092, "proj-a3", "prompting", "hello from the dashboard via A")))}
	mustIngest(t, e.db, nil, []Event{recv})
	var (
		matched, recvMatched sql.NullInt64
		from                 sql.NullString
		body                 string
	)
	if err := e.db.QueryRow(`SELECT matched_id FROM link_messages WHERE id = ?`, msgID).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT from_session, matched_id, body FROM link_messages WHERE kind = 'receive'`).Scan(&from, &recvMatched, &body); err != nil {
		t.Fatal(err)
	}
	if !matched.Valid || from.String != e.uuid || recvMatched.Int64 != msgID || body != "hello from the dashboard via A" {
		t.Errorf("dashboard matched=%v; receive from=%v matched=%v body=%q", matched, from, recvMatched, body)
	}
}

// pluginSend is the session.send event the swarmery-link mod queues for a
// dashboard message, byte for byte as plugins/swarmery-link/hooks/swarm.test.ts
// expects it (sendPayload); commandID is spliced in raw, so a test can send
// the JSON number the mod sends (wireCommandId) or the digit string mods
// before the phase-6 fix sent.
func pluginSend(from, to, text, at, isDelivered, commandID string) Event {
	extra := ""
	if isDelivered == "false" {
		extra = `,"reason":"session is not running"`
	}
	return Event{SessionID: from, Kind: KindSessionSend, At: at, Payload: json.RawMessage(fmt.Sprintf(
		`{"to":%q,"text":%q,"truncated":false,"isDelivered":%s%s,"origin":"plugin","commandId":%s}`,
		to, text, isDelivered, extra, commandID))}
}

// The mod's session.send for a dashboard message is accepted and folds into
// the dashboard row whether its commandId is a number (the mod now) or a digit
// string (mods before the fix): never a second row, never a per-event reject.
func TestCommandsMessagePluginSendShapeFolds(t *testing.T) {
	for name, quote := range map[string]bool{"number": false, "digit string": true} {
		t.Run(name, func(t *testing.T) {
			e := newMsgEnv(t)
			e.poll(t)
			id, msgID, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "please pull main")
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprint(id)
			if quote {
				raw = fmt.Sprintf("%q", raw)
			}
			at := e.clk.Now().Add(time.Second).Format(time.RFC3339Nano)
			res := mustIngest(t, e.db, nil, []Event{pluginSend(e.uuid, msgRecipient, "please pull main", at, "true", raw)})
			if res.Accepted != 1 || res.Rejected != 0 {
				t.Fatalf("ingest = %+v, want the send accepted", res)
			}
			var n int
			if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_messages`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("rows = %d (%v), want 1 (folded)", n, err)
			}
			var origin sql.NullString
			if err := e.db.QueryRow(`SELECT origin FROM link_messages WHERE id = ?`, msgID).Scan(&origin); err != nil || origin.String != "plugin" {
				t.Errorf("folded row origin = %v (%v), want plugin", origin, err)
			}
		})
	}
	e := newMsgEnv(t)
	res := mustIngest(t, e.db, nil, []Event{pluginSend(e.uuid, msgRecipient, "x", "2026-10-14T09:00:01Z", "true", `"0"`)})
	if res.Rejected != 1 || len(res.Rejects) != 1 || !strings.Contains(res.Rejects[0].Reason, "commandId must be a positive integer") {
		t.Errorf("commandId \"0\": %+v", res)
	}
}

// The mod acks failed after its 10 s message timeout, then the send it gave
// up on settles and is reported with the command id: the engine's verdict
// overrides the ack's on the dashboard row — delivered clears the reason, not
// delivered takes the engine's reason. Before any ack, the send leaves
// delivered to the ack (TestCommandsMessageSendEventFolds).
func TestCommandsMessageLateSendOverridesFailedAck(t *testing.T) {
	for _, tc := range []struct {
		isDelivered string
		want        int64
		reason      string
	}{
		{"true", 1, ""},
		{"false", 0, "undeliverable: session is not running"},
	} {
		t.Run("isDelivered "+tc.isDelivered, func(t *testing.T) {
			e := newMsgEnv(t)
			e.poll(t)
			id, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "status?")
			if err != nil {
				t.Fatal(err)
			}
			e.poll(t)
			if err := e.c.Ack(context.Background(), id, ResultFailed, "undeliverable: no answer in 10s"); err != nil {
				t.Fatal(err)
			}
			if r := e.dashboardRow(t, id); r.delivered.Int64 != 0 || !r.delivered.Valid {
				t.Fatalf("after the timeout ack: %+v", r)
			}
			at := e.clk.Now().Add(12 * time.Second).Format(time.RFC3339Nano)
			mustIngest(t, e.db, nil, []Event{pluginSend(e.uuid, msgRecipient, "status?", at, tc.isDelivered, fmt.Sprint(id))})
			r := e.dashboardRow(t, id)
			if !r.delivered.Valid || r.delivered.Int64 != tc.want || r.reason.String != tc.reason || r.reason.Valid != (tc.reason != "") {
				t.Errorf("after the late send: %+v, want delivered %d reason %q", r, tc.want, tc.reason)
			}
			var n int
			if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_messages`).Scan(&n); err != nil || n != 1 {
				t.Errorf("rows = %d (%v), want 1", n, err)
			}
		})
	}
}

// failSettles makes every UPDATE of a dashboard row fail until the returned
// func drops the trigger.
func failSettles(t *testing.T, db *sql.DB) func() {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER fail_settle BEFORE UPDATE ON link_messages
		WHEN OLD.kind = 'dashboard' BEGIN SELECT RAISE(ABORT, 'settle refused'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER fail_settle`); err != nil {
			t.Fatal(err)
		}
	}
}

// The ack's command update and the dashboard row's settle are one
// transaction: a settle that fails leaves the command delivered (not a final
// state with a row pending forever), and the mod's retried ack settles both.
func TestCommandsMessageAckSettleIsAtomic(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	id, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "atomic?")
	if err != nil {
		t.Fatal(err)
	}
	e.poll(t)
	restore := failSettles(t, e.db)
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err == nil || !strings.Contains(err.Error(), "settle refused") {
		t.Fatalf("Ack with a failing settle: err = %v, want the settle's error", err)
	}
	if got := e.row(t, id); got.state != StateDelivered {
		t.Errorf("after the failed settle: command state = %s, want delivered (rolled back)", got.state)
	}
	if r := e.dashboardRow(t, id); r.delivered.Valid {
		t.Errorf("after the failed settle: dashboard row = %+v, want delivered NULL", r)
	}
	if got := e.notes.states(id); !equalStrings(got, []string{StateQueued, StateDelivered}) {
		t.Errorf("link_command states after the failed settle = %v, want no done note", got)
	}
	restore()
	if err := e.c.Ack(context.Background(), id, ResultDone, ""); err != nil {
		t.Fatalf("retried Ack: %v", err)
	}
	if got := e.row(t, id); got.state != StateDone {
		t.Errorf("after the retried ack: command state = %s, want done", got.state)
	}
	if r := e.dashboardRow(t, id); !r.delivered.Valid || r.delivered.Int64 != 1 {
		t.Errorf("after the retried ack: dashboard row = %+v, want delivered 1", r)
	}
}

// Expiry is atomic the same way: a failing settle keeps the message queued,
// and the next sweep expires it and settles its row.
func TestCommandsMessageExpireSettleIsAtomic(t *testing.T) {
	e := newMsgEnv(t)
	e.poll(t)
	id, _, err := e.c.EnqueueMessage(context.Background(), e.sid, msgRecipient, "late")
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Add(PromptTTL + time.Second)
	restore := failSettles(t, e.db)
	if err := e.c.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep with a failing settle: err = nil, want the settle's error")
	}
	if got := e.row(t, id); got.state != StateQueued {
		t.Errorf("after the failed settle: command state = %s, want queued (rolled back)", got.state)
	}
	restore()
	if err := e.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, id); got.state != StateExpired {
		t.Errorf("after the next sweep: command state = %s, want expired", got.state)
	}
	if r := e.dashboardRow(t, id); !r.delivered.Valid || r.delivered.Int64 != 0 || r.reason.String != expiredReason {
		t.Errorf("after the next sweep: dashboard row = %+v", r)
	}
}
