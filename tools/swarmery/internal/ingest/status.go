package ingest

import (
	"database/sql"
	"log"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// Thresholds configure the time-based session status heuristic (C5).
// The ingest path emits ONLY active | idle | completed:
//
//	active    — last record < Active ago
//	idle      — last record < Idle ago
//	completed — otherwise
//
// The status ticker (RecomputeStatuses) additionally moves an interactive
// session to awaiting_reply once it has been quiet for AwaitAfter after a
// main-thread end_turn — see the detector there.
type Thresholds struct {
	Active time.Duration
	Idle   time.Duration
	// AwaitAfter is the quiet time after a main-thread end_turn before an
	// interactive session counts as waiting for the operator's reply.
	AwaitAfter time.Duration
}

// StatusAwaitingReply is the sessions.status value for an interactive session
// that ended its turn and waits for a typed reply. Entered only by
// RecomputeStatuses; cleared by the ingest upsert on the next batch, which
// deliberately does not protect it (ingest.go, the session UPDATE).
const StatusAwaitingReply = "awaiting_reply"

// entrypointHeadless is the transcript entrypoint of a headless -p / SDK run
// (every daemon spawn included): such a session never awaits a reply.
const entrypointHeadless = "sdk-cli"

// DefaultThresholds returns the documented defaults: 2 min / 30 min / 3 min.
func DefaultThresholds() Thresholds {
	return Thresholds{Active: 2 * time.Minute, Idle: 30 * time.Minute, AwaitAfter: 3 * time.Minute}
}

func (t Thresholds) orDefaults() Thresholds {
	d := DefaultThresholds()
	if t.Active <= 0 {
		t.Active = d.Active
	}
	if t.Idle <= 0 {
		t.Idle = d.Idle
	}
	if t.AwaitAfter <= 0 {
		t.AwaitAfter = d.AwaitAfter
	}
	return t
}

// StatusFor computes the session status from its last-activity time.
func StatusFor(lastActivity, now time.Time, t Thresholds) string {
	t = t.orDefaults()
	age := now.Sub(lastActivity)
	switch {
	case age < t.Active:
		return "active"
	case age < t.Idle:
		return "idle"
	default:
		return "completed"
	}
}

// procAlive reports whether procwatch currently believes the backing process
// exists. Orphaned counts as alive — the process is reparented, not gone.
func procAlive(state string) bool {
	return state == procwatch.StateRunning || state == procwatch.StateOrphaned
}

// StatusChange is one ticker transition: the session row id and the status
// it was just moved to.
type StatusChange struct {
	ID     int64
	Status string
}

// RecomputeStatuses moves stale sessions forward (active → idle → completed)
// based on their last record timestamp, returning each changed session with
// its NEW status so the caller can emit session_updated (and, for
// 'completed', the terminal-notify callback). Reactivation happens on the
// ingest path (new records re-upsert the session), never here.
//
// Liveness override: a session is never fast-forwarded to "completed" while
// procwatch believes its process is still alive — it caps at "idle" so a
// live-but-quiet session stops reporting "Done". Sessions with no liveness
// signal (proc_state NULL/dead/unknown) keep the pure time-based fallback;
// procwatch itself already flips genuinely dead ones to "completed".
//
// Awaiting-reply detector: an interactive session (entrypoint != 'sdk-cli')
// whose process is not known dead, that has been quiet for AwaitAfter, would
// not otherwise close, and whose last main-thread turn ended on end_turn with
// nothing still open (lastTurnAwaitsReply) moves to 'awaiting_reply'. The
// time-based 'completed' is decided first, so a row with no liveness signal
// still closes after Idle; a row whose process is alive stays awaiting until
// the reply lands (the ingest upsert clears it) or the process exits
// (procwatch flips it to 'completed').
//
// A row with no parseable timestamp at all is closed rather than skipped —
// see the branch below. This is the backstop that makes "stuck in active
// forever" unreachable no matter which channel minted the row.
func RecomputeStatuses(db *sql.DB, t Thresholds, now time.Time) ([]StatusChange, error) {
	t = t.orDefaults()
	type candidate struct {
		id                         int64
		status, lastTS, entrypoint string
		procState                  sql.NullString
	}
	// Collect first, query per row only after Close: the store runs on a
	// single connection, so a nested query under an open cursor would block.
	rows, err := db.Query(
		`SELECT id, status, COALESCE(ended_at, started_at), proc_state, entrypoint FROM sessions
		 WHERE status IN ('active','idle','awaiting_reply')`)
	if err != nil {
		return nil, err
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.status, &c.lastTS, &c.procState, &c.entrypoint); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var changes []StatusChange
	for _, c := range cands {
		var want string
		if last := parseTS(c.lastTS); last.IsZero() {
			// No parseable timestamp on either end (a row minted from a
			// transcript batch that carried no record timestamps). Such a row
			// can NEVER age out on the time-based path — it used to be skipped
			// here and stayed 'active' for the life of the database, which is
			// how the Sessions page ended up reporting dozens of live agents
			// that had no process behind them. A row with no clock is by
			// definition not recent, so close it; the liveness override still
			// applies, so anything procwatch believes is alive caps at 'idle'.
			//
			// StatusFor is deliberately NOT reused here: now.Sub(zero time)
			// overflows time.Duration (int64 ns caps at ~292 years) and would
			// wrap to a small — even negative — age, i.e. back to 'active'.
			want = "completed"
			if procAlive(c.procState.String) {
				want = "idle"
			}
		} else {
			want = StatusFor(last, now, t)
			if want == "completed" && procAlive(c.procState.String) {
				want = "idle" // alive but quiet — don't claim it finished
			}
			// awaitingCandidate: not headless, quiet ≥ AwaitAfter, and either a
			// known interactive entrypoint (proc alive or unknown, never dead) or
			// an unknown entrypoint ('' = pre-migration) whose process is alive.
			if c.entrypoint != entrypointHeadless && c.procState.String != procwatch.StateDead &&
				(c.entrypoint != "" || procAlive(c.procState.String)) &&
				now.Sub(last) >= t.AwaitAfter && want != "completed" && lastTurnAwaitsReply(db, c.id) {
				want = StatusAwaitingReply
			}
		}
		if want != c.status {
			changes = append(changes, StatusChange{ID: c.id, Status: want})
		}
	}

	changed := make([]StatusChange, 0, len(changes))
	for _, c := range changes {
		if _, err := db.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, c.Status, c.ID); err != nil {
			return changed, err
		}
		changed = append(changed, c)
	}
	return changed, nil
}

// lastTurnAwaitsReply reports, in one query, whether the session's newest
// main-thread turn (agent_name IS NULL) is an assistant turn that ended on
// end_turn, with no tool call / subagent / skill on that turn still open, and
// no permission request pending for the session.
//
// stop_sequence is deliberately not "awaiting": it is what headless runs with
// sentinels end on. A query error is logged and read as "not awaiting" — the
// conservative answer, which leaves the row on the time-based path.
func lastTurnAwaitsReply(db *sql.DB, sessionID int64) bool {
	var ok bool
	err := db.QueryRow(`
		SELECT EXISTS (
		    SELECT 1 FROM turns t
		    WHERE t.id = (SELECT id FROM turns
		                  WHERE session_id = ?1 AND agent_name IS NULL
		                  ORDER BY seq DESC LIMIT 1)
		      AND t.role = 'assistant' AND t.stop_reason = 'end_turn'
		      AND NOT EXISTS (SELECT 1 FROM events e
		                      WHERE e.session_id = ?1 AND e.turn_id = t.id
		                        AND e.type IN ('tool_call','subagent_start','skill_use')
		                        AND e.status IS NULL)
		) AND NOT EXISTS (SELECT 1 FROM permission_requests
		                  WHERE session_id = ?1 AND status = 'pending')`,
		sessionID).Scan(&ok)
	if err != nil {
		log.Printf("warn: ingest: awaiting-reply check for session %d: %v", sessionID, err)
		return false
	}
	return ok
}
