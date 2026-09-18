package routines

// Tests for runner.go's account resolution: ClaudeRunner.Run must resolve the
// spawn's CLAUDE_CONFIG_DIR from the routine's project path, not leave it to
// whatever the daemon process happens to be running under. Mirrors
// dispatch/runner_account_test.go and provision/runner_account_test.go — the
// same plan-A3 pattern extended to this fourth spawn site.
//
// Unlike dispatch/verify/planrun/phaserun, cwd here already IS the project
// path (Service.projectPath in store.go resolves projects.path directly, not
// a worktree), so there is no separate "spec.Account" field to carry the
// resolution around a worktree that lacks its own settings file — the
// runner just resolves straight off its own cwd argument, the same shape
// internal/provision uses.

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
// "unset" from "set to empty", which matters here: the "nothing broke" claim
// is that an unbound run's child has no such variable, not that it has an
// empty one.
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
// "$PWD/acct.txt" — cwd is "" for the global-routine case, so the child's
// actual working directory is whatever the test process's happens to be, and
// the assertion must not depend on that.
//
// Observing the child rather than cmd.Env is deliberate: cmd.Env is not
// readable from the test, and the contract this phase ships is about the
// environment the spawned process actually runs with.
func childConfigDir(t *testing.T, cwd string) string {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "acct.txt")
	fakeClaudeRunner(t, `printf '%s\n' "${CLAUDE_CONFIG_DIR-`+acctUnsetMarker+`}" > "`+outFile+`"`)
	if _, err := (ClaudeRunner{}).Run(context.Background(), cwd, "p", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// A project-scoped routine's run lands on that project's bound account.
func TestRunBoundProjectSetsConfigDir(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	got := childConfigDir(t, project)
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// The "nothing broke" criterion: a project-scoped routine whose project has
// no account binding spawns with no CLAUDE_CONFIG_DIR — the process
// environment is what it was before this feature existed.
func TestRunUnboundProjectLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, t.TempDir())
	if got != acctUnsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// TestRunGlobalRoutineEmptyCwdGuardsAccountResolution is the mandatory guard:
// a global routine (routines.ProjectID NULL) resolves projectPath="" (see
// Service.projectPath), and accountEnvFor must short-circuit that to nil
// BEFORE claudeacct.EnvFor is ever called. claudeacct.Binding joins its
// argument with ".claude/settings.local.json" unconditionally, so
// EnvFor("") would resolve that RELATIVE path against the daemon's OWN
// process working directory and read whatever unrelated settings file
// happens to sit there — proven here by making that relative path a real,
// bound settings file (mirrors term_account_test.go's
// TestTermAccountEnvGuardsEmptyProjectPath) and confirming the guard still
// holds.
func TestRunGlobalRoutineEmptyCwdGuardsAccountResolution(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())

	// Non-vacuity: make the daemon's OWN process cwd a bound project for the
	// duration of the call. If accountEnvFor ever called claudeacct.EnvFor("")
	// instead of short-circuiting, THIS is what it would pick up.
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

	// Precondition: the trap this test guards against is real.
	if env := claudeacct.EnvFor(""); env == nil {
		t.Fatalf("precondition: EnvFor(\"\") = nil — the relative-path trap this test guards is gone")
	}

	base := []string{"PATH=/usr/bin"}
	if got := claudeacct.SpawnEnvFor(base, ""); len(got) != 1 || got[0] != base[0] {
		t.Errorf("SpawnEnvFor(base, \"\") = %v, want base untouched — it must never resolve a binding for an empty project path", got)
	}

	got := childConfigDir(t, "") // the global-routine cwd
	if got != acctUnsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent — a global routine (cwd=\"\") must not resolve an account", got)
	}
}

// The byte-for-byte half of the "nothing broke" criterion. The child test
// above proves the observable consequence (no variable); this pins the exact
// expression the spawn site uses, which the child cannot show — a shell adds
// PWD/SHLVL/_ of its own, so the full environment can only be compared here.
func TestRunUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
	base := os.Environ()
	got := claudeacct.SpawnEnvFor(os.Environ(), t.TempDir()) // the spawn line, verbatim
	if len(got) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(got), len(base))
	}
	for i := range base {
		if got[i] != base[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], base[i])
		}
	}
}
