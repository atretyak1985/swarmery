package phaserun

// Tests for runner.go's account resolution: ClaudeRunner.Start must resolve
// the spawn's CLAUDE_CONFIG_DIR from spec.ProjectPath, never from spec.Cwd —
// the acquired worktree, which carries no .claude/settings.local.json of its
// own. Mirrors dispatch/runner_account_test.go and planrun's twin: the same
// plan-A3 pattern extended to this spawn site.
//
// fakeClaudeAcct is named distinctly from runner_test.go's fakeClaude (same
// package, different signature: that one asserts argv, this one reports
// CLAUDE_CONFIG_DIR) to avoid a redeclaration.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// unsetMarker is what the fake claude prints when CLAUDE_CONFIG_DIR is not in
// its environment AT ALL. `${VAR-default}` (no colon) distinguishes "unset"
// from "set to empty", which matters here: the whole "nothing broke" claim is
// that an unbound project's child has no such variable, not that it has an
// empty one.
const unsetMarker = "__UNSET__"

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

// fakeClaudeAcct installs a fake `claude` binary via SWARMERY_CLAUDE_BIN
// (planning.ClaudeBin's override, honored ahead of the PATH/common-locations
// fallback) that reports its own CLAUDE_CONFIG_DIR into outFile — an ABSOLUTE
// path rather than "$PWD/acct.txt", so the assertion never depends on Cwd.
func fakeClaudeAcct(t *testing.T, outFile string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fakeclaude-acct.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"${CLAUDE_CONFIG_DIR-" + unsetMarker + "}\" > \"" + outFile + "\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
}

// childConfigDir spawns the run through the REAL ClaudeRunner.Start and
// returns what the CHILD saw (unsetMarker when the variable is absent).
//
// Observing the child rather than cmd.Env is deliberate: cmd.Env is not
// readable from the test, and the contract this phase ships is about the
// environment the spawned process actually runs with.
func childConfigDir(t *testing.T, spec RunSpec) string {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "acct.txt")
	fakeClaudeAcct(t, outFile)
	if spec.Cwd == "" {
		spec.Cwd = t.TempDir()
	}
	if spec.SessionUUID == "" {
		spec.SessionUUID = "acct-test"
	}
	if spec.Prompt == "" {
		spec.Prompt = "p"
	}
	if _, err := (ClaudeRunner{Timeout: 30 * time.Second}).Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// Direct test of trap A3, extended to phase runs: Cwd is a worktree with NO
// .claude/settings.local.json (asserted below as a precondition, so this test
// fails loudly if that ever stops being true) — yet the account still reaches
// the child, because it travels via spec.ProjectPath, set by the service from
// phaseInfo.ProjectPath (service.go), not resolved from Cwd.
func TestStartAppliesAccountWhenCwdHasNoProjectSettings(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	worktree := t.TempDir()

	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	if env := claudeacct.EnvFor(worktree); env != nil {
		t.Fatalf("precondition: EnvFor(worktree) = %v, want nil — the trap this test guards is gone", env)
	}

	got := childConfigDir(t, RunSpec{Cwd: worktree, ProjectPath: project})
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q — the account was resolved from cwd, not from ProjectPath", got, want)
	}
}

// The "nothing broke" criterion: a phase whose project has no account binding
// spawns with no CLAUDE_CONFIG_DIR — the process environment is what it was
// before this feature existed.
func TestStartUnboundProjectLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, RunSpec{Cwd: t.TempDir(), ProjectPath: t.TempDir()})
	if got != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// TestStartEmptyProjectPathGuardsAccountResolution is the mandatory guard: an
// empty spec.ProjectPath (no known project path — the same ErrNoPath gate
// that Start's admission checks enforce upstream) must short-circuit to nil
// BEFORE claudeacct.EnvFor is ever called. claudeacct.Binding joins its
// argument with ".claude/settings.local.json" unconditionally, so
// EnvFor("") would resolve that RELATIVE path against the daemon's OWN
// process working directory and read whatever unrelated settings file
// happens to sit there — proven here by making that relative path a real,
// bound settings file (mirrors term_account_test.go's
// TestTermAccountEnvGuardsEmptyProjectPath) and confirming the guard holds
// regardless.
func TestStartEmptyProjectPathGuardsAccountResolution(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())

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

	// The guard now lives in runcore.AccountFor (one copy for the phase and plan
	// spawn sites): an empty path yields the ZERO Resolution, and the env it
	// composes is the base, untouched.
	if got := runcore.AccountFor(""); got != (claudeacct.Resolution{}) {
		t.Errorf("AccountFor(\"\") = %+v, want the zero Resolution — an empty project path must never reach a binding file", got)
	}
	if base := []string{"PATH=/usr/bin"}; len(claudeacct.SpawnEnvResolved(base, runcore.AccountFor(""))) != 1 {
		t.Errorf("an empty project path composed an env delta")
	}

	got := childConfigDir(t, RunSpec{Cwd: t.TempDir(), ProjectPath: ""})
	if got != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent — an empty ProjectPath must not resolve an account", got)
	}
}

// The byte-for-byte half of the "nothing broke" criterion. The child tests
// above prove the observable consequence (no variable); this pins the exact
// expression the spawn site uses, which the child cannot show — a shell adds
// PWD/SHLVL/_ of its own, so the full environment can only be compared here.
func TestUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
	base := os.Environ()
	got := claudeacct.SpawnEnvResolved(os.Environ(), runcore.AccountFor(t.TempDir())) // the spawn line, verbatim; an unbound project adds nothing
	if len(got) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(got), len(base))
	}
	for i := range base {
		if got[i] != base[i] {
			t.Errorf("env[%d] = %s=…, want %s=… (values withheld: this is the real environment)", i, strings.SplitN(got[i], "=", 2)[0], strings.SplitN(base[i], "=", 2)[0])
		}
	}
}
