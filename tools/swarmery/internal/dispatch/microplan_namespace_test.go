package dispatch

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// carveWorkspace registers a project's workspace namespace the way wsingest
// does — under the ONBOARDING slug, which is deliberately not the registry slug
// ('p') the test project carries.
func carveWorkspace(t *testing.T, db *sql.DB, slug, rootPath string, projectID int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO workspaces(slug, root_path, project_id, last_scanned)
		 VALUES(?, ?, ?, '2026-09-19T00:00:00Z')`, slug, rootPath, projectID); err != nil {
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
