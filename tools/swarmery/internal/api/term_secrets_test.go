package api

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// A dock shell for a bound project sees the account's secret store, the same way
// a dashboard-dispatched run and `swarmery account exec` do — the delta carries
// the config dir first and the store after it.
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
	want := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"], "MCP_TOKEN=abc"}
	if !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v — the dock shell would start its MCP servers without the token", got, want)
	}

	// The account-scoped terminal (connect flow) takes the same delta by key.
	if _, env, ok := resolveTermAccount("nabu-org"); !ok || !slices.Equal(env, want) {
		t.Errorf("resolveTermAccount env = %v, ok=%v; want %v", env, ok, want)
	}
}
