package retroanalysis

// The headless runner behind a system analysis. Shaped after
// internal/improve/runner.go on purpose: same binary, same flag order, same
// account resolution, same stderr-tail-into-the-row error contract. The one
// thing that differs is what comes back — prose instead of a diff — and that
// difference is the whole reason this is a separate package.

import (
	"context"
	"os/exec"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
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
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	systemspawn.Attach(cmd)
	// One error path for all five runners: stdout is quoted alongside stderr,
	// because the CLI prints some failures there and exits with an empty stderr.
	return systemspawn.Run(ctx, cmd, prompt)
}
