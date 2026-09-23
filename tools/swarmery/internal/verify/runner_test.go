package verify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeClaudeRunner writes a shell script named `claude` into a temp dir and
// prepends it to PATH so ClaudeRunner.Run spawns IT instead of the real binary.
// Mirrors the dispatch package's arg-assertion shim.
func fakeClaudeRunner(t *testing.T, body string) {
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

// TestClaudeRunnerArgs asserts the built claude arg list carries the headless
// slimming flag (--setting-sources project,local) alongside the model override.
func TestClaudeRunnerArgs(t *testing.T) {
	// Echo the args into the run cwd so we can assert on them. Exit 0.
	fakeClaudeRunner(t, `echo "$@" > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()
	_, err := ClaudeRunner{}.Run(context.Background(),
		RunSpec{Prompt: "hello", SessionUUID: "u1", Cwd: cwd, Model: "opus"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	// Exact argv, not a contains-check per flag: since the extraction to
	// internal/runcore the ORDER comes from a builder five engines share, and
	// `claude` is order-insensitive, so an accidental reordering here would be
	// invisible to everything except an assertion like this one. Mirrors the
	// verify/model case in internal/runcore/spawner_test.go.
	got := strings.TrimSpace(string(out))
	want := "-p hello --session-id u1 --setting-sources project,local --model opus --effort " + DefaultEffort
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
