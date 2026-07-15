package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// Headless resume message: send a prompt to a session's conversation from the
// dashboard. `claude -r <uuid> -p <text>` starts a NEW print-mode process that
// reads the session's transcript from disk, continues the conversation, and
// appends BOTH the user prompt and the assistant reply to the SAME .jsonl file
// (verified: same session_id, same file). The ingest watcher then tails those
// lines and surfaces the new turns on the open detail via the WS bus — no
// synthetic event needed.
//
// This is NOT input into a live TUI. The daemon only reads transcripts; there
// is no supported way to inject text into an already-running interactive
// session (anthropics/claude-code#27441). A genuinely live session
// (status active | waiting_approval) is therefore rejected — two processes
// writing the same JSONL would race — and the feature targets resuming an
// idle/completed/killed conversation.
const (
	sessionMessageTimeout = 15 * time.Minute
	maxSessionMessageLen  = 16000
)

// resumeRun tracks one in-flight dashboard resume: its cancel (aborts the
// child claude) and start time (drives the live "Working… (Ns)" indicator).
type resumeRun struct {
	cancel    context.CancelFunc
	startedAt time.Time
}

// Single-flight per session_uuid: a second resume while one is still running
// would interleave writes into the same transcript file.
var (
	msgInFlightMu sync.Mutex
	msgInFlight   = map[string]resumeRun{}
)

// setResumeState fills the in-memory resume fields on a session DTO (both the
// getSession detail and the WS session_updated payload go through here).
func setResumeState(s *sessionDTO) {
	msgInFlightMu.Lock()
	run, ok := msgInFlight[s.SessionUUID]
	msgInFlightMu.Unlock()
	s.ResumeInFlight = ok
	if ok {
		started := run.startedAt.UTC().Format(time.RFC3339)
		s.ResumeStartedAt = &started
	}
}

// cancelResume aborts an in-flight resume for uuid, returning whether one was
// active. The run's own defer removes the map entry and republishes state.
func cancelResume(uuid string) bool {
	msgInFlightMu.Lock()
	run, ok := msgInFlight[uuid]
	msgInFlightMu.Unlock()
	if ok {
		run.cancel()
	}
	return ok
}

// claudeBin resolves the Claude Code executable.
//
// launchd starts the daemon with a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin)
// that omits the npm/homebrew install dirs, so a bare `exec.LookPath("claude")`
// fails under the service even though `claude` is on the user's interactive
// PATH. Resolution order: explicit SWARMERY_CLAUDE_BIN override → PATH lookup →
// probe the common install locations.
func claudeBin() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SWARMERY_CLAUDE_BIN")); v != "" {
		return v, nil
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		filepath.Join(home, ".claude", "local", "claude"),
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".npm-global", "bin", "claude"),
		filepath.Join(home, "bin", "claude"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", fmt.Errorf("claude not found in PATH or common install locations")
}

// PostSessionMessage implements POST /api/sessions/{id}/message.
// Validation order keeps every reject path (400/404/409) BEFORE the claude
// spawn so the guards are unit-testable without the binary installed.
// Exported so the _test package can reach it directly (pattern: KillSession).
func (h *Handler) PostSessionMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, `{"error":"empty message"}`, http.StatusBadRequest)
		return
	}
	if len(text) > maxSessionMessageLen {
		http.Error(w, `{"error":"message too long"}`, http.StatusRequestEntityTooLarge)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	var (
		sessionUUID string
		procState   sql.NullString
		cwd         sql.NullString
	)
	err = h.DB.QueryRow(
		`SELECT session_uuid, proc_state, cwd FROM sessions WHERE id = ?`, id,
	).Scan(&sessionUUID, &procState, &cwd)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// Gate on a LIVE PROCESS, not the time-based status. A bound, running (or
	// orphaned) process means a real terminal owns the transcript — a parallel
	// headless resume would race, so the caller must Stop it first. Crucially,
	// our own resume run leaves proc_state dead/null, so a session that reads
	// "active" purely because we just appended to it stays writable (no false
	// lockout after each send).
	if procState.String == procwatch.StateRunning || procState.String == procwatch.StateOrphaned {
		http.Error(w, `{"error":"session has a live process — stop it before sending"}`, http.StatusConflict)
		return
	}
	if !cwd.Valid || strings.TrimSpace(cwd.String) == "" {
		http.Error(w, `{"error":"session has no known working directory to resume in"}`, http.StatusConflict)
		return
	}

	bin, err := claudeBin()
	if err != nil {
		http.Error(w, `{"error":"claude executable not found (set SWARMERY_CLAUDE_BIN)"}`, http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionMessageTimeout)
	msgInFlightMu.Lock()
	if _, busy := msgInFlight[sessionUUID]; busy {
		msgInFlightMu.Unlock()
		cancel()
		http.Error(w, `{"error":"a message is already being processed for this session"}`, http.StatusConflict)
		return
	}
	msgInFlight[sessionUUID] = resumeRun{cancel: cancel, startedAt: time.Now()}
	msgInFlightMu.Unlock()

	log.Printf("session_message: resume session id=%d uuid=%s cwd=%q (%d chars)", id, sessionUUID, cwd.String, len(text))
	go runSessionMessage(ctx, cancel, id, bin, sessionUUID, cwd.String, text)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// CancelSessionMessage implements POST /api/sessions/{id}/message/cancel — abort
// the in-flight headless resume run (kill the child claude process). Exported so
// the _test package can reach it directly (pattern: KillSession).
func (h *Handler) CancelSessionMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var sessionUUID string
	err = h.DB.QueryRow(`SELECT session_uuid FROM sessions WHERE id = ?`, id).Scan(&sessionUUID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if !cancelResume(sessionUUID) {
		http.Error(w, `{"error":"no message is being processed for this session"}`, http.StatusConflict)
		return
	}
	log.Printf("session_message: cancelled resume session id=%d uuid=%s", id, sessionUUID)
	w.WriteHeader(http.StatusAccepted)
}

// runSessionMessage spawns the detached resume run. It does not parse stdout —
// the ingest watcher is the source of truth for the resulting turns; here we
// only log completion/failure and publish session_updated at the run's edges so
// the composer flips to Stop (and back) while it is in flight.
func runSessionMessage(ctx context.Context, cancel context.CancelFunc, id int64, bin, sessionUUID, cwd, text string) {
	defer func() {
		cancel()
		msgInFlightMu.Lock()
		delete(msgInFlight, sessionUUID)
		msgInFlightMu.Unlock()
		publishSessionUpdated(id) // resumeInFlight is now false → composer shows Send
	}()
	publishSessionUpdated(id) // resumeInFlight is now true → composer shows Stop

	cmd := exec.CommandContext(ctx, bin, "-r", sessionUUID, "-p", text, "--output-format", "json")
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("session_message: resume uuid=%s ended: %v — output: %s", sessionUUID, err, truncateOutput(string(out), 500))
		return
	}
	log.Printf("session_message: resume uuid=%s completed", sessionUUID)
}

func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
