package handoff

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// claudeTimeout bounds one headless handoff generation run.
const claudeTimeout = 10 * time.Minute

// stderrTailBytes caps how much captured stderr lands in the returned error.
const stderrTailBytes = 4096

// ClaudeRunner runs `claude -p --model <id> --output-format text` with the
// prompt on stdin. Binary resolution is a plain PATH lookup (same as
// internal/improve.ClaudeRunner and internal/trajjudge.ClaudeRunner). Twin —
// keep in lockstep with those two.
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model pins the run; the caller always sets it (SWARMERY_HANDOFF_MODEL).
	Model string
}

func (r ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// launchd hands the daemon a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that
	// omits every usual install dir, so a bare exec of "claude" fails with ENOENT
	// under the service while working in the operator's shell. Resolve explicitly.
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}

	// --setting-sources project,local: skip user-level settings (global plugin
	// stack) — headless runs don't need them; project plugins and OAuth are
	// unaffected. Keep the flag order identical to the improve/trajjudge twins.
	cmd := exec.CommandContext(ctx, bin, "-p", "--model", r.Model, "--output-format", "text", "--setting-sources", "project,local")
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	systemspawn.Attach(cmd)
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
