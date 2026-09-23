package planrun

// Runner is the headless-claude boundary for a plan run. ClaudeRunner is
// production; tests substitute a stub that returns without spawning a process
// (mirrors planning.Runner / phaserun.Runner). Start BLOCKS until the process
// exits — the service calls it inside its own goroutine (the async seam is the
// goroutine, not the Runner), which keeps exit handling and single-flight
// release in one place and makes the flow stub-testable.
//
// Knobs (all optional):
//   - SWARMERY_PLANRUN_AGENT   default --agent when the caller names none
//     (falls back to "tech-lead"). The UI's agent picker overrides it per run.
//   - SWARMERY_PLANRUN_MODEL   passed as --model verbatim; unset ⇒ the account
//     default. Pin full model IDs, not aliases — aliases re-resolve over time.
//   - SWARMERY_PLANRUN_TIMEOUT Go duration bounding one plan run (default 8h).
//     A whole plan is many phases of real work, so it gets far more room than a
//     single phase (SWARMERY_PHASERUN_TIMEOUT, default 4h) — but it must not
//     wedge a worktree forever.
//   - SWARMERY_PLANRUN_PERMISSION_MODE  --permission-mode for this site; see
//     internal/claudeflags for the default and the measurements behind it. A
//     headless run with no permission mode set denies every Write/Edit and every
//     un-allowlisted Bash command, then exits 0 having landed nothing.
//
// Binary resolution reuses planning.ClaudeBin (SWARMERY_CLAUDE_BIN override →
// PATH → common install locations), so the spawn works under launchd's minimal
// PATH exactly like the planner's.

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// Runner is the spawn seam the service depends on.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) (*Run, error)
}

// RunSpec is one dispatched plan run.
type RunSpec struct {
	Prompt      string // full plan-run prompt (contract + README + phase manifest)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the acquired worktree path — the process runs here
	Agent       string // --agent; "" ⇒ omitted (the session's default agent)
	// SettingsFile is passed as --settings when non-empty: the project's
	// .claude/settings.json, lent to a run whose worktree cannot discover it
	// (see repopath.InheritedSettings). It carries the project's enabled plugins,
	// permissions and additionalDirectories — without it a multi-repo run has no
	// core@swarmery, and the default agent does not exist.
	SettingsFile string

	// Resume makes this spawn a CONTINUATION of SessionUUID (`claude -r <uuid>`):
	// the completion loop's nudge to an orchestrator that ended its turn with
	// phase criteria still unticked. The service copies the original spec and
	// changes only this and Prompt, so --agent, --settings and the account are
	// unchanged; model and effort are re-resolved identically by this runner.
	Resume bool

	// ProjectPath is the plan's project — planInfo.ProjectPath (projects.path),
	// the SAME value SettingsFile is derived from. Used ONLY to resolve the
	// Claude account this run must execute under: Cwd is the acquired
	// WORKTREE, which carries no .claude/settings.local.json of its own, so
	// resolving the account from Cwd here would silently fall back to the
	// default account — plan A3, extended from dispatch/verify to this spawn
	// site. "" (no known project path) means no account resolution at all;
	// see runcore.AccountFor's guard.
	ProjectPath string
}

// Run is the outcome of a completed plan-run process.
type Run struct {
	SessionUUID string        // echoed back for the plan↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started)
	TimedOut    bool          // true if the ctx deadline fired
	Stderr      string        // tail of stderr, surfaced in run_error on failure
	Duration    time.Duration // wall-clock spawn→exit
}

// Env knobs — see the file header.
const (
	agentEnv   = "SWARMERY_PLANRUN_AGENT"
	modelEnv   = "SWARMERY_PLANRUN_MODEL"
	effortEnv  = "SWARMERY_PLANRUN_EFFORT"
	timeoutEnv = "SWARMERY_PLANRUN_TIMEOUT"
	permEnv    = "SWARMERY_PLANRUN_PERMISSION_MODE"
)

// DefaultEffort pins how hard a plan run's orchestrator thinks. A plan run is
// the longest-lived shape this daemon has (an 8h window) and it spends that time
// routing and verifying phases, so an unpinned --effort — the CLI's xhigh —
// bought maximum reasoning depth on every turn of every one of them. high is the
// deliberate choice; phase 7 re-measures it.
const DefaultEffort = "high"

// DefaultModel is the model a plan run uses when SWARMERY_PLANRUN_MODEL names
// none. It was "" — no --model flag — which does NOT mean "a sensible house
// default": it means the ACCOUNT default, which on these accounts is Fable, at
// roughly twice the Opus price. So the path every un-picked "Run plan" took was
// the most expensive one available, for eight hours at a time. Exported, like
// DefaultAgent, so the API can tell the UI what an un-picked run will use.
func DefaultModel() string {
	if m := strings.TrimSpace(os.Getenv(modelEnv)); m != "" {
		return m
	}
	return planning.DefaultModel
}

// fallbackAgent is the orchestrating agent a plan run is handed to when neither
// the caller nor the environment names one. tech-lead is core's routing agent —
// the one whose job description already is "take a plan and get it executed".
const fallbackAgent = "tech-lead"

// planRunTimeout bounds one plan execution when SWARMERY_PLANRUN_TIMEOUT is
// unset or unparseable.
const planRunTimeout = 8 * time.Hour

// DefaultAgent is the agent a plan run uses when the caller names none: the
// SWARMERY_PLANRUN_AGENT override, else fallbackAgent. Exported so the API can
// tell the UI which agent an un-picked "Run plan" will actually use.
func DefaultAgent() string {
	if a := strings.TrimSpace(os.Getenv(agentEnv)); a != "" {
		return a
	}
	return fallbackAgent
}

// runTimeout resolves the configured plan-run window.
func runTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(timeoutEnv))
	if raw == "" {
		return planRunTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("warning: planrun: ignoring invalid %s=%q: %v", timeoutEnv, raw, err)
		return planRunTimeout
	}
	return d
}

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid> [--agent <a>]
// [--model <m>]` with cwd set to the worktree. The prompt is passed as an
// argument (not stdin) so --session-id positioning is unambiguous (same as
// dispatch/phaserun).
type ClaudeRunner struct {
	// Timeout overrides the env/default window when > 0 (tests shrink it).
	Timeout time.Duration
}

// Start maps this engine's RunSpec onto runcore.Spec and its Result back onto
// Run. Everything shared — the argv, the account env merge, the process group,
// the drain that must finish before the service removes the worktree, the exit
// ladder — lives in internal/runcore; what stays here is the plan run's own
// policy: its 8h window, its four env knobs, the orchestrating agent, and the
// settings file that enables the plugin that agent ships in.
func (r ClaudeRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = runTimeout()
	}

	res, err := runcore.ClaudeRunner{Engine: "planrun"}.Start(ctx, runcore.Spec{
		Prompt:      spec.Prompt,
		SessionUUID: spec.SessionUUID,
		Cwd:         spec.Cwd,
		Resume:      spec.Resume,
		// Without a permission mode the orchestrator cannot write, run or commit —
		// and it still exits 0. See internal/claudeflags.
		PermissionMode: claudeflags.Mode(permEnv),
		Agent: spec.Agent,
		Model: DefaultModel(),
		// Without --effort the orchestrator thinks at the CLI's xhigh for every
		// turn of an 8-hour window. Resolved through internal/claudeflags:
		// SWARMERY_PLANRUN_EFFORT → SWARMERY_EFFORT → DefaultEffort.
		Effort:       claudeflags.Effort(effortEnv, DefaultEffort),
		SettingsFile: spec.SettingsFile,
		// The account comes from spec.ProjectPath, never from Cwd: Cwd is the plan's
		// acquired worktree, which has no .claude/settings.local.json of its own, so
		// resolving it there would silently run the plan under the default account
		// (plan A3). An empty/unbound project resolves to "" and produces no env
		// delta, so cmd.Env stays a byte-identical copy of os.Environ().
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
