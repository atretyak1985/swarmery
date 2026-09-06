package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
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
// probe the common install locations. The implementation lives in
// internal/claudebin, shared with planning.ClaudeBin and mcpcfg.
func claudeBin() (string, error) { return claudebin.Resolve() }

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
		account     sql.NullString
		branch      sql.NullString
		repoRoot    sql.NullString
	)
	// git_branch and the project path ride along for the worktree recovery below:
	// a finished run's cwd is gone, but its branch still holds the work.
	err = h.DB.QueryRow(
		`SELECT s.session_uuid, s.proc_state, s.cwd, s.account, s.git_branch, p.path
		   FROM sessions s JOIN projects p ON p.id = s.project_id
		  WHERE s.id = ?`, id,
	).Scan(&sessionUUID, &procState, &cwd, &account, &branch, &repoRoot)
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

	// The spawn body lives in resume.go so the planning-wizard endpoints share
	// the exact same single-flight map, timeout, and session_updated edges.
	started, err := startResume(id, sessionUUID, cwd.String, account.String, text, "", nil)
	if errors.Is(err, errResumeCwdGone) {
		// A run's worktree is removed when the run ends — but only the CHECKOUT is
		// disposable: the commits stay on swarm/<taskID>. So the missing directory
		// is not the end of the conversation, it is something to put back. Re-attach
		// the branch at the same path and resume there; the agent then sees exactly
		// the tree it left behind.
		if rErr := h.reattachRunWorktree(repoRoot.String, cwd.String); rErr != nil {
			writeClientErr(w, http.StatusConflict, reattachFailureMessage(cwd.String, branch.String, rErr))
			return
		}
		log.Printf("session_message: re-attached run worktree %s for session id=%d", cwd.String, id)
		started, err = startResume(id, sessionUUID, cwd.String, account.String, text, "", nil)
	}
	if err != nil {
		http.Error(w, `{"error":"claude executable not found (set SWARMERY_CLAUDE_BIN)"}`, http.StatusServiceUnavailable)
		return
	}
	if !started {
		http.Error(w, `{"error":"a message is already being processed for this session"}`, http.StatusConflict)
		return
	}

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

func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// reattachRunWorktree puts a finished run's checkout back at its own path, on the
// branch it already had. Nothing here moves a ref or deletes a directory (see
// worktree.ReattachPath) — the worst case is a refusal, which the caller turns
// into an explanation.
func (h *Handler) reattachRunWorktree(repoRoot, cwd string) error {
	if h.Wt == nil {
		return errors.New("worktree recovery is not wired in this build")
	}
	if strings.TrimSpace(repoRoot) == "" {
		return errors.New("the session's project has no known path")
	}
	_, err := h.Wt.ReattachPath(repoRoot, cwd)
	return err
}

// reattachFailureMessage explains a refusal in the operator's terms. The two
// cases differ in what is actually lost: a vanished BRANCH means the run's work
// is gone and there is nothing to continue, while any other refusal means the
// work is still on the branch and only the checkout could not be restored.
func reattachFailureMessage(cwd, branch string, err error) string {
	if branch == "" {
		branch = "the run's branch"
	}
	if errors.Is(err, worktree.ErrBranchGone) {
		return "this session ran in " + cwd + ", and " + branch +
			" no longer exists either — the run's work is not recoverable, so re-run the phase instead of resuming this session."
	}
	return "this session ran in " + cwd + ", which was removed when the run ended. " +
		branch + " still holds the work, but the checkout could not be restored: " + err.Error()
}
