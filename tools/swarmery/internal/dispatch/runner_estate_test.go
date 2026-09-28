package dispatch

// The estate half of the account contract at the dispatch seam: the service
// resolves the task's PROJECT (account + estate) once, and the runner delivers
// that resolution's credential store into the child's environment.
//
// Stores live in a t.TempDir() behind SWARMERY_SECRETS_DIR and hold a literal
// non-secret marker.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const (
	estateVar    = "SWARMERY_TEST_ESTATE_SECRET"
	estateMarker = "estate-delivered"
)

// seedEstateStore writes <name>.env (0600) into a fresh secrets dir.
func seedEstateStore(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", dir)
	os.Unsetenv(estateVar)
	p := filepath.Join(dir, name+".env")
	if err := os.WriteFile(p, []byte(estateVar+"="+estateMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The runner reads spec.Resolution: an estate whose store exists reaches the
// child, while the account (default here) contributes no config dir.
func TestSpawnCarriesTheEstateStore(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	seedEstateStore(t, "acme")
	fakeClaude(t, `printf '%s\n' "${`+estateVar+`-`+unsetMarker+`}" > "$PWD/estate.txt"; exit 0`)
	cwd := t.TempDir()
	spec := RunSpec{Prompt: "p", SessionUUID: "estate-run", Cwd: cwd,
		Resolution: claudeacct.Resolution{Account: "default", Estate: "acme"}}
	if _, err := (ClaudeRunner{}).Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cwd, "estate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != estateMarker {
		t.Fatalf("child saw %s=%q, want the estate's store", estateVar, got)
	}
}

// The service populates the Resolution from the PROJECT path — its own pin for
// the account, the ancestor's declaration for the estate — never from the
// worktree the run executes in.
func TestRunPlaybook_ResolvesTheProjectsAccountAndEstate(t *testing.T) {
	t.Setenv(microPlansEnv, "0")
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
	if err := claudeacct.SetBinding(proj, "default"); err != nil {
		t.Fatal(err)
	}
	// admit() resolves the project path through repopath.ResolveTrusted, which
	// requires projects.path to be a git checkout. mkRepo's bare .git directory
	// is not a repository git recognises, so the bindings above stay not-a-repo
	// for the provenance gate rather than turning tracked or indeterminate.
	mkRepo(t, proj)

	db := testDB(t)
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, proj); err != nil {
		t.Fatal(err)
	}
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	id := insertTask(t, db, "T-estate", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	if r.count() == 0 {
		t.Fatal("no run was started")
	}
	got := r.spec(0).Resolution
	if got.Account != "default" || got.Source != claudeacct.SourcePin {
		t.Errorf("Resolution account = %q (%s), want the project's own default pin", got.Account, got.Source)
	}
	if got.Estate != "acme" || got.EstateRoot != root {
		t.Errorf("Resolution estate = %q at %q, want acme at %s", got.Estate, got.EstateRoot, root)
	}
}
