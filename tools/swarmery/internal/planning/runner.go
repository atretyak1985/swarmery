package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// Runner is the headless-claude boundary for a planner run. ClaudeRunner is
// production; tests substitute a stub that returns without spawning a process
// (mirrors improve.Runner / dispatch.Runner / routines.Runner). Start BLOCKS
// until the process exits — the service calls it inside its own goroutine (the
// async seam is the goroutine, not the Runner), which keeps exit handling and
// single-flight release in one place and makes the flow stub-testable.
type Runner interface {
	Start(ctx context.Context, spec RunSpec) (*Run, error)
}

// RunSpec is one dispatched planner run.
type RunSpec struct {
	Prompt      string // full planner prompt (idea + instructions)
	SessionUUID string // daemon-generated; passed as --session-id (explicit link)
	Cwd         string // the project path — the process runs here (hooks active)
	Model       string // full model ID for --model; "" falls back to the runner's default
}

// Run is the outcome of a completed planner process.
type Run struct {
	SessionUUID string        // echoed back for the task↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started)
	TimedOut    bool          // true if the ctx deadline fired
	Stderr      string        // tail of stderr, surfaced on failure
	Duration    time.Duration // wall-clock spawn→exit
}

// planTimeout bounds one planner run (a planner may think + ask + write a plan
// dir, which is longer than a mechanical run but must not wedge a slot forever).
const planTimeout = 20 * time.Minute

// DefaultModel pins planner runs: without --model the CLI inherits the account
// default (Fable-5 here — 2× the Opus price). Full ID, not an alias — aliases
// re-resolve over time.
const DefaultModel = "claude-opus-5"

// Models is the closed set an operator may plan with, keyed by the short name
// the dashboard shows and valued by the full ID that reaches --model. The
// resolved ID is what the planning_sessions row stores, so every later resume
// of the same wizard runs on the same model as its first turn.
var Models = map[string]string{
	"opus":   DefaultModel,
	"sonnet": "claude-sonnet-5",
	"fable":  "claude-fable-5-1",
}

// ErrUnknownModel: the requested model is neither a short name nor a full ID
// from Models (400 at the api layer).
var ErrUnknownModel = errors.New("unknown planning model")

// ResolveModel maps an operator's choice to the full ID: "" is the default, a
// short name or a full ID from Models passes, anything else is ErrUnknownModel.
func ResolveModel(choice string) (string, error) {
	c := strings.TrimSpace(choice)
	if c == "" {
		return DefaultModel, nil
	}
	if id, ok := Models[c]; ok {
		return id, nil
	}
	for _, id := range Models {
		if id == c {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownModel, choice)
}

// permEnv is this spawn site's --permission-mode knob (internal/claudeflags owns
// the resolution and the "off" escape hatch). A planner run's whole product is
// files — a plan dir under the private workspace — so the flag is not optional
// here: without it every Write and every mkdir is auto-denied, the process still
// exits 0, and the plan survives only in a reply nobody stores.
const permEnv = "SWARMERY_PLANNING_PERMISSION_MODE"

// ClaudeRunner spawns `claude -p <prompt> --session-id <uuid>` with cwd set to
// the project path. Binary resolution mirrors session_message.go's claudeBin:
// launchd starts the daemon with a minimal PATH that omits npm/homebrew, so a
// bare LookPath can miss — an explicit SWARMERY_CLAUDE_BIN override, then PATH,
// then the common install locations. The prompt is passed as an argument (not
// stdin) so --session-id positioning is unambiguous (same as dispatch).
type ClaudeRunner struct {
	// Timeout overrides planTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides DefaultModel when non-empty and the spec carries none.
	Model string
}

// Start maps this engine's RunSpec onto runcore.Spec and its Result back onto
// Run. Everything shared — the argv, the account env merge, the process group,
// the drain, the exit ladder — lives in internal/runcore; what stays here is the
// planner's own policy: its timeout, its model default, and resolving the account
// from cwd.
func (r ClaudeRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = planTimeout
	}
	model := spec.Model
	if model == "" {
		model = r.Model
	}
	if model == "" {
		model = DefaultModel
	}

	res, err := runcore.ClaudeRunner{Engine: "planning"}.Start(ctx, runcore.Spec{
		Prompt:      spec.Prompt,
		SessionUUID: spec.SessionUUID,
		Cwd:         spec.Cwd,
		Model:       model,
		// The planner writes: a plan dir with README/spec/phase docs, and nothing
		// else it does matters if that write is denied. See internal/claudeflags.
		PermissionMode: claudeflags.Mode(permEnv),
		// Resolving the account from cwd is correct HERE — and only here and in
		// provision. A planner run's Cwd is the PROJECT path (see RunSpec.Cwd), so it
		// carries the project's .claude/settings.local.json. dispatch and verify look
		// the same but are not: their Cwd is a worktree with no settings file, which
		// is why they take the key from the caller instead (plan A3).
		// An unbound project resolves to "" and produces no env delta, so cmd.Env is
		// then a byte-identical copy of os.Environ().
		Account: claudeacct.Binding(spec.Cwd),
		Timeout: timeout,
		// launchd starts the daemon with a minimal PATH that omits npm/homebrew, so a
		// bare PATH lookup can miss — ClaudeBin probes the install locations too.
		Bin: ClaudeBin,
	})
	return &Run{
		SessionUUID: res.SessionUUID,
		ExitCode:    res.ExitCode,
		TimedOut:    res.TimedOut,
		Stderr:      res.Stderr,
		Duration:    res.Duration,
	}, err
}

// ClaudeBin resolves the Claude Code executable so the planner spawn works
// under launchd's minimal PATH: explicit SWARMERY_CLAUDE_BIN override → PATH
// lookup → probe the common install locations. Exported so tests can assert the
// resolution and the service can surface a clear "binary missing" error before
// spawning. The logic lives in internal/claudebin, shared with the API layer's
// resume spawn and mcpcfg's `claude mcp …` shell-out.
func ClaudeBin() (string, error) { return claudebin.Resolve() }
