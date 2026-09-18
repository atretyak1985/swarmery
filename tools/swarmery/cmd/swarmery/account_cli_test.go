package main

// Contract tests for `swarmery account`. cmd/swarmery is excluded from the
// coverage gate precisely because logic does not belong here — what IS pinned
// below is the handful of OUTPUT contracts other software depends on:
//
//   - `account env` prints exactly zero or one line (both shell surfaces in
//     plugins/accounts-pack consume its stdout);
//   - `account list` prints five fields and no credential material;
//   - `account use` refuses an account that is not installed;
//   - `account exec` wins over a CLAUDE_CONFIG_DIR the caller's shell exported.
//
// Every test points $HOME at a t.TempDir() so discovery never reads the
// operator's real ~/.claude — the same reason internal/claudeacct keeps a
// userHomeDir seam.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// fakeHome points $HOME at a temp dir holding the named accounts' config dirs
// (an account is discovered by its projects/ tree) and returns it.
func fakeHome(t *testing.T, accounts ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range accounts {
		dir := ".claude"
		if key != "default" {
			dir = ".claude-" + key
		}
		if err := os.MkdirAll(filepath.Join(home, dir, "projects"), 0o700); err != nil {
			t.Fatalf("seed account %q: %v", key, err)
		}
	}
	return home
}

// project creates a project dir, optionally bound to an account.
func project(t *testing.T, account string) string {
	t.Helper()
	dir := t.TempDir()
	if account != "" {
		if err := claudeacct.SetBinding(dir, account); err != nil {
			t.Fatalf("SetBinding(%q): %v", account, err)
		}
	}
	return dir
}

// `account env` is the contract `eval`/`env "$(...)"` rests on: zero or one
// line, and NOTHING else on stdout. A second line, a header, or a friendly
// "(none)" would be evaluated by the caller's shell.
func TestAccountEnvPrintsZeroOrOneLine(t *testing.T) {
	home := fakeHome(t, "default", "work")

	cases := []struct {
		name    string
		account string
		want    string // "" ⇒ no output at all
	}{
		// An unbound project runs under the default account, which is env-LESS
		// by design — so the honest delta is empty, not a variable.
		{name: "unbound", account: "", want: ""},
		// Bound to the default is byte-identical to unbound, deliberately.
		{name: "bound to default", account: "default", want: ""},
		{name: "bound to a second account", account: "work",
			want: "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-work")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := accountEnv([]string{"--path", project(t, tc.account)}, &out); err != nil {
				t.Fatalf("accountEnv: %v", err)
			}
			got := out.String()
			if tc.want == "" {
				if got != "" {
					t.Fatalf("stdout = %q, want EMPTY — the shell function would eval this", got)
				}
				return
			}
			if got != tc.want+"\n" {
				t.Fatalf("stdout = %q, want exactly %q", got, tc.want+"\n")
			}
			if n := strings.Count(got, "\n"); n != 1 {
				t.Fatalf("stdout has %d lines, want exactly 1", n)
			}
		})
	}
}

// `account list` reports state, never credential material: five fields per row,
// and the credential-derived one is the plan tier.
func TestAccountListPrintsStateNotSecrets(t *testing.T) {
	fakeHome(t, "default", "work")
	// The kill switch makes credential resolution answer "disabled" without
	// touching a file or the login keychain, so the test is deterministic on
	// any machine — and it pins the tri-state rendering at the same time.
	t.Setenv("SWARMERY_USAGE_OAUTH", "0")

	var out bytes.Buffer
	if err := accountList(nil, &out); err != nil {
		t.Fatalf("accountList: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines (%q), want a header plus 2 accounts", len(lines), out.String())
	}
	if header := strings.Join(strings.Fields(lines[0]), " "); header != "KEY CONFIG DIR DEFAULT CONNECTED PLAN" {
		t.Fatalf("header = %q — a new column here may be a new leak", header)
	}
	for _, line := range lines[1:] {
		// key, config dir, default?, connected?, plan — the plan is "-" here
		// because the credential read is switched off.
		if fields := strings.Fields(line); len(fields) != 5 {
			t.Fatalf("row %q has %d fields, want 5", line, len(fields))
		}
		if !strings.Contains(line, "unknown") {
			t.Errorf("row %q: SWARMERY_USAGE_OAUTH=0 must render as `unknown`, "+
				"not as `no` — the question was switched off, not answered", line)
		}
	}
}

// Binding a project to an account that is not installed would make every
// session in it start in a config dir with no login — the same refusal
// PUT /api/projects/{id}/account makes, for the same reason.
func TestAccountUseRefusesAnAccountThatIsNotInstalled(t *testing.T) {
	fakeHome(t, "default")
	dir := project(t, "")

	var out bytes.Buffer
	err := accountUse([]string{"ghost", "--path", dir}, &out)
	if err == nil {
		t.Fatal("accountUse accepted an account with no config dir on this machine")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the rejected account", err)
	}
	if got := claudeacct.Binding(dir); got != "" {
		t.Errorf("binding = %q after a refused use — nothing may be written", got)
	}
}

// use → which → clear, the ordinary operator loop. `use` must accept the key
// before OR after --path: the flag package stops at the first positional, and
// an operator typing the natural order should not get a usage error.
func TestAccountUseWhichClearRoundTrip(t *testing.T) {
	home := fakeHome(t, "default", "work")
	dir := project(t, "")

	var out bytes.Buffer
	if err := accountUse([]string{"work", "--path", dir}, &out); err != nil {
		t.Fatalf("accountUse: %v", err)
	}
	if got := claudeacct.Binding(dir); got != "work" {
		t.Fatalf("binding = %q, want %q", got, "work")
	}

	out.Reset()
	if err := accountWhich([]string{"--path", dir}, &out); err != nil {
		t.Fatalf("accountWhich: %v", err)
	}
	for _, want := range []string{"account:    work", "source:     binding", filepath.Join(home, ".claude-work")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("which output %q missing %q", out.String(), want)
		}
	}

	out.Reset()
	if err := accountClear([]string{"--path", dir}, &out); err != nil {
		t.Fatalf("accountClear: %v", err)
	}
	if got := claudeacct.Binding(dir); got != "" {
		t.Fatalf("binding = %q after clear, want empty", got)
	}

	// An unbound project reports the default account, sourced from the default.
	out.Reset()
	if err := accountWhich([]string{"--path", dir}, &out); err != nil {
		t.Fatalf("accountWhich after clear: %v", err)
	}
	if !strings.Contains(out.String(), "source:     default") {
		t.Errorf("which after clear = %q, want source: default", out.String())
	}
}

// ── exec ────────────────────────────────────────────────────────────────────

// execHelperProject carries the project dir into the helper process below. Its
// presence is also what tells the helper it is the child.
const execHelperProject = "SWARMERY_TEST_ACCOUNT_EXEC_PROJECT"

// staleConfigDir stands in for a CLAUDE_CONFIG_DIR left over in the operator's
// shell — the value that must NOT reach the command.
const staleConfigDir = "/stale/leftover/exported/by/the/shell"

// `account exec` must win over a CLAUDE_CONFIG_DIR the caller's shell already
// exported, or it runs the command under the WRONG account while `account which`
// reports the right one — a silent failure, and the worst one this feature has.
//
// syscall.Exec is why this needs a real child: execve(2) hands the envp array to
// the child verbatim, and a libc getenv() returns the FIRST match it walks to,
// so a duplicated key is decided by ORDER, not by last-wins. Nothing about that
// is observable in-process — appending the delta looks perfectly correct until
// something actually calls getenv.
//
// The child is /usr/bin/printenv, a getenv(3) caller, and deliberately NOT a
// shell: a shell parses envp into its own variable table (last assignment wins)
// and would mask exactly the bug under test.
func TestAccountExecOverridesAStaleConfigDirFromTheCallersShell(t *testing.T) {
	home := fakeHome(t, "default", "work")
	dir := project(t, "work")
	want := filepath.Join(home, ".claude-work")

	// 1. The unit-level property: one entry per overridden key, override value,
	//    and every unrelated variable carried through untouched.
	merged := claudeacct.SpawnEnvFor(
		[]string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=" + staleConfigDir, "TERM=xterm"},
		dir,
	)
	var seen []string
	for _, kv := range merged {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("merged env has %d CLAUDE_CONFIG_DIR entries (%q), want exactly 1 — "+
			"execve keeps duplicates and getenv takes the first", len(seen), seen)
	}
	if seen[0] != "CLAUDE_CONFIG_DIR="+want {
		t.Fatalf("merged CLAUDE_CONFIG_DIR = %q, want %q", seen[0], "CLAUDE_CONFIG_DIR="+want)
	}
	for _, keep := range []string{"PATH=/usr/bin", "TERM=xterm"} {
		if !slicesContains(merged, keep) {
			t.Errorf("merged env dropped %q — only overridden keys may be removed", keep)
		}
	}

	// 2. The end-to-end property, through a real execve and a real getenv.
	printenv, err := exec.LookPath("printenv")
	if err != nil {
		t.Skipf("printenv not on this machine: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAccountExecHelper")
	cmd.Env = []string{
		execHelperProject + "=" + dir,
		"HOME=" + home,
		// The bug's trigger: the caller's shell got here first.
		"CLAUDE_CONFIG_DIR=" + staleConfigDir,
		"PATH=" + filepath.Dir(printenv),
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v (stdout %q)", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got == staleConfigDir {
		t.Fatalf("child observed the STALE dir %q — the command just ran under the "+
			"wrong account with no signal; want the bound account's %q", got, want)
	}
	if got != want {
		t.Fatalf("child observed CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// TestAccountExecHelper is the child half of the test above: it calls the real
// accountExec, which replaces this process with printenv. It is a no-op unless
// the parent selected it, so it costs the ordinary suite nothing.
func TestAccountExecHelper(t *testing.T) {
	dir := os.Getenv(execHelperProject)
	if dir == "" {
		t.Skip("helper process for TestAccountExecOverridesAStaleConfigDirFromTheCallersShell")
	}
	// Returns only on failure — on success this process IS printenv, and its
	// stdout is what the parent reads.
	if err := accountExec([]string{"--path", dir, "--", "printenv", "CLAUDE_CONFIG_DIR"}); err != nil {
		t.Fatalf("accountExec: %v", err)
	}
}

// slicesContains keeps the assertion above readable without pulling "slices" in
// for one call.
func slicesContains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
