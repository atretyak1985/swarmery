package store

import "testing"

// operatorCorrectionsVersion finds the operator_corrections migration by NAME,
// so a renumber never breaks these tests.
func operatorCorrectionsVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_operator_corrections.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the operator_corrections migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateOperatorCorrectionsFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustHaveColumns(t, db, "operator_corrections",
		"id", "source", "ref", "project_id", "before", "after", "reason", "norm_key", "created_at")
	var idx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name = 'idx_operator_corrections_key' AND tbl_name = 'operator_corrections'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Error("idx_operator_corrections_key is missing")
	}
	// No foreign keys (0073): ref is a typed text pointer, never an FK.
	var fks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('operator_corrections')`).Scan(&fks); err != nil {
		t.Fatal(err)
	}
	if fks != 0 {
		t.Errorf("operator_corrections declares %d foreign keys, want 0", fks)
	}
}

// On a populated DB: absent before, present after; the source CHECK holds,
// project_id may be NULL, ref is NOT NULL; a re-run is a no-op.
func TestMigrateOperatorCorrectionsOnPopulatedDB(t *testing.T) {
	v := operatorCorrectionsVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if len(columnSet(t, db, "operator_corrections")) != 0 {
		t.Fatal("operator_corrections exists before its migration")
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ins := func(source string, ref any) error {
		_, err := db.Exec(`INSERT INTO operator_corrections (source, ref, project_id, reason, norm_key, created_at)
			VALUES (?, ?, NULL, 'r', 'r', '2026-10-07T00:00:00.000Z')`, source, ref)
		return err
	}
	if err := ins("lesson_edit", "lesson:1"); err != nil {
		t.Fatalf("insert (NULL project_id): %v", err)
	}
	if err := ins("lesson_poke", "lesson:2"); err == nil {
		t.Error("a source outside the vocabulary was accepted — CHECK missing")
	}
	if err := ins("triage_undo", nil); err == nil {
		t.Error("a NULL ref was accepted — NOT NULL missing")
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
