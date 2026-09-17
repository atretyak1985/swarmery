package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// fakeClaude writes a shell script named `claude` into a temp dir and prepends
// it to PATH so ClaudeRunner.Start spawns IT instead of the real binary. The
// script body decides the behavior (exit code / sleep). This exercises the real
// process-spawn + exit-routing branches without invoking a real claude session.
func fakeClaude(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestClaudeRunnerExitZero(t *testing.T) {
	fakeClaude(t, `exit 0`)
	run, err := ClaudeRunner{}.Start(context.Background(),
		RunSpec{Prompt: "p", SessionUUID: "u1", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if run.ExitCode != 0 || run.TimedOut {
		t.Errorf("clean exit: code=%d timedOut=%v", run.ExitCode, run.TimedOut)
	}
	if run.SessionUUID != "u1" {
		t.Errorf("uuid not echoed: %q", run.SessionUUID)
	}
}

func TestClaudeRunnerNonzeroExit(t *testing.T) {
	fakeClaude(t, `echo "explosion" 1>&2; exit 3`)
	run, err := ClaudeRunner{}.Start(context.Background(),
		RunSpec{Prompt: "p", SessionUUID: "u2", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("nonzero exit should be an outcome, not a Start error: %v", err)
	}
	if run.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", run.ExitCode)
	}
	if run.Stderr == "" {
		t.Error("stderr tail should be captured")
	}
}

// The full argv, exactly: since the extraction to internal/runcore the ORDER
// comes from a builder five engines share, and `claude` is order-insensitive, so
// an accidental reordering would be invisible to everything but an assertion like
// this one. Mirrors the dispatch cases in internal/runcore/spawner_test.go.
func TestClaudeRunnerModelFlag(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	// Echo the args so we can assert --model is passed through. Exit 0.
	fakeClaude(t, `echo "$@" > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()
	_, err := ClaudeRunner{}.Start(context.Background(),
		RunSpec{Prompt: "hello", SessionUUID: "u3", Cwd: cwd, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "-p hello --session-id u3 --setting-sources project,local --permission-mode " +
		claudeflags.DefaultMode + " --model sonnet"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// spawnArgs runs ClaudeRunner against a fake claude that echoes its argv into a
// file, and returns that argv line. Shared by the agent-prefix tests.
func spawnArgs(t *testing.T, spec RunSpec) string {
	t.Helper()
	fakeClaude(t, `echo "$@" > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()
	spec.Cwd = cwd
	if _, err := (ClaudeRunner{}).Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	return string(out)
}

// A task carrying an agent must reach `claude -p` as "@<agent>: <prompt>" —
// ONCE. The count assertion is the real contract: the prefix has exactly one
// application site (agentPrompt), so a service that also prefixed would show up
// here as two.
func TestClaudeRunnerAgentPrefixesPromptOnce(t *testing.T) {
	got := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u6", Agent: "tech-lead"})
	if !contains(got, "@tech-lead: hello") {
		t.Errorf("args %q missing the agent-prefixed prompt", got)
	}
	if n := strings.Count(got, "@tech-lead: "); n != 1 {
		t.Errorf("prefix applied %d times, want exactly 1 (args %q)", n, got)
	}
	// Model/session flags are untouched by the prefix.
	for _, want := range []string{"--session-id", "u6", "--setting-sources", "project,local"} {
		if !contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
}

// Regression: a task with no agent dispatches byte-identically to pre-feature
// behavior — the prompt is passed through with no mention of any kind.
func TestClaudeRunnerNoAgentLeavesPromptUnchanged(t *testing.T) {
	got := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u7"})
	if !contains(got, "-p hello ") {
		t.Errorf("args %q should carry the bare prompt", got)
	}
	if contains(got, "@") {
		t.Errorf("args %q must contain no agent mention when Agent is unset", got)
	}
}

// agentPrompt is the single prefix site; pin its whole closed set of behaviors
// here so the argv tests above only have to prove the wiring.
func TestAgentPromptSingleSite(t *testing.T) {
	for _, tc := range []struct{ name, agent, prompt, want string }{
		{"unset", "", "do the thing", "do the thing"},
		{"whitespace only is no agent", "   ", "do the thing", "do the thing"},
		{"set", "tech-lead", "do the thing", "@tech-lead: do the thing"},
		{"trimmed", "  tech-lead  ", "do the thing", "@tech-lead: do the thing"},
		{"prompt already mentioning someone is not re-owned",
			"tech-lead", "ask @qa about it", "@tech-lead: ask @qa about it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentPrompt(RunSpec{Agent: tc.agent, Prompt: tc.prompt}); got != tc.want {
				t.Errorf("agentPrompt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeRunnerTimeout(t *testing.T) {
	fakeClaude(t, `sleep 5`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	run, err := ClaudeRunner{}.Start(ctx,
		RunSpec{Prompt: "p", SessionUUID: "u4", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("timeout should be an outcome, not a Start error: %v", err)
	}
	if !run.TimedOut {
		t.Errorf("expected TimedOut, got code=%d", run.ExitCode)
	}
}

func TestClaudeRunnerStartError(t *testing.T) {
	// An empty PATH is NO LONGER enough to make this fail, and that is the point:
	// dispatch leaves runcore.Spec.Bin nil, which now resolves through
	// internal/claudebin — PATH, then the common install dirs. Under launchd the
	// service PATH is /usr/bin:/bin:/usr/sbin:/sbin and contains no claude, which
	// is exactly how dispatch used to die with ENOENT before spending a token.
	// Force the failure hermetically instead, through the documented override, so
	// the assertion holds on a box that has claude installed and on one that
	// does not.
	t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	run, err := ClaudeRunner{}.Start(context.Background(),
		RunSpec{Prompt: "p", SessionUUID: "u5", Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("expected a Start error when the claude binary cannot be executed")
	}
	if run.ExitCode != -1 {
		t.Errorf("start-failure exit code = %d, want -1", run.ExitCode)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A dispatched executor with no --permission-mode cannot write, run or commit:
// there is no approver in a headless run, so every prompting tool call is
// auto-denied and the process still exits 0 — the board task is stamped done
// over an untouched worktree. Assert the flag reaches argv, and that the escape
// hatch drops it.
func TestClaudeRunnerPassesPermissionMode(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	got := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u-pm1"})
	for _, want := range []string{"--permission-mode", claudeflags.DefaultMode} {
		if !contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}

	t.Setenv(permEnv, "off")
	if off := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u-pm2"}); contains(off, "--permission-mode") {
		t.Errorf("args %q carry --permission-mode although %s=off", off, permEnv)
	}
}

// A playbook's permission_mode is a PER-RUN override of the site knob (phase 5).
// Set → that mode reaches argv even when the env says otherwise; the literal
// "default" → no flag at all; unset → the global knob still decides.
func TestClaudeRunnerPlaybookPermissionModeOverridesKnob(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, claudeflags.DefaultMode) // global says bypassPermissions

	got := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u-pm3", PermissionMode: "acceptEdits"})
	if !contains(got, "--permission-mode acceptEdits") {
		t.Errorf("args %q missing the playbook's acceptEdits mode", got)
	}
	if contains(got, claudeflags.DefaultMode) {
		t.Errorf("args %q still carry the global knob's mode; the playbook must win", got)
	}

	// "default" is the recipe saying "pass no flag" — distinct from an unset knob.
	if d := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u-pm4", PermissionMode: "default"}); contains(d, "--permission-mode") {
		t.Errorf("args %q carry --permission-mode although the playbook asked for 'default'", d)
	}

	// Unset → the site knob decides, exactly as before playbooks had the field.
	if u := spawnArgs(t, RunSpec{Prompt: "hello", SessionUUID: "u-pm5"}); !contains(u, "--permission-mode "+claudeflags.DefaultMode) {
		t.Errorf("args %q dropped the global knob for a recipe with no permission_mode", u)
	}
}
