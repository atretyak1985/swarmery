package ingest

// projects.slug carries a UNIQUE index (store migration projects_slug_unique).
// Two distinct paths can derive the same slug — SlugForPath only maps '/'→'-',
// so "/a/b" and "/a-b" both become "-a-b" — and without freeSlug that second
// INSERT would fail outright, turning routine ingest of a new cwd into a hard
// error.

import "testing"

func TestUpsertProjectSuffixesACollidingDerivedSlug(t *testing.T) {
	db := testDB(t)
	seedProject(t, db, "/a/b") // slug "-a-b"

	id, created, err := UpsertProject(db, "/a-b", "2026-07-01T00:00:00.000Z", "")
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if !created {
		t.Fatal("second distinct path must mint its own row, not reuse the first")
	}
	var slug string
	if err := db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, id).Scan(&slug); err != nil {
		t.Fatalf("read slug: %v", err)
	}
	if slug != "-a-b-2" {
		t.Errorf("colliding derived slug got %q, want the suffixed %q", slug, "-a-b-2")
	}
}

func TestUpsertProjectLeavesDistinctSlugsAlone(t *testing.T) {
	db := testDB(t)
	seedProject(t, db, "/a")
	id, _, err := UpsertProject(db, "/b", "2026-07-01T00:00:00.000Z", "")
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	var slug string
	if err := db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, id).Scan(&slug); err != nil {
		t.Fatalf("read slug: %v", err)
	}
	if slug != "-b" {
		t.Errorf("unrelated path got suffixed: %q", slug)
	}
}

// A row for the path that already exists is returned as it is — never a second
// row, never an error — so a writer that raced another to the same path gets
// that writer's row.
func TestInsertProjectReturnsTheExistingRowForAPath(t *testing.T) {
	db := testDB(t)
	first, created, err := InsertProject(db, "/a", "a", "a", "2026-07-01T00:00:00.000Z", nil)
	if err != nil || !created {
		t.Fatalf("first insert: id=%d created=%v err=%v", first, created, err)
	}
	again, created, err := InsertProject(db, "/a", "a", "a", "2026-07-02T00:00:00.000Z", nil)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created || again != first {
		t.Errorf("second insert = (%d, created=%v), want the existing row %d", again, created, first)
	}
}

// The retry keys off the slug index's own error, and nothing else.
func TestIsSlugConflict(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/a', 'same', 'x')`); err != nil {
		t.Fatal(err)
	}
	_, slugErr := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/b', 'same', 'x')`)
	if slugErr == nil || !isSlugConflict(slugErr) {
		t.Errorf("duplicate slug: isSlugConflict(%v) = false, want true", slugErr)
	}
	_, pathErr := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/a', 'other', 'x')`)
	if pathErr == nil || isSlugConflict(pathErr) {
		t.Errorf("duplicate path: isSlugConflict(%v) = true, want false", pathErr)
	}
}
