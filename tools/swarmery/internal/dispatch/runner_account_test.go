package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

// unsetMarker is what the fake claude prints when CLAUDE_CONFIG_DIR is not in
// its environment AT ALL. `${VAR-default}` (no colon) distinguishes "unset" from
// "set to empty", which matters here: the whole "nothing broke" claim is that an
// unbound project's child has no such variable, not that it has an empty one.
const unsetMarker = "__UNSET__"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// for the duration of the test. Without it these tests would inherit whatever
// the developer's shell (or a daemon under a non-default account) exports and
// report a false positive/negative — os.Environ() is the base every spawn site
// appends to.
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

// childConfigDir spawns the run through the REAL ClaudeRunner.Start against a
// fake `claude` that reports its own CLAUDE_CONFIG_DIR into cwd, and returns
// what the CHILD saw (unsetMarker when the variable is absent).
//
// Observing the child rather than cmd.Env is deliberate: cmd.Env is not readable
// from the test, and the contract this phase ships is about the environment the
// spawned process actually runs with.
func childConfigDir(t *testing.T, spec RunSpec, cwd string) string {
	t.Helper()
	fakeClaude(t, `printf '%s\n' "${CLAUDE_CONFIG_DIR-`+unsetMarker+`}" > "$PWD/acct.txt"; exit 0`)
	spec.Cwd = cwd
	if _, err := (ClaudeRunner{}).Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cwd, "acct.txt"))
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// The "nothing broke" criterion: a task whose project has no account binding
// spawns with no CLAUDE_CONFIG_DIR — the process environment is what it was
// before this feature existed.
func TestSpawnUnboundAccountLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-unbound"}, t.TempDir())
	if got != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// "No binding ≡ bound to default" — decided by the KEY being the default one,
// never by an empty ConfigDir (claudeacct.Account.ConfigDir IS populated for the
// default account, unlike usage.Source.ConfigDir).
func TestSpawnDefaultAccountKeyProducesNoDelta(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-default", Resolution: claudeacct.Resolution{Account: "default"}}, t.TempDir())
	if got != unsetMarker {
		t.Errorf("default account produced CLAUDE_CONFIG_DIR=%q, want no variable at all", got)
	}
}

// A bound project's run reaches the CLI with the account's config dir.
func TestSpawnBoundAccountSetsConfigDir(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-bound", Resolution: claudeacct.Resolution{Account: "nabu-org"}}, t.TempDir())
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// Direct test of trap A3: Cwd is a worktree with NO .claude/settings.local.json,
// so a cwd-side resolve yields nothing (asserted as a precondition, so this test
// fails loudly if that ever stops being true) — yet the account still reaches the
// child, because it travels in the spec instead.
func TestSpawnAppliesAccountWhenCwdHasNoProjectSettings(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	worktree := t.TempDir()

	if _, err := os.Stat(filepath.Join(worktree, claudeacct.BindingFile)); !os.IsNotExist(err) {
		t.Fatalf("precondition: worktree must carry no %s (stat err = %v)", claudeacct.BindingFile, err)
	}
	if env := claudeacct.EnvFor(worktree); env != nil {
		t.Fatalf("precondition: EnvFor(worktree) = %v, want nil — the trap this test guards is gone", env)
	}

	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-worktree", Resolution: claudeacct.Resolution{Account: "nabu-org"}}, worktree)
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("worktree run CLAUDE_CONFIG_DIR = %q, want %q — the account was resolved from cwd, not from the spec", got, want)
	}
}

// startWithVerdictHook runs one spec through the REAL ClaudeRunner.Start with
// an AccountVerdict hook recording what it was called with. Returns the run
// plus the hook's observations (calls == 0 → account/result are zero values).
func startWithVerdictHook(t *testing.T, spec RunSpec) (run *Run, calls int, account string, result claudeprobe.Result) {
	t.Helper()
	r := ClaudeRunner{AccountVerdict: func(a string, res claudeprobe.Result) {
		calls++
		account, result = a, res
	}}
	spec.Cwd = t.TempDir()
	run, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run, calls, account, result
}

// SC-8, dispatch half: a run whose `claude` dies demanding a login (the
// recorded plain-run line on STDOUT — the stream this runner otherwise
// discards) reaches the hook as no-login for the run's account, with no extra
// claude invocation (the fake would have logged a second run into cwd).
func TestAccountVerdictHookNoLoginExit(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t, `echo 'Not logged in · Please run /login'; exit 1`)
	run, calls, account, result := startWithVerdictHook(t,
		RunSpec{Prompt: "p", SessionUUID: "verdict-nl", Resolution: claudeacct.Resolution{Account: "nabu-org"}})
	if run.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", run.ExitCode)
	}
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	if account != "nabu-org" {
		t.Errorf("hook account = %q, want nabu-org", account)
	}
	if result.Status != claudeprobe.StatusNoLogin {
		t.Errorf("hook status = %q, want %q", result.Status, claudeprobe.StatusNoLogin)
	}
}

// An ordinary task failure classifies unknown — a status the store adapter
// drops, so a broken task can never demote a working account.
func TestAccountVerdictHookOrdinaryFailure(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t, `echo "task blew up" 1>&2; exit 3`)
	_, calls, _, result := startWithVerdictHook(t,
		RunSpec{Prompt: "p", SessionUUID: "verdict-fail", Resolution: claudeacct.Resolution{Account: "nabu-org"}})
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	if result.Status != claudeprobe.StatusUnknown {
		t.Errorf("hook status = %q, want %q", result.Status, claudeprobe.StatusUnknown)
	}
}

// A clean exit classifies ready (the adapter decides whether that clears a
// stored no-login).
func TestAccountVerdictHookCleanExit(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t, `exit 0`)
	_, calls, account, result := startWithVerdictHook(t,
		RunSpec{Prompt: "p", SessionUUID: "verdict-ok", Resolution: claudeacct.Resolution{Account: "nabu-org"}})
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	if account != "nabu-org" {
		t.Errorf("hook account = %q, want nabu-org", account)
	}
	if result.Status != claudeprobe.StatusReady {
		t.Errorf("hook status = %q, want %q", result.Status, claudeprobe.StatusReady)
	}
}

// A nil AccountVerdict hook leaves run behaviour byte-identical: the same
// failing spec produces the same Run whether the hook exists or not (Duration
// aside — it is wall clock), and the hook-less path never observes stdout.
func TestNilAccountVerdictHookLeavesRunUnchanged(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaude(t, `echo 'Not logged in · Please run /login'; echo "boom" 1>&2; exit 1`)
	spec := RunSpec{Prompt: "p", SessionUUID: "verdict-nil", Resolution: claudeacct.Resolution{Account: "nabu-org"}}

	spec.Cwd = t.TempDir()
	bare, err := ClaudeRunner{}.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start (nil hook): %v", err)
	}
	hooked, _, _, _ := startWithVerdictHook(t, spec)

	bare.Duration, hooked.Duration = 0, 0
	if *bare != *hooked {
		t.Errorf("nil-hook run %+v differs from hooked run %+v", *bare, *hooked)
	}
}

// The byte-for-byte half of the "nothing broke" criterion. The child test above
// proves the observable consequence (no variable); this pins the exact expression
// the spawn site uses, which the child cannot show — a shell adds PWD/SHLVL/_ of
// its own, so the full environment can only be compared here.
func TestUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
	base := os.Environ()
	got := append(os.Environ(), claudeacct.EnvForAccount("")...) // the spawn line, verbatim
	if len(got) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(got), len(base))
	}
	for i := range base {
		if got[i] != base[i] {
			t.Errorf("env[%d] = %s=…, want %s=… (values withheld: this is the real environment)", i, strings.SplitN(got[i], "=", 2)[0], strings.SplitN(base[i], "=", 2)[0])
		}
	}
}
