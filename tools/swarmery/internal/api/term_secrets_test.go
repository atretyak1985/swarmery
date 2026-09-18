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
	t.Setenv("SWARMERY_SECRETS_DIR", store)
	if err := os.WriteFile(filepath.Join(store, "nabu-org.env"), []byte("MCP_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	got := termAccountEnv(project)
	wantTail := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"], "MCP_TOKEN=abc"}
	if n := len(got); n < 2 || !slices.Equal(got[n-2:], wantTail) {
		t.Errorf("env tail = %v, want %v — the dock shell would start its MCP servers without the token", got, wantTail)
	}
	if len(got) < len(os.Environ()) {
		t.Errorf("env has %d entries, fewer than the daemon's %d — inherited variables were dropped", len(got), len(os.Environ()))
	}

	// The account-scoped terminal (connect flow) composes the same environment.
	if _, env, ok := resolveTermAccount("nabu-org"); !ok || !slices.Equal(env, got) {
		t.Errorf("resolveTermAccount env = %v, ok=%v; want the project-bound answer %v", env, ok, got)
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
