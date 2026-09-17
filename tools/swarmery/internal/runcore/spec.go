// Package runcore is the one process-spawning primitive behind every engine
// that runs headless `claude` on the daemon's behalf: dispatch (board cards),
// phaserun (one plan phase), planrun (a whole plan), planning (the planner) and
// verify (the read-only judge).
//
// Before this package each of those five carried its own copy of the same
// machinery — argv assembly, account env merge, process-group isolation, the
// post-exit drain, the exit-classification ladder, a `tail` and a `newUUID`.
// Five copies of one behaviour is five places for it to drift, and drift here is
// invisible: a flag that stops being passed does not fail a build, it silently
// changes how an agent runs.
//
// What runcore owns is the SPAWN and nothing else. What stays with each engine
// is its admission policy (which run may start), its persistence (what a run
// stamps where), its timestamp format, and its prompt construction. In
// particular:
//
//   - Timestamp formats stay per-engine on purpose — dispatch writes ms-Z,
//     phaserun/planrun write RFC3339, and those strings are already on disk.
//   - The agent MENTION dispatch prefixes onto a prompt is prompt construction,
//     not argv: it stays in dispatch. Spec.Agent is the `--agent` flag, which is
//     a different mechanism (planrun uses it).
//   - Account RESOLUTION stays with the caller (see AccountFor): only the env
//     merge is shared, because which project a run belongs to is knowledge the
//     spawn layer does not have.
package runcore

import (
	"context"
	"time"
)

// Spec is the superset of the five engines' RunSpecs. A zero field means "omit
// the flag" — Args emits only what is set — so an engine that never used a flag
// keeps an argv byte-identical to the one it emitted before this package.
type Spec struct {
	Prompt      string // -p; passed as an argument (not stdin) so --session-id positioning is unambiguous
	SessionUUID string // --session-id: the daemon-generated explicit run↔session link
	Cwd         string // the process's working directory (a worktree, or a project path for planning)

	Model          string // --model; "" inherits the account default
	Agent          string // --agent; the "@name: " prompt mention is the CALLER's job, not this
	PermissionMode string // --permission-mode; "" omits the flag (see internal/claudeflags)
	SettingsFile   string // --settings; a project settings file lent to a worktree that cannot discover one
	SettingSources string // --setting-sources; dispatch/verify pass "project,local" to skip user-level settings

	// Account is the claudeacct key this run executes under, resolved by the
	// CALLER from the run's PROJECT — never from Cwd. Cwd is usually a worktree,
	// which carries no .claude/settings.local.json, so resolving it here would
	// silently fall back to the default account (plan A3). "" means the default
	// account and produces no env delta. See AccountFor.
	Account string

	// ExtraArgs is appended verbatim after every flag above. No engine needs it
	// today; it exists so a sixth frontend does not have to reopen Args.
	ExtraArgs []string

	// CaptureStdout keeps the child's FULL stdout in Result.Output. Only verify
	// wants it — the verdict lives in the transcript. Everyone else ingests the
	// transcript out of band and must leave stdout discarded.
	CaptureStdout bool

	// StdoutTailBytes > 0 keeps only the last N bytes of stdout in
	// Result.StdoutTail. This is dispatch's shape: it needs enough output to
	// classify an exit as a login failure (claudeprobe) and nothing more, and it
	// must NOT buffer a whole session transcript. Ignored when CaptureStdout is
	// set (a full capture already carries the tail).
	StdoutTailBytes int

	// Timeout bounds the run. 0 means the CALLER owns cancellation through ctx —
	// dispatch's shape, where the stage's deadline lives with the dispatcher.
	Timeout time.Duration

	// Bin resolves the executable. nil means claudebin.Resolve — the daemon's
	// service PATH is launchd's minimal one (/usr/bin:/bin:/usr/sbin:/sbin) and
	// does NOT contain the usual install dirs, so a bare exec of "claude" fails
	// with ENOENT under the service while working fine in the operator's shell.
	// Only tests pass a non-nil Bin, to point the spawn at a fixture script.
	Bin func() (string, error)
}

// Result is the outcome of one completed spawn. It is deliberately a plain
// value: every engine maps it into its own Run type, because those types are
// part of each engine's public seam and its tests' stub returns.
type Result struct {
	SessionUUID string        // echoed back for the run↔session link
	ExitCode    int           // process exit status (0 = clean; -1 = never started, or timed out)
	TimedOut    bool          // true if the deadline fired — an OUTCOME, never an error
	Stderr      string        // tail of stderr (StderrTailBytes), for the error detail
	Output      string        // full stdout, only when Spec.CaptureStdout
	StdoutTail  string        // last Spec.StdoutTailBytes of stdout, only when that is > 0
	Duration    time.Duration // wall-clock spawn→exit, excluding the drain (which is teardown)
}

// Runner is the spawn seam. ClaudeRunner is production; an engine's tests
// substitute their own stub at their own Runner interface, one level up.
type Runner interface {
	Start(ctx context.Context, spec Spec) (*Result, error)
}
