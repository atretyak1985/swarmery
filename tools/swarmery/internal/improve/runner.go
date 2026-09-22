package improve

import (
	"context"
	"os/exec"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
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
const defaultModel = "claude-opus-5-5"

// defaultEffort pins reasoning depth for this headless run: without --effort
// the CLI inherits its xhigh default. Proposal generation needs real
// reasoning but not the ceiling — per the Opus 5 prompting guide, high is the
// cost/quality sweet spot and lower efforts hold quality unusually well.
const defaultEffort = "high"

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
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	systemspawn.Attach(cmd)
	// One error path for all five runners: stdout is quoted alongside stderr,
	// because the CLI prints some failures there and exits with an empty stderr.
	return systemspawn.Run(ctx, cmd, prompt)
}
