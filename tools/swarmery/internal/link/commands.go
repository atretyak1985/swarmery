package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Command kinds the dashboard can queue for a mod-linked session
// (docs/mods-link-protocol.md "Commands").
const (
	CmdStop   = "stop"
	CmdPrompt = "prompt"
)

// Command states: link_commands.state and the state of a link_command note.
const (
	StateQueued    = "queued"
	StateDelivered = "delivered"
	StateDone      = "done"
	StateFailed    = "failed"
	StateExpired   = "expired"
)

// Ack results the mod may send.
const (
	ResultDone   = "done"
	ResultFailed = "failed"
)

const (
	// PollFreshness: a session is controllable while its mod's last poll is
	// younger than this.
	PollFreshness = 60 * time.Second
	// RedeliverAfter: a delivered-but-unacked command is handed out again on a
	// poll once its last delivery is older than this, so a daemon restart or a
	// lost reply never strands it.
	RedeliverAfter = 30 * time.Second
	// StopTTL: an undelivered stop older than this expires — a late stop must
	// not kill a later turn.
	StopTTL = 30 * time.Second
	// PromptTTL: an undelivered prompt older than this expires.
	PromptTTL = 10 * time.Minute
	// MaxAttempts bounds the "turn in progress" bounces of one prompt; the
	// last one is a final failed.
	MaxAttempts = 3
	// MaxWait caps one long-poll (the mod's $.http.fetch limit is 30 s, M3).
	MaxWait = 25 * time.Second
	// MaxAckMessage bounds an ack message, in characters.
	MaxAckMessage = 500
	// TurnInProgress is the exact ack message that sends a prompt back to the
	// queue until the session's next turn.complete.
	TurnInProgress = "turn in progress"
	// SweepEvery is how often Run expires overdue commands between polls.
	SweepEvery = 5 * time.Second
)

var (
	// ErrSessionNotFound: no sessions row has that id.
	ErrSessionNotFound = errors.New("session not found")
	// ErrNotControllable: no mod has polled for the session within
	// PollFreshness, so nothing would ever pick a command up. Its text is the
	// 409 body the API answers with.
	ErrNotControllable = errors.New("session has no link")
	// ErrCommandNotFound: no link_commands row has that id.
	ErrCommandNotFound = errors.New("link command not found")
	// ErrBadAck: the ack's result or message is out of contract.
	ErrBadAck = errors.New("bad ack")
	// ErrUnknownKind: Enqueue got a kind other than stop / prompt.
	ErrUnknownKind = errors.New("unknown command kind")
)

// Command is one delivered command. Its JSON form is the 200 body of
// GET /api/link/commands.
type Command struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	SessionID int64           `json:"-"` // sessions.id
}

// PromptPayload is the payload of a prompt command.
type PromptPayload struct {
	Text   string `json:"text"`
	AsUser bool   `json:"asUser"`
}

// Commands is the dashboard → mod command queue. Rows live in link_commands
// (the source of truth, so a restart loses nothing but the poll times); what is
// kept in memory is only what a restart may lose: which session's mod polled
// when (controllability) and the wake-up channels of the waiting long-polls.
type Commands struct {
	db  *sql.DB
	now func() time.Time // injectable for tests

	// rmw serializes every read-modify-write of a command (claim, ack, expiry)
	// so two concurrent polls never hand one command out twice.
	rmw sync.Mutex

	mu       sync.Mutex
	publish  func(ingest.Notification)
	lastPoll map[string]time.Time // session uuid → last poll
	slots    map[string]*waitSlot // session uuid → current wake-up channel
}

// waitSlot is one session's broadcast wake-up: closed (and dropped from the
// map) on the next change, so every waiter holding it wakes at once. n counts
// the waiters holding it, so an idle slot does not outlive them.
type waitSlot struct {
	ch chan struct{}
	n  int
}

// NewCommands returns a queue over db. Notes are not published until
// SetPublish.
func NewCommands(db *sql.DB) *Commands {
	return &Commands{
		db:       db,
		now:      time.Now,
		lastPoll: map[string]time.Time{},
		slots:    map[string]*waitSlot{},
	}
}

// SetPublish sets where link_command notes go (the WS bus). nil drops them.
func (c *Commands) SetPublish(fn func(ingest.Notification)) {
	c.mu.Lock()
	c.publish = fn
	c.mu.Unlock()
}

// Controllable reports whether the mod of the session with this Claude session
// id (sessions.session_uuid) polled within PollFreshness.
func (c *Commands) Controllable(sessionUUID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.lastPoll[sessionUUID]
	return ok && c.now().Sub(at) < PollFreshness
}

// Enqueue queues a command for the session with this sessions.id and returns
// the command id. payload is marshalled as the command's payload; nil is {}.
// ErrSessionNotFound when the row does not exist, ErrNotControllable when no
// mod polls for it.
func (c *Commands) Enqueue(ctx context.Context, sessionID int64, kind string, payload any) (int64, error) {
	if kind != CmdStop && kind != CmdPrompt {
		return 0, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	var uuid string
	err := c.db.QueryRowContext(ctx, `SELECT session_uuid FROM sessions WHERE id = ?`, sessionID).Scan(&uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("link commands: find session: %w", err)
	}
	if !c.Controllable(uuid) {
		return 0, ErrNotControllable
	}
	raw := []byte("{}")
	if payload != nil {
		if raw, err = json.Marshal(payload); err != nil {
			return 0, fmt.Errorf("link commands: payload: %w", err)
		}
	}
	r, err := c.db.ExecContext(ctx,
		`INSERT INTO link_commands (session_id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, kind, string(raw), stamp(c.now()))
	if err != nil {
		return 0, fmt.Errorf("link commands: insert: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("link commands: insert: %w", err)
	}
	c.note(sessionID, id, kind, StateQueued, "")
	c.Wake(uuid)
	return id, nil
}

// WaitNext is the mod's long-poll. It records the poll (controllability), then
// returns the oldest deliverable command of the session with this Claude
// session id, marking it delivered — waiting up to wait (capped at MaxWait) for
// one to appear. It returns (nil, nil) when nothing became deliverable before
// wait elapsed or ctx ended.
//
// Deliverable: queued and not held for a turn end, or delivered more than
// RedeliverAfter ago and still unacked.
func (c *Commands) WaitNext(ctx context.Context, sessionUUID string, wait time.Duration) (*Command, error) {
	c.touch(sessionUUID)
	wait = min(max(wait, 0), MaxWait)
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	for {
		// Subscribe before looking, so a command queued between the look and
		// the wait still wakes us.
		slot := c.subscribe(sessionUUID)
		cmd, err := c.claim(ctx, sessionUUID)
		if err != nil || cmd != nil || wait == 0 {
			c.release(sessionUUID, slot)
			return cmd, err
		}
		select {
		case <-slot.ch:
			c.release(sessionUUID, slot)
		case <-timeout:
			c.release(sessionUUID, slot)
			return nil, nil
		case <-ctx.Done():
			c.release(sessionUUID, slot)
			return nil, nil
		}
	}
}

// Ack records the mod's verdict on a delivered command. result is done or
// failed; message is optional, at most MaxAckMessage characters (ErrBadAck
// otherwise). ErrCommandNotFound for an unknown id.
//
// Idempotent: an ack of a command that is not currently delivered (already
// acked, expired, or back in the queue after a bounce) changes nothing. A
// failed prompt whose message is exactly TurnInProgress goes back to the queue
// held for the session's next turn.complete (attempts+1); the MaxAttempts-th
// such bounce is a final failed. Any other failure is final.
func (c *Commands) Ack(ctx context.Context, id int64, result, message string) error {
	if result != ResultDone && result != ResultFailed {
		return fmt.Errorf("%w: result must be done or failed", ErrBadAck)
	}
	if utf8.RuneCountInString(message) > MaxAckMessage {
		return fmt.Errorf("%w: message exceeds %d characters", ErrBadAck, MaxAckMessage)
	}
	c.rmw.Lock()
	defer c.rmw.Unlock()

	var (
		sid         int64
		kind, state string
		attempts    int
		msg         = nullable(message)
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT session_id, kind, state, attempts FROM link_commands WHERE id = ?`, id).
		Scan(&sid, &kind, &state, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCommandNotFound
	}
	if err != nil {
		return fmt.Errorf("link commands: ack: %w", err)
	}
	if state != StateDelivered {
		return nil
	}

	if kind == CmdPrompt && result == ResultFailed && message == TurnInProgress && attempts+1 < MaxAttempts {
		if _, err := c.db.ExecContext(ctx,
			`UPDATE link_commands
			    SET state = 'queued', delivered_at = NULL, attempts = attempts + 1,
			        wait_turn = 1, result = ?, message = ?
			  WHERE id = ?`, result, msg, id); err != nil {
			return fmt.Errorf("link commands: ack: %w", err)
		}
		c.note(sid, id, kind, StateQueued, message)
		return nil
	}

	final := StateDone
	if result == ResultFailed {
		final = StateFailed
	}
	bump := 0
	if kind == CmdPrompt && message == TurnInProgress {
		bump = 1 // the MaxAttempts-th bounce
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE link_commands
		    SET state = ?, acked_at = ?, result = ?, message = ?, attempts = attempts + ?
		  WHERE id = ?`, final, stamp(c.now()), result, msg, bump, id); err != nil {
		return fmt.Errorf("link commands: ack: %w", err)
	}
	c.note(sid, id, kind, final, message)
	return nil
}

// ReleaseWaitTurn makes the session's prompts that bounced on "turn in
// progress" deliverable again and wakes its waiting poll. link.Ingest does the
// same release inside its transaction when it stores a turn.complete; this is
// the direct form.
func (c *Commands) ReleaseWaitTurn(ctx context.Context, sessionID int64) (int64, error) {
	n, err := releaseWaitTurn(ctx, c.db, sessionID)
	if err != nil || n == 0 {
		return n, err
	}
	var uuid string
	if err := c.db.QueryRowContext(ctx,
		`SELECT session_uuid FROM sessions WHERE id = ?`, sessionID).Scan(&uuid); err == nil {
		c.Wake(uuid)
	}
	return n, nil
}

// execer is the part of *sql.DB / *sql.Tx releaseWaitTurn needs.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// releaseWaitTurn clears wait_turn on the session's held prompts.
func releaseWaitTurn(ctx context.Context, ex execer, sessionID int64) (int64, error) {
	r, err := ex.ExecContext(ctx,
		`UPDATE link_commands SET wait_turn = 0
		  WHERE session_id = ? AND state = 'queued' AND wait_turn = 1`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("link commands: release wait_turn: %w", err)
	}
	return r.RowsAffected()
}

// Wake wakes every long-poll waiting for the session with this Claude session
// id, so it looks for a deliverable command again. Spurious wakes are harmless.
func (c *Commands) Wake(sessionUUID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.slots[sessionUUID]; ok {
		close(s.ch)
		delete(c.slots, sessionUUID)
	}
}

// Sweep expires overdue undelivered commands (StopTTL / PromptTTL) and forgets
// poll times older than PollFreshness. WaitNext sweeps inline before every
// look; Run sweeps between polls so an expiry is announced on time.
func (c *Commands) Sweep(ctx context.Context) error {
	c.rmw.Lock()
	err := c.expireLocked(ctx)
	c.rmw.Unlock()

	c.mu.Lock()
	now := c.now()
	for uuid, at := range c.lastPoll {
		if now.Sub(at) >= PollFreshness {
			delete(c.lastPoll, uuid)
		}
	}
	c.mu.Unlock()
	return err
}

// Run sweeps every interval until ctx ends.
func (c *Commands) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Sweep(ctx); err != nil && ctx.Err() == nil {
				log.Printf("warn: link commands sweep: %v", err)
			}
		}
	}
}

// touch records a poll.
func (c *Commands) touch(sessionUUID string) {
	c.mu.Lock()
	c.lastPoll[sessionUUID] = c.now()
	c.mu.Unlock()
}

func (c *Commands) subscribe(sessionUUID string) *waitSlot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.slots[sessionUUID]
	if !ok {
		s = &waitSlot{ch: make(chan struct{})}
		c.slots[sessionUUID] = s
	}
	s.n++
	return s
}

func (c *Commands) release(sessionUUID string, s *waitSlot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.n--
	if s.n == 0 && c.slots[sessionUUID] == s {
		delete(c.slots, sessionUUID)
	}
}

// claim expires what is overdue, then marks the session's oldest deliverable
// command delivered and returns it; (nil, nil) when there is none.
func (c *Commands) claim(ctx context.Context, sessionUUID string) (*Command, error) {
	c.rmw.Lock()
	defer c.rmw.Unlock()
	if err := c.expireLocked(ctx); err != nil {
		return nil, err
	}
	now := c.now()
	var (
		cmd     Command
		payload string
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT c.id, c.session_id, c.kind, c.payload
		   FROM link_commands c JOIN sessions s ON s.id = c.session_id
		  WHERE s.session_uuid = ? AND c.wait_turn = 0
		    AND (c.state = 'queued' OR (c.state = 'delivered' AND c.delivered_at < ?))
		  ORDER BY c.created_at, c.id
		  LIMIT 1`, sessionUUID, stamp(now.Add(-RedeliverAfter))).
		Scan(&cmd.ID, &cmd.SessionID, &cmd.Kind, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("link commands: next: %w", err)
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE link_commands SET state = 'delivered', delivered_at = ? WHERE id = ?`,
		stamp(now), cmd.ID); err != nil {
		return nil, fmt.Errorf("link commands: deliver: %w", err)
	}
	cmd.Payload = json.RawMessage(payload)
	c.note(cmd.SessionID, cmd.ID, cmd.Kind, StateDelivered, "")
	return &cmd, nil
}

// expireLocked moves overdue undelivered commands to expired and announces
// each. The caller holds c.rmw.
func (c *Commands) expireLocked(ctx context.Context) error {
	now := c.now()
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, session_id, kind FROM link_commands
		  WHERE state = 'queued' AND delivered_at IS NULL
		    AND created_at < CASE kind WHEN 'stop' THEN ? ELSE ? END`,
		stamp(now.Add(-StopTTL)), stamp(now.Add(-PromptTTL)))
	if err != nil {
		return fmt.Errorf("link commands: expire: %w", err)
	}
	type overdue struct {
		id, sid int64
		kind    string
	}
	var due []overdue
	for rows.Next() {
		var o overdue
		if err := rows.Scan(&o.id, &o.sid, &o.kind); err != nil {
			rows.Close()
			return fmt.Errorf("link commands: expire: %w", err)
		}
		due = append(due, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("link commands: expire: %w", err)
	}
	for _, o := range due {
		if _, err := c.db.ExecContext(ctx,
			`UPDATE link_commands SET state = 'expired' WHERE id = ? AND state = 'queued'`, o.id); err != nil {
			return fmt.Errorf("link commands: expire: %w", err)
		}
		c.note(o.sid, o.id, o.kind, StateExpired, "")
	}
	return nil
}

// note publishes one link_command transition.
func (c *Commands) note(sessionID, commandID int64, kind, state, message string) {
	c.mu.Lock()
	pub := c.publish
	c.mu.Unlock()
	if pub == nil {
		return
	}
	pub(ingest.Notification{
		Type:      ingest.NoteLinkCommand,
		SessionID: sessionID,
		CommandID: commandID,
		Kind:      kind,
		State:     state,
		Message:   message,
	})
}

// stamp formats t the way every link timestamp is stored.
func stamp(t time.Time) string { return t.UTC().Format(tsFormat) }

// nullable maps "" to SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
