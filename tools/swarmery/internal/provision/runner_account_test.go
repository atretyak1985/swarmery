package provision

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// unsetMarker is what the fake claude prints when CLAUDE_CONFIG_DIR is not in
// its environment AT ALL — `${VAR-default}` (no colon) separates "unset" from
// "set to empty".
const unsetMarker = "__UNSET__"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// so a developer shell cannot leak into os.Environ() and skew the result.
func unsetConfigDir(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("CLAUDE_CONFIG_DIR")
	if !had {
		return
	}
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatalf("unset CLAUDE_CONFIG_DIR: %v", err)
	}
	t.Cleanup(func() { os.Setenv("CLAUDE_CONFIG_DIR", prev) })
}

// fakeClaude puts a shell script named `claude` first on PATH so ClaudeRunner
// spawns IT (this runner resolves the binary with a plain PATH lookup). The
// script reports its own CLAUDE_CONFIG_DIR on stdout, which Claude returns
// trimmed — so the child's view is the function's return value.
func fakeClaude(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"${CLAUDE_CONFIG_DIR-" + unsetMarker + "}\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A provision run's dir IS the project path, so resolving the account from it is
// correct — a bound project's generator run lands on that account's config dir.
func TestProvisionResolvesAccountFromDir(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	fakeClaude(t)

	out, err := ClaudeRunner{}.Claude(context.Background(), project, "", "--version")
	if err != nil {
		t.Fatalf("Claude: %v", err)
	}
	if want := filepath.Join(home, ".claude-nabu-org"); strings.TrimSpace(out) != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", out, want)
	}
}

// The same resolve on an UNBOUND project must add nothing.
func TestProvisionUnboundProjectLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t)

	out, err := ClaudeRunner{}.Claude(context.Background(), t.TempDir(), "", "--version")
	if err != nil {
		t.Fatalf("Claude: %v", err)
	}
	if strings.TrimSpace(out) != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", out)
	}
}

// dir=="" names no project (it means "inherit the daemon cwd"), so no binding is
// resolved — even when the daemon's own cwd happens to carry a settings file.
// Resolving there would bind a provision run to whatever project the daemon sits
// in, which is never what the caller asked for.
func TestProvisionEmptyDirResolvesNoAccount(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t)

	// Make the daemon's cwd a bound project for the duration of the call: if the
	// runner resolved a relative .claude/settings.local.json, this is what it
	// would pick up.
	bound := t.TempDir()
	if err := claudeacct.SetBinding(bound, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })

	out, err := ClaudeRunner{}.Claude(context.Background(), "", "", "--version")
	if err != nil {
		t.Fatalf("Claude: %v", err)
	}
	if strings.TrimSpace(out) != unsetMarker {
		t.Errorf("dir==\"\" resolved CLAUDE_CONFIG_DIR=%q from the daemon cwd, want no variable at all", out)
	}
}

// The byte-for-byte half: for an unbound dir the provision spawn expression is an
// exact copy of os.Environ().
func TestProvisionUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	base := os.Environ()
	got := claudeacct.SpawnEnvFor(os.Environ(), dir) // the spawn line, verbatim
	if len(got) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(got), len(base))
	}
	for i := range base {
		if got[i] != base[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], base[i])
		}
	}
}
