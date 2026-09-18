package runcore

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
)

// resolveBin is the default executable resolver for a Spec that leaves Bin nil,
// which is every production caller. A package var only so a test can assert the
// default is wired at all without a claude on the box — the same seam
// claudeprobe uses, and for the same reason.
var resolveBin = claudebin.Resolve

// StderrTailBytes caps the stderr kept on a Result. Every engine surfaces that
// tail in its own error column (dispatch_error, run_error, verify_detail), and
// all five capped it at the same 4 KiB before this package existed.
const StderrTailBytes = 4096

// drainGrace bounds the post-exit wait for the run's process group to empty out.
// It matters most where a service deletes the worktree the instant Start returns
// (phaserun/planrun): a survivor would be writing into a directory being removed
// under it.
const drainGrace = 5 * time.Second

// Args builds the canonical argv. The `claude` CLI is order-insensitive, so the
// order below is a CHOICE, not a constraint — it is the one order that reproduces
// what all five engines emitted before they shared this builder, which is why
// spawner_test.go pins it per engine rather than describing it in prose:
//
//	-p <prompt> --session-id <uuid> [--setting-sources S] [--permission-mode P]
//	[--agent A] [--model M] [--settings F] [extra…]
//
// Prompt and SessionUUID are NOT trimmed: they are values, not flags, and a
// prompt's leading whitespace is the caller's business.
func Args(spec Spec) []string {
	args := []string{"-p", spec.Prompt, "--session-id", spec.SessionUUID}
	if s := strings.TrimSpace(spec.SettingSources); s != "" {
		args = append(args, "--setting-sources", s)
	}
	// Omitting this is not a cosmetic difference: a headless run with no
	// permission mode auto-denies every Write/Edit and every un-allowlisted Bash
	// call — there is no approver in a headless run — and still exits 0, so the
	// work is recorded as a clean success that landed nothing. The resolution and
	// its escape hatch live in internal/claudeflags; the empty string here means
	// the caller resolved "omit".
	if m := strings.TrimSpace(spec.PermissionMode); m != "" {
		args = append(args, "--permission-mode", m)
	}
	if a := strings.TrimSpace(spec.Agent); a != "" {
		args = append(args, "--agent", a)
	}
	if m := strings.TrimSpace(spec.Model); m != "" {
		args = append(args, "--model", m)
	}
	// After --agent deliberately: the settings file is what enables the plugin the
	// agent ships in, and planrun emitted it last for that reason.
	if f := strings.TrimSpace(spec.SettingsFile); f != "" {
		args = append(args, "--settings", f)
	}
	return append(args, spec.ExtraArgs...)
}

// ClaudeRunner is the production spawner: one argv builder, one env merge, one
// process-group isolate + drain, one exit ladder.
type ClaudeRunner struct {
	// Engine names the frontend for the drain warning ("dispatch", "phaserun",
	// …). It only reaches log lines, and it is a field rather than a Spec value
	// because it describes the caller, not the run.
	Engine string
}

// Start spawns the run and BLOCKS until the process exits and its process group
// is drained. The async seam is the caller's goroutine, not this method — that is
// what keeps exit handling and slot release in one place in every engine.
//
// The three-way outcome ladder is the contract every engine already spoke:
//
//	deadline fired  → (Result{TimedOut: true, ExitCode: -1}, nil)  an outcome
//	nonzero exit    → (Result{ExitCode: n}, nil)                   an outcome
//	could not start → (Result{ExitCode: -1}, err)                  an error
//
// A timeout and a nonzero exit are things that HAPPENED and each engine routes
// them (stamp failed, mark timeout, grade INCONCLUSIVE). Only a process that
// never ran — PATH miss, fork failure — is an error, because there is no outcome
// to route.
func (r ClaudeRunner) Start(ctx context.Context, spec Spec) (*Result, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	resolve := spec.Bin
	if resolve == nil {
		resolve = resolveBin
	}
	bin, err := resolve()
	if err != nil {
		// Resolution failed before anything was spawned, so there is no
		// duration and no stderr to report — only the missing binary.
		return &Result{SessionUUID: spec.SessionUUID, ExitCode: -1}, err
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, Args(spec)...)
	cmd.Dir = spec.Cwd
	// The env delta comes from the resolved KEY, never from cmd.Dir: cmd.Dir is
	// usually a worktree, which has no .claude/settings.local.json of its own, so
	// claudeacct.EnvFor(cmd.Dir) here would resolve nothing and silently run under
	// the default account (plan A3). EnvForAccount("") returns nil, so an unbound
	// project's cmd.Env is a byte-identical copy of os.Environ().
	//
	// SecretEnvForAccount carries the same account's MCP credentials. It is keyed
	// the same way and for the same reason, and it is the ONLY channel that
	// works here: a headless spawn passes --setting-sources project,local, under
	// which a settings `env` block does not expand ${VAR} at all. An account with
	// no secret store adds nothing.
	cmd.Env = append(os.Environ(), claudeacct.EnvForAccount(spec.Account)...)
	cmd.Env = append(cmd.Env, claudeacct.SecretEnvForAccount(spec.Account)...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// stdout is the assistant text. Only verify parses it (the verdict lives
	// there); everyone else ingests the transcript independently and leaves
	// cmd.Stdout nil so the child writes to /dev/null. That default is load-
	// bearing, not laziness: attaching a writer puts a pipe between the daemon and
	// every run whose output nobody reads.
	var (
		stdout     bytes.Buffer
		stdoutTail *tailBuffer
	)
	switch {
	case spec.CaptureStdout:
		cmd.Stdout = &stdout
	case spec.StdoutTailBytes > 0:
		stdoutTail = &tailBuffer{max: spec.StdoutTailBytes}
		cmd.Stdout = stdoutTail
	}

	// Own process group: a timeout or cancel must reach the run's WHOLE tree
	// (shells, tools, MCP servers, sub-agents), and Wait must not block on a pipe
	// an orphan still holds.
	procgroup.Isolate(cmd, 0)

	runErr := cmd.Run()
	elapsed := time.Since(start) // the run's own wall clock — the drain below is teardown

	// Wait only guarantees the leader is reaped.
	if cmd.Process != nil && procgroup.Drain(cmd.Process.Pid, drainGrace) {
		log.Printf("warning: %s: uuid=%s left processes behind; killed the run's process group", r.Engine, spec.SessionUUID)
	}

	res := &Result{
		SessionUUID: spec.SessionUUID,
		Stderr:      Tail(stderr.String(), StderrTailBytes),
		Duration:    elapsed,
	}
	if spec.CaptureStdout {
		res.Output = stdout.String()
	}
	res.StdoutTail = stdoutTail.String()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		res.ExitCode = -1
		return res, runErr
	}
	res.ExitCode = 0
	return res, nil
}

// AccountFor is the account-resolution guard the phase/plan spawn sites need:
// claudeacct.Binding joins its argument with ".claude/settings.local.json"
// unconditionally, so EnvFor("") would resolve that RELATIVE path against the
// daemon's OWN working directory and bind the run to whatever unrelated settings
// file happens to sit there. An empty projectPath must short-circuit to "" before
// Binding is ever called.
//
// Callers that already know the key (dispatch, verify — they resolve it once per
// task from the project path) skip this and set Spec.Account directly.
func AccountFor(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	return claudeacct.Binding(projectPath)
}

// tailBuffer is an io.Writer keeping only the last max bytes written — enough for
// a classifier's fixed markers (a no-login run dies after one short line) without
// buffering a whole session transcript.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// String tolerates the nil buffer of a run that captured no tail.
func (t *tailBuffer) String() string {
	if t == nil {
		return ""
	}
	return string(t.buf)
}
