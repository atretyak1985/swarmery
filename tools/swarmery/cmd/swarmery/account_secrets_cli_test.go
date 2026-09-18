package main

// The two CLI-side contracts of the per-account secret store:
//
//   - `account exec` CARRIES the account's secrets into the child;
//   - `account env` still does NOT, because its output is printed to the
//     operator's terminal and its scrollback, and because the accounts-pack
//     shell function matches that whole output against `CLAUDE_CONFIG_DIR=?*`
//     — a second line there silently drops the binding.
//
// The variable seeded below is a literal non-secret and the store lives in a
// t.TempDir(): no test reads the operator's real ~/.swarmery/secrets.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// storeSecretVar is the name the store delivers. Prefixed so it cannot
	// collide with anything the test process really carries.
	storeSecretVar = "SWARMERY_TEST_CLI_SECRET"
	// storeSecretValue is a marker, not a secret.
	storeSecretValue = "store-delivered"
	// execSecretHelperProject carries the project dir into the helper process
	// and tells it that it is the child.
	execSecretHelperProject = "SWARMERY_TEST_ACCOUNT_EXEC_SECRET_PROJECT"
	// secretsDirVar is claudeacct's store-directory override.
	secretsDirVar = "SWARMERY_SECRETS_DIR"
)

// seedSecretStore writes one account's store (0600) into a temp dir and points
// SWARMERY_SECRETS_DIR at it, returning the dir.
func seedSecretStore(t *testing.T, account string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(secretsDirVar, dir)
	path := filepath.Join(dir, account+".env")
	if err := os.WriteFile(path, []byte(storeSecretVar+"="+storeSecretValue+"\n"), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod store: %v", err)
	}
	return dir
}

// `account env` stays the CONFIG-DIR line and nothing else, even when the bound
// account has a fully populated secret store. Not the name, not the value, not
// a count, not a hint.
func TestAccountEnvNeverPrintsASecret(t *testing.T) {
	home := fakeHome(t, "default", "work")
	seedSecretStore(t, "work")
	dir := project(t, "work")

	var out bytes.Buffer
	if err := accountEnv([]string{"--path", dir}, &out); err != nil {
		t.Fatalf("accountEnv: %v", err)
	}
	got := out.String()
	if want := "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-work") + "\n"; got != want {
		t.Fatalf("stdout = %q, want exactly %q", got, want)
	}
	if strings.Contains(got, storeSecretVar) || strings.Contains(got, storeSecretValue) {
		t.Fatalf("stdout leaked the store into the terminal: %q", got)
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("stdout has %d lines, want exactly 1 — the shell function's case would stop matching", n)
	}
}

// `account exec` DOES carry them, through a real execve and a real getenv — the
// terminal half of the channel that makes ${VAR} in a plugin's .mcp.json expand.
func TestAccountExecCarriesTheAccountSecretStore(t *testing.T) {
	home := fakeHome(t, "default", "work")
	secretsDir := seedSecretStore(t, "work")
	dir := project(t, "work")

	printenv, err := exec.LookPath("printenv")
	if err != nil {
		t.Skipf("printenv not on this machine: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAccountExecSecretHelper")
	cmd.Env = []string{
		execSecretHelperProject + "=" + dir,
		"HOME=" + home,
		secretsDirVar + "=" + secretsDir,
		"PATH=" + filepath.Dir(printenv),
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v (stdout %q)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != storeSecretValue {
		t.Fatalf("child observed %s=%q, want %q — the MCP servers would fail to start",
			storeSecretVar, got, storeSecretValue)
	}
}

// An UNBOUND project's `account exec` carries nothing from the store, even
// though a store exists on the machine. The scoping property, at the terminal
// seam: `printenv` exits 1 with no output when the variable is absent.
func TestAccountExecUnboundProjectCarriesNoSecrets(t *testing.T) {
	home := fakeHome(t, "default", "work")
	secretsDir := seedSecretStore(t, "work")
	dir := project(t, "") // no binding

	printenv, err := exec.LookPath("printenv")
	if err != nil {
		t.Skipf("printenv not on this machine: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAccountExecSecretHelper")
	cmd.Env = []string{
		execSecretHelperProject + "=" + dir,
		"HOME=" + home,
		secretsDirVar + "=" + secretsDir,
		"PATH=" + filepath.Dir(printenv),
	}
	out, _ := cmd.Output() // printenv exits nonzero when the name is absent
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Fatalf("an unbound project's exec saw %s=%q, want it ABSENT — another "+
			"account's secrets leaked into this command", storeSecretVar, got)
	}
}

// TestAccountExecSecretHelper is the child half of the two tests above: it calls
// the real accountExec, which replaces this process with printenv. A no-op
// unless the parent selected it.
func TestAccountExecSecretHelper(t *testing.T) {
	dir := os.Getenv(execSecretHelperProject)
	if dir == "" {
		t.Skip("helper process for TestAccountExecCarriesTheAccountSecretStore")
	}
	if err := accountExec([]string{"--path", dir, "--", "printenv", storeSecretVar}); err != nil {
		t.Fatalf("accountExec: %v", err)
	}
}
