package verify

// The estate half of the account contract at the verify seam: the service
// resolves the task's PROJECT (account + estate), and the runner delivers the
// estate's credential store into the child's environment. Stores are literal
// non-secret markers in a t.TempDir().

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

func TestVerifySpawnCarriesTheEstateStore(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", dir)
	os.Unsetenv(estateVar)
	p := filepath.Join(dir, "acme.env")
	// D5: an estate store releases only when ROOTED and its roots admit the
	// estate root, so the store names the root the Resolution carries.
	estateRoot := t.TempDir()
	if err := os.WriteFile(p, []byte("# swarmery-root: "+estateRoot+"\n"+estateVar+"="+estateMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeClaudeRunner(t, `printf '%s\n' "${`+estateVar+`-`+unsetMarker+`}"; exit 0`)
	run, err := ClaudeRunner{Timeout: 30 * time.Second}.Run(context.Background(), RunSpec{
		Prompt: "p", SessionUUID: "estate-run", Cwd: t.TempDir(),
		Resolution: claudeacct.Resolution{Account: "default", Estate: "acme", EstateRoot: estateRoot},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(run.Output); got != estateMarker {
		t.Fatalf("child saw %s=%q, want the estate's store", estateVar, got)
	}
}

// The service builds the Resolution from the PROJECT path, not the worktree.
func TestVerifyTask_ResolvesTheProjectsAccountAndEstate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "projects", "acme")
	proj := filepath.Join(root, "repo")
	if err := claudeacct.SetBinding(root, "work"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}

	db := testDB(t)
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, proj); err != nil {
		t.Fatal(err)
	}
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-estate"})
	id := insertTask(t, db, taskOpts{})
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(r.specs))
	}
	got := r.specs[0].Resolution
	if got.Account != "work" || got.Source != claudeacct.SourcePinParent {
		t.Errorf("Resolution account = %q (%s), want work inherited from %s", got.Account, got.Source, root)
	}
	if got.Estate != "acme" || got.EstateRoot != root {
		t.Errorf("Resolution estate = %q at %q, want acme at %s", got.Estate, got.EstateRoot, root)
	}
}
