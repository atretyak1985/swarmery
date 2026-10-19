package link

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Event kinds the mod sends (mods-swarmery-link phase 1, Implementation
// Details 1). Anything else is rejected per event, never per batch.
const (
	KindTurnStart      = "turn.start"
	KindTurnComplete   = "turn.complete"
	KindToolCall       = "tool.call"
	KindSessionMeasure = "session.measure"
	KindSessionStart   = "session.start"
	KindSessionEnd     = "session.end"
)

// link_status values written to sessions.link_status.
const (
	StatusWorking = "working"
	StatusWaiting = "waiting"
	StatusDone    = "done"
)

// statusFor maps the status-bearing kinds; the others leave link_status alone.
var statusFor = map[string]string{
	KindTurnStart:    StatusWorking,
	KindTurnComplete: StatusWaiting,
	KindSessionEnd:   StatusDone,
}

var knownKinds = map[string]bool{
	KindTurnStart: true, KindTurnComplete: true, KindToolCall: true,
	KindSessionMeasure: true, KindSessionStart: true, KindSessionEnd: true,
	KindTurnForecast: true, KindTurnActual: true, // phase 10, forecast.go
	// phase 6, swarm.go
	KindSessionSend: true, KindSessionReceive: true,
	KindClaim: true, KindRelease: true, KindConflict: true,
}

// tsFormat is the fixed-width UTC form every link timestamp is stored in, so
// the TEXT columns order lexically the same as in time (the daemon-wide form,
// e.g. internal/approvals tsFormat).
const tsFormat = "2006-01-02T15:04:05.000Z"

// Event is one mod event as it arrives on POST /api/link/events. SessionID is
// the Claude session id, which equals sessions.session_uuid (mods-format §M1).
type Event struct {
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Kind      string          `json:"kind"`
	At        string          `json:"at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Reject names one event of a batch that was not stored, by its index in the
// batch.
type Reject struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// Result counts a batch's outcome. Accepted + Duplicates + Rejected equals the
// batch length.
type Result struct {
	Accepted   int      `json:"accepted"`
	Duplicates int      `json:"duplicates"`
	Rejected   int      `json:"rejected"`
	Rejects    []Reject `json:"rejects,omitempty"`
}

// measure is the session.measure payload (mods-format §M2, fixture
// testdata/fixtures/mods/session-usage.json). Every unit is optional: a
// process's first measure carries no context.percent, and rateLimits is empty
// off a subscription.
type measure struct {
	Context *struct {
		Percent *float64 `json:"percent"`
	} `json:"context"`
	RateLimits json.RawMessage `json:"rateLimits"`
	Cost       *struct {
		USD *float64 `json:"usd"` // per process, not lifetime (§M2)
	} `json:"cost"`
}

// normalized is a validated event, ready to store.
type normalized struct {
	Event
	at      string   // tsFormat
	payload any      // nil → SQL NULL
	measure *measure // set for session.measure only

	// turn is set for turn.forecast / turn.actual only (forecast.go).
	turn *turnHalf

	// swarm is set for the swarm kinds only (swarm.go).
	swarm *swarmEvent
}

// Ingest stores a batch of mod events in one transaction and returns its
// counts. For each event it finds the sessions row by Claude session id —
// minting a minimal source='mod' row (project resolved from cwd through
// ingest.UpsertProject, the resolver every channel shares) when the transcript
// has not been tailed yet, and promoting a 'jsonl' row to 'both' — inserts the
// event into link_events (a repeat of (session, kind, at) is a Duplicate), and
// folds it into the sessions.link_* columns.
//
// Only the newest status-bearing event decides link_status and only the newest
// session.measure decides the measure columns, so events arriving out of order
// (§M2: measure and turn.complete have no fixed order) never roll state back.
//
// An invalid event (unknown kind, missing session id, bad timestamp, bad
// payload) is counted in Rejected and skipped; it never fails the batch. A
// database error does, and rolls the whole batch back.
//
// A newly stored turn.complete also releases the session's link_commands
// held for a turn end (see Commands.Ack); the caller wakes the poll.
//
// publish is called once per session the batch touched, after commit:
// NoteSessionStarted for a row the batch minted, NoteSessionUpdated otherwise,
// then NoteTurnSurprise once per turn.forecast/turn.actual that left its
// turn_forecasts row scored (forecast.go), then NoteSwarmMessage once per
// stored session.send / session.receive and NoteSwarmClaim once per project
// whose live claims a claim / release changed (swarm.go). It may be nil.
func Ingest(ctx context.Context, db *sql.DB, publish func(ingest.Notification), batch []Event) (Result, error) {
	var res Result
	valid := make([]normalized, 0, len(batch))
	firstAt := map[string]string{} // session uuid → earliest valid at in the batch
	for i, ev := range batch {
		n, err := validate(ev)
		if err != nil {
			res.Rejected++
			res.Rejects = append(res.Rejects, Reject{Index: i, Reason: err.Error()})
			continue
		}
		if cur, ok := firstAt[n.SessionID]; !ok || n.at < cur {
			firstAt[n.SessionID] = n.at
		}
		valid = append(valid, n)
	}
	if len(valid) == 0 {
		return res, nil
	}

	conn, err := acquire(ctx, db)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("link ingest: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	ids := map[string]int64{} // session uuid → sessions.id
	created := map[int64]bool{}
	touched := []int64{}
	seen := map[int64]bool{}
	var scored []ingest.Notification  // turn_surprise, published after commit
	var swarmed []ingest.Notification // swarm_message / swarm_claim, after commit
	claimed := map[int64]bool{}       // projects whose swarm_claim is queued
	projects := map[int64]int64{}     // sessions.id → project_id
	for _, n := range valid {
		sid, ok := ids[n.SessionID]
		if !ok {
			var minted bool
			sid, minted, err = resolveSession(ctx, tx, n.SessionID, n.Cwd, firstAt[n.SessionID])
			if err != nil {
				return Result{}, err
			}
			ids[n.SessionID] = sid
			if minted {
				created[sid] = true
				seen[sid] = true
				touched = append(touched, sid)
			}
		}
		stored, err := insertEvent(ctx, tx, sid, n)
		if err != nil {
			return Result{}, err
		}
		if !stored {
			res.Duplicates++
			continue
		}
		res.Accepted++
		if err := apply(ctx, tx, sid, n); err != nil {
			return Result{}, err
		}
		// Control channel (Commands): a prompt that bounced on "turn in
		// progress" is held until the session's next turn end. Released in the
		// same transaction, and only for a newly stored turn.complete, so a
		// resent one never releases twice. Waking the waiting poll is the
		// caller's job (Commands.Wake): this function has no queue.
		if n.Kind == KindTurnComplete {
			if _, err := releaseWaitTurn(ctx, tx, sid); err != nil {
				return Result{}, err
			}
		}
		if id, err := applyTurn(ctx, tx, sid, n); err != nil {
			return Result{}, err
		} else if id != 0 {
			scored = append(scored, ingest.Notification{Type: ingest.NoteTurnSurprise, SessionID: sid, TurnForecastID: id})
		}
		if msg, claims, err := applySwarm(ctx, tx, n); err != nil {
			return Result{}, err
		} else if msg || claims {
			pid, ok := projects[sid]
			if !ok {
				if pid, err = sessionProject(ctx, tx, sid); err != nil {
					return Result{}, err
				}
				projects[sid] = pid
			}
			if msg {
				swarmed = append(swarmed, ingest.Notification{Type: ingest.NoteSwarmMessage, SessionID: sid, ProjectID: pid})
			}
			if claims && !claimed[pid] {
				claimed[pid] = true
				swarmed = append(swarmed, ingest.Notification{Type: ingest.NoteSwarmClaim, SessionID: sid, ProjectID: pid})
			}
		}
		if !seen[sid] {
			seen[sid] = true
			touched = append(touched, sid)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("link ingest: commit: %w", err)
	}
	if publish != nil {
		for _, sid := range touched {
			typ := ingest.NoteSessionUpdated
			if created[sid] {
				typ = ingest.NoteSessionStarted
			}
			publish(ingest.Notification{Type: typ, SessionID: sid})
		}
		for _, n := range scored {
			publish(n)
		}
		for _, n := range swarmed {
			publish(n)
		}
	}
	return res, nil
}

// AcquireTimeout bounds how long Ingest queues for a store connection. The
// store runs on one connection (internal/store.Open: SetMaxOpenConns(1)) and
// database/sql hands a freed connection to a random waiter, so under load a
// single acquire can wait far longer than the mod's 5 s request budget
// (reports/link-ingest-stall.md). The bound sits under that budget, so the mod
// hears "busy, retry" before it gives up on its own.
const AcquireTimeout = 4 * time.Second

// ErrBusy is Ingest's answer when no store connection came free within
// AcquireTimeout. Nothing of the batch was stored; the caller may resend it
// as is (dedup makes that safe).
var ErrBusy = errors.New("link ingest: database busy")

// acquire checks out one store connection, waiting at most AcquireTimeout.
// Only the wait is bounded: once Ingest holds the connection its transaction
// runs on ctx alone. A caller whose own ctx ended gets that error, not ErrBusy.
func acquire(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	wait, cancel := context.WithTimeout(ctx, AcquireTimeout)
	defer cancel()
	conn, err := db.Conn(wait)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: no connection within %s", ErrBusy, AcquireTimeout)
	}
	return nil, fmt.Errorf("link ingest: begin: %w", err)
}

// sessionProject returns the session's project_id.
func sessionProject(ctx context.Context, q dbtx, sid int64) (int64, error) {
	var pid int64
	if err := q.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id = ?`, sid).Scan(&pid); err != nil {
		return 0, fmt.Errorf("link: project of session %d: %w", sid, err)
	}
	return pid, nil
}

// validate checks one event and brings it to its stored form.
func validate(ev Event) (normalized, error) {
	n := normalized{Event: ev}
	if ev.SessionID == "" {
		return n, errors.New("sessionId is required")
	}
	if !knownKinds[ev.Kind] {
		return n, fmt.Errorf("unknown kind %q", ev.Kind)
	}
	t, err := time.Parse(time.RFC3339Nano, ev.At)
	if err != nil {
		return n, fmt.Errorf("at %q is not RFC 3339", ev.At)
	}
	n.at = t.UTC().Format(tsFormat)
	raw := bytes.TrimSpace(ev.Payload)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if !json.Valid(raw) {
			return n, errors.New("payload is not valid JSON")
		}
		n.payload = string(raw)
	}
	if ev.Kind == KindSessionMeasure {
		var m measure
		if n.payload != nil {
			if err := json.Unmarshal(raw, &m); err != nil {
				return n, fmt.Errorf("session.measure payload: %v", err)
			}
		}
		n.measure = &m
	}
	if n.turn, err = validateTurn(ev.Kind, raw); err != nil {
		return n, err
	}
	if n.swarm, err = validateSwarm(ev.Kind, ev.SessionID, n.at, raw); err != nil {
		return n, err
	}
	return n, nil
}

// resolveSession returns the sessions.id for uuid, minting a source='mod' row
// when none exists (minted=true) and promoting a 'jsonl' row to 'both'.
// startedAt is the earliest event time the batch carries for this session.
func resolveSession(ctx context.Context, tx *sql.Tx, uuid, cwd, startedAt string) (id int64, minted bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE session_uuid = ?`, uuid).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET source = 'both' WHERE id = ? AND source = 'jsonl'`, id); err != nil {
			return 0, false, fmt.Errorf("link ingest: promote session %d: %w", id, err)
		}
		return id, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, fmt.Errorf("link ingest: find session: %w", err)
	}

	// Same attribution as the JSONL ingest and the hooks channel: a real cwd
	// keys the project; an absent one parks the row on '(unknown)', which the
	// ingest upsert / HealStubSessions re-attribute once the transcript shows.
	if cwd == "" {
		cwd = ingest.UnknownProjectPath
	}
	projectID, _, err := ingest.UpsertProject(tx, cwd, startedAt, startedAt)
	if err != nil {
		return 0, false, fmt.Errorf("link ingest: project for %s: %w", cwd, err)
	}
	r, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (?, ?, ?, 'active', ?, 'mod')`,
		projectID, uuid, cwd, startedAt)
	if err != nil {
		return 0, false, fmt.Errorf("link ingest: insert session: %w", err)
	}
	id, _ = r.LastInsertId()
	// A SessionStart hook that beat every row-minting channel parked the
	// session's identity (pid, terminal); every minting site applies it.
	if err := ingest.ApplyPendingTerminal(tx, uuid, id); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// insertEvent stores n; stored=false means (session, kind, at) was already
// there — a resent event.
func insertEvent(ctx context.Context, tx *sql.Tx, sid int64, n normalized) (stored bool, err error) {
	r, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO link_events (session_id, kind, at, payload) VALUES (?, ?, ?, ?)`,
		sid, n.Kind, n.at, n.payload)
	if err != nil {
		return false, fmt.Errorf("link ingest: insert event: %w", err)
	}
	rows, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("link ingest: insert event: %w", err)
	}
	return rows == 1, nil
}

// apply folds a stored event into sessions.link_*. The event is already in
// link_events, so "no newer event of this family" is a NOT EXISTS over it,
// served by idx_link_events_session_at.
func apply(ctx context.Context, tx *sql.Tx, sid int64, n normalized) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET link_seen_at = MAX(COALESCE(link_seen_at, ''), ?) WHERE id = ?`,
		n.at, sid); err != nil {
		return fmt.Errorf("link ingest: seen_at: %w", err)
	}
	if status, ok := statusFor[n.Kind]; ok {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET link_status = ? WHERE id = ? AND NOT EXISTS (
			   SELECT 1 FROM link_events
			    WHERE session_id = ? AND at > ?
			      AND kind IN ('turn.start', 'turn.complete', 'session.end'))`,
			status, sid, sid, n.at); err != nil {
			return fmt.Errorf("link ingest: status: %w", err)
		}
	}
	if m := n.measure; m != nil {
		var pct, cost, limits any
		if m.Context != nil && m.Context.Percent != nil {
			pct = *m.Context.Percent
		}
		if m.Cost != nil && m.Cost.USD != nil {
			cost = *m.Cost.USD
		}
		if raw := bytes.TrimSpace(m.RateLimits); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			limits = string(raw)
		}
		// An absent unit keeps its last value: "not reported in this measure"
		// (a fresh process before its first response) is not "zero".
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET link_context_pct = COALESCE(?, link_context_pct),
			                     link_rate_limits = COALESCE(?, link_rate_limits),
			                     link_cost_usd    = COALESCE(?, link_cost_usd)
			  WHERE id = ? AND NOT EXISTS (
			    SELECT 1 FROM link_events
			     WHERE session_id = ? AND kind = 'session.measure' AND at > ?)`,
			pct, limits, cost, sid, sid, n.at); err != nil {
			return fmt.Errorf("link ingest: measure: %w", err)
		}
	}
	return nil
}
