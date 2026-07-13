package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// POST /api/hooks/session-start — called by the hookshim when a new Claude
// Code session starts. Binds the reported PID to the session after verifying
// the process command is "claude". Fire-and-forget: always returns 204.
func (h *Handler) hookSessionStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		PID       int    `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PID <= 0 || body.SessionID == "" {
		w.WriteHeader(http.StatusNoContent) // fire-and-forget — never error back
		return
	}

	info, err := procwatch.OsProvider{}.Info(body.PID)
	if err != nil || info == nil || !strings.Contains(strings.ToLower(info.Command), "claude") {
		w.WriteHeader(http.StatusNoContent) // not a claude process — ignore silently
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err = h.DB.Exec(`UPDATE sessions SET pid = ?, pid_source = 'hook',
		proc_started_at = ?, proc_state = 'running', proc_checked_at = ?
		WHERE session_uuid = ?`,
		body.PID, info.StartTime, now, body.SessionID); err != nil {
		log.Printf("prockill: bind pid for session %s: %v", body.SessionID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// KillSession implements POST /api/sessions/{id}/kill.
// Exported so the _test package can reach it directly.
func (h *Handler) KillSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck // empty body → Force=false

	idArg := r.PathValue("id")
	id, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	var (
		pid           sql.NullInt64
		procStartedAt sql.NullString
		procState     sql.NullString
		sessionUUID   string
	)
	err = h.DB.QueryRow(
		`SELECT session_uuid, pid, proc_started_at, proc_state FROM sessions WHERE id = ?`, id,
	).Scan(&sessionUUID, &pid, &procStartedAt, &procState)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	if !pid.Valid || pid.Int64 == 0 {
		http.Error(w, `{"error":"session has no known PID"}`, http.StatusConflict)
		return
	}
	if procState.String != procwatch.StateRunning && procState.String != procwatch.StateOrphaned {
		http.Error(w, `{"error":"session is not in a killable state"}`, http.StatusConflict)
		return
	}

	// Re-verify process identity immediately before signaling.
	info, err := procwatch.OsProvider{}.Info(int(pid.Int64))
	if err != nil || info == nil {
		http.Error(w, `{"error":"process not found"}`, http.StatusConflict)
		return
	}
	if !strings.Contains(strings.ToLower(info.Command), "claude") {
		http.Error(w, `{"error":"PID does not belong to a claude process"}`, http.StatusConflict)
		return
	}
	if procStartedAt.Valid && procStartedAt.String != "" && info.StartTime != procStartedAt.String {
		http.Error(w, `{"error":"PID reused — refusing to kill"}`, http.StatusConflict)
		return
	}

	sig := syscall.SIGTERM
	if req.Force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(int(pid.Int64), sig); err != nil {
		writeErr(w, fmt.Errorf("kill pid %d sig %d: %w", pid.Int64, sig, err))
		return
	}
	log.Printf("prockill: sent sig %d to pid %d (session %s, force=%v)", sig, pid.Int64, sessionUUID, req.Force)
	w.WriteHeader(http.StatusAccepted)
}
