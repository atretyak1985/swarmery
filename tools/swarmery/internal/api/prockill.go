package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plugindrift"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// killEscalationDelay is how long a graceful SIGTERM is given to work before the
// process is escalated to SIGKILL. Override with SWARMERY_KILL_ESCALATION (a Go
// duration, e.g. "8s"); default 5s, and "0" disables escalation entirely.
func killEscalationDelay() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SWARMERY_KILL_ESCALATION")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 5 * time.Second
}

// isSameClaudeProc reports whether info still describes the claude process we
// originally recorded: command matches and, when a start time was captured,
// it is unchanged (PID-reuse guard). Security-relevant — every signal path
// must go through this exact predicate.
func isSameClaudeProc(info *procwatch.ProcInfo, procStartedAt string) bool {
	if info == nil {
		return false
	}
	if !strings.Contains(strings.ToLower(info.Command), "claude") {
		return false
	}
	return procStartedAt == "" || info.StartTime == procStartedAt
}

// escalateKill waits delay, then SIGKILLs pid if it is still the same live
// claude process. It re-runs the full identity guard (command name +
// start-time) so a PID recycled to another process after SIGTERM is never
// signalled. Best-effort: the session is already marked killed by the caller.
func escalateKill(pid int, procStartedAt, sessionUUID string, delay time.Duration) {
	if delay <= 0 {
		return
	}
	time.Sleep(delay)
	info, err := procwatch.OsProvider{}.Info(pid)
	if err != nil || !isSameClaudeProc(info, procStartedAt) {
		return // SIGTERM worked, or PID recycled — nothing left to kill
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		log.Printf("prockill: escalate SIGKILL pid %d (session %s): %v", pid, sessionUUID, err)
		return
	}
	log.Printf("prockill: escalated to SIGKILL pid %d (session %s survived SIGTERM for %s)", pid, sessionUUID, delay)
}

// sessionStartProcInfo resolves the process behind a SessionStart PID.
// Swappable in tests so terminal-identity coverage does not depend on a real
// OS process literally named "claude"; production always uses the real OS
// provider.
var sessionStartProcInfo = procwatch.OsProvider{}.Info

// POST /api/hooks/session-start — called by the hookshim when a new Claude
// Code session starts. Binds the reported PID to the session after verifying
// the process command is "claude", records the terminal identity the shim
// read from its environment (migration 0068) plus the tty derived from that
// same PID, then answers 200 + additionalContext when this project has
// unloadable plugins, or 204 when it does not.
func (h *Handler) hookSessionStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		PID       int    `json:"pid"`
		// CWD is the project the session is starting in. Findings are keyed by
		// project path, and at SessionStart the sessions row may not exist yet,
		// so this — not a sessions lookup — is how the project is resolved.
		CWD string `json:"cwd"`
		// Terminal is the hookshim's terminal-identity object
		// (docs/hooks-protocol.md); every field is "" when its source
		// variable was absent (a daemon-spawned `claude -p` run sends none).
		// sessionId (ITERM_SESSION_ID/TERM_SESSION_ID) rides along on the wire
		// but is not decoded here — no column stores it in this phase.
		Terminal struct {
			Program  string `json:"program"`
			FocusURL string `json:"focusUrl"`
			BundleID string `json:"bundleId"`
		} `json:"terminal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PID <= 0 || body.SessionID == "" {
		w.WriteHeader(http.StatusNoContent) // fire-and-forget — never error back
		return
	}

	info, err := sessionStartProcInfo(body.PID)
	if err != nil || info == nil || !strings.Contains(strings.ToLower(info.Command), "claude") {
		w.WriteHeader(http.StatusNoContent) // not a claude process — ignore silently
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.DB.Exec(`UPDATE sessions SET pid = ?, pid_source = 'hook',
		proc_started_at = ?, proc_state = 'running', proc_checked_at = ?,
		term_program = ?, term_focus_url = ?, term_bundle_id = ?, term_tty = ?
		WHERE session_uuid = ?`,
		body.PID, info.StartTime, now,
		nullStr(body.Terminal.Program), nullStr(body.Terminal.FocusURL), nullStr(body.Terminal.BundleID), nullStr(info.TTY),
		body.SessionID)
	if err != nil {
		log.Printf("prockill: bind pid for session %s: %v", body.SessionID, err)
	} else if n, _ := res.RowsAffected(); n == 0 {
		// The transcript hasn't been ingested yet, so there is no sessions row
		// to UPDATE. Park the terminal identity — ingest applies it the moment
		// it mints the row (session_started path) — so the session does not
		// lose its terminal to this race. The pid/proc_state columns above are
		// lost to the same race today; that gap predates this phase and stays
		// out of scope here.
		ingest.ParkPendingTerminal(body.SessionID, ingest.SessionTerminal{
			Program:  body.Terminal.Program,
			FocusURL: body.Terminal.FocusURL,
			BundleID: body.Terminal.BundleID,
			TTY:      info.TTY,
		})
	}
	if ctx := h.driftContext(body.CWD); ctx != "" {
		writeJSON(w, map[string]string{"additionalContext": ctx}, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nullStr converts "" to a SQL NULL bind value so an absent terminal field
// stores as NULL, not the empty string.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// maxDriftContextLines caps the injection: this text is prepended to every
// affected session and is paid for in tokens each time.
const maxDriftContextLines = 5

// driftRefresher asks the drift ticker for an out-of-band pass; attached at
// startup, nil in tests and when the scanner is disabled.
var driftRefresher func()

// AttachDriftRefresher wires the out-of-band drift refresh.
func AttachDriftRefresher(f func()) { driftRefresher = f }

// driftContext renders the active error-severity plugin findings for the
// project owning cwd, as a short block for injection into the starting session.
// Returns "" when cwd is unknown or nothing is wrong.
//
// Reads only: the shim's 2s SessionStart budget rules out running a detection
// pass here, so this serves the last persisted pass and asks the ticker for a
// fresh one.
func (h *Handler) driftContext(cwd string) string {
	if cwd == "" {
		return ""
	}
	path := ingest.CanonicalProjectPath(h.DB, cwd)
	rows, err := h.DB.Query(
		`SELECT target, message FROM config_lint_findings
		  WHERE resolved_at IS NULL AND severity = 'error'
		    AND rule LIKE 'plugin\_%' ESCAPE '\'
		  ORDER BY target`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var target, message string
		if err := rows.Scan(&target, &message); err != nil {
			return ""
		}
		// plugin:detector has no "|", so ParseTarget rejects it: machine-wide
		// blindness belongs on the dashboard, not inside every session on the box.
		id, p, ok := plugindrift.ParseTarget(target)
		if !ok || p != path {
			continue
		}
		lines = append(lines, "- "+id+": "+message)
		if len(lines) == maxDriftContextLines {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if driftRefresher != nil {
		go driftRefresher()
	}
	return "Plugin problem detected in this project — the following enabled plugins are NOT loaded in this session, " +
		"so their agents, skills and commands are unavailable:\n" + strings.Join(lines, "\n") +
		"\nFix from the swarmery dashboard (project → plugins → repair), or run `claude plugin install <name>@<marketplace>`. " +
		"A restart is required for a repair to take effect."
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

	// A graceful SIGTERM may be ignored by a wedged process — escalate to
	// SIGKILL after a grace period if it is still the same live claude process.
	// Force kills are already SIGKILL, so they need no escalation.
	if !req.Force {
		go escalateKill(int(pid.Int64), procStartedAt.String, sessionUUID, killEscalationDelay())
	}

	// Eagerly reflect the kill so the UI unblocks immediately instead of waiting
	// up to one procwatch tick (30s): mark the session finished and its process
	// dead, then push a session_updated so the detail view (and its message
	// composer) re-enable in real time. procwatch/ingest never revert a 'killed'
	// row, and gating the composer on proc_state means it becomes writable at once.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.DB.Exec(
		`UPDATE sessions SET status = 'killed', proc_state = ?, proc_checked_at = ?,
		 ended_at = COALESCE(ended_at, ?) WHERE id = ?`,
		procwatch.StateDead, now, now, id); err != nil {
		log.Printf("prockill: mark session %d killed after signal: %v", id, err)
	}
	publishSessionUpdated(id)

	// Board capture lifecycle signal 2 (ingest/capture.go). A kill ends the work
	// this session's ACCEPTED cards stand for, so they move in_progress →
	// in_review and wait for the user's verdict instead of sitting in a column
	// that claims a dead session is still working on them. Same reasoning as the
	// hook-B call below for why the kill path has to carry it: 'killed' is
	// terminal and no later pass revisits the row.
	//
	// Cards the user never accepted (still 'triage'/'todo') are left alone —
	// a suggestion does not expire when its session does — and dispatcher-owned
	// rows are excluded by the sweep's own `origin != 'manual'` guard, so this
	// can never collide with dispatch/service.go's exit routing. Like the call
	// below it returns no error and cannot fail the kill; it runs on h.DB after
	// the UPDATE committed and opens its own short transaction.
	for _, taskID := range ingest.SweepSessionToReview(h.DB, id) {
		publishTaskUpdated(taskID)
	}

	// Board capture hook B (ingest/capture.go). 'killed' is a terminal status
	// nothing else ever revisits: the ingest status ticker's RecomputeStatuses
	// only scans `WHERE status IN ('active','idle')`, so a row this handler
	// flipped to 'killed' can never re-enter it. That makes THIS the only site
	// where the hook can fire for an operator-killed session — without it, a
	// stuck session that produced no TodoWrite calls loses its context from the
	// board permanently, with no later pass to retry.
	//
	// Three properties this call site depends on, all owned by CaptureSessionCard:
	//   - it cannot fail the kill. It returns no error and swallows every
	//     storage failure into a debug log, so the operator's primary intent
	//     (the signal, already sent above) is never affected by a card;
	//   - it is idempotent on 'sess:<uuid>', so killing the same session twice
	//     mints one card, and publishing only on `inserted` keeps that at
	//     exactly one task_updated frame — a repeat kill stays off the wire;
	//   - it re-evaluates every skip rule itself (dispatched run, System
	//     project, a session that already has todo: cards), so nothing is
	//     duplicated or bypassed here.
	//
	// Runs against h.DB after the UPDATE has committed, never inside a
	// transaction: store.Open caps the pool at one connection, so a query issued
	// on this handle while a tx is open on it would deadlock.
	if taskID, inserted := ingest.CaptureSessionCard(h.DB, id); inserted {
		publishTaskUpdated(taskID)
	}

	w.WriteHeader(http.StatusAccepted)
}
