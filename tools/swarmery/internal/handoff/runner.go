package handoff

import (
	"context"
	"os/exec"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// claudeTimeout bounds one headless handoff generation run.
const claudeTimeout = 10 * time.Minute

// DefaultModel pins handoff runs that carry no override. It used to live in
// cmd/swarmery/main.go beside the SWARMERY_HANDOFF_MODEL read, where the
// defaults table test could not see it — and a default no test can name is a
// default that drifts. Full ID, not an alias: aliases re-resolve over time.
const DefaultModel = "claude-sonnet-5"

// DefaultEffort pins how hard a handoff run thinks. Writing a handoff note is
// summarisation over material the prompt already carries — the cheapest shape
// of work this daemon asks for — so it is pinned low. The point is that the
// unpinned alternative is not "cheap": it is the CLI's xhigh, i.e. this site was
// paying maximum reasoning depth to write a paragraph. Phase 7 re-measures it.
const DefaultEffort = "low"

// effortEnv is this spawn site's --effort knob; internal/claudeflags owns the
// resolution, the validation and the "off" escape hatch.
const effortEnv = "SWARMERY_HANDOFF_EFFORT"

// ClaudeRunner runs `claude -p --model <id> --effort <e> --output-format text`
// with the prompt on stdin. Binary resolution is a plain PATH lookup (same as
// internal/improve.ClaudeRunner and internal/trajjudge.ClaudeRunner). Twin —
// keep in lockstep with those two.
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides DefaultModel when non-empty (SWARMERY_HANDOFF_MODEL).
	Model string
	// Effort overrides the resolved default when non-empty.
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
	// unaffected. Keep the flag order identical to the improve/trajjudge twins.
	//
	// --effort is APPENDED rather than interpolated into a fixed slot: its
	// resolved value is legitimately empty when an operator sets the knob to
	// claudeflags.OmitEffort, and a fixed slot turned that into `--effort ""`,
	// which the CLI rejects — the escape hatch killed the engine instead of
	// restoring its pre-pin behaviour. EffortArgsWith returns nil there and the
	// flag simply disappears. Order is otherwise unchanged.
	args := []string{"-p", "--model", model}
	args = append(args, claudeflags.EffortArgsWith(r.Effort, effortEnv, DefaultEffort)...)
	args = append(args, "--output-format", "text", "--setting-sources", "project,local")
	cmd := exec.CommandContext(ctx, bin, args...)
	// Cwd and account in one decision, both taken from the System project home:
	// see internal/systemspawn for why they are inseparable and why a missing
	// home means neither.
	systemspawn.Attach(cmd)
	// One error path for all five runners: stdout is quoted alongside stderr,
	// because the CLI prints some failures there and exits with an empty stderr.
	return systemspawn.Run(ctx, cmd, prompt)
}
