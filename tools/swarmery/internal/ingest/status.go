package ingest

import (
	"database/sql"
	"time"
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

// RecomputeStatuses moves stale sessions forward (active → idle → completed)
// based on their last record timestamp, returning the ids of changed sessions
// so the caller can emit session_updated for each. Reactivation happens on the
// ingest path (new records re-upsert the session), never here.
func RecomputeStatuses(db *sql.DB, t Thresholds, now time.Time) ([]int64, error) {
	rows, err := db.Query(
		`SELECT id, status, COALESCE(ended_at, started_at) FROM sessions
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
		if err := rows.Scan(&id, &status, &lastTS); err != nil {
			rows.Close()
			return nil, err
		}
		last := parseTS(lastTS)
		if last.IsZero() {
			continue // unparseable timestamp — leave the session as-is
		}
		if want := StatusFor(last, now, t); want != status {
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
