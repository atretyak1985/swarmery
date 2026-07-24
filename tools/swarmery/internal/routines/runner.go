package routines

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Runner is the ai-prompt boundary: it spawns one headless `claude -p` run in
// the given cwd and returns raw stdout. Mocked in every test — no real claude
// invocation outside production (mirrors improve.Runner / dispatch.Runner).
type Runner interface {
	Run(ctx context.Context, cwd, prompt, model string) (string, error)
}

// TaskCreator is the create-task boundary: it inserts a board task (source=
// 'queue') and returns its external card id. Injected from the api/cmd layer so
// board semantics — external-id minting, column validation, task_updated WS
// publish, dispatcher poke — stay in one place (the api package) and this
// package never imports it (no cycle). Nil is tolerated (create-task steps then
// fail with a clear error rather than panicking).
type TaskCreator interface {
	// CreateTask inserts a board task for projectID (0/"" → global is NOT
	// allowed; a task needs a project) in the given column and returns the new
	// card's external id (e.g. "T-ab12cd").
	CreateTask(projectID int64, title, prompt, column string) (externalID string, err error)
}

// aiPromptTimeout bounds one ai-prompt run when the step sets no override.
const aiPromptTimeout = 10 * time.Minute

// stderrTailBytes caps captured stderr landing in a step's error field.
const stderrTailBytes = 4096

// ClaudeRunner runs `claude -p --output-format text` with the prompt on stdin,
// cwd set to the routine's project path (global → daemon cwd), optionally
// pinning --model. Binary resolution is a plain PATH lookup — identical to
// improve.ClaudeRunner / dispatch.ClaudeRunner (the daemon's launchd/service
// PATH must contain the claude binary).
type ClaudeRunner struct {
	// Timeout overrides aiPromptTimeout when > 0 (tests shrink it; the executor
	// passes the step's per-step timeout via the ctx deadline instead).
	Timeout time.Duration
}

func (r ClaudeRunner) Run(ctx context.Context, cwd, prompt, model string) (string, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	args := []string{"-p", "--output-format", "text"}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", wrapRunErr(ctx, err, stderr.String())
	}
	return stdout.String(), nil
}

func wrapRunErr(ctx context.Context, err error, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return &timeoutError{stderr: tail(stderr, stderrTailBytes)}
	}
	return &runError{err: err, stderr: tail(stderr, stderrTailBytes)}
}

// timeoutError signals a deadline-exceeded run so the executor can record status
// 'timeout' (distinct from a plain failure).
type timeoutError struct{ stderr string }

func (e *timeoutError) Error() string {
	if e.stderr != "" {
		return "timed out; stderr: " + e.stderr
	}
	return "timed out"
}

type runError struct {
	err    error
	stderr string
}

func (e *runError) Error() string {
	if e.stderr != "" {
		return e.err.Error() + "; stderr: " + e.stderr
	}
	return e.err.Error()
}

func isTimeout(err error) bool {
	_, ok := err.(*timeoutError)
	return ok
}

// tail returns the last <= n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
