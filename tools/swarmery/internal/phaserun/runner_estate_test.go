package phaserun

// The estate half of the account contract at the phase-run seam: the runner
// resolves spec.ProjectPath (never the worktree Cwd) through runcore.AccountFor,
// and the child sees the estate's credential store while keeping the project's
// own account pin. Stores are literal non-secret markers in a t.TempDir().

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const (
	estateVar    = "SWARMERY_TEST_ESTATE_SECRET"
	estateMarker = "estate-delivered"
)

func TestStartCarriesTheProjectsEstateStore(t *testing.T) {
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
	if err := os.WriteFile(store, []byte("# swarmery-root: "+filepath.Join(home, "projects", "acme")+"\n"+estateVar+"="+estateMarker+"\n"), 0o600); err != nil {
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

	outFile := filepath.Join(t.TempDir(), "env.txt")
	script := filepath.Join(t.TempDir(), "fakeclaude-estate.sh")
	body := "#!/bin/sh\nprintf '%s|%s\\n' \"${" + estateVar + "-" + unsetMarker + "}\" \"${CLAUDE_CONFIG_DIR-" + unsetMarker + "}\" > \"" + outFile + "\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)

	spec := RunSpec{Prompt: "p", SessionUUID: "estate", Cwd: t.TempDir(), ProjectPath: project}
	if _, err := (ClaudeRunner{Timeout: 30 * time.Second}).Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	gotStore, gotDir, _ := strings.Cut(strings.TrimSpace(string(b)), "|")
	if gotStore != estateMarker {
		t.Errorf("child saw %s=%q, want the estate's store", estateVar, gotStore)
	}
	if gotDir != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent — the project pins the default account", gotDir)
	}
}
