package improve

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes one improvement prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeTimeout bounds one headless generation run.
const claudeTimeout = 10 * time.Minute

// stderrTailBytes caps how much captured stderr lands in the error (and thus
// in agent_change_proposals.error).
const stderrTailBytes = 4096

// ClaudeRunner runs `claude -p --output-format text` with the prompt on
// stdin. Binary resolution is a plain PATH lookup — the same pattern as
// internal/toolproc launching `serena` (the daemon's launchd/service PATH
// must contain the claude binary).
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
}

func (r ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "text")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude -p timed out after %s; stderr: %s", timeout, tail(stderr.String(), stderrTailBytes))
		}
		return "", fmt.Errorf("claude -p: %w; stderr: %s", err, tail(stderr.String(), stderrTailBytes))
	}
	return stdout.String(), nil
}

// tail returns the last ≤ n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
