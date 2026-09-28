package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

// unsetMarker is what the fake claude prints when CLAUDE_CONFIG_DIR is not in
// its environment AT ALL. `${VAR-default}` (no colon) distinguishes "unset" from
// "set to empty" — the "nothing broke" claim is that the variable is absent, not
// that it is empty.
const unsetMarker = "__UNSET__"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// for the duration of the test, so a developer shell (or a daemon running under
// a non-default account) cannot leak into os.Environ() and skew the result.
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

// childConfigDir runs the REAL ClaudeRunner against a fake `claude` that reports
// its own CLAUDE_CONFIG_DIR on stdout, and returns what the CHILD saw
// (unsetMarker when the variable is absent). The verifier captures stdout, so
// unlike dispatch this needs no scratch file.
func childConfigDir(t *testing.T, spec RunSpec, cwd string) string {
	t.Helper()
	fakeClaudeRunner(t, `printf '%s\n' "${CLAUDE_CONFIG_DIR-`+unsetMarker+`}"; exit 0`)
	spec.Cwd = cwd
	run, err := ClaudeRunner{Timeout: 30 * time.Second}.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return strings.TrimSpace(run.Output)
}

// The "nothing broke" criterion for verification: a task whose project has no
// account binding spawns with no CLAUDE_CONFIG_DIR.
func TestVerifySpawnUnboundAccountLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-unbound"}, t.TempDir())
	if got != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// "No binding ≡ bound to default" — keyed on the account KEY, never on an empty
// ConfigDir (claudeacct.Account.ConfigDir IS populated for the default account).
func TestVerifySpawnDefaultAccountKeyProducesNoDelta(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-default", Resolution: claudeacct.Resolution{Account: "default"}}, t.TempDir())
	if got != unsetMarker {
		t.Errorf("default account produced CLAUDE_CONFIG_DIR=%q, want no variable at all", got)
	}
}

// A bound project's verifier run reaches the CLI with the account's config dir.
func TestVerifySpawnBoundAccountSetsConfigDir(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := childConfigDir(t, RunSpec{Prompt: "p", SessionUUID: "acct-bound", Resolution: claudeacct.Resolution{Account: "nabu-org"}}, t.TempDir())
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// Direct test of trap A3 on the verify side: Cwd is a worktree with NO
// .claude/settings.local.json (asserted as a precondition), yet the account still
// reaches the child because it travels in the spec.
func TestVerifySpawnAppliesAccountWhenCwdHasNoProjectSettings(t *testing.T) {
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

// runWithVerdictHook runs one spec through the REAL ClaudeRunner.Run with an
// AccountVerdict hook recording what it was called with.
func runWithVerdictHook(t *testing.T, spec RunSpec) (run *Run, calls int, account string, result claudeprobe.Result) {
	t.Helper()
	r := ClaudeRunner{Timeout: 30 * time.Second, AccountVerdict: func(a string, res claudeprobe.Result) {
		calls++
		account, result = a, res
	}}
	spec.Cwd = t.TempDir()
	run, err := r.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return run, calls, account, result
}

// SC-8, verify half: a verifier whose `claude` dies demanding a login reaches
// the hook as no-login for the run's account — no extra claude invocation, the
// verdict is read off the run this service was going to make anyway.
func TestVerifyAccountVerdictHookNoLoginExit(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaudeRunner(t, `echo 'Not logged in · Please run /login'; exit 1`)
	run, calls, account, result := runWithVerdictHook(t,
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

// An ordinary verifier failure classifies unknown — a status the store adapter
// drops, so a failed verification can never demote a working account.
func TestVerifyAccountVerdictHookOrdinaryFailure(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaudeRunner(t, `echo "verifier blew up" 1>&2; exit 3`)
	_, calls, _, result := runWithVerdictHook(t,
		RunSpec{Prompt: "p", SessionUUID: "verdict-fail", Resolution: claudeacct.Resolution{Account: "nabu-org"}})
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	if result.Status != claudeprobe.StatusUnknown {
		t.Errorf("hook status = %q, want %q", result.Status, claudeprobe.StatusUnknown)
	}
}

// A nil AccountVerdict hook leaves run behaviour byte-identical: the same
// failing spec produces the same Run whether the hook exists or not (Duration
// aside — it is wall clock).
func TestVerifyNilAccountVerdictHookLeavesRunUnchanged(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	fakeClaudeRunner(t, `echo 'Not logged in · Please run /login'; echo "boom" 1>&2; exit 1`)
	spec := RunSpec{Prompt: "p", SessionUUID: "verdict-nil", Resolution: claudeacct.Resolution{Account: "nabu-org"}}

	spec.Cwd = t.TempDir()
	bare, err := ClaudeRunner{Timeout: 30 * time.Second}.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run (nil hook): %v", err)
	}
	hooked, _, _, _ := runWithVerdictHook(t, spec)

	bare.Duration, hooked.Duration = 0, 0
	if *bare != *hooked {
		t.Errorf("nil-hook run %+v differs from hooked run %+v", *bare, *hooked)
	}
}

// The byte-for-byte half of the "nothing broke" criterion: the exact expression
// the spawn site uses adds nothing for an unbound project.
func TestVerifyUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
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
