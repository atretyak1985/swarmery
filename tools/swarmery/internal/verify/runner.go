package verify

import (
	"context"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// RunSpec is one bounded read-only verifier run.
type RunSpec struct {
	Prompt      string // the read-only verifier prompt (BuildPrompt output)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the task's worktree path — the process runs here
	Model       string // optional --model override ("" = account default at the spawn layer; the service fills DefaultModel before building the spec)

	// Account is the Claude Code account key this run must execute under,
	// resolved by the CALLER from the task's PROJECT — never from Cwd. Cwd is a
	// worktree, which carries no project settings file, so resolving it here
	// would silently fall back to the default account (plan A3).
	// "" means the default account and produces no env delta.
	Account string
}

// Run is the outcome of a completed verifier process. Unlike the dispatcher,
// verification READS the model's stdout — the verdict lives in the transcript —
// so Output carries the captured stdout for the parser.
type Run struct {
	Output   string        // captured stdout (the verifier's reasoning + VERDICT line)
	ExitCode int           // process exit status (0 = clean; -1 = never started / timeout)
	TimedOut bool          // true if the hard timeout fired (ctx deadline)
	Stderr   string        // tail of stderr, for the detail on an error
	Duration time.Duration // wall-clock spawn→exit
}

// Runner is the headless-claude boundary for verification. ClaudeRunner is
// production; tests substitute a stub that returns a canned Run without spawning
// a process (mirroring dispatch.Runner / improve.Runner / provision.Runner).
// Run BLOCKS until the process exits — the service calls it inside its own
// goroutine, keeping parse + stamp in one place and the whole flow
// stub-testable. A timeout is an OUTCOME (TimedOut=true), not an error — the
// service maps it to INCONCLUSIVE.
type Runner interface {
	Run(ctx context.Context, spec RunSpec) (*Run, error)
}

// claudeTimeout is the hard wall-clock bound for one verification run (phase-6
// spec: 15 minutes). Overridable via SWARMERY_VERIFY_TIMEOUT_MIN at the service
// layer; ClaudeRunner uses this constant when the spec carries no ctx deadline.
const claudeTimeout = 15 * time.Minute

// DefaultModel pins verifier runs whose task carries no model override: an
// unset --model inherits the account default (Fable-5 here — 2× the Opus
// price). Full ID, not an alias — aliases re-resolve over time.
const DefaultModel = "claude-opus-5-5"

// effortEnv is this spawn site's --effort knob; DefaultEffort is what it falls
// back to. internal/claudeflags owns the resolution and the "off" escape hatch.
//
// medium, not the CLI's unpinned xhigh: the verifier's job is to run the checks
// the task declared and read what they printed, then emit one verdict token.
// That is a bounded, evidence-driven judgement, not open-ended reasoning — and
// it runs after EVERY graded task, so it is one of the highest-frequency spawns
// here. Phase 7 re-measures it.
const (
	effortEnv = "SWARMERY_VERIFY_EFFORT"
	// DefaultEffort is exported so the defaults table test can pin it beside
	// every other engine's.
	DefaultEffort = "medium"
)

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. Binary resolution is a plain PATH lookup — the
// same pattern as dispatch.ClaudeRunner / internal/toolproc (the daemon's
// launchd/service PATH must contain the claude binary). The prompt is passed as
// an argument (not stdin) so --session-id positioning is unambiguous, matching
// the dispatcher. NOTE: read-only-ness is enforced by the PROMPT contract, not
// by a sandbox — the security review must confirm the run cannot mutate the
// worktree in a way that would corrupt the graded diff (it runs in the task's
// own throwaway worktree, so at worst it dirties that worktree, never main).
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it; the service
	// sets it from SWARMERY_VERIFY_TIMEOUT_MIN).
	Timeout time.Duration

	// AccountVerdict, when set, is called after a run finishes with the account
	// the run used ("" = the default account, spec.Account's own convention) and
	// how its exit reads as a readiness verdict — a verifier already runs
	// `claude` under the account's config dir, so its death demanding a login is
	// a free authoritative probe. Optional: a nil hook leaves run behaviour
	// byte-identical to before this existed. Not called on a timeout or a
	// failed start: neither is an exit, so neither says anything about the
	// account. The classified output is the stdout this runner captures anyway
	// plus the stderr tail — matched, never logged through this path.
	AccountVerdict func(account string, r claudeprobe.Result)
}

// Run maps this engine's RunSpec onto runcore.Spec and its Result back onto Run.
// Everything shared — the argv, the account env merge, the process group, the
// drain, the exit ladder — lives in internal/runcore; what stays here is
// verification's own policy: its 15-minute wall clock, --setting-sources, the
// stdout capture the verdict is parsed out of, and the account-readiness verdict
// a finished run reports for free.
//
// The method is named Run, not Start, and stays that way: it is the Runner
// interface the service depends on and the name every test stub implements.
func (r ClaudeRunner) Run(ctx context.Context, spec RunSpec) (*Run, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}

	res, err := runcore.ClaudeRunner{Engine: "verify"}.Start(ctx, runcore.Spec{
		Prompt:      spec.Prompt,
		SessionUUID: spec.SessionUUID,
		Cwd:         spec.Cwd,
		Model:       spec.Model,
		// Resolved, never omitted: an absent --effort is the CLI's xhigh, paid on
		// every graded task in the fleet.
		Effort: claudeflags.Effort(effortEnv, DefaultEffort),
		// --setting-sources project,local: skip user-level settings (global plugin
		// stack) — headless runs don't need them; project plugins and OAuth are
		// unaffected.
		SettingSources: "project,local",
		// The account comes from the SPEC, not from Cwd: Cwd is the task's worktree,
		// which has no .claude/settings.local.json of its own, so resolving it there
		// would silently verify under the default account (plan A3). The service
		// resolves the binding from the project path. "" produces no env delta.
		Account: spec.Account,
		Timeout: timeout,
		// Unlike the dispatcher, verification READS stdout — the verdict lives in
		// the transcript, so the parser needs all of it.
		CaptureStdout: true,
	})

	run := &Run{
		Output:   res.Output,
		Stderr:   res.Stderr,
		Duration: res.Duration,
		ExitCode: res.ExitCode,
		TimedOut: res.TimedOut,
	}
	if res.TimedOut {
		return run, nil // an outcome (→ INCONCLUSIVE), not an error — and not an exit, so no verdict
	}
	if err != nil {
		// The process could not be started/observed at all (PATH miss, fork
		// failure). That IS an error — the service maps it to INCONCLUSIVE.
		return run, err
	}
	// Both a clean and a nonzero exit are outcomes the service routes (it still
	// parses stdout on a nonzero exit), and both say something about the account.
	r.reportVerdict(spec, run.ExitCode, run.Output, run.Stderr)
	return run, nil
}

// reportVerdict feeds one finished run's exit through the probe's shared
// classifier and into the AccountVerdict hook. The combined output exists in
// this call only for matching — it is never stored or logged through this path.
func (r ClaudeRunner) reportVerdict(spec RunSpec, exitCode int, stdout, stderrTail string) {
	if r.AccountVerdict == nil {
		return
	}
	r.AccountVerdict(spec.Account, claudeprobe.ClassifyExit(exitCode, stdout+"\n"+stderrTail))
}
