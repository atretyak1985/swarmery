package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// resumePermEnv is this spawn site's --permission-mode knob (internal/claudeflags
// owns the resolution and the "off" escape hatch). A resumed turn is as headless
// as the initial run: nobody can answer a permission prompt, so without the flag
// the CLI refuses every write — including writes INSIDE the session's allowed
// directories, reported as "may only create or modify files in the allowed
// working directories" — and the process still exits 0. That is silent data loss
// for any resume whose product is files: the planning wizard writes its plan on
// the PROCEED turn, which is a resume, so the plan only ever existed in a reply.
const resumePermEnv = "SWARMERY_RESUME_PERMISSION_MODE"

// resumeArgs is the argv of one resume spawn, split out so the flags are
// assertable without spawning a process (mirrors runcore.Args). model is
// optional: "" lets the CLI pick (the composer's behaviour — the operator is
// continuing a session they ran themselves); the planning wizard always passes
// the wizard's pinned model so a resume never inherits the account default.
func resumeArgs(sessionUUID, text, model string) []string {
	args := []string{"-r", sessionUUID, "-p", text, "--output-format", "json"}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	return append(args, claudeflags.PermissionModeArgs(resumePermEnv)...)
}

// errResumeCwdGone reports that the directory a session recorded as its cwd no
// longer exists. The common case is a phase or plan run: the daemon removes the
// run's worktree the moment the run ends (keeping only the branch), while the
// session row keeps pointing at it. Resuming there would drop the agent into a
// deleted directory — it would read no repo, and nothing it wrote could be
// committed — so the resume is refused instead of silently doing nothing useful.
var errResumeCwdGone = errors.New("session working directory no longer exists")

// startResume spawns `claude -r <uuid> -p <text>` in cwd with single-flight per
// session uuid (msgInFlight), sessionMessageTimeout, and session_updated edges.
// It is the ONE resume spawner: both the composer (PostSessionMessage) and the
// planning-wizard endpoints go through it, so a wizard answer and a composer
// message can never race the same transcript — they contend on the same map.
//
// account is the sessions row's account key — the resume MUST run under the same
// Claude config dir the transcript was written in, or `claude -r` reports "No
// conversation found with session ID" and the caller's action silently fails.
//
// onExit (nil ok) runs after the process exits, BEFORE the slot is released, so
// its state change (planning rolls status back to awaiting_answer on error) is
// already visible when the final session_updated frame goes out. Returns
// (false, nil) when a resume is already in flight for uuid; a non-nil err means
// the claude binary could not be resolved (nothing was spawned).
func startResume(sessionID int64, sessionUUID, cwd, account, text, model string, onExit func(err error)) (started bool, err error) {
	// Cheapest, most specific reject first: a vanished cwd is a state error the
	// caller must explain to the operator, not a missing-binary condition.
	if fi, statErr := os.Stat(cwd); statErr != nil || !fi.IsDir() {
		return false, fmt.Errorf("%w: %s", errResumeCwdGone, cwd)
	}
	bin, binErr := claudeBin()
	if binErr != nil {
		return false, binErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionMessageTimeout)
	msgInFlightMu.Lock()
	if _, busy := msgInFlight[sessionUUID]; busy {
		msgInFlightMu.Unlock()
		cancel()
		return false, nil
	}
	msgInFlight[sessionUUID] = resumeRun{cancel: cancel, startedAt: time.Now()}
	msgInFlightMu.Unlock()

	log.Printf("session_message: resume session id=%d uuid=%s cwd=%q account=%q (%d chars)", sessionID, sessionUUID, cwd, account, len(text))
	go runSessionMessage(ctx, cancel, sessionID, bin, sessionUUID, cwd, account, text, model, onExit)
	return true, nil
}

// resumeInFlight reports whether a headless resume is currently running for a
// session uuid — the planning service's "process alive" seam (its raw-fallback
// parsing and stale-generating reconcile must not fire mid-resume).
func resumeInFlight(sessionUUID string) bool {
	msgInFlightMu.Lock()
	_, ok := msgInFlight[sessionUUID]
	msgInFlightMu.Unlock()
	return ok
}

// runSessionMessage spawns the detached resume run. It does not parse stdout —
// the ingest watcher is the source of truth for the resulting turns; here we
// only log completion/failure and publish session_updated at the run's edges so
// the composer flips to Stop (and back) while it is in flight. onExit (nil ok)
// observes the process outcome before the slot release (see startResume).
func runSessionMessage(ctx context.Context, cancel context.CancelFunc, id int64, bin, sessionUUID, cwd, account, text, model string, onExit func(err error)) {
	var runErr error
	defer func() {
		if onExit != nil {
			onExit(runErr)
		}
		cancel()
		msgInFlightMu.Lock()
		delete(msgInFlight, sessionUUID)
		msgInFlightMu.Unlock()
		publishSessionUpdated(id) // resumeInFlight is now false → composer shows Send
	}()
	publishSessionUpdated(id) // resumeInFlight is now true → composer shows Stop

	cmd := exec.CommandContext(ctx, bin, resumeArgs(sessionUUID, text, model)...)
	cmd.Dir = cwd
	// The transcript `claude -r` must find lives under the config dir of the
	// account that WROTE it, so the resume takes the account from the sessions row
	// rather than from cwd: a dispatched session's cwd is a worktree with no
	// project settings file, which would resolve to the default account and read
	// an empty projects/ dir. EnvForAccount yields nil for the default account, so
	// cmd.Env is then a byte-identical copy of os.Environ().
	cmd.Env = append(os.Environ(), claudeacct.EnvForAccount(account)...)
	// Own process group: a daemon restart (make install / launchd job stop)
	// SIGKILLs the daemon's process group — without this, every in-flight
	// dashboard-driven session dies mid-turn. Detached children survive as
	// procwatch 'orphaned' and finish their work.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		runErr = err
		log.Printf("session_message: resume uuid=%s ended: %v — output: %s", sessionUUID, err, truncateOutput(string(out), 500))
		return
	}
	log.Printf("session_message: resume uuid=%s completed", sessionUUID)
}
