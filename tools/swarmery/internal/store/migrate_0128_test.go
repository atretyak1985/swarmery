package store

import "testing"

const insLesson128 = `INSERT INTO surprise_lessons
	(source_phase_run, phase_id, seq, title, guidance, created_at, updated_at, source)
	VALUES (?, 1, 1, 't', 'g', '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z', ?)`

// TestMigrate0128LessonsFromReview: surprise_lessons gains source (CHECKed to the
// two producers, default 'surprise') and the review group identity; lesson_mutes
// lands with one row per (type, area); review_findings is indexed by type.
func TestMigrate0128LessonsFromReview(t *testing.T) {
	db, err := Open(t.TempDir() + "/m128.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 128`).Scan(&name); err != nil {
		t.Fatalf("migration 128 not recorded: %v", err)
	}
	if name != "0128_lessons_from_review.sql" {
		t.Errorf("migration 128 name = %s", name)
	}

	if _, err := db.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, guidance, created_at, updated_at)
		VALUES ('run-a', 1, 1, 't', 'g', '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var source, typ, area string
	if err := db.QueryRow(`SELECT source, review_type, review_area FROM surprise_lessons WHERE source_phase_run = 'run-a'`).
		Scan(&source, &typ, &area); err != nil {
		t.Fatal(err)
	}
	if source != "surprise" || typ != "" || area != "" {
		t.Errorf("defaults: source=%q review_type=%q review_area=%q, want surprise, '', ''", source, typ, area)
	}
	if _, err := db.Exec(insLesson128, "run-b", "review"); err != nil {
		t.Errorf("source 'review' refused: %v", err)
	}
	if _, err := db.Exec(insLesson128, "run-c", "model"); err == nil {
		t.Error("source 'model' accepted; the CHECK allows surprise|review")
	}

	const mute = `INSERT INTO lesson_mutes (type, area, muted_at, muted_until) VALUES (?, ?, '2026-10-22T00:00:00Z', '2026-11-21T00:00:00Z')`
	if _, err := db.Exec(mute, "correctness", "internal/api"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(mute, "correctness", "internal/api"); err == nil {
		t.Error("a second mute for the same (type, area) accepted")
	}
	if _, err := db.Exec(mute, "quality", "internal/api"); err != nil {
		t.Errorf("another type in the same area refused: %v", err)
	}

	var idx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_review_findings_type'`).
		Scan(&idx); err != nil || idx != 1 {
		t.Errorf("idx_review_findings_type present = %d, %v", idx, err)
	}
}

// TestMigrate0128OnAnExistingDB: a database built up to 0127 that already holds
// surprise lessons (a copied live DB) migrates, and every existing row reads as
// a surprise lesson.
func TestMigrate0128OnAnExistingDB(t *testing.T) {
	raw := openRaw(t)
	if err := MigrateUpTo(raw, 127); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, guidance, status, created_at, updated_at)
		VALUES ('run-old', 3, 1, 't', 'g', 'active', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(raw); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var source, status string
	if err := raw.QueryRow(`SELECT source, status FROM surprise_lessons WHERE source_phase_run = 'run-old'`).
		Scan(&source, &status); err != nil {
		t.Fatal(err)
	}
	if source != "surprise" || status != "active" {
		t.Errorf("after 0128: source=%q status=%q, want surprise and active", source, status)
	}
}
