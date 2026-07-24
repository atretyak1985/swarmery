package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func TestClaudeRunnerModelFlag(t *testing.T) {
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
	got := string(out)
	for _, want := range []string{"-p", "hello", "--session-id", "u3", "--model", "sonnet"} {
		if !contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
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
	// Point PATH at an empty dir so `claude` cannot be resolved → Start error.
	t.Setenv("PATH", t.TempDir())
	run, err := ClaudeRunner{}.Start(context.Background(),
		RunSpec{Prompt: "p", SessionUUID: "u5", Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("expected a Start error when claude is absent from PATH")
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
