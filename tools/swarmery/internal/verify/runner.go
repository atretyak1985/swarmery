package verify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
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

	// Resume makes this spawn a CONTINUATION of SessionUUID (`claude -r <uuid>`).
	// One use only: a verifier that produced output but no VERDICT: line is asked
	// once for the line it owed, in the session that already holds every piece of
	// evidence it gathered. Re-running from scratch instead would pay for the whole
	// read-only pass a second time and could reach a different conclusion, which is
	// not a retry but a second opinion.
	Resume bool

	// Resolution is what this run executes under — the Claude Code account and
	// the project's estate — resolved by the CALLER from the task's PROJECT path,
	// never from Cwd (a worktree). The zero value resolves nothing and adds no
	// env delta.
	Resolution claudeacct.Resolution

	// SettingsFile is passed as --settings when non-empty: runsettings.Compose's
	// result — the admitted estate's EstateKeys — composed by the CALLER from the
	// same resolution. "" (no admitted estate) passes none, as before.
	SettingsFile string

	// DisallowedTools are denied ON TOP of the read-only set every run of this
	// runner gets (readOnlyTools). The phase review stage passes "Bash": a
	// reviewer reads, it does not run checks. Merged into the ONE --disallowedTools
	// flag ToolDenyArgs builds — a second flag on the argv would leave it to the
	// CLI whether the lists merge or the last one wins, and the read-only set must
	// not depend on that.
	DisallowedTools []string
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

// permEnv is this spawn site's --permission-mode knob. internal/claudeflags owns
// the resolution: this knob, then SWARMERY_PERMISSION_MODE, then
// bypassPermissions; "off" omits the flag.
//
// The verifier is the one runcore engine that used to omit the flag, and that
// was the main cause of its inconclusive verdicts. With no mode, every Bash
// check outside the project allowlist raised a permission prompt. A headless
// run cannot answer one, and the approvals long-poll held each for its full 10
// minutes. Two such prompts outlast the 15-minute hard timeout, so the run was
// stamped verifier-timed-out after about a minute of real work (verification
// runs 6 and 8, 2026-10-02). The one run that reached a verdict that afternoon
// did so only because the operator approved its four prompts from the dashboard
// within seconds.
const permEnv = "SWARMERY_VERIFY_PERMISSION_MODE"

// readOnlyTools keeps the edit tools denied. Before permEnv existed they were
// denied as a side effect of the missing mode, and a mode that stops the asking
// must not also start the editing. The verifier grades the worktree it runs in,
// so it may run checks (Bash) but may not change files with the edit tools. A
// Bash command can still write; the prompt contract forbids that, as before. The
// list is applied whatever permEnv resolves to, including "off".
var readOnlyTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}

// ToolDenyArgs is the argv tail of one run: a SINGLE --disallowedTools flag
// whose list is readOnlyTools followed by extra (blanks and duplicates dropped,
// order kept). Exported so a caller that adds to the list can pin the exact
// flag its runs get without spawning one.
func ToolDenyArgs(extra []string) []string {
	seen := make(map[string]bool, len(readOnlyTools)+len(extra))
	tools := make([]string, 0, len(readOnlyTools)+len(extra))
	for _, list := range [][]string{readOnlyTools, extra} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			tools = append(tools, t)
		}
	}
	return []string{"--disallowedTools", strings.Join(tools, ",")}
}

// daemonClients and daemonHosts span DaemonDenyPatterns: the HTTP clients a model
// reaches for, against both spellings of the loopback address the daemon binds by
// default.
var (
	daemonClients = []string{"curl", "wget", "http"}
	daemonHosts   = []string{"127.0.0.1", "localhost"}
)

// DaemonDenyPatterns are the deny rules that keep a run off this daemon's own HTTP
// API on port: one Bash rule per client and host, `Bash(curl *127.0.0.1:7777*)`. A
// port <= 0 is DefaultDaemonPort. The verifier and the reviewer run under
// bypassPermissions, and that API does what neither of them may: stamp verdicts,
// tick phases, start runs, answer approvals. Callers pass the result as extra to
// ToolDenyArgs (RunSpec.DisallowedTools), so it joins the ONE flag.
//
// Syntax, from the Claude Code permissions reference
// (https://code.claude.com/docs/en/permissions — "Wildcard patterns", "Compound
// commands", "What a Bash rule doesn't match"; checked 2026-10-10):
//   - `*` may stand anywhere in a Bash rule and matches any text, spaces included,
//     so `curl *127.0.0.1:7777*` matches `curl -s http://127.0.0.1:7777/api/x`.
//   - the `:*` suffix is only recognised at the END of a rule. Mid-rule the colon is
//     literal, so `Bash(curl:*127.0.0.1:7777*)` would match no curl command at all.
//   - a deny rule applies when ANY subcommand of a compound command matches, and
//     past a leading env assignment; and "deny rules block in every mode, including
//     bypassPermissions" (https://code.claude.com/docs/en/permission-modes).
//   - it does not match the program by path (`/usr/bin/curl …`) or inside `sh -c`:
//     this guards the commands a model writes, it is not a network boundary. The
//     prompt sentence (DaemonAPINotice) is the other half.
//
// A space inside the parentheses survives the comma-joined list: `claude --help`
// documents `"Bash(git *) Edit"` as one --disallowedTools value holding two rules.
func DaemonDenyPatterns(port int) []string {
	if port <= 0 {
		port = DefaultDaemonPort
	}
	out := make([]string, 0, len(daemonClients)*len(daemonHosts))
	for _, c := range daemonClients {
		for _, h := range daemonHosts {
			out = append(out, fmt.Sprintf("Bash(%s *%s:%d*)", c, h, port))
		}
	}
	return out
}

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. Binary resolution is a plain PATH lookup — the
// same pattern as dispatch.ClaudeRunner / internal/toolproc (the daemon's
// launchd/service PATH must contain the claude binary). The prompt is passed as
// an argument (not stdin) so --session-id positioning is unambiguous, matching
// the dispatcher. NOTE: read-only-ness is enforced by the PROMPT contract plus
// the edit tools denied on the argv (ToolDenyArgs), not by a sandbox: a
// Bash command can still write. The security review must confirm the run cannot mutate the
// worktree in a way that would corrupt the graded diff (it runs in the task's
// own throwaway worktree, so at worst it dirties that worktree, never main).
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it; the service
	// sets it from SWARMERY_VERIFY_TIMEOUT_MIN).
	Timeout time.Duration

	// AccountVerdict, when set, is called after a run finishes with the account
	// the run used ("" = the default account, Resolution.Account's own convention) and
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
		Resume:      spec.Resume,
		Model:       spec.Model,
		// Resolved, never omitted: an absent --effort is the CLI's xhigh, paid on
		// every graded task in the fleet.
		Effort: claudeflags.Effort(effortEnv, DefaultEffort),
		// A mode that never asks: a prompt in a headless run waits for nobody
		// until the timeout kills the run (see permEnv).
		PermissionMode: claudeflags.Mode(permEnv),
		// Last on the argv: --disallowedTools takes a variadic list, so a bare
		// positional argument after it would be read as another tool name.
		ExtraArgs: ToolDenyArgs(spec.DisallowedTools),
		// --setting-sources project,local: skip user-level settings (global plugin
		// stack) — headless runs don't need them; project plugins and OAuth are
		// unaffected.
		SettingSources: "project,local",
		// The resolution comes from the SPEC, not from Cwd: Cwd is the task's
		// worktree. The service resolves the project path once per run.
		Resolution:   spec.Resolution,
		SettingsFile: spec.SettingsFile,
		Timeout:      timeout,
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
// ClassifyRun reads a NON-ZERO exit's last output line as an account failure
// line, the same rule dispatch applies: verification has no admission gate of its
// own, but its verdicts still open the account's breaker.
func (r ClaudeRunner) reportVerdict(spec RunSpec, exitCode int, stdout, stderrTail string) {
	if r.AccountVerdict == nil {
		return
	}
	r.AccountVerdict(spec.Resolution.Account, claudeprobe.ClassifyRun(exitCode, stdout, stderrTail))
}
