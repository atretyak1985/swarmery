package api

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// termConfigDirs is every CLAUDE_CONFIG_DIR entry of a whole environment — the
// part of termAccountEnv's answer the account tests are about.
func termConfigDirs(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			out = append(out, kv)
		}
	}
	return out
}

// envNames is env with every VALUE dropped — what a failure message may print.
// These slices are built on the daemon's real os.Environ(), so formatting one
// whole would write the operator's live tokens into a test log.
func envNames(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		out[i], _, _ = strings.Cut(kv, "=")
	}
	return out
}

// A dock shell for a bound project sees the account's secret store, the same way
// a dashboard-dispatched run and `swarmery account exec` do — the config dir and
// the store land after everything inherited, exactly once each.
func TestTermAccountEnvCarriesTheAccountsSecretStore(t *testing.T) {
	unsetConfigDir(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	store := t.TempDir()
	if err := os.Chmod(store, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", store)
	if err := os.WriteFile(filepath.Join(store, "nabu-org.env"), []byte("MCP_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	got := termAccountEnv(project)
	wantTail := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"], "MCP_TOKEN=abc"}
	if n := len(got); n < 2 || !slices.Equal(got[n-2:], wantTail) {
		t.Errorf("env tail names = %v, want %v (values withheld) — the dock shell would start its MCP servers without the token",
			envNames(got[max(0, n-2):]), envNames(wantTail))
	}
	if len(got) < len(os.Environ()) {
		t.Errorf("env has %d entries, fewer than the daemon's %d — inherited variables were dropped", len(got), len(os.Environ()))
	}

	// The account-scoped terminal (connect flow) composes the same environment.
	if _, env, ok := resolveTermAccount("nabu-org"); !ok || !slices.Equal(env, got) {
		t.Errorf("resolveTermAccount env names = %v, ok=%v; want the project-bound answer's names %v (values withheld)",
			envNames(env), ok, envNames(got))
	}
}

// A PROJECT-scoped dock shell resolves the project's estate like every other
// path-carrying seam; the ACCOUNT-scoped terminal carries no estate — the
// documented gap (term.go, resolveTermAccount): it belongs to no project.
func TestTermAccountEnvCarriesTheProjectsEstate(t *testing.T) {
	unsetConfigDir(t)
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	root := filepath.Join(home, "projects", "acme")
	project := filepath.Join(root, "repo")
	if err := claudeacct.SetBinding(root, "nabu-org"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := os.Chmod(store, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", store)
	// D5: an estate store releases only when ROOTED and its roots admit the
	// estate root.
	if err := os.WriteFile(filepath.Join(store, "acme.env"), []byte("# swarmery-root: "+root+"\nESTATE_TOKEN=e\n"), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if got := termAccountEnv(project); !slices.Contains(got, "ESTATE_TOKEN=e") {
		t.Error("a project-scoped dock shell did not carry the project's estate store")
	}
	if _, env, ok := resolveTermAccount("nabu-org"); !ok || slices.Contains(env, "ESTATE_TOKEN=e") {
		t.Errorf("account-scoped terminal ok=%v carried an estate store — it belongs to no project", ok)
	}
}

// THE case a delta could never express: a project bound EXPLICITLY to the
// default account must not inherit a CLAUDE_CONFIG_DIR the daemon itself carries
// (a dir baked into the plist by `swarmery install --claude-config-dir`).
func TestTermAccountEnvDropsInheritedConfigDirForAnExplicitDefaultBinding(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	t.Setenv("CLAUDE_CONFIG_DIR", "/baked/by/launchd/.claude-nabu-org")
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, ingest.DefaultAccount); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	if got := termConfigDirs(termAccountEnv(project)); len(got) != 0 {
		t.Errorf("explicit default binding kept %v — the dock shell would run under the daemon's baked account", got)
	}
	// And the account terminal for "default" behaves the same.
	if _, env, ok := resolveTermAccount(ingest.DefaultAccount); ok && len(termConfigDirs(env)) != 0 {
		t.Errorf("resolveTermAccount(default) kept %v", termConfigDirs(env))
	}
}
