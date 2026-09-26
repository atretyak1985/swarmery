package lessons

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// A committed CLAUDE.md that is an ABSOLUTE symlink out of the repository must
// not be followed: promotion would otherwise append to the link target in the
// operator's checkout, and no cleanup could undo it.
func TestPromoteRefusesASymlinkedClaudeMD(t *testing.T) {
	db := openDB(t)
	repo := gitRepo(t)
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outside, []byte("# agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "internal", "store", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "add", ".")
	gitOut(t, repo, "commit", "-q", "-m", "link CLAUDE.md")

	phaseID := seedRun(t, db, "promo-link", 0.2, "")
	id := seedActive(t, db, phaseID, 1, "Index every child column before a prune.", "internal/store/**", "2026-09-20T00:00:00Z")
	resolve := func(string, ...string) (string, error) { return repo, nil }

	if _, err := Promote(db, worktree.ExecGit{}, resolve, id, PromoteInput{}, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Promote err = %v, want ErrInvalid for a symlinked CLAUDE.md", err)
	}
	body, _ := os.ReadFile(outside)
	if string(body) != "# agents\n" {
		t.Fatalf("the link target outside the worktree was written: %q", body)
	}
}
