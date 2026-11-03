package store

import "testing"

// TestMigrate0134LessonConfidence: surprise_lessons grows confidence (default
// 'medium', CHECKed) and confidence_reason; lesson_generations grows
// no_entry_reason. A row written without the new columns — every row that
// existed before the migration — reads as 'medium'.
func TestMigrate0134LessonConfidence(t *testing.T) {
	db, err := Open(t.TempDir() + "/m134.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 134`).Scan(&name); err != nil {
		t.Fatalf("migration 134 not recorded: %v", err)
	}
	if name != "0134_lesson_confidence.sql" {
		t.Errorf("migration 134 name = %s", name)
	}
	const ins = `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, guidance, created_at, updated_at)
		VALUES (?, 1, 1, 't', 'g', '2026-11-02T00:00:00Z', '2026-11-02T00:00:00Z')`
	if _, err := db.Exec(ins, "run-old"); err != nil {
		t.Fatal(err)
	}
	var conf, why string
	if err := db.QueryRow(`SELECT confidence, confidence_reason FROM surprise_lessons WHERE source_phase_run = 'run-old'`).
		Scan(&conf, &why); err != nil {
		t.Fatal(err)
	}
	if conf != "medium" || why != "" {
		t.Errorf("default = (%q, %q), want (medium, \"\")", conf, why)
	}
	for _, c := range []string{"high", "low"} {
		if _, err := db.Exec(`UPDATE surprise_lessons SET confidence = ? WHERE source_phase_run = 'run-old'`, c); err != nil {
			t.Errorf("confidence %q refused: %v", c, err)
		}
	}
	if _, err := db.Exec(`UPDATE surprise_lessons SET confidence = 'certain' WHERE source_phase_run = 'run-old'`); err == nil {
		t.Error("confidence 'certain' accepted")
	}
	if _, err := db.Exec(`INSERT INTO lesson_generations (source_phase_run, phase_id, state, created_at, no_entry_reason)
		VALUES ('run-old', 1, 'done', '2026-11-02T00:00:00Z', 'nothing transferable')`); err != nil {
		t.Fatalf("no_entry_reason: %v", err)
	}
}
