package ingest

import (
	"database/sql"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// Thresholds configure the time-based session status heuristic (C5).
// MVP emits ONLY active | idle | completed:
//
//	active    — last record < Active ago
//	idle      — last record < Idle ago
//	completed — otherwise
type Thresholds struct {
	Active time.Duration
	Idle   time.Duration
}

// DefaultThresholds returns the documented defaults: 2 min / 30 min.
func DefaultThresholds() Thresholds {
	return Thresholds{Active: 2 * time.Minute, Idle: 30 * time.Minute}
}

func (t Thresholds) orDefaults() Thresholds {
	d := DefaultThresholds()
	if t.Active <= 0 {
		t.Active = d.Active
	}
	if t.Idle <= 0 {
		t.Idle = d.Idle
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

// RecomputeStatuses moves stale sessions forward (active → idle → completed)
// based on their last record timestamp, returning the ids of changed sessions
// so the caller can emit session_updated for each. Reactivation happens on the
// ingest path (new records re-upsert the session), never here.
//
// Liveness override: a session is never fast-forwarded to "completed" while
// procwatch believes its process is still alive — it caps at "idle" so a
// live-but-quiet session stops reporting "Done". Sessions with no liveness
// signal (proc_state NULL/dead/unknown) keep the pure time-based fallback;
// procwatch itself already flips genuinely dead ones to "completed".
func RecomputeStatuses(db *sql.DB, t Thresholds, now time.Time) ([]int64, error) {
	rows, err := db.Query(
		`SELECT id, status, COALESCE(ended_at, started_at), proc_state FROM sessions
		 WHERE status IN ('active','idle')`)
	if err != nil {
		return nil, err
	}
	type change struct {
		id     int64
		status string
	}
	var changes []change
	for rows.Next() {
		var id int64
		var status, lastTS string
		var procState sql.NullString
		if err := rows.Scan(&id, &status, &lastTS, &procState); err != nil {
			rows.Close()
			return nil, err
		}
		last := parseTS(lastTS)
		if last.IsZero() {
			continue // unparseable timestamp — leave the session as-is
		}
		want := StatusFor(last, now, t)
		if want == "completed" && procAlive(procState.String) {
			want = "idle" // alive but quiet — don't claim it finished
		}
		if want != status {
			changes = append(changes, change{id, want})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	changed := make([]int64, 0, len(changes))
	for _, c := range changes {
		if _, err := db.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, c.status, c.id); err != nil {
			return changed, err
		}
		changed = append(changed, c.id)
	}
	return changed, nil
}
