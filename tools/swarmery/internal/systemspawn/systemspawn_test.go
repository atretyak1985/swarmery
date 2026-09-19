package systemspawn

// Attach's whole contract, proven on the three states ~/.swarmery can be in.
// The per-runner tests (extract, handoff, trajjudge, improve) prove only that
// their spawn site CALLS this; the behaviour itself is pinned once, here.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const configDirEnv = "CLAUDE_CONFIG_DIR"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// for the duration of the test. os.Environ() is the base Attach composes onto,
// so without this the assertions would report whatever the developer's shell
// (or a daemon running under a non-default account) happens to export.
func unsetConfigDir(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv(configDirEnv)
	if !had {
		return
	}
	if err := os.Unsetenv(configDirEnv); err != nil {
		t.Fatalf("unset %s: %v", configDirEnv, err)
	}
	t.Cleanup(func() { os.Setenv(configDirEnv, prev) })
}

// systemHome points HOME at a fresh temp dir and creates ~/.swarmery under it,
// returning that directory.
func systemHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".swarmery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir ~/.swarmery: %v", err)
	}
	return dir
}

// configDirEntries is every CLAUDE_CONFIG_DIR assignment in env, in order.
// Plural on purpose: execve(2) copies the array verbatim and getenv() returns
// the FIRST match, so "exactly one" is a real assertion, not a formality.
func configDirEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, configDirEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// A missing ~/.swarmery must leave BOTH fields alone — the spawn stays
// byte-identical to one issued before either feature existed.
func TestAttachMissingSystemDirTouchesNothing(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home) // deliberately: no .swarmery created under it

	if _, err := os.Stat(filepath.Join(home, ".swarmery")); !os.IsNotExist(err) {
		t.Fatalf("precondition: ~/.swarmery must not exist (stat err = %v)", err)
	}

	cmd := exec.Command("true")
	Attach(cmd)

	if cmd.Dir != "" {
		t.Errorf("cmd.Dir = %q, want empty — a missing System home must not be chdir'd into", cmd.Dir)
	}
	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil — no project means no account to resolve", cmd.Env)
	}
}

// A path that exists but is a FILE is not a System home either: chdir into it
// would fail the spawn with ENOTDIR.
func TestAttachSystemPathIsFileTouchesNothing(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".swarmery"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write ~/.swarmery as a file: %v", err)
	}

	cmd := exec.Command("true")
	Attach(cmd)

	if cmd.Dir != "" || cmd.Env != nil {
		t.Errorf("Attach acted on a file: Dir=%q Env=%v, want both untouched", cmd.Dir, cmd.Env)
	}
}

// Existing but unbound: the cwd is set (that half predates accounts entirely)
// and the environment is os.Environ() byte for byte — the "nothing broke" half
// of the contract.
func TestAttachUnboundSetsDirAndLeavesEnvByteIdentical(t *testing.T) {
	unsetConfigDir(t)
	dir := systemHome(t)

	cmd := exec.Command("true")
	Attach(cmd)

	if cmd.Dir != dir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, dir)
	}
	base := os.Environ()
	if len(cmd.Env) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(cmd.Env), len(base))
	}
	for i := range base {
		if cmd.Env[i] != base[i] {
			t.Errorf("env[%d] = %q, want %q", i, cmd.Env[i], base[i])
		}
	}
}

// Bound: the binding on ~/.swarmery decides the account, and it lands as
// exactly one CLAUDE_CONFIG_DIR.
func TestAttachBoundSetsConfigDirExactlyOnce(t *testing.T) {
	unsetConfigDir(t)
	dir := systemHome(t)
	if err := claudeacct.SetBinding(dir, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	cmd := exec.Command("true")
	Attach(cmd)

	if cmd.Dir != dir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, dir)
	}
	entries := configDirEntries(cmd.Env)
	if len(entries) != 1 {
		t.Fatalf("%s entries = %v, want exactly one", configDirEnv, entries)
	}
	want := configDirEnv + "=" + filepath.Join(os.Getenv("HOME"), ".claude-nabu-org")
	if entries[0] != want {
		t.Errorf("env carries %q, want %q", entries[0], want)
	}
}

// A stale CLAUDE_CONFIG_DIR exported by whoever started the daemon must not
// survive next to the binding's — getenv() would return the wrong one.
func TestAttachBoundReplacesInheritedConfigDir(t *testing.T) {
	dir := systemHome(t)
	t.Setenv(configDirEnv, "/stale/from/the/parent")
	if err := claudeacct.SetBinding(dir, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	cmd := exec.Command("true")
	Attach(cmd)

	entries := configDirEntries(cmd.Env)
	if len(entries) != 1 {
		t.Fatalf("%s entries = %v, want exactly one (the inherited value must be dropped, not shadowed)", configDirEnv, entries)
	}
	if strings.Contains(entries[0], "/stale/") {
		t.Errorf("env carries the inherited %q, want the binding's value", entries[0])
	}
}

// The account's secret store rides along — this is the half the three
// background runners were missing, and the reason their plugin MCP servers
// received a literal ${VAR} instead of a credential.
func TestAttachBoundCarriesTheAccountSecretStore(t *testing.T) {
	unsetConfigDir(t)
	dir := systemHome(t)
	if err := claudeacct.SetBinding(dir, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	store := t.TempDir()
	if err := os.Chmod(store, 0o700); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store, "nabu-org.env"), []byte("SOME_TOKEN=s3cr3t\n"), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", store)

	cmd := exec.Command("true")
	Attach(cmd)

	var found bool
	for _, kv := range cmd.Env {
		if kv == "SOME_TOKEN=s3cr3t" {
			found = true
		}
	}
	if !found {
		t.Error("the bound account's secret store did not reach the spawn environment")
	}
}
