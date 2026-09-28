package api

// Onboarding must REGISTER the project, not just write its files. Before this,
// onboarding wrote .claude/ + carved the workspace and stopped; the registry row
// appeared only later, minted by the ingester under its path-derived slug — so
// the dashboard named the project one thing while project.json, AGENT_PROJECT
// and the carved namespace named it another.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// onboardServerWithDB is onboardServer plus a handle on the store, so a test can
// assert what onboarding wrote to the registry.
func onboardServerWithDB(t *testing.T, cfg OnboardConfig) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "onboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	AttachOnboard(cfg)
	t.Cleanup(func() { AttachOnboard(OnboardConfig{}) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
}

// resolvedTempDir is t.TempDir() with symlinks resolved. resolveUnderRoots
// stores the symlink-resolved target (on macOS /var → /private/var), so a test
// that looks the row up by the unresolved path would find nothing.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}

func onboardDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func steps(out map[string]any) string {
	raw, _ := out["steps"].([]any)
	var b strings.Builder
	for _, s := range raw {
		if str, ok := s.(string); ok {
			b.WriteString(str)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestOnboardRegistersProjectUnderTheOnboardingSlug(t *testing.T) {
	root, ws := resolvedTempDir(t), resolvedTempDir(t)
	proj := onboardDir(t, root, "my-project")
	srv, db := onboardServerWithDB(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: ws})

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "my-project", "path": proj}, http.StatusCreated)

	var slug string
	if err := db.QueryRow(`SELECT slug FROM projects WHERE path = ?`, proj).Scan(&slug); err != nil {
		t.Fatalf("onboarding wrote no project row: %v", err)
	}
	if slug != "my-project" {
		t.Fatalf("registry slug = %q, want %q (the path-derived name leaked through)", slug, "my-project")
	}
	if !strings.Contains(steps(out), "registered in swarmery") {
		t.Errorf("registration not reported in steps:\n%s", steps(out))
	}
}

// Re-onboarding a project the ingester already registered must NOT rename its
// row: projects.slug names the project's worktree folders
// (<worktrees>/<projects.slug>/<task>), and both CanonicalProjectPath and
// phaserun's adopt path resolve by it — a rename would orphan every worktree cut
// before it. The row keeps its slug, stays the one and only row for the path,
// and onboarding still succeeds and says so.
func TestOnboardKeepsAnExistingRowsSlug(t *testing.T) {
	root, ws := resolvedTempDir(t), resolvedTempDir(t)
	proj := onboardDir(t, root, "my-project")
	srv, db := onboardServerWithDB(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: ws})

	// The ingester got there first, as it does whenever a session ran before
	// onboarding.
	seededID, _, err := ingest.UpsertProject(db, proj, "2026-09-19T00:00:00.000Z", "2026-09-19T00:00:00.000Z")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var seededSlug, seededName string
	if err := db.QueryRow(`SELECT slug, name FROM projects WHERE id = ?`, seededID).Scan(&seededSlug, &seededName); err != nil {
		t.Fatal(err)
	}
	if seededSlug == "my-project" {
		t.Fatalf("precondition: seeded slug already equals the onboarding slug")
	}

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "my-project", "path": proj}, http.StatusCreated)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("projects = %d, want 1 — onboarding forked the row", n)
	}
	var id int64
	var slug, name string
	if err := db.QueryRow(`SELECT id, slug, name FROM projects WHERE path = ?`, proj).Scan(&id, &slug, &name); err != nil {
		t.Fatal(err)
	}
	if id != seededID {
		t.Fatalf("row id = %d, want the seeded %d", id, seededID)
	}
	if slug != seededSlug {
		t.Fatalf("slug = %q, want the existing %q — onboarding renamed project identity in place", slug, seededSlug)
	}
	if name != seededName {
		t.Errorf("name = %q, want %q", name, seededName)
	}
	if !strings.Contains(steps(out), "keeps its existing slug") {
		t.Errorf("kept slug not reported in steps:\n%s", steps(out))
	}
	// The files half (the rest of onboarding) still happened.
	if _, err := os.Stat(filepath.Join(proj, ".claude", "settings.json")); err != nil {
		t.Errorf("onboarding files not written: %v", err)
	}
}

// A slug already claimed by a DIFFERENT project must not be stolen —
// projects.slug carries no unique index, and two rows sharing one name is the
// state that made by-slug resolution a coin flip. Onboarding still succeeds:
// the files on disk are correct and are the point of the call.
func TestOnboardReportsSlugConflictWithoutStealingIt(t *testing.T) {
	root, ws := resolvedTempDir(t), resolvedTempDir(t)
	first := onboardDir(t, root, "taken")
	second := onboardDir(t, root, "second")
	srv, db := onboardServerWithDB(t, OnboardConfig{Roots: []string{root}, WorkspaceRoot: ws})

	doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "taken", "path": first}, http.StatusCreated)

	out := doJSON(t, http.MethodPost, srv.URL+"/api/projects/onboard",
		map[string]any{"slug": "taken", "path": second}, http.StatusCreated)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE slug = 'taken'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows claiming 'taken' = %d, want 1", n)
	}
	if !strings.Contains(steps(out), "already answers to") {
		t.Errorf("conflict not reported in steps:\n%s", steps(out))
	}
	// The files half still has to have happened.
	if _, err := os.Stat(filepath.Join(second, ".claude", "settings.json")); err != nil {
		t.Errorf("conflict aborted the file writes: %v", err)
	}
}
