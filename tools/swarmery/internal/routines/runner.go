package routines

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// permEnv is this spawn site's --permission-mode knob (internal/claudeflags owns
// the resolution and the "off" escape hatch). An ai-prompt step is operator
// authored and routinely asks for files — refreshed docs, a written report — and
// a headless run without the flag can write NOTHING, not even inside its own
// cwd, while still exiting 0. The routine would then report success over an
// artifact that was never created.
const permEnv = "SWARMERY_ROUTINES_PERMISSION_MODE"

// Runner is the ai-prompt boundary: it spawns one headless `claude -p` run in
// the given cwd and returns raw stdout. Mocked in every test — no real claude
// invocation outside production (mirrors improve.Runner / dispatch.Runner).
type Runner interface {
	Run(ctx context.Context, cwd, prompt, model string) (string, error)
}

// TaskCreator is the create-task boundary: it inserts a board task (source=
// 'queue') and returns its external card id. Injected from the api/cmd layer so
// board semantics — external-id minting, column validation, task_updated WS
// publish, dispatcher poke — stay in one place (the api package) and this
// package never imports it (no cycle). Nil is tolerated (create-task steps then
// fail with a clear error rather than panicking).
type TaskCreator interface {
	// CreateTask inserts a board task for projectID (0/"" → global is NOT
	// allowed; a task needs a project) in the given column and returns the new
	// card's external id (e.g. "T-ab12cd").
	CreateTask(projectID int64, title, prompt, column string) (externalID string, err error)
}

// aiPromptTimeout bounds one ai-prompt run when the step sets no override.
const aiPromptTimeout = 10 * time.Minute

// stderrTailBytes caps captured stderr landing in a step's error field.
const stderrTailBytes = 4096

// ClaudeRunner runs `claude -p --output-format text` with the prompt on stdin,
// cwd set to the routine's project path (global → daemon cwd), optionally
// pinning --model. Binary resolution is a plain PATH lookup — identical to
// improve.ClaudeRunner / dispatch.ClaudeRunner (the daemon's launchd/service
// PATH must contain the claude binary).
type ClaudeRunner struct {
	// Timeout overrides aiPromptTimeout when > 0 (tests shrink it; the executor
	// passes the step's per-step timeout via the ctx deadline instead).
	Timeout time.Duration
}

func (r ClaudeRunner) Run(ctx context.Context, cwd, prompt, model string) (string, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	// --setting-sources project,local: skip user-level settings (global plugin
	// stack) — headless runs don't need them; project plugins and OAuth are
	// unaffected.
	args := []string{"-p", "--output-format", "text",
		"--setting-sources", "project,local"}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	args = append(args, claudeflags.PermissionModeArgs(permEnv)...)
	// launchd hands the daemon a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that
	// omits every usual install dir, so a bare exec of "claude" fails with ENOENT
	// under the service while working in the operator's shell. Resolve explicitly.
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	// cwd doubles as the account lookup key here: for a project-scoped routine it
	// IS the project's real directory (projects.path, resolved by
	// Service.projectPath in store.go — never a worktree, unlike
	// dispatch/verify/planrun/phaserun), so it carries that project's own
	// .claude/settings.local.json directly. For a GLOBAL routine (ProjectID NULL)
	// projectPath is "", and accountEnvFor's guard keeps that from ever reaching
	// claudeacct.EnvFor. nil delta for an unbound project ⇒ cmd.Env is a
	// byte-identical copy of os.Environ() — behaviour unchanged from before.
	cmd.Env = append(os.Environ(), accountEnvFor(cwd)...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", wrapRunErr(ctx, err, stderr.String())
	}
	return stdout.String(), nil
}

// accountEnvFor resolves the CLAUDE_CONFIG_DIR env delta for projectPath.
// Mirrors internal/api/term.go's termAccountEnv (plan A3, extended to this
// spawn site): claudeacct.Binding joins its argument with
// ".claude/settings.local.json" unconditionally, so claudeacct.EnvFor("")
// would resolve that RELATIVE path against the daemon's OWN process working
// directory and silently bind the run to whatever unrelated settings file
// happens to sit there. An empty projectPath (the global-routine case,
// ProjectID NULL) must short-circuit to nil before EnvFor is ever called.
func accountEnvFor(projectPath string) []string {
	if projectPath == "" {
		return nil
	}
	return claudeacct.EnvFor(projectPath)
}

func wrapRunErr(ctx context.Context, err error, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return &timeoutError{stderr: tail(stderr, stderrTailBytes)}
	}
	return &runError{err: err, stderr: tail(stderr, stderrTailBytes)}
}

// timeoutError signals a deadline-exceeded run so the executor can record status
// 'timeout' (distinct from a plain failure).
type timeoutError struct{ stderr string }

func (e *timeoutError) Error() string {
	if e.stderr != "" {
		return "timed out; stderr: " + e.stderr
	}
	return "timed out"
}

type runError struct {
	err    error
	stderr string
}

func (e *runError) Error() string {
	if e.stderr != "" {
		return e.err.Error() + "; stderr: " + e.stderr
	}
	return e.err.Error()
}

func isTimeout(err error) bool {
	_, ok := err.(*timeoutError)
	return ok
}

// tail returns the last <= n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
