package dispatch

import (
	"os"
	"path/filepath"
	"strings"
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

// The gap runRoot's resolve left open: resolving the correct sub-repo checkout
// for worktree.Acquire is not enough on its own. The worktree IS a checkout of
// sk-next, not of the umbrella project, so it carries no .claude/settings.json —
// the plugin stack that ships c.Agent is otherwise unreachable from it, and the
// run proceeds as an unresolved @mention instead of failing loudly. phaserun and
// planrun already lend the project's settings.json via repopath.InheritedSettings
// for exactly this case; dispatch must too, plus tell the agent what happened.
func TestRunPlaybook_MultiRepoSubCheckout_LendsSettingsAndOrientsPrompt(t *testing.T) {
	db := testDB(t)
	umbrella := t.TempDir()
	mkRepo(t, filepath.Join(umbrella, "sk-next"))
	if err := os.MkdirAll(filepath.Join(umbrella, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(umbrella, ".claude", "project.json"),
		[]byte(`{"mainApp":"sk-next"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(umbrella, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2, ?, 'umbrella', '2026-01-01T00:00:00Z')`,
		umbrella); err != nil {
		t.Fatal(err)
	}

	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	insertTask(t, db, "T-multi", taskOpts{projectID: 2})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runs = %d, want 1", r.count())
	}
	spec := r.spec(0)
	if !sameDir(t, spec.SettingsFile, settingsPath) {
		t.Errorf("SettingsFile = %q, want the project's own %q", spec.SettingsFile, settingsPath)
	}
	if !strings.Contains(spec.Prompt, "REPOSITORY: your worktree is a checkout of") {
		t.Errorf("prompt carries no multi-repo orientation note:\n%s", spec.Prompt)
	}
}

// The regression every existing (single-repo) project depends on: repoRoot ==
// projectPath there, so neither SettingsFile nor the orientation note should
// appear — lending a redundant settings file or an unsolicited REPOSITORY note
// would change behaviour for every project that isn't multi-repo.
func TestRunPlaybook_SingleRepoProject_NoSettingsFileOrNote(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	insertTask(t, db, "T-solo", taskOpts{})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runs = %d, want 1", r.count())
	}
	spec := r.spec(0)
	if spec.SettingsFile != "" {
		t.Errorf("SettingsFile = %q, want \"\" for a single-repo project", spec.SettingsFile)
	}
	if strings.Contains(spec.Prompt, "REPOSITORY:") {
		t.Errorf("prompt carries an unsolicited multi-repo note:\n%s", spec.Prompt)
	}
}

// repoNote is two independent blocks (multiRepoNote + AdditionalDirsNote), and
// the second fires for ANY project declaring permissions.additionalDirectories
// — single-repo included, same as phaserun/planrun. The test above never
// exercises this half (its fixture project has no settings.json, so the block
// is absent for an unrelated reason); this pins the half it left accidental.
func TestRunPlaybook_SingleRepoProject_AdditionalDirsNoteStillApplies(t *testing.T) {
	db := testDB(t)
	var projectPath string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id=1`).Scan(&projectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".claude", "settings.json"),
		[]byte(`{"permissions":{"additionalDirectories":["/srv/shared-lib"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	insertTask(t, db, "T-solo", taskOpts{})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runs = %d, want 1", r.count())
	}
	spec := r.spec(0)
	if strings.Contains(spec.Prompt, "REPOSITORY:") {
		t.Errorf("prompt carries an unsolicited multi-repo note:\n%s", spec.Prompt)
	}
	if !strings.Contains(spec.Prompt, "ADDITIONAL ACCESS") || !strings.Contains(spec.Prompt, "/srv/shared-lib") {
		t.Errorf("prompt carries no additionalDirectories note despite the project declaring one:\n%s", spec.Prompt)
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

// A removal targets the repository the worktree was actually cut from, read from
// the worktree's own `.git` file, not a fresh resolve: when project.json's
// mainApp changes while a card runs, re-resolving points at the other repo, whose
// `git worktree remove` fails and leaks the worktree.
func TestRemoveWorktreeFor_UsesTheWorktreesOwnRepo(t *testing.T) {
	db := testDB(t)
	umbrella := t.TempDir()
	started := mkRepo(t, filepath.Join(umbrella, "sk-next"))
	mkRepo(t, filepath.Join(umbrella, "sk-other"))
	if err := os.MkdirAll(filepath.Join(umbrella, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// mainApp now names a DIFFERENT repo than the one the worktree came from.
	if err := os.WriteFile(filepath.Join(umbrella, ".claude", "project.json"),
		[]byte(`{"mainApp":"sk-other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2, ?, 'umbrella', '2026-01-01T00:00:00Z')`,
		umbrella); err != nil {
		t.Fatal(err)
	}
	// A linked worktree of `started`: <wt>/.git names <started>/.git/worktrees/card.
	wtPath := filepath.Join(t.TempDir(), "card")
	if err := os.MkdirAll(filepath.Join(started, ".git", "worktrees", "card"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := "gitdir: " + filepath.Join(started, ".git", "worktrees", "card") + "\n"
	if err := os.WriteFile(filepath.Join(wtPath, ".git"), []byte(gitFile), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	id := insertTask(t, db, "T-moved", taskOpts{projectID: 2})
	if _, err := db.Exec(`UPDATE tasks SET worktree_path=?, branch='swarm/T-moved' WHERE id=?`, wtPath, id); err != nil {
		t.Fatal(err)
	}

	s.RemoveWorktreeFor(id)

	wt.mu.Lock()
	defer wt.mu.Unlock()
	if len(wt.removeRoots) != 1 || !sameDir(t, wt.removeRoots[0], started) {
		t.Fatalf("Remove repoRoot = %v, want the worktree's own repo %q", wt.removeRoots, started)
	}
}

// workspaces.project_id is not UNIQUE: a project mapped by two workspace rows
// must still yield each card once, or the scheduler would consider it twice.
func TestCandidates_TwoWorkspacesForOneProject_ListCardOnce(t *testing.T) {
	db := testDB(t)
	for i, slug := range []string{"ws-a", "ws-b"} {
		if _, err := db.Exec(`INSERT INTO workspaces(slug, root_path, project_id) VALUES(?, ?, 1)`,
			slug, filepath.Join(t.TempDir(), slug)); err != nil {
			t.Fatalf("insert workspace %d: %v", i, err)
		}
	}
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	insertTask(t, db, "T-once", taskOpts{})

	cands, err := s.candidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 (one per card, not one per workspace)", len(cands))
	}
}
