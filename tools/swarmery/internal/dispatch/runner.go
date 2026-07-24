package dispatch

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// RunSpec is one dispatched headless run.
type RunSpec struct {
	Prompt      string // full prompt (task body + execution contract)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the task's worktree path — the process runs here
	Model       string // optional --model override ("" = account default)
}

// Run is the outcome of a completed dispatched process.
type Run struct {
	SessionUUID string        // echoed back for the task↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started)
	TimedOut    bool          // true if RunTimeout fired (ctx deadline)
	Stderr      string        // tail of stderr, surfaced in dispatch_error on failure
	Duration    time.Duration // wall-clock spawn→exit
}

// Runner is the headless-claude boundary. ClaudeRunner is production; tests
// substitute a stub that returns a canned Run without spawning a process
// (mirroring improve.Runner / provision.Runner). Start BLOCKS until the process
// exits — the dispatcher calls it inside its own goroutine (the async seam is
// the goroutine, not the Runner), which keeps exit + sentinel handling in one
// place and makes the whole flow stub-testable.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) (*Run, error)
}

// stderrTailBytes caps captured stderr landing in dispatch_error.
const stderrTailBytes = 4096

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. Binary resolution is a plain PATH lookup — the
// same pattern as improve.ClaudeRunner and internal/toolproc (the daemon's
// launchd/service PATH must contain the claude binary). The prompt is passed as
// an argument (not stdin) so --session-id positioning is unambiguous.
type ClaudeRunner struct{}

func (ClaudeRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	args := []string{"-p", spec.Prompt, "--session-id", spec.SessionUUID}
	if m := strings.TrimSpace(spec.Model); m != "" {
		args = append(args, "--model", m)
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = spec.Cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// stdout is the assistant text; we do NOT parse it here — the run's transcript
	// is ingested independently and the dispatcher reads the linked session's
	// last assistant turn from the DB for sentinels. Discard it.
	err := cmd.Run()

	run := &Run{
		SessionUUID: spec.SessionUUID,
		Stderr:      tail(stderr.String(), stderrTailBytes),
		Duration:    time.Since(start),
	}
	if ctx.Err() == context.DeadlineExceeded {
		run.TimedOut = true
		run.ExitCode = -1
		return run, nil // a timeout is an outcome, not a Start error
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			run.ExitCode = ee.ExitCode()
			return run, nil // nonzero exit is an outcome the dispatcher routes
		}
		// The process could not be started/observed at all (PATH miss, fork
		// failure). That IS a Start error — the dispatcher marks the row and
		// releases the slot.
		run.ExitCode = -1
		return run, err
	}
	run.ExitCode = 0
	return run, nil
}

// tail returns the last <= n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
