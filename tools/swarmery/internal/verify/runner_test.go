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
	// The permission mode resolves from env; pin it to the code default so an
	// operator's own knob cannot change the argv this test pins.
	clearPermissionKnobs(t)
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
	want := "-p hello --session-id u1 --setting-sources project,local --permission-mode bypassPermissions --model opus --effort " +
		DefaultEffort + " --disallowedTools Edit,Write,MultiEdit,NotebookEdit"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// TestClaudeRunnerMergesDisallowedTools: a caller's extra denials join the
// read-only set in ONE --disallowedTools flag, still last on the argv. Two flags
// would leave it to the CLI whether the lists merge or the last one wins.
func TestClaudeRunnerMergesDisallowedTools(t *testing.T) {
	clearPermissionKnobs(t)
	fakeClaudeRunner(t, `echo "$@" > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()
	_, err := ClaudeRunner{}.Run(context.Background(), RunSpec{
		Prompt: "review", SessionUUID: "u2", Cwd: cwd,
		DisallowedTools: []string{"Bash", "Edit", " "},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := readArgs(t, cwd)
	if n := strings.Count(argv, "--disallowedTools"); n != 1 {
		t.Fatalf("argv %q carries %d --disallowedTools flags, want exactly 1", argv, n)
	}
	if !strings.HasSuffix(argv, "--disallowedTools Edit,Write,MultiEdit,NotebookEdit,Bash") {
		t.Errorf("argv %q does not end with the merged, de-duplicated denial list", argv)
	}
}

func TestToolDenyArgs(t *testing.T) {
	if got := strings.Join(ToolDenyArgs(nil), " "); got != "--disallowedTools Edit,Write,MultiEdit,NotebookEdit" {
		t.Errorf("ToolDenyArgs(nil) = %q", got)
	}
	if got := strings.Join(ToolDenyArgs([]string{"Bash"}), " "); got != "--disallowedTools Edit,Write,MultiEdit,NotebookEdit,Bash" {
		t.Errorf("ToolDenyArgs(Bash) = %q", got)
	}
}
