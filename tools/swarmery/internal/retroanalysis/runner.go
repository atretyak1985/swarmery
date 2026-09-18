package retroanalysis

// The headless runner behind a system analysis. Shaped after
// internal/improve/runner.go on purpose: same binary, same flag order, same
// account resolution, same stderr-tail-into-the-row error contract. The one
// thing that differs is what comes back — prose instead of a diff — and that
// difference is the whole reason this is a separate package.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// Runner executes one analysis prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeTimeout bounds one headless analysis run.
const claudeTimeout = 10 * time.Minute

// defaultModel pins headless runs that carry no explicit override: without
// --model the CLI inherits the account default. Full ID, not an alias —
// aliases re-resolve over time.
const defaultModel = "claude-opus-5"

// defaultEffort pins reasoning depth. Cross-cutting diagnosis over noisy
// aggregates is the hard part of this feature, and 'high' is the cost/quality
// sweet spot the Opus 5 prompting guide names.
const defaultEffort = "high"

// stderrTailBytes caps how much captured stderr lands in retro_analyses.error.
const stderrTailBytes = 4096

// ClaudeRunner runs `claude -p --output-format text` with the prompt on stdin.
//
// It passes NO --permission-mode, deliberately: the run's entire contract is
// stdout, and internal/retroanalysis writes the row. The spawn-site scanner
// (internal/claudeflags) records that decision explicitly rather than letting
// it look like an omission.
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides defaultModel when non-empty.
	Model string
	// Effort overrides defaultEffort when non-empty.
	Effort string
}

func (r ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model := r.Model
	if model == "" {
		model = defaultModel
	}
	effort := r.Effort
	if effort == "" {
		effort = defaultEffort
	}
	// launchd hands the daemon a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that
	// omits every usual install dir, so a bare exec of "claude" fails with ENOENT
	// under the service while working in the operator's shell. Resolve explicitly.
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}

	// Flag order kept identical to the internal/improve twin so the two spawn
	// sites stay diffable at a glance.
	cmd := exec.CommandContext(ctx, bin, "-p", "--model", model, "--effort", effort,
		"--output-format", "text", "--setting-sources", "project,local")
	// System home rather than the inherited launchd cwd "/": transcripts then
	// attribute to the deliberate "System" project, and ~/.swarmery is itself a
	// registered project, so the account binding resolves from it. Both are
	// gated on the directory actually existing — a missing dir would fail the
	// spawn with chdir ENOENT, and losing attribution beats not running.
	if home, err := os.UserHomeDir(); err == nil {
		if dir := filepath.Join(home, ".swarmery"); isDir(dir) {
			cmd.Dir = dir
			// Config dir AND secret store, the same composition every other
			// swarmery spawn uses; "" is guarded inside SpawnEnvFor.
			cmd.Env = claudeacct.SpawnEnvFor(os.Environ(), dir)
		}
	}
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

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// tail returns the last ≤ n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
