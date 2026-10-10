package store

import "testing"

// TestMigrate0104AddsPhaseReopens: the reopen ledger lands with both indexes,
// takes a row, and refuses a caught_by outside its vocabulary.
func TestMigrate0104AddsPhaseReopens(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 104`).Scan(&name); err != nil {
		t.Fatalf("migration 104 not recorded: %v", err)
	}
	if name != "0104_phase_reopens.sql" {
		t.Errorf("migration 104 name: want 0104_phase_reopens.sql, got %s", name)
	}
	mustHaveColumns(t, db, "phase_reopens",
		"id", "phase_id", "workspace_task_id", "doc_path", "reason", "fix_url",
		"caught_by", "criteria_json", "created_at")
	for _, idx := range []string{"idx_phase_reopens_phase", "idx_phase_reopens_created"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).
			Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s missing", idx)
		}
	}

	if _, err := db.Exec(`INSERT INTO phase_reopens
		(phase_id, workspace_task_id, doc_path, reason, caught_by, created_at)
		VALUES (7, '3', '/plan/phase-1.md', 'regressed', 'operator', '2026-10-09T12:00:00Z')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var fixURL, criteria string
	if err := db.QueryRow(`SELECT fix_url, criteria_json FROM phase_reopens WHERE phase_id = 7`).
		Scan(&fixURL, &criteria); err != nil {
		t.Fatal(err)
	}
	if fixURL != "" || criteria != "[]" {
		t.Errorf("defaults = %q/%q, want ''/'[]'", fixURL, criteria)
	}
	if _, err := db.Exec(`INSERT INTO phase_reopens
		(phase_id, workspace_task_id, doc_path, reason, caught_by, created_at)
		VALUES (7, '3', '/plan/phase-1.md', 'x', 'nobody', '2026-10-09T12:00:00Z')`); err == nil {
		t.Error("caught_by 'nobody' accepted; want a CHECK failure")
	}
}
