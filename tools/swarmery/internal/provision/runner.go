// Package provision owns the "enable pack → install + generate" pipeline: a
// mocked Runner (the only seam touching the real claude binary), a pack→action
// policy map, and a Service that enqueues single-flight jobs with durable status.
package provision

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes the claude binary. It is the ONLY seam that touches a real
// process — every test injects a stub. Mirrors internal/improve.Runner.
type Runner interface {
	// Claude runs `claude <args...>` with cwd=dir (dir=="" inherits the daemon
	// cwd), feeding stdin (""=none), and returns trimmed stdout. A non-nil error
	// carries a stderr tail. The daemon's launchd PATH must contain `claude`
	// (already ensured for the serena/graphify tool dashboards).
	Claude(ctx context.Context, dir, stdin string, args ...string) (string, error)
}

// stderrTailBytes caps how much captured stderr lands in the error (and thus in
// provision_jobs.error).
const stderrTailBytes = 4096

// ClaudeRunner is the production Runner: a plain PATH lookup of the claude
// binary, the same pattern internal/improve and internal/toolproc use.
type ClaudeRunner struct{}

func (ClaudeRunner) Claude(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	joined := "claude " + strings.Join(args, " ")
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timed out; stderr: %s", joined, tail(errb.String(), stderrTailBytes))
		}
		return "", fmt.Errorf("%s: %w; stderr: %s", joined, err, tail(errb.String(), stderrTailBytes))
	}
	return strings.TrimSpace(out.String()), nil
}

// tail returns the last ≤ n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
