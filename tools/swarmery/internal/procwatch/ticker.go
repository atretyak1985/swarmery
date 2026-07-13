package procwatch

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

const defaultInterval = 30 * time.Second

// Ticker periodically checks process liveness for active/idle sessions with a
// known PID and updates proc_state in the DB. Dead sessions are fast-forwarded
// to status='completed' so "Active now" stops lying.
type Ticker struct {
	DB       *sql.DB
	Provider Provider
	Interval time.Duration
	// OnStateChange is called with the session ID whenever proc_state changes.
	// Use this to publish session_updated on the ingest bus.
	OnStateChange func(sessionID int64)
}

// CheckAll runs one liveness pass. Returns the IDs of sessions whose
// proc_state changed.
func (t *Ticker) CheckAll(now time.Time) ([]int64, error) {
	type row struct {
		id            int64
		pid           int
		procStartedAt sql.NullString
		procState     sql.NullString
		cwd           sql.NullString
	}

	rows, err := t.DB.Query(`
		SELECT id, pid, proc_started_at, proc_state, cwd
		FROM sessions
		WHERE status IN ('active','idle') AND pid IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	var sessions []row
	for rows.Next() {
		var s row
		if err := rows.Scan(&s.id, &s.pid, &s.procStartedAt, &s.procState, &s.cwd); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	nowStr := now.UTC().Format(time.RFC3339)
	var changed []int64
	for _, s := range sessions {
		newState := t.checkOne(s.pid, s.procStartedAt.String, s.procState.String)
		if newState == s.procState.String {
			t.DB.Exec(`UPDATE sessions SET proc_checked_at = ? WHERE id = ?`, nowStr, s.id) //nolint:errcheck
			continue
		}
		var execErr error
		if newState == StateDead {
			_, execErr = t.DB.Exec(
				`UPDATE sessions SET proc_state = ?, proc_checked_at = ?, status = 'completed' WHERE id = ?`,
				newState, nowStr, s.id)
		} else {
			_, execErr = t.DB.Exec(
				`UPDATE sessions SET proc_state = ?, proc_checked_at = ? WHERE id = ?`,
				newState, nowStr, s.id)
		}
		if execErr != nil {
			log.Printf("procwatch: update session %d: %v", s.id, execErr)
			continue
		}
		changed = append(changed, s.id)
	}

	if err := t.heuristicMatch(nowStr); err != nil {
		log.Printf("procwatch: heuristic match: %v", err)
	}
	return changed, nil
}

// checkOne derives the new proc_state for one session without touching the DB.
func (t *Ticker) checkOne(pid int, procStartedAt, currentState string) string {
	info, err := t.Provider.Info(pid)
	if err != nil {
		log.Printf("procwatch: info pid %d: %v", pid, err)
		return currentState // leave unchanged on transient error
	}
	if info == nil {
		return StateDead
	}
	// PID reuse guard: if we recorded a start time and it changed, the original
	// claude process is gone and the PID has been recycled.
	if procStartedAt != "" && info.StartTime != procStartedAt {
		return StateDead
	}
	if !strings.Contains(strings.ToLower(info.Command), "claude") {
		return StateDead
	}
	orphaned, err := t.Provider.IsOrphaned(pid)
	if err != nil {
		log.Printf("procwatch: orphan check pid %d: %v", pid, err)
		return StateRunning // err → assume alive
	}
	if orphaned {
		return StateOrphaned
	}
	return StateRunning
}

// heuristicMatch tries to bind PIDs for active/idle sessions without one,
// using cwd + command matching. Skips ambiguous cases (multiple candidates).
func (t *Ticker) heuristicMatch(nowStr string) error {
	rows, err := t.DB.Query(`
		SELECT id, cwd FROM sessions
		WHERE status IN ('active','idle') AND pid IS NULL AND cwd IS NOT NULL AND cwd != ''`)
	if err != nil {
		return err
	}
	type unmapped struct {
		id  int64
		cwd string
	}
	var unmappeds []unmapped
	for rows.Next() {
		var u unmapped
		if err := rows.Scan(&u.id, &u.cwd); err != nil {
			rows.Close()
			return err
		}
		unmappeds = append(unmappeds, u)
	}
	rows.Close()

	for _, u := range unmappeds {
		pids, err := t.Provider.MatchByDir(u.cwd)
		if err != nil || len(pids) != 1 {
			continue // error or ambiguous — leave unbound
		}
		pid := pids[0]
		info, err := t.Provider.Info(pid)
		if err != nil || info == nil {
			continue
		}
		t.DB.Exec(`UPDATE sessions SET pid = ?, pid_source = 'scan',
			proc_started_at = ?, proc_state = 'running', proc_checked_at = ?
			WHERE id = ?`,
			pid, info.StartTime, nowStr, u.id)
	}
	return nil
}

// Run starts the periodic liveness ticker. Blocks until ctx is cancelled.
func (t *Ticker) Run(ctx context.Context) {
	interval := t.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			changed, err := t.CheckAll(now)
			if err != nil {
				log.Printf("procwatch: tick: %v", err)
				continue
			}
			if t.OnStateChange != nil {
				for _, id := range changed {
					t.OnStateChange(id)
				}
			}
		}
	}
}
