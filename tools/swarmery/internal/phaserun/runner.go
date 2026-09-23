package phaserun

// Runner is the headless-claude boundary for a phase run (interactive planning
// v2 phase 5). ClaudeRunner is production; tests substitute a stub that returns
// without spawning a process (mirrors planning.Runner / dispatch.Runner). Start
// BLOCKS until the process exits — the service calls it inside its own
// goroutine (the async seam is the goroutine, not the Runner), which keeps exit
// handling and single-flight release in one place and makes the flow
// stub-testable.
//
// Knobs (all optional):
//   - SWARMERY_PHASERUN_EFFORT  the FALLBACK reasoning depth for a run whose
//     request named none AND whose phase doc declares no **Effort:** header — the
//     last rung before DefaultEffort. Unlike the model knob it IS validated
//     (internal/claudeflags): --effort takes a closed set of five values, an unknown
//     one makes the CLI reject the flag and the spawn die before the run starts, so
//     passing a typo through verbatim would turn it into a dead phase. Set it to
//     "off" to pass no --effort at all and inherit the CLI's xhigh.
//   - SWARMERY_PHASERUN_MODEL   the FALLBACK model for a run whose request named
//     none AND whose phase doc declares no **Model:** header — the LAST rung before
//     planning.DefaultModel. The service reads it (one resolution site) and puts it on
//     RunSpec.Model; the runner itself no longer touches the environment. It is
//     passed as --model VERBATIM and is never validated — unlike the two rungs above
//     it, both checked against the dashboard's closed model set. An operator pins a
//     full ID here, including forms that set does not know (e.g. a "[1m]"
//     context-window suffix), and validating it would silently drop every run back
//     to planning.DefaultModel. Pin full model IDs, not aliases — aliases re-resolve
//     over time. A model on the request outranks it, and so does one declared by the
//     phase doc; see Service.Start for the whole ladder.
//   - SWARMERY_PHASERUN_TIMEOUT Go duration bounding one phase run (default 4h).
//   - SWARMERY_PHASERUN_PERMISSION_MODE  --permission-mode for this site; see
//     internal/claudeflags for the default and the measurements behind it. A
//     headless phase run with no permission mode set denies every Write/Edit and
//     every un-allowlisted Bash command, then exits 0 having landed nothing.
//
// Binary resolution reuses planning.ClaudeBin (SWARMERY_CLAUDE_BIN override →
// PATH → common install locations), so the spawn works under launchd's minimal
// PATH exactly like the planner's.
//
// The spawn is process-group isolated (procgroup): the timeout must take the
// run's whole tree — shells, node, browsers, MCP servers — because the service
// deletes the worktree the instant Start returns. Killing the `claude` leader
// alone used to leave that tree writing into a directory being removed under it.

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// Runner is the spawn seam the service depends on.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) (*Run, error)
}

// RunSpec is one dispatched phase run.
type RunSpec struct {
	Prompt      string // full phase-run prompt (contract + embedded phase doc)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the acquired worktree path — the process runs here
	// SettingsFile is passed as --settings when non-empty: the project's
	// .claude/settings.json, lent to a run whose worktree cannot discover it
	// (see repopath.InheritedSettings). It carries the project's enabled plugins,
	// permissions and additionalDirectories.
	SettingsFile string

	// Model is the already-resolved model for this run, passed as --model when
	// non-empty. The service owns the ladder that fills it (request model → the
	// phase doc's **Model:** → SWARMERY_PHASERUN_MODEL → nothing); the runner only
	// forwards it, so "" means "emit no --model flag" and the run inherits the
	// account default.
	Model string

	// Effort is the already-resolved reasoning depth for this run, passed as
	// --effort. The service owns its ladder too (request effort → the phase
	// doc's **Effort:** → SWARMERY_PHASERUN_EFFORT → DefaultEffort), so the
	// runner only forwards it. Unlike Model, "" here is an explicit "off" rather
	// than a shrug: an omitted --effort means the CLI's xhigh, and a 4-hour
	// phase run at maximum depth is the most expensive shape this daemon has.
	Effort string

	// ProjectPath is the phase's project — phaseInfo.ProjectPath (projects.path),
	// the SAME value SettingsFile is derived from. Used ONLY to resolve the
	// Claude account this run must execute under: Cwd is the acquired
	// WORKTREE, which carries no .claude/settings.local.json of its own, so
	// resolving the account from Cwd here would silently fall back to the
	// default account — plan A3, extended from dispatch/verify to this spawn
	// site. "" (no known project path) means no account resolution at all;
	// see runcore.AccountFor's guard.
	ProjectPath string
}

// Run is the outcome of a completed phase-run process.
type Run struct {
	SessionUUID string        // echoed back for the phase↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started)
	TimedOut    bool          // true if the ctx deadline fired
	Stderr      string        // tail of stderr, surfaced in run_error on failure
	Duration    time.Duration // wall-clock spawn→exit
}

// phaseRunTimeout bounds one phase execution when SWARMERY_PHASERUN_TIMEOUT is
// unset or unparseable. A phase is a unit of real work — implement, test,
// verify, commit — and plan docs routinely estimate one at a day, so the old
// 60m ceiling killed long phases mid-flight and stamped them 'failed/timeout'
// with nothing landed. It still must not wedge a worktree forever, hence a
// bounded default well under the whole plan's 8h.
const phaseRunTimeout = 4 * time.Hour

// Env knobs — see the file header. modelEnv is read by the SERVICE (Start), not
// here: the ladder has one resolution site, so it stays testable without a
// t.Setenv reaching into the spawn.
const (
	modelEnv   = "SWARMERY_PHASERUN_MODEL"
	effortEnv  = "SWARMERY_PHASERUN_EFFORT"
	timeoutEnv = "SWARMERY_PHASERUN_TIMEOUT"
	permEnv    = "SWARMERY_PHASERUN_PERMISSION_MODE"
)

// DefaultEffort pins how hard a phase run thinks when neither the request nor
// the phase doc nor SWARMERY_PHASERUN_EFFORT says otherwise. A phase is real
// implementation work — read the code, edit it, run the checks, commit — so it
// is pinned high rather than down; the point of pinning it at all is that the
// CLI's unpinned default is xhigh, and four hours at maximum depth is this
// daemon's most expensive run shape. Phase 7 re-measures this value.
const DefaultEffort = "high"

// timeoutFromEnv reads SWARMERY_PHASERUN_TIMEOUT, falling back to the default on
// an unset or unusable value (an operator typo must not mean "no timeout").
func timeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(timeoutEnv))
	if raw == "" {
		return phaseRunTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("warning: phaserun: ignoring invalid %s=%q: %v", timeoutEnv, raw, err)
		return phaseRunTimeout
	}
	return d
}

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--model <m>]`
// with cwd set to the worktree. The prompt is passed as an argument (not
// stdin) so --session-id positioning is unambiguous (same as dispatch).
type ClaudeRunner struct {
	// Timeout overrides phaseRunTimeout when > 0 (tests shrink it).
	Timeout time.Duration
}

// Start maps this engine's RunSpec onto runcore.Spec and its Result back onto
// Run. Everything shared — the argv, the account env merge, the process group,
// the drain that must finish before the service removes the worktree, the exit
// ladder — lives in internal/runcore; what stays here is the phase run's own
// policy: its timeout window, its two env knobs, the model the service already
// resolved, and the settings file it lends a worktree that cannot discover the
// project's own.
func (r ClaudeRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = timeoutFromEnv()
	}

	res, err := runcore.ClaudeRunner{Engine: "phaserun"}.Start(ctx, runcore.Spec{
		Prompt:      spec.Prompt,
		SessionUUID: spec.SessionUUID,
		Cwd:         spec.Cwd,
		// Without a permission mode the run cannot write, cannot run its
		// verification command and cannot commit — and it still exits 0. See
		// internal/claudeflags for the resolution and its escape hatch.
		PermissionMode: claudeflags.Mode(permEnv),
		// Already resolved by the service (request → doc → env → planning.DefaultModel).
		Model: spec.Model,
		// Also already resolved by the service (request → doc → env → DefaultEffort).
		Effort:       spec.Effort,
		SettingsFile: spec.SettingsFile,
		// The account comes from spec.ProjectPath, never from Cwd: Cwd is the
		// phase's acquired worktree, which has no .claude/settings.local.json of its
		// own, so resolving it there would silently run the phase under the default
		// account (plan A3). An empty/unbound project resolves to "" and produces no
		// env delta, so cmd.Env stays a byte-identical copy of os.Environ().
		Account: runcore.AccountFor(spec.ProjectPath),
		Timeout: timeout,
		// Bin left nil: runcore resolves through claudebin by default (launchd's
		// minimal PATH omits npm/homebrew, so a bare lookup would miss).
	})
	return &Run{
		SessionUUID: res.SessionUUID,
		ExitCode:    res.ExitCode,
		TimedOut:    res.TimedOut,
		Stderr:      res.Stderr,
		Duration:    res.Duration,
	}, err
}
