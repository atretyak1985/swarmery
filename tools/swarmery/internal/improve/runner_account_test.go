package improve

// Tests for runner.go's account resolution: ClaudeRunner.Run must resolve the
// spawn's CLAUDE_CONFIG_DIR from ~/.swarmery — the SAME directory it already
// chdirs into so transcripts attribute to the "System" project (see
// internal/ingest) — rather than leave the account to whatever the daemon
// process happens to be running under. Mirrors dispatch/runner_account_test.go
// and provision/runner_account_test.go: the same plan-A3 pattern extended to
// this fifth spawn site.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// acctUnsetMarker is what the fake claude reports when CLAUDE_CONFIG_DIR is
// not in its environment AT ALL. `${VAR-default}` (no colon) distinguishes
// "unset" from "set to empty" — the "nothing broke" claim is that an unbound
// run's child has no such variable, not that it has an empty one.
const acctUnsetMarker = "__UNSET__"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's
// environment for the duration of the test. Without it these tests would
// inherit whatever the developer's shell (or a daemon under a non-default
// account) exports and report a false positive/negative — os.Environ() is
// the base every spawn site appends to.
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

// childConfigDir spawns Run through the REAL ClaudeRunner against a fake
// `claude` that reports its own CLAUDE_CONFIG_DIR into an ABSOLUTE file, not
// "$PWD/acct.txt" — when ~/.swarmery does not exist, cmd.Dir is never set, so
// the child's actual working directory is whatever the test process's
// happens to be, and the assertion must not depend on that.
//
// Observing the child rather than cmd.Env is deliberate: cmd.Env is not
// readable from the test, and the contract this phase ships is about the
// environment the spawned process actually runs with.
func childConfigDir(t *testing.T) string {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "acct.txt")
	stubClaude(t, `printf '%s\n' "${CLAUDE_CONFIG_DIR-`+acctUnsetMarker+`}" > "`+outFile+`"`)
	if _, err := (ClaudeRunner{}).Run(context.Background(), "p"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// A ~/.swarmery bound to an account lands the improve run on it.
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

	got := childConfigDir(t)
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// The "nothing broke" criterion: ~/.swarmery exists but carries no binding —
// the process environment is what it was before this feature existed.
func TestRunUnboundSystemProjectLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".swarmery"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.swarmery: %v", err)
	}

	got := childConfigDir(t)
	if got != acctUnsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// TestRunMissingSystemDirGuardsAccountResolution is the mandatory guard: when
// ~/.swarmery does not exist, cmd.Dir is never set (pre-existing behaviour,
// see isDir) and cmd.Env must stay untouched right alongside it — proven both
// directly (accountEnvFor("") is nil regardless of what reaches it) and
// behaviourally (the spawned child sees no CLAUDE_CONFIG_DIR at all,
// byte-for-byte as before this feature existed).
func TestRunMissingSystemDirGuardsAccountResolution(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home) // deliberately: no .swarmery created under it

	if _, err := os.Stat(filepath.Join(home, ".swarmery")); !os.IsNotExist(err) {
		t.Fatalf("precondition: ~/.swarmery must not exist (stat err = %v)", err)
	}

	base := []string{"PATH=/usr/bin"}
	if got := claudeacct.SpawnEnvFor(base, ""); len(got) != 1 || got[0] != base[0] {
		t.Errorf("SpawnEnvFor(base, \"\") = %v, want base untouched — it must never resolve a binding for an empty project path", got)
	}

	got := childConfigDir(t)
	if got != acctUnsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent — a missing ~/.swarmery must not resolve an account", got)
	}
}

// The byte-for-byte half of the "nothing broke" criterion: pins the exact
// expression the spawn site uses for an existing-but-unbound project, which
// the child test cannot show — a shell adds PWD/SHLVL/_ of its own, so the
// full environment can only be compared here.
func TestAccountEnvForUnboundDirIsByteIdenticalToOsEnviron(t *testing.T) {
	dir := t.TempDir() // exists, but unbound
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
