package handoff

// Wiring test for handoff's spawn: ClaudeRunner.Run must resolve the run's
// account from ~/.swarmery — the same directory it already chdirs into so the
// transcript attributes to the "System" project (internal/ingest) — instead of
// leaving it to whatever account the daemon process happens to run under.
//
// This runner was one of the three left behind when claudeacct.SpawnEnvFor
// became the single spawn-env composition, so the assertion here is the
// regression: before the fix the child saw no CLAUDE_CONFIG_DIR at all even
// with ~/.swarmery bound. Attach's full behaviour is pinned in
// internal/systemspawn; these two cases prove only that this site calls it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// acctUnsetMarker is what the stub reports when CLAUDE_CONFIG_DIR is not in its
// environment AT ALL. `${VAR-default}` (no colon) distinguishes "unset" from
// "set to empty".
const acctUnsetMarker = "__UNSET__"

// unsetConfigDir drops CLAUDE_CONFIG_DIR from the TEST process's environment:
// os.Environ() is the base the spawn composes onto, so without this the result
// would depend on the developer's shell.
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

// childConfigDir runs the REAL ClaudeRunner against a `claude` stub that
// reports its own CLAUDE_CONFIG_DIR into an ABSOLUTE path — when ~/.swarmery
// is missing the child's cwd is the test process's, so the output file must
// not be relative. Observing the child rather than cmd.Env is deliberate: the
// contract is about the environment the spawned process actually runs with.
func childConfigDir(t *testing.T) string {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "acct.txt")
	stubDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"${CLAUDE_CONFIG_DIR-" + acctUnsetMarker + "}\" > \"" + outFile + "\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := (ClaudeRunner{}).Run(context.Background(), "p"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// A bound ~/.swarmery lands this runner on that account.
func TestRunBoundSystemProjectSetsConfigDir(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".swarmery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir ~/.swarmery: %v", err)
	}
	if err := claudeacct.SetBinding(dir, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	if got, want := childConfigDir(t), filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// The "nothing broke" half: no ~/.swarmery means no account resolution, and
// the child's environment is what it was before the feature existed.
func TestRunMissingSystemDirLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home) // deliberately: no .swarmery created under it

	if got := childConfigDir(t); got != acctUnsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}
