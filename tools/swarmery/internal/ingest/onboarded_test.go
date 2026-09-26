package ingest

import (
	"database/sql"
	"testing"
)

const onboardTS = "2026-09-19T12:00:00.000Z"

func slugOf(t *testing.T, db *sql.DB, path string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT slug FROM projects WHERE path = ?`, path).Scan(&s); err != nil {
		t.Fatalf("slug for %s: %v", path, err)
	}
	return s
}

// The common case: onboarding runs BEFORE any session, so there is no row yet
// and the project must be minted already carrying the operator's slug.
func TestSetOnboardedSlug_MintsRowWhenUnseen(t *testing.T) {
	db := testDB(t)
	out, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS)
	if err != nil {
		t.Fatalf("SetOnboardedSlug: %v", err)
	}
	if out != SlugSet {
		t.Fatalf("outcome = %v, want SlugSet", out)
	}
	if got := slugOf(t, db, "/home/dev/skygor"); got != "skygor" {
		t.Fatalf("slug = %q, want %q", got, "skygor")
	}
}

// The ingester saw a session first and minted the row under the path-derived
// slug. Onboarding must NOT rename it: projects.slug names the project's
// worktree folders and CanonicalProjectPath / phaserun adopt resolve by it, so a
// rename would orphan every worktree cut before it. The row keeps its slug and
// is not forked.
func TestSetOnboardedSlug_KeepsAnExistingRowsSlug(t *testing.T) {
	db := testDB(t)
	if _, _, err := UpsertProject(db, "/home/dev/skygor", onboardTS, onboardTS); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := slugOf(t, db, "/home/dev/skygor"); got != "-home-dev-skygor" {
		t.Fatalf("precondition: slug = %q, want the path-derived one", got)
	}

	out, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS)
	if err != nil {
		t.Fatalf("SetOnboardedSlug: %v", err)
	}
	if out != SlugKept {
		t.Fatalf("outcome = %v, want SlugKept", out)
	}
	if got := slugOf(t, db, "/home/dev/skygor"); got != "-home-dev-skygor" {
		t.Fatalf("slug = %q, want the existing %q — renamed in place", got, "-home-dev-skygor")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 1 {
		t.Fatalf("projects = %d, want 1 — the row was forked", n)
	}
	// A worktree cut under the existing slug must still resolve to the project.
	prev := worktreeRootOverride
	worktreeRootOverride = "/tmp/worktrees"
	t.Cleanup(func() { worktreeRootOverride = prev })
	wt := "/tmp/worktrees/-home-dev-skygor/task-1"
	if got := CanonicalProjectPath(db, wt); got != "/home/dev/skygor" {
		t.Fatalf("CanonicalProjectPath(%s) = %q, want the project path", wt, got)
	}
}

// Re-onboarding is idempotent: the files step is, and so must the registry be.
func TestSetOnboardedSlug_UnchangedOnRerun(t *testing.T) {
	db := testDB(t)
	if _, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS); err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if out != SlugUnchanged {
		t.Fatalf("outcome = %v, want SlugUnchanged", out)
	}
}

// projects.slug carries no unique index, so a second claimant must be refused
// rather than allowed to create two rows answering to one name — the state that
// made by-slug resolution a coin flip in the first place.
func TestSetOnboardedSlug_RefusesToStealAnotherProjectsSlug(t *testing.T) {
	db := testDB(t)
	if _, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := SetOnboardedSlug(db, "/home/dev/other-skygor", "skygor", onboardTS)
	if err != nil {
		t.Fatalf("SetOnboardedSlug: %v", err)
	}
	if out != SlugConflict {
		t.Fatalf("outcome = %v, want SlugConflict", out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE slug = 'skygor'`); n != 1 {
		t.Fatalf("rows claiming 'skygor' = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = '/home/dev/other-skygor'`); n != 0 {
		t.Fatalf("a conflicted onboarding minted a row anyway (%d)", n)
	}
}

// A prior session under an ANCESTOR of the onboarded path (e.g. the project's
// parent dir) left that ancestor with its own projects row. UpsertProject's
// ordinary exact-miss behaviour would canonicalize the onboarded path to that
// ancestor and hand back its id — which would make SetOnboardedSlug rename the
// unrelated ancestor row instead of minting one for the directory actually
// onboarded. It must mint a row for the exact onboarded path instead.
func TestSetOnboardedSlug_DoesNotStealAnAncestorsRow(t *testing.T) {
	db := testDB(t)
	if _, _, err := UpsertProject(db, "/home/dev", onboardTS, onboardTS); err != nil {
		t.Fatalf("seed ancestor: %v", err)
	}

	out, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS)
	if err != nil {
		t.Fatalf("SetOnboardedSlug: %v", err)
	}
	if out != SlugSet {
		t.Fatalf("outcome = %v, want SlugSet", out)
	}
	if got := slugOf(t, db, "/home/dev/skygor"); got != "skygor" {
		t.Fatalf("slug for the onboarded path = %q, want %q", got, "skygor")
	}
	if got := slugOf(t, db, "/home/dev"); got == "skygor" {
		t.Fatalf("the ancestor row was renamed to the onboarded slug instead of a new row being minted")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 2 {
		t.Fatalf("projects = %d, want 2 (ancestor + onboarded)", n)
	}
}

// The slug set here must survive later ingest passes — UpsertProject heals name
// but must never rewrite slug.
func TestSetOnboardedSlug_SurvivesLaterIngest(t *testing.T) {
	db := testDB(t)
	if _, err := SetOnboardedSlug(db, "/home/dev/skygor", "skygor", onboardTS); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if _, _, err := UpsertProject(db, "/home/dev/skygor", onboardTS, onboardTS); err != nil {
		t.Fatalf("later ingest: %v", err)
	}
	if got := slugOf(t, db, "/home/dev/skygor"); got != "skygor" {
		t.Fatalf("slug = %q after ingest, want %q", got, "skygor")
	}
}

func TestSetOnboardedSlug_RejectsEmptyArgs(t *testing.T) {
	db := testDB(t)
	if _, err := SetOnboardedSlug(db, "", "skygor", onboardTS); err == nil {
		t.Fatal("expected an error for an empty path")
	}
	if _, err := SetOnboardedSlug(db, "/home/dev/skygor", "", onboardTS); err == nil {
		t.Fatal("expected an error for an empty slug")
	}
}
