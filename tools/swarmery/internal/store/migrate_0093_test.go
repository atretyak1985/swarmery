package store

import "testing"

// The migration must run on a store that ALREADY holds the duplicate it exists
// to outlaw — that is the whole live case. Lowest id keeps the name; the loser
// is renamed, never deleted.
func TestMigrate0093DeduplicatesExistingSlugs(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 92)

	// The live shape: a real checkout and the workspace dir that shadowed it,
	// both answering to one path-derived slug.
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES
		   (1, '/home/u/Lab/app', '-home-u-Lab-app', '2026-09-01T00:00:00Z')`,
		`INSERT INTO projects (id, path, slug, first_seen) VALUES
		   (2, '/home/u/ws/-home-u-Lab-app', '-home-u-Lab-app', '2026-09-02T00:00:00Z')`,
		`INSERT INTO projects (id, path, slug, first_seen) VALUES
		   (3, '/home/u/other', 'other', '2026-09-03T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	var slug1, slug2, slug3 string
	row := func(id int, into *string) {
		t.Helper()
		if err := db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, id).Scan(into); err != nil {
			t.Fatalf("project %d gone after migrate: %v", id, err)
		}
	}
	row(1, &slug1)
	row(2, &slug2)
	row(3, &slug3)

	if slug1 != "-home-u-Lab-app" {
		t.Errorf("lowest id lost the name: %q", slug1)
	}
	if slug2 != "-home-u-Lab-app-dup2" {
		t.Errorf("duplicate renamed to %q, want %q", slug2, "-home-u-Lab-app-dup2")
	}
	if slug3 != "other" {
		t.Errorf("an unaffected row was renamed: %q", slug3)
	}
}

// The point of the migration: a second row claiming a taken slug must now be
// rejected by the store instead of silently making every by-slug lookup a coin
// flip.
func TestMigrate0093RejectsADuplicateAfterwards(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, first_seen) VALUES ('/a', 'same', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, first_seen) VALUES ('/b', 'same', '2026-09-01T00:00:00Z')`); err == nil {
		t.Fatal("a duplicate slug was accepted — the unique index is not in force")
	}
}

// Distinct paths must still be insertable; the index constrains slug, not path.
func TestMigrate0093LeavesDistinctSlugsAlone(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, p := range []struct{ path, slug string }{{"/a", "a"}, {"/b", "b"}} {
		if _, err := db.Exec(
			`INSERT INTO projects (path, slug, first_seen) VALUES (?, ?, '2026-09-01T00:00:00Z')`,
			p.path, p.slug); err != nil {
			t.Fatalf("insert %s: %v", p.slug, err)
		}
	}
}

// Running the migration when the index was already applied ad hoc under an
// earlier, unmerged migration number (this dev machine's real history) must
// not fail — IF NOT EXISTS makes it a no-op on the index, and the dedup UPDATE
// is naturally a no-op once there is nothing left to rename.
func TestMigrate0093IsIdempotentWhenIndexAlreadyExists(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 92)
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_projects_slug ON projects(slug)`); err != nil {
		t.Fatalf("simulate pre-applied index: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate onto a pre-applied index must not fail: %v", err)
	}
}
