package planning

// The estate half of the account contract at the planner seam: a planner's Cwd
// IS the project path, so the runner resolves it directly — and a project under
// a declaring estate root gets the estate's credential store in the child's
// environment. Stores are literal non-secret markers in a t.TempDir().

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

func TestPlannerCarriesTheProjectsEstateStore(t *testing.T) {
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
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "fakeclaude-estate.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"${" + estateVar + "-" + unsetMarker + "}\" > \"$PWD/estate.txt\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)

	if _, err := (ClaudeRunner{Timeout: 30 * time.Second}).Start(context.Background(),
		RunSpec{Prompt: "plan it", SessionUUID: "estate", Cwd: project}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(project, "estate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != estateMarker {
		t.Fatalf("child saw %s=%q, want the estate's store", estateVar, got)
	}
}
