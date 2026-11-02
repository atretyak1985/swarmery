package store

import "testing"

// TestMigrate0133LessonEffectVerdict: verdict and confounders_json land on
// lesson_effectiveness with defaults that read as "cannot tell yet".
func TestMigrate0133LessonEffectVerdict(t *testing.T) {
	db, err := Open(t.TempDir() + "/m133.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 133`).Scan(&name); err != nil {
		t.Fatalf("migration 133 not recorded: %v", err)
	}
	if name != "0133_lesson_effect_verdict.sql" {
		t.Errorf("migration 133 name = %s", name)
	}
	if _, err := db.Exec(`INSERT INTO lesson_effectiveness (lesson_id, window_n, min_runs, computed_at)
		VALUES (1, 10, 5, '2026-11-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var verdict, conf string
	if err := db.QueryRow(`SELECT verdict, confounders_json FROM lesson_effectiveness WHERE lesson_id = 1`).
		Scan(&verdict, &conf); err != nil {
		t.Fatal(err)
	}
	if verdict != "insufficient" || conf != "[]" {
		t.Errorf("defaults: verdict=%q confounders=%q", verdict, conf)
	}
}
