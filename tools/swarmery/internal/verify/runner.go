package verify

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// RunSpec is one bounded read-only verifier run.
type RunSpec struct {
	Prompt      string // the read-only verifier prompt (BuildPrompt output)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the task's worktree path — the process runs here
	Model       string // optional --model override ("" = account default)
}

// Run is the outcome of a completed verifier process. Unlike the dispatcher,
// verification READS the model's stdout — the verdict lives in the transcript —
// so Output carries the captured stdout for the parser.
type Run struct {
	Output      string        // captured stdout (the verifier's reasoning + VERDICT line)
	ExitCode    int           // process exit status (0 = clean; -1 = never started / timeout)
	TimedOut    bool          // true if the hard timeout fired (ctx deadline)
	Stderr      string        // tail of stderr, for the detail on an error
	Duration    time.Duration // wall-clock spawn→exit
}

// Runner is the headless-claude boundary for verification. ClaudeRunner is
// production; tests substitute a stub that returns a canned Run without spawning
// a process (mirroring dispatch.Runner / improve.Runner / provision.Runner).
// Run BLOCKS until the process exits — the service calls it inside its own
// goroutine, keeping parse + stamp in one place and the whole flow
// stub-testable. A timeout is an OUTCOME (TimedOut=true), not an error — the
// service maps it to INCONCLUSIVE.
type Runner interface {
	Run(ctx context.Context, spec RunSpec) (*Run, error)
}

// claudeTimeout is the hard wall-clock bound for one verification run (phase-6
// spec: 15 minutes). Overridable via SWARMERY_VERIFY_TIMEOUT_MIN at the service
// layer; ClaudeRunner uses this constant when the spec carries no ctx deadline.
const claudeTimeout = 15 * time.Minute

// stderrTailBytes caps captured stderr landing in verify_detail on an error.
const stderrTailBytes = 4096

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. Binary resolution is a plain PATH lookup — the
// same pattern as dispatch.ClaudeRunner / internal/toolproc (the daemon's
// launchd/service PATH must contain the claude binary). The prompt is passed as
// an argument (not stdin) so --session-id positioning is unambiguous, matching
// the dispatcher. NOTE: read-only-ness is enforced by the PROMPT contract, not
// by a sandbox — the security review must confirm the run cannot mutate the
// worktree in a way that would corrupt the graded diff (it runs in the task's
// own throwaway worktree, so at worst it dirties that worktree, never main).
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it; the service
	// sets it from SWARMERY_VERIFY_TIMEOUT_MIN).
	Timeout time.Duration
}

func (r ClaudeRunner) Run(ctx context.Context, spec RunSpec) (*Run, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-p", spec.Prompt, "--session-id", spec.SessionUUID}
	if m := strings.TrimSpace(spec.Model); m != "" {
		args = append(args, "--model", m)
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = spec.Cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	run := &Run{
		Output:   stdout.String(),
		Stderr:   tail(stderr.String(), stderrTailBytes),
		Duration: time.Since(start),
	}
	if ctx.Err() == context.DeadlineExceeded {
		run.TimedOut = true
		run.ExitCode = -1
		return run, nil // a timeout is an outcome (→ INCONCLUSIVE), not a Start error
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			run.ExitCode = ee.ExitCode()
			return run, nil // nonzero exit is an outcome the service routes (still parse stdout)
		}
		// The process could not be started/observed at all (PATH miss, fork
		// failure). That IS an error — the service maps it to INCONCLUSIVE.
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
