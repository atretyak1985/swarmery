package extract

import (
	"context"
	"os/exec"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// Runner executes one extraction prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeTimeout bounds one extraction run. Shorter than internal/improve's 10
// minutes because this one is INTERACTIVE: an operator is holding a button and
// the HTTP response carries the count (see Service.ExtractTasks). A pass that
// has not classified a ≤16KB digest in five minutes is stuck, not slow.
const claudeTimeout = 5 * time.Minute

// DefaultModel pins headless runs that carry no explicit override: without
// --model the CLI inherits the account default (Fable-5 here — 2× the Opus
// price). Full ID, not an alias — aliases re-resolve over time. Same pin as
// internal/improve, internal/provision, internal/planning and internal/verify.
const DefaultModel = "claude-opus-5-5"

// DefaultEffort pins reasoning depth for this headless run: without --effort
// the CLI inherits its xhigh default. Extraction is a mechanical
// classification pass over a ≤16KB digest — per the Opus 5 prompting guide,
// medium effort holds quality on such tasks at a fraction of the tokens.
const DefaultEffort = "medium"

// effortEnv is this spawn site's --effort knob. Without it the Effort field
// below is unreachable in production — extract.go builds a bare ClaudeRunner{}
// — so the only way to retune this site's depth was to edit and rebuild.
// internal/claudeflags owns the resolution and the "off" escape hatch.
const effortEnv = "SWARMERY_EXTRACT_EFFORT"

// ClaudeRunner runs `claude -p --output-format text` with the prompt on stdin.
// Binary resolution is a plain PATH lookup — the same pattern as
// internal/improve's twin (the daemon's launchd/service PATH must contain the
// claude binary).
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides DefaultModel when non-empty.
	Model string
	// Effort overrides DefaultEffort when non-empty.
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
		model = DefaultModel
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
	// unaffected. Keep the flag order identical to the improve twin (trajjudge
	// matches minus --effort).
	//
	// --effort is APPENDED rather than interpolated into a fixed slot, and the
	// Effort field goes through claudeflags too: the resolved value is
	// legitimately empty for claudeflags.OmitEffort, and a fixed slot turned
	// that into `--effort ""` — a flag value the CLI rejects, so the escape
	// hatch killed the run. EffortArgsWith walks field → site knob → cross-site
	// knob → DefaultEffort and returns nil when the answer is "omit".
	args := []string{"-p", "--model", model}
	args = append(args, claudeflags.EffortArgsWith(r.Effort, effortEnv, DefaultEffort)...)
	args = append(args, "--output-format", "text", "--setting-sources", "project,local")
	cmd := exec.CommandContext(ctx, bin, args...)
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	//
	// Attributing to "System" is also what keeps THIS run from capturing itself,
	// since CaptureSkipReason refuses System-project sessions.
	systemspawn.Attach(cmd)
	// One error path for all five runners: stdout is quoted alongside stderr,
	// because the CLI prints some failures there and exits with an empty stderr.
	return systemspawn.Run(ctx, cmd, prompt)
}
