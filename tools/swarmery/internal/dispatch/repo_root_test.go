package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// The change under test: a hand-added board card for a multi-repo project used
// to hand worktree.Acquire the raw project path — the umbrella directory,
// never a git checkout — and every such card failed immediately with
// worktree.ErrNotARepo, regardless of project.json's mainApp (project Skygor,
// 2026-09-17: the phase/plan engines already resolved mainApp correctly,
// dispatch never did).
func TestAdmit_MultiRepoProject_ResolvesMainApp(t *testing.T) {
	db := testDB(t)
	umbrella := t.TempDir()
	repo := mkRepo(t, filepath.Join(umbrella, "sk-next"))
	if err := os.MkdirAll(filepath.Join(umbrella, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(umbrella, ".claude", "project.json"),
		[]byte(`{"mainApp":"sk-next"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2, ?, 'umbrella', '2026-01-01T00:00:00Z')`,
		umbrella); err != nil {
		t.Fatal(err)
	}

	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	insertTask(t, db, "T-multi", taskOpts{projectID: 2})

	s.Schedule()

	if got := wt.lastAcquireRoot(); !sameDir(t, got, repo) {
		t.Fatalf("Acquire repoRoot = %q, want the resolved mainApp checkout %q", got, repo)
	}
}

// The regression guarding every existing project: a project path that already
// IS a checkout stays the run root, exactly as before this fix.
func TestAdmit_SingleRepoProject_UsesProjectPath(t *testing.T) {
	db := testDB(t)
	var projectPath string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id=1`).Scan(&projectPath); err != nil {
		t.Fatal(err)
	}

	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	insertTask(t, db, "T-solo", taskOpts{})

	s.Schedule()

	if got := wt.lastAcquireRoot(); !sameDir(t, got, projectPath) {
		t.Fatalf("Acquire repoRoot = %q, want the project path %q", got, projectPath)
	}
}

// Nothing resolves ⇒ admission refuses with a dispatch_error naming what was
// tried, and no worktree is acquired — the card stays a Todo candidate for the
// next pass or a human to intervene, same posture as every other admission gate.
func TestAdmit_NoRepoRoot_SurfacesDispatchErrorAndAcquiresNothing(t *testing.T) {
	db := testDB(t)
	umbrella := t.TempDir() // no .git, no project.json mainApp
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2, ?, 'umbrella', '2026-01-01T00:00:00Z')`,
		umbrella); err != nil {
		t.Fatal(err)
	}

	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	id := insertTask(t, db, "T-nowhere", taskOpts{projectID: 2})

	s.Schedule()

	if wt.acquiredCount() != 0 {
		t.Error("a worktree was acquired despite no repo resolving")
	}
	var col, dispatchErr string
	if err := db.QueryRow(`SELECT board_column, COALESCE(dispatch_error,'') FROM tasks WHERE id=?`, id).
		Scan(&col, &dispatchErr); err != nil {
		t.Fatal(err)
	}
	if col != "todo" {
		t.Errorf("board_column = %q, want todo (admission refused, not failed-in-progress)", col)
	}
	if dispatchErr == "" {
		t.Error("dispatch_error is empty, want it to name what repopath tried")
	}
}

// sameDir compares paths after symlink resolution — mirrors phaserun/planrun's
// own helper of the same name (package-local, not shared across packages).
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}
