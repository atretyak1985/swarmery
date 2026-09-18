package improve

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

// Runner executes one improvement prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeTimeout bounds one headless generation run.
const claudeTimeout = 10 * time.Minute

// defaultModel pins headless runs that carry no explicit override: without
// --model the CLI inherits the account default (Fable-5 here — 2× the Opus
// price). Full ID, not an alias — aliases re-resolve over time.
const defaultModel = "claude-opus-5"

// defaultEffort pins reasoning depth for this headless run: without --effort
// the CLI inherits its xhigh default. Proposal generation needs real
// reasoning but not the ceiling — per the Opus 5 prompting guide, high is the
// cost/quality sweet spot and lower efforts hold quality unusually well.
const defaultEffort = "high"

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
	// Model overrides defaultModel when non-empty.
	Model string
	// Effort overrides defaultEffort when non-empty.
	Effort string
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
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

	// --setting-sources project,local: skip user-level settings (global plugin
	// stack) — headless runs don't need them; project plugins and OAuth are
	// unaffected. Keep the flag order identical to the extract twin (trajjudge
	// matches minus --effort).
	cmd := exec.CommandContext(ctx, bin, "-p", "--model", model, "--effort", effort, "--output-format", "text", "--setting-sources", "project,local")
	// System home, not the inherited launchd cwd "/": transcripts then
	// attribute to the deliberate "System" project (see internal/ingest).
	// Only when it actually exists — a missing dir would fail the spawn with
	// chdir ENOENT, and losing attribution beats not running at all (the
	// daemon owns ~/.swarmery, so in production it is always there).
	//
	// ~/.swarmery is ALSO improve's own project for account resolution — it is
	// itself a registered project (see internal/ingest), so SpawnEnvFor picks up
	// whatever Claude account it is bound to (config dir AND secret store), the
	// same composition every other spawn site in this program uses. Gated behind
	// the SAME isDir check as cmd.Dir, deliberately: when the directory does not
	// exist there is no project to resolve an account FOR, so cmd.Env must stay
	// untouched too — a byte-identical spawn to before this feature existed.
	if home, err := os.UserHomeDir(); err == nil {
		if dir := filepath.Join(home, ".swarmery"); isDir(dir) {
			cmd.Dir = dir
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

// tail returns the last ≤ n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
