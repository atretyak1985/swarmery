package ingest

// projects.slug carries a UNIQUE index (store migration 0091). Two distinct
// paths can derive the same slug — SlugForPath only maps '/'→'-', so "/a/b"
// and "/a-b" both become "-a-b" — and without freeSlug that second INSERT
// would fail outright, turning routine ingest of a new cwd into a hard error.

import "testing"

func TestUpsertProjectSuffixesAColliddingDerivedSlug(t *testing.T) {
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
