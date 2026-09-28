package routines

// The estate half of the account contract at the routines seam: a routine's cwd
// IS the project path, and claudeacct.SpawnEnvFor now resolves the ESTATE by the
// same walk — so a routine under a declaring estate root carries that estate's
// credential store even when its own pin names the default account. Stores are
// literal non-secret markers in a t.TempDir().

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const estateVar = "SWARMERY_TEST_ESTATE_SECRET"

func TestRunCarriesTheProjectsEstateStore(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := t.TempDir()
	if err := os.Chmod(secrets, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	os.Unsetenv(estateVar)
	store := filepath.Join(secrets, "acme.env")
	// D5: an estate store releases only when ROOTED and its roots admit the
	// estate root, so the store names the fixture root it serves.
	if err := os.WriteFile(store, []byte("# swarmery-root: "+filepath.Join(home, "projects", "acme")+"\n"+estateVar+"=estate-delivered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "projects", "acme")
	project := filepath.Join(root, "repo")
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetBinding(project, "default"); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(t.TempDir(), "estate.txt")
	fakeClaudeRunner(t, `printf '%s\n' "${`+estateVar+`-`+acctUnsetMarker+`}" > "`+outFile+`"`)
	if _, err := (ClaudeRunner{}).Run(context.Background(), project, "p", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "estate-delivered" {
		t.Fatalf("child saw %s=%q, want the estate's store", estateVar, got)
	}
}
