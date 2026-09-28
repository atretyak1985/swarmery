package dispatch

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// RunSpec is one dispatched headless run.
type RunSpec struct {
	Prompt      string // full prompt (task body + execution contract)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the task's worktree path — the process runs here
	Model       string // optional --model override ("" = account default)
	Agent       string // optional registry agent name ("" = plain run, no mention)

	// Effort is this run's --effort, set by the service when a rung above this
	// spawn site's env knob chose one (today: the complexity router in active
	// mode). "" — every run before that rung existed, and every run in off or
	// shadow — resolves exactly as before: SWARMERY_DISPATCH_EFFORT, then
	// DefaultEffort. See runEffort.
	Effort string

	// PermissionMode is the task's playbook-declared --permission-mode, a
	// per-run override of this spawn site's env knob. "" (the common case)
	// inherits the knob, so a card whose recipe says nothing behaves exactly as
	// it did before the field existed. Validated against a closed set at
	// playbook parse time — an unknown mode makes `claude` refuse to start.
	PermissionMode string

	// Resolution is what this run executes under — the Claude Code account and
	// the project's estate — resolved by the CALLER from the task's PROJECT path,
	// never from Cwd: Cwd is a worktree, and the service resolves the project once
	// per playbook. The zero value resolves nothing and adds no env delta.
	Resolution claudeacct.Resolution

	// SettingsFile names a project settings file to lend the run on the command
	// line, or "" when it needs none — set via repopath.InheritedSettings when
	// Cwd is a worktree cut from a SUB-repo of a multi-repo project (the
	// project's own .claude/settings.json, which enables the plugin stack, is
	// otherwise unreachable from that worktree; phaserun and planrun guard the
	// same case). "" is the common single-repo case and changes nothing.
	SettingsFile string
}

// Run is the outcome of a completed dispatched process.
type Run struct {
	SessionUUID string        // echoed back for the task↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started)
	TimedOut    bool          // true if RunTimeout fired (ctx deadline)
	Stderr      string        // tail of stderr, surfaced in dispatch_error on failure
	Duration    time.Duration // wall-clock spawn→exit
}

// Runner is the headless-claude boundary. ClaudeRunner is production; tests
// substitute a stub that returns a canned Run without spawning a process
// (mirroring improve.Runner / provision.Runner). Start BLOCKS until the process
// exits — the dispatcher calls it inside its own goroutine (the async seam is
// the goroutine, not the Runner), which keeps exit + sentinel handling in one
// place and makes the whole flow stub-testable.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) (*Run, error)
}

// permEnv is this spawn site's --permission-mode knob (internal/claudeflags owns
// the default and the precedence). A dispatched executor with no permission mode
// has every Write/Edit and every un-allowlisted Bash command auto-denied — there
// is no approver in a headless run — and still exits 0, so the task is stamped
// done over an untouched worktree.
const permEnv = "SWARMERY_DISPATCH_PERMISSION_MODE"

// DefaultModel pins dispatched implementation runs whose task carries no model
// override: an unset --model inherits the account default (Fable-5 here — 5×
// the Sonnet price). Executor tier — full ID, not an alias, because aliases
// re-resolve over time.
const DefaultModel = "claude-sonnet-5"

// effortEnv is this spawn site's --effort knob; DefaultEffort is what it falls
// back to. internal/claudeflags owns the resolution and the "off" escape hatch.
//
// medium, not the CLI's unpinned xhigh: a dispatched card is a scoped unit of
// work whose contract already tells the executor what to do and how to verify
// it, and the playbook's stage bodies narrow it further. The depth that pays
// off on an open-ended plan is waste here, once per stage of every chain.
// Phase 7 re-measures it.
const (
	effortEnv = "SWARMERY_DISPATCH_EFFORT"
	// DefaultEffort is exported so the defaults table test can pin it beside
	// every other engine's.
	DefaultEffort = "medium"
)

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. Binary resolution is a plain PATH lookup — the
// same pattern as improve.ClaudeRunner and internal/toolproc (the daemon's
// launchd/service PATH must contain the claude binary). The prompt is passed as
// an argument (not stdin) so --session-id positioning is unambiguous.
type ClaudeRunner struct {
	// AccountVerdict, when set, is called after a run finishes with the account
	// the run used ("" = the default account, Resolution.Account's own convention) and
	// how its exit reads as a readiness verdict — a dispatched run already runs
	// `claude` under the account's config dir, so its death demanding a login is
	// a free authoritative probe. Optional: a nil hook leaves run behaviour
	// byte-identical to before this existed (in particular, the child's stdout
	// stays discarded — the bounded tail below is captured only to classify).
	// Not called on a timeout or a failed start: neither is an exit, so neither
	// says anything about the account.
	AccountVerdict func(account string, r claudeprobe.Result)
}

// agentPrompt is the ONE place a dispatched run's agent mention is applied. A
// board task carrying tasks.agent runs AS that registry agent, and the mention
// is the convention the rest of the product already speaks: Agent Hub's "Run
// now" composes the same "@<name>: " prefix for the board's ?compose= deep-link
// (web/src/pages/agent-hub/RunNow.tsx), so a dispatched run and a hand-composed
// one reach the same agent by the same route.
//
// The prefix lives HERE and not in the service on purpose: the service stays a
// pure field-mapper (it copies tasks.agent into the spec and nothing else), so
// there is exactly one code path that can prefix and double-prefixing is not
// expressible. TrimSpace matches the Model handling below — a whitespace-only
// value is no value, never a "@ : " mention.
//
// No stripping of an existing leading mention, deliberately: a prompt seeded by
// Agent Hub's ?compose= deep-link already carries "@<name>: " in its text, and a
// card that ALSO sets tasks.agent to that same name would spawn
// "@tech-lead: @tech-lead: ...". That is redundant, not wrong — both mentions
// name the same agent, so routing is unaffected — and today no UI sets both on
// one task. A New-Task modal that prefills the agent picker FROM a compose link
// would make it reachable; if that lands, dedupe at the point the modal fills
// the picker (drop the mention from the prompt text once it owns the field),
// not here, so this stays the one unconditional prefix site.
//
// No registry validation here, also on purpose: names are checked against the
// registry when the card is written (POST/PATCH), and the registry is re-scanned
// from disk, so an agent deleted between write and dispatch would fail a
// dispatcher-side check for no gain. An unknown mention simply does not route in
// Claude Code and the run proceeds on the plain prompt — a degraded run beats a
// refused one.
func agentPrompt(spec RunSpec) string {
	agent := strings.TrimSpace(spec.Agent)
	if agent == "" {
		return spec.Prompt
	}
	return "@" + agent + ": " + spec.Prompt
}

// permissionMode resolves ONE run's --permission-mode: the task's playbook wins
// over this spawn site's env knob, which internal/claudeflags resolves as
// before. The precedence is the same shape as the model fallback in
// runPlaybook — the specific choice beats the general default.
//
// The three cases are deliberately distinct:
//
//	""        the recipe says nothing → inherit the knob (pre-phase-5 behaviour)
//	"default" the recipe says "no flag" → claude's own default, flag omitted
//	<mode>    the recipe pins a mode → it reaches argv verbatim
//
// A pinned mode is passed through without re-validation because the closed set
// lives at the parse boundary (internal/playbooks): a value that never parsed
// cannot be on a Playbook, and re-checking here would duplicate the set in a
// second place where the two could drift apart.
//
// The return is the MODE, not a flag pair: internal/runcore builds the argv, and
// "" there means "omit the flag" — the same convention claudeflags.Mode uses.
func permissionMode(playbookMode string) string {
	switch mode := strings.TrimSpace(playbookMode); mode {
	case "":
		return claudeflags.Mode(permEnv)
	case "default":
		return ""
	default:
		return mode
	}
}

// runEffort resolves ONE run's --effort: the spec's own Effort when the service
// set one, otherwise this spawn site's knob and default, exactly as every run
// resolved before RunSpec.Effort existed.
//
// The spec value is normalised, not trusted: the router's efforts were already
// validated at policy load, but this is the one place a value reaches argv, and
// an unknown one would make `claude` refuse the flag and die before the run
// starts. An unusable value is demoted to the knob with a warning — the same
// degradation claudeflags applies to a bad env value — never to "no flag",
// which would be the CLI's xhigh.
func runEffort(spec RunSpec) string {
	if raw := strings.TrimSpace(spec.Effort); raw != "" {
		if canonical, ok := claudeflags.NormalizeEffort(raw); ok && canonical != "" {
			return canonical
		}
		log.Printf("warning: dispatch: ignoring unusable run effort %q; falling back to %s", raw, effortEnv)
	}
	return claudeflags.Effort(effortEnv, DefaultEffort)
}

// Start maps this engine's RunSpec onto runcore.Spec and its Result back onto
// Run. Everything shared — the argv, the account env merge, the process group,
// the drain, the exit ladder — lives in internal/runcore; what stays here is
// dispatch's own policy: the agent mention, the playbook's permission mode, the
// account-readiness verdict, and letting the CALLER own the deadline (Timeout is
// deliberately unset — a stage's ctx carries the dispatcher's RunTimeout).
func (r ClaudeRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	// A bounded stdout tail is kept ONLY when a verdict hook wants the exit
	// classified (the CLI's no-login line prints on stdout —
	// docs/claude-cli-credential-behaviour.md §1). With no hook, stdout stays
	// discarded at the OS level: the run's transcript is ingested independently and
	// the dispatcher reads sentinels from the linked session in the DB, so a pipe
	// here would buy nothing and cost a copy of every run's output.
	tailBytes := 0
	if r.AccountVerdict != nil {
		tailBytes = runcore.StderrTailBytes
	}

	res, err := runcore.ClaudeRunner{Engine: "dispatch"}.Start(ctx, runcore.Spec{
		Prompt:      agentPrompt(spec),
		SessionUUID: spec.SessionUUID,
		Cwd:         spec.Cwd,
		Model:       spec.Model,
		// Resolved, never omitted: an absent --effort is not "the cheap default"
		// but the CLI's xhigh, paid once per stage of every playbook chain.
		Effort: runEffort(spec),
		// Without this the executor cannot write, run or commit — and it still exits
		// 0, so the task is stamped done over an empty diff. See internal/claudeflags
		// (knob: SWARMERY_DISPATCH_PERMISSION_MODE).
		PermissionMode: permissionMode(spec.PermissionMode),
		// --setting-sources project,local: skip user-level settings (global plugin
		// stack) — headless runs don't need them; project plugins and OAuth are
		// unaffected.
		SettingSources: "project,local",
		// The resolution comes from the SPEC, not from Cwd: Cwd is the task's
		// worktree. The service resolves the project path once per playbook.
		Resolution:      spec.Resolution,
		SettingsFile:    spec.SettingsFile,
		StdoutTailBytes: tailBytes,
		// No Timeout: the dispatcher's stage ctx owns cancellation (runStage).
	})

	run := &Run{
		SessionUUID: res.SessionUUID,
		Stderr:      res.Stderr,
		Duration:    res.Duration,
		ExitCode:    res.ExitCode,
		TimedOut:    res.TimedOut,
	}
	if res.TimedOut {
		return run, nil // a timeout is an outcome, not a Start error — and not an exit, so no verdict
	}
	if err != nil {
		// The process could not be started/observed at all (PATH miss, fork
		// failure). That IS a Start error — the dispatcher marks the row and
		// releases the slot.
		return run, err
	}
	// A clean exit and a nonzero one are both outcomes the dispatcher routes, and
	// both say something about the account.
	r.reportVerdict(spec, run.ExitCode, res.StdoutTail, run.Stderr)
	return run, nil
}

// reportVerdict feeds one finished run's exit through the probe's shared
// classifier and into the AccountVerdict hook. The combined tail exists only
// for the duration of this call — matched, never stored.
func (r ClaudeRunner) reportVerdict(spec RunSpec, exitCode int, stdoutTail, stderrTail string) {
	if r.AccountVerdict == nil {
		return
	}
	r.AccountVerdict(spec.Resolution.Account, claudeprobe.ClassifyExit(exitCode, stdoutTail+"\n"+stderrTail))
}
