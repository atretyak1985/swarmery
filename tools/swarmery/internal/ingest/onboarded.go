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
	// SlugSet — the project row now carries the onboarding slug.
	SlugSet SlugOutcome = iota
	// SlugUnchanged — the row already carried it.
	SlugUnchanged
	// SlugConflict — a DIFFERENT project already answers to that slug, so
	// nothing was written. projects.slug has no unique index (projects.path is
	// the unique key), so stealing it would leave two rows with one name and
	// make every by-slug lookup a coin flip — the exact failure this whole
	// alignment exists to remove.
	SlugConflict
)

// SetOnboardedSlug aligns the registry slug of the project at path with the
// slug onboarding just used, minting the project row when the ingester has not
// seen a session there yet (the common case: onboarding usually runs BEFORE the
// first session).
//
// Safe to re-run: UpsertProject never rewrites slug after INSERT, and no other
// code path updates the column, so the value set here survives every later
// ingest pass.
func SetOnboardedSlug(q dbtx, path, slug, now string) (SlugOutcome, error) {
	if path == "" || slug == "" {
		return SlugConflict, fmt.Errorf("ingest: path and slug are required")
	}

	var ownerPath string
	err := q.QueryRow(`SELECT path FROM projects WHERE slug = ?`, slug).Scan(&ownerPath)
	switch {
	case err == nil && ownerPath != path:
		return SlugConflict, nil
	case err != nil && err != sql.ErrNoRows:
		return SlugConflict, err
	}

	id, err := upsertExactProject(q, path, now, now)
	if err != nil {
		return SlugConflict, err
	}

	res, err := q.Exec(`UPDATE projects SET slug = ? WHERE id = ? AND slug <> ?`, slug, id, slug)
	if err != nil {
		return SlugConflict, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SlugUnchanged, nil
	}
	return SlugSet, nil
}

// upsertExactProject resolves or creates the projects row for path, WITHOUT
// UpsertProject's ancestor-canonicalization fallback (CanonicalProjectPath).
// That fallback exists to attribute a session's satellite cwd (a dispatcher
// worktree, an in-repo subdirectory) to its parent project during ingest — but
// onboarding names one specific directory, and reusing the fallback here would
// let a pre-existing ancestor row (e.g. a project umbrella dir the ingester
// already saw a session under) get renamed to the onboarded slug instead of a
// row for the directory actually onboarded ever being created.
func upsertExactProject(q dbtx, path, firstSeen, lastActivity string) (int64, error) {
	var id int64
	err := q.QueryRow(`SELECT id FROM projects WHERE path = ?`, path).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case err != sql.ErrNoRows:
		return 0, err
	}
	res, err := q.Exec(
		`INSERT INTO projects (path, slug, name, first_seen, last_activity) VALUES (?, ?, ?, ?, ?)`,
		path, SlugForPath(path), projectNameFor(path), firstSeen, lastActivity)
	if err != nil {
		return 0, fmt.Errorf("insert project: %w", err)
	}
	id, _ = res.LastInsertId()
	return id, nil
}
