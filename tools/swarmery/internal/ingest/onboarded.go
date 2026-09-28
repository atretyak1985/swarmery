package ingest

// Registration of an onboarded project's slug.
//
// The daemon carries TWO names for one project and upstream documents them as
// never matching (internal/onboard/onboard.go): the REGISTRY slug, which
// UpsertProject derives from the project path ('/'→'-') because the ingester
// discovers a project from a transcript cwd and has no config to consult; and
// the ONBOARDING slug, the operator's kebab name, which onboarding writes into
// .claude/project.json + settings.json env.AGENT_PROJECT and carves the
// workspace namespace under.
//
// Onboarding used to write files and stop — it never touched the registry, so a
// project's row appeared only later, minted by the ingester under the derived
// name, and "onboarded" and "registered" were two unrelated events. This closes
// that gap: onboarding now names the project in the registry too, so the
// dashboard, the workspace namespace and AGENT_PROJECT all agree.

import (
	"database/sql"
	"fmt"
)

// SlugOutcome reports what SetOnboardedSlug did, so the caller can put an
// honest line in the onboarding step list rather than claiming success.
type SlugOutcome int

const (
	// SlugSet — a NEW project row was minted carrying the onboarding slug.
	SlugSet SlugOutcome = iota
	// SlugUnchanged — the existing row already carried it.
	SlugUnchanged
	// SlugConflict — a DIFFERENT project already answers to that slug, so
	// nothing was written. projects.slug has no unique index (projects.path is
	// the unique key), so stealing it would leave two rows with one name and
	// make every by-slug lookup a coin flip — the exact failure this whole
	// alignment exists to remove.
	SlugConflict
	// SlugKept — a row for this path already existed under a different slug and
	// KEEPS it. projects.slug is project identity, not a display label: worktree
	// folders are named <worktrees>/<projects.slug>/<task>, and both
	// CanonicalProjectPath (which resolves a worktree cwd by that folder name)
	// and phaserun's adopt path (which rebuilds a worktree path from
	// ProjectSlug) look rows up by it. Renaming in place would orphan every
	// worktree cut before the rename — their sessions would mint phantom
	// project rows, and an in-flight phase run could not be re-adopted after a
	// daemon restart.
	SlugKept
)

// SetOnboardedSlug registers the project at path under the slug onboarding just
// used, minting the project row when the ingester has not seen a session there
// yet (the common case: onboarding usually runs BEFORE the first session).
//
// It only ever sets the slug on a row it MINTS. An existing row keeps its slug
// (SlugKept) — see SlugKept for why a rename is unsafe.
//
// Safe to re-run: UpsertProject never rewrites slug after INSERT, and no other
// code path updates the column, so the value set here survives every later
// ingest pass.
func SetOnboardedSlug(q dbtx, path, slug, now string) (SlugOutcome, error) {
	if path == "" || slug == "" {
		return SlugConflict, fmt.Errorf("ingest: path and slug are required")
	}

	// An existing row for this exact path is never renamed.
	var existing string
	err := q.QueryRow(`SELECT slug FROM projects WHERE path = ?`, path).Scan(&existing)
	switch {
	case err == nil && existing == slug:
		return SlugUnchanged, nil
	case err == nil:
		return SlugKept, nil
	case err != sql.ErrNoRows:
		return SlugConflict, err
	}

	var ownerPath string
	err = q.QueryRow(`SELECT path FROM projects WHERE slug = ?`, slug).Scan(&ownerPath)
	switch {
	case err == nil:
		return SlugConflict, nil
	case err != sql.ErrNoRows:
		return SlugConflict, err
	}

	// Insert by the exact path, WITHOUT UpsertProject's ancestor-
	// canonicalization fallback (CanonicalProjectPath). That fallback exists to
	// attribute a session's satellite cwd (a dispatcher worktree, an in-repo
	// subdirectory) to its parent project during ingest — but onboarding names
	// one specific directory, and a pre-existing ancestor row must not stand in
	// for it.
	if _, err := q.Exec(
		`INSERT INTO projects (path, slug, name, first_seen, last_activity) VALUES (?, ?, ?, ?, ?)`,
		path, slug, projectNameFor(path), now, now); err != nil {
		return SlugConflict, fmt.Errorf("insert project: %w", err)
	}
	return SlugSet, nil
}
