package api

// Auto-verification endpoint (fusion phase 6 — internal/verify): a manual
// re-run of the read-only verifier for a task (the AUTO trigger fires from the
// dispatcher's exit path, wired via AttachVerify). Verification shells out to a
// bounded headless `claude -p` (minutes), so the POST validates synchronously —
// 404/409/422 come back immediately — then runs VerifyTask async and answers
// 202, the same fire-and-observe idiom as POST /api/retro/agents/{agent}/improve
// and the dispatcher. The verdict lands on the task row + a task_updated WS frame
// (the FROZEN bus — no new message type).

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// verifySvc is attached once at daemon startup (nil ⇒ the verify endpoint 503s
// and the dispatcher's auto-trigger is a no-op). Mirrors dispatchSvc.
var verifySvc *verify.Service

// AttachVerify wires the verifier service into the api layer and gives it the
// api-owned task_updated emitter (so the WS envelope stays defined in one place
// — the frozen NoteTaskUpdated type, no new message type). Called from
// cmd/swarmery after the service is constructed and healed. In unit tests it is
// left nil so board writes never trigger a real headless verify.
func AttachVerify(s *verify.Service) {
	if s != nil {
		s.Notify = publishTaskUpdated
	}
	verifySvc = s
}

// VerifierForDispatch returns the attached verifier as the dispatch.Verifier
// seam (nil-safe: a nil service yields a nil interface so the dispatcher's guard
// treats it as "no verifier attached"). cmd/swarmery passes the result to the
// dispatcher so a dispatched run's no-sentinel exit pokes verification.
func VerifierForDispatch() *verify.Service { return verifySvc }

// verifyTask — POST /api/tasks/{id}/verify: manual re-run of auto-verification
// for a task (non-dispatched/legacy tasks, or a forced re-grade). 404 unknown
// task; 422 the task has no worktree to grade; 409 a verification is already
// running (single-flight); 503 the verifier is not attached. requireLocalOrigin.
func (h *Handler) verifyTask(w http.ResponseWriter, r *http.Request) {
	if verifySvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "verifier not attached")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}

	// Pre-flight the gates synchronously so the client gets a real status code:
	// begin a run (which enforces existence-of-worktree + single-flight) is done
	// inside VerifyTask, but we want 404/422/409 BEFORE answering 202. VerifyTask
	// is idempotent-safe to call; to surface the sync errors we do a cheap probe.
	if code, msg, ok := h.verifyPreflight(id); !ok {
		writeClientErr(w, code, msg)
		return
	}

	// Async: run the full flow off-thread and answer 202. The service's own Go
	// seam (nil in prod ⇒ real goroutine) keeps this deterministic under httptest
	// when a test sets it; here we just spawn.
	go func() {
		if err := verifySvc.VerifyTask(context.Background(), id); err != nil &&
			!errors.Is(err, verify.ErrAlreadyRunning) {
			log.Printf("error: verify: manual VerifyTask(%d): %v", id, err)
		}
	}()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "verifying", "taskId": id})
}

// verifyPreflight cheaply validates the sync-error gates (404 unknown, 422 no
// worktree, 409 already running) so the endpoint can answer before the async
// run. It does not mutate state.
func (h *Handler) verifyPreflight(id int64) (code int, msg string, ok bool) {
	var wtpath *string
	err := h.DB.QueryRow(`SELECT worktree_path FROM tasks WHERE id=?`, id).Scan(&wtpath)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, "task not found", false
	}
	if err != nil {
		return http.StatusInternalServerError, err.Error(), false
	}
	if wtpath == nil || *wtpath == "" {
		return http.StatusUnprocessableEntity, "task has no worktree to grade (never dispatched, or already reclaimed)", false
	}
	var running int
	if err := h.DB.QueryRow(
		`SELECT 1 FROM verification_runs WHERE task_id=? AND status='running' LIMIT 1`, id).Scan(&running); err == nil {
		return http.StatusConflict, "a verification is already running for this task", false
	}
	return 0, "", true
}
