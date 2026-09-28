package provision

// The estate half of the account contract at the provision seam: a provision
// run's dir IS the project path, and claudeacct.SpawnEnvFor resolves the ESTATE
// by the same walk as the account — so a project under a declaring estate root
// carries that estate's credential store. Stores are literal non-secret markers
// in a t.TempDir().

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const estateVar = "SWARMERY_TEST_ESTATE_SECRET"

func TestProvisionCarriesTheProjectsEstateStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
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
	if err := os.WriteFile(store, []byte(estateVar+"=estate-delivered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "projects", "acme")
	project := filepath.Join(root, "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	body := "#!/bin/sh\nprintf '%s\\n' \"${" + estateVar + "-" + unsetMarker + "}\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := ClaudeRunner{}.Claude(context.Background(), project, "", "--version")
	if err != nil {
		t.Fatalf("Claude: %v", err)
	}
	if got := strings.TrimSpace(out); got != "estate-delivered" {
		t.Fatalf("child saw %s=%q, want the estate's store", estateVar, got)
	}
}
