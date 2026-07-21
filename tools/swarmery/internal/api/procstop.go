package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// StopSession implements POST /api/sessions/{id}/stop.
//
// Stop is the graceful sibling of KillSession: the session ends as
// 'completed' (not 'killed'), and it succeeds even with no known PID — a
// zombie row must always be closable. When the process IS alive and provably
// the same claude process, it gets the same SIGTERM + SIGKILL escalation as
// Kill; any identity-guard failure silently downgrades to "mark only".
//
// Unlike 'killed', 'completed' is not terminal in ingest: a stopped session
// that later produces transcript records legitimately resurrects to active.
func (h *Handler) StopSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	var (
		status        string
		pid           sql.NullInt64
		procStartedAt sql.NullString
		procState     sql.NullString
		sessionUUID   string
	)
	err = h.DB.QueryRow(
		`SELECT session_uuid, status, pid, proc_started_at, proc_state FROM sessions WHERE id = ?`, id,
	).Scan(&sessionUUID, &status, &pid, &procStartedAt, &procState)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if status == "completed" || status == "killed" {
		http.Error(w, `{"error":"session already finished"}`, http.StatusConflict)
		return
	}

	signalled := false
	if pid.Valid && pid.Int64 > 0 &&
		(procState.String == procwatch.StateRunning || procState.String == procwatch.StateOrphaned) {
		info, infoErr := procwatch.OsProvider{}.Info(int(pid.Int64))
		alive := infoErr == nil && isSameClaudeProc(info, procStartedAt.String)
		if alive {
			if killErr := syscall.Kill(int(pid.Int64), syscall.SIGTERM); killErr != nil {
				log.Printf("procstop: SIGTERM pid %d (session %s): %v", pid.Int64, sessionUUID, killErr)
			} else {
				signalled = true
				go escalateKill(int(pid.Int64), procStartedAt.String, sessionUUID, killEscalationDelay())
			}
		}
	}

	// Unlike KillSession (which log-and-202s a failed UPDATE because the signal
	// already went out), Stop surfaces UPDATE failures as 500: marking the row
	// IS the endpoint's core job, and a retry is safe — the process, if it died
	// meanwhile, simply fails the alive check and the retry becomes mark-only.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.DB.Exec(
		`UPDATE sessions SET status = 'completed', proc_state = ?, proc_checked_at = ?,
		 ended_at = COALESCE(ended_at, ?)
		 WHERE id = ? AND status NOT IN ('completed','killed')`,
		procwatch.StateDead, now, now, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost a race with a concurrent Stop/Kill that finished the row after our
		// SELECT — report the truth instead of overwriting 'killed' with 'completed'.
		http.Error(w, `{"error":"session already finished"}`, http.StatusConflict)
		return
	}
	log.Printf("procstop: session %s stopped (signalled=%v)", sessionUUID, signalled)
	publishSessionUpdated(id)
	w.WriteHeader(http.StatusAccepted)
}
