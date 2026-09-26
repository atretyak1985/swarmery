package dispatch

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// carveWorkspace registers a project's workspace namespace the way wsingest
// does — under the ONBOARDING slug, which is deliberately not the registry slug
// ('p') the test project carries.
func carveWorkspace(t *testing.T, db *sql.DB, slug, rootPath string, projectID int64) {
	t.Helper()
	carveWorkspaceAt(t, db, slug, rootPath, projectID, "2026-09-19T00:00:00Z")
}

// carveWorkspaceAt is carveWorkspace with an explicit last_scanned, creating the
// namespace dir on disk as onboarding does.
func carveWorkspaceAt(t *testing.T, db *sql.DB, slug, rootPath string, projectID int64, scanned string) {
	t.Helper()
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rootPath, err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspaces(slug, root_path, project_id, last_scanned)
		 VALUES(?, ?, ?, ?)`, slug, rootPath, projectID, scanned); err != nil {
		t.Fatalf("carve workspace %s: %v", slug, err)
	}
}

// The regression this whole change exists for. A project whose workspace was
// carved by onboarding must have its micro-plan minted INTO that namespace —
// not into <root>/<registry slug>, which names a dir onboarding never created.
// Minting there produced a second tree; wsingest then indexed it as its own
// workspace and project, so the card and its plan landed on different projects.
func TestMintMicroPlan_UsesTheCarvedWorkspaceNotTheRegistrySlug(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()

	carved := filepath.Join(s.WorkspaceRoot, "carved-by-onboarding")
	carveWorkspace(t, db, "carved-by-onboarding", carved, 1)

	id := insertTask(t, db, "T-42", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	if dir == "" {
		t.Fatal("tasks.workspace_dir is empty — nothing joins the card to its micro-plan")
	}
	if !strings.HasPrefix(dir, carved+string(filepath.Separator)) {
		t.Fatalf("micro-plan landed outside the carved workspace.\n got: %q\nwant under: %q", dir, carved)
	}
	// The precise failure mode: a second tree named after the registry slug.
	if stray := filepath.Join(s.WorkspaceRoot, "p"); strings.HasPrefix(dir, stray+string(filepath.Separator)) {
		t.Fatalf("micro-plan minted into the registry-slug tree %q — the split is back", stray)
	}
}

// A project with no workspace row has no carved dir to prefer, so the
// <root>/<registry slug> spelling remains correct there and must be kept.
func TestMintMicroPlan_FallsBackToRegistrySlugWhenNoWorkspaceMapped(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()

	id := insertTask(t, db, "T-42", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	want := filepath.Join(s.WorkspaceRoot, "p")
	if !strings.HasPrefix(dir, want+string(filepath.Separator)) {
		t.Fatalf("fallback changed.\n got: %q\nwant under: %q", dir, want)
	}
}

// Coverage for workspaces.project_id having no uniqueness constraint (two
// workspace rows mapped to one project must still yield the card once, not
// fanned out) now lives on the workspaceOnePerProject fix itself:
// TestCandidates_TwoWorkspacesForOneProject_ListCardOnce in repo_root_test.go.

// When a project maps to two workspace rows, workspaceOnePerProject picks ONE
// for both runRoot (the repo overlay) and mintMicroPlan: the most recently
// scanned. The stale namespace here sorts first lexicographically, so the old
// MIN(root_path) rule would have picked it.
func TestMintMicroPlan_PrefersTheMostRecentlyScannedWorkspaceWhenProjectHasTwo(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()

	stale := filepath.Join(s.WorkspaceRoot, "a-stale")
	live := filepath.Join(s.WorkspaceRoot, "z-live")
	carveWorkspaceAt(t, db, "a-stale", stale, 1, "2026-01-01T00:00:00Z")
	carveWorkspaceAt(t, db, "z-live", live, 1, "2026-09-20T00:00:00Z")

	// The shared rule, observed where runRoot reads it.
	insertTask(t, db, "T-probe", taskOpts{})
	cands, err := s.candidates()
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %d (%v), want 1", len(cands), err)
	}
	if cands[0].WorkspaceRoot != live {
		t.Fatalf("candidate WorkspaceRoot (runRoot's input) = %q, want the live %q", cands[0].WorkspaceRoot, live)
	}
	if _, err := db.Exec(`DELETE FROM tasks`); err != nil {
		t.Fatal(err)
	}

	id := insertTask(t, db, "T-42", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	if !strings.HasPrefix(dir, live+string(filepath.Separator)) {
		t.Fatalf("micro-plan landed outside the live workspace.\n got: %q\nwant under: %q", dir, live)
	}
}

// workspaces.root_path comes straight from the DB; a mapping that points
// OUTSIDE the configured workspace root must never be written to.
func TestMintMicroPlan_RefusesAWorkspaceOutsideTheRoot(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()

	outside := filepath.Join(t.TempDir(), "elsewhere")
	carveWorkspace(t, db, "elsewhere", outside, 1)

	id := insertTask(t, db, "T-42", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	if strings.HasPrefix(dir, outside+string(filepath.Separator)) {
		t.Fatalf("micro-plan minted outside the workspace root: %q", dir)
	}
	if want := filepath.Join(s.WorkspaceRoot, "p"); !strings.HasPrefix(dir, want+string(filepath.Separator)) {
		t.Fatalf("fallback not used.\n got: %q\nwant under: %q", dir, want)
	}
}

// A mapped namespace that no longer exists on disk is not recreated from a DB
// row; the mint falls back to the default location.
func TestMintMicroPlan_FallsBackWhenTheMappedWorkspaceIsGone(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{root: t.TempDir()})
	s.WorkspaceRoot = t.TempDir()

	gone := filepath.Join(s.WorkspaceRoot, "removed-namespace")
	if _, err := db.Exec(
		`INSERT INTO workspaces(slug, root_path, project_id, last_scanned) VALUES('removed-namespace', ?, 1, '2026-09-19T00:00:00Z')`,
		gone); err != nil {
		t.Fatal(err)
	}

	id := insertTask(t, db, "T-42", taskOpts{})
	s.Schedule()
	waitFor(t, func() bool { return column(t, db, id) != "todo" })

	dir := workspaceDirOf(t, s, id)
	if strings.HasPrefix(dir, gone+string(filepath.Separator)) {
		t.Fatalf("micro-plan minted into a namespace that no longer exists: %q", dir)
	}
	if want := filepath.Join(s.WorkspaceRoot, "p"); !strings.HasPrefix(dir, want+string(filepath.Separator)) {
		t.Fatalf("fallback not used.\n got: %q\nwant under: %q", dir, want)
	}
}
