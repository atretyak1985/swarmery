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

// TestDaemonDenyPatterns pins the exact rules: three clients × two loopback
// spellings, the port taken from the argument, and the zero value falling back to
// the daemon's default. The space form, never `curl:*…` — mid-rule the colon is a
// literal and the rule would match nothing.
func TestDaemonDenyPatterns(t *testing.T) {
	for _, tc := range []struct {
		port int
		want []string
	}{
		{7777, []string{
			"Bash(curl *127.0.0.1:7777*)", "Bash(curl *localhost:7777*)",
			"Bash(wget *127.0.0.1:7777*)", "Bash(wget *localhost:7777*)",
			"Bash(http *127.0.0.1:7777*)", "Bash(http *localhost:7777*)",
		}},
		{8080, []string{
			"Bash(curl *127.0.0.1:8080*)", "Bash(curl *localhost:8080*)",
			"Bash(wget *127.0.0.1:8080*)", "Bash(wget *localhost:8080*)",
			"Bash(http *127.0.0.1:8080*)", "Bash(http *localhost:8080*)",
		}},
	} {
		if got := DaemonDenyPatterns(tc.port); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("DaemonDenyPatterns(%d) = %q, want %q", tc.port, got, tc.want)
		}
	}
	if got, want := strings.Join(DaemonDenyPatterns(0), "|"), strings.Join(DaemonDenyPatterns(DefaultDaemonPort), "|"); got != want {
		t.Errorf("DaemonDenyPatterns(0) = %q, want the default port's %q", got, want)
	}
}

// TestToolDenyArgsDaemonDeny pins the full flag a verifier (port from config) and a
// reviewer (Bash on top) get: the read-only set first, then the daemon rules, in
// ONE --disallowedTools value.
func TestToolDenyArgsDaemonDeny(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"verifier on 7777", DaemonDenyPatterns(7777),
			"--disallowedTools Edit,Write,MultiEdit,NotebookEdit," +
				"Bash(curl *127.0.0.1:7777*),Bash(curl *localhost:7777*)," +
				"Bash(wget *127.0.0.1:7777*),Bash(wget *localhost:7777*)," +
				"Bash(http *127.0.0.1:7777*),Bash(http *localhost:7777*)"},
		{"reviewer on 8080", append([]string{"Bash"}, DaemonDenyPatterns(8080)...),
			"--disallowedTools Edit,Write,MultiEdit,NotebookEdit,Bash," +
				"Bash(curl *127.0.0.1:8080*),Bash(curl *localhost:8080*)," +
				"Bash(wget *127.0.0.1:8080*),Bash(wget *localhost:8080*)," +
				"Bash(http *127.0.0.1:8080*),Bash(http *localhost:8080*)"},
	} {
		args := ToolDenyArgs(tc.extra)
		if len(args) != 2 {
			t.Fatalf("%s: ToolDenyArgs = %q, want exactly the flag and one value", tc.name, args)
		}
		if got := strings.Join(args, " "); got != tc.want {
			t.Errorf("%s: ToolDenyArgs = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestClaudeRunnerDaemonDenyIsOneArgvElement: the spaces inside the rules do not
// split the value on the real spawn path — the stand-in claude prints one argv
// element per line, and the whole list must arrive as the single element after
// the flag.
func TestClaudeRunnerDaemonDenyIsOneArgvElement(t *testing.T) {
	clearPermissionKnobs(t)
	fakeClaudeRunner(t, `for a in "$@"; do printf '%s\n' "$a"; done > "$PWD/args.txt"; exit 0`)
	cwd := t.TempDir()
	if _, err := (ClaudeRunner{}).Run(context.Background(), RunSpec{
		Prompt: "verify", SessionUUID: "u3", Cwd: cwd, DisallowedTools: DaemonDenyPatterns(8080),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := strings.Split(readArgs(t, cwd), "\n")
	if n := len(argv); n < 2 || argv[n-2] != "--disallowedTools" {
		t.Fatalf("argv does not end with --disallowedTools <list>: %q", argv)
	}
	want := strings.Join(ToolDenyArgs(DaemonDenyPatterns(8080))[1:], "")
	if got := argv[len(argv)-1]; got != want {
		t.Errorf("--disallowedTools value = %q, want %q", got, want)
	}
}
