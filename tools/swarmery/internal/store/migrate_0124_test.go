package store

import "testing"

// TestMigrate0124AddsGuardJSON: both guard_json columns land with the '{}'
// default, so rows written before the guard check read as "no findings".
func TestMigrate0124AddsGuardJSON(t *testing.T) {
	db, err := Open(t.TempDir() + "/m124.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 124`).Scan(&name); err != nil {
		t.Fatalf("migration 124 not recorded: %v", err)
	}
	if name != "0124_phase_reviews_guard.sql" {
		t.Errorf("migration 124 name: want 0124_phase_reviews_guard.sql, got %s", name)
	}
	if _, err := db.Exec(`INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, verdict, started_at)
		VALUES ('phase', 1, '1', 'pass', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, status, created_at)
		VALUES (1, 'D-1', 1, 'stop', 'undecided', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"phase_reviews", "phase_decisions"} {
		var g string
		if err := db.QueryRow(`SELECT guard_json FROM ` + table).Scan(&g); err != nil {
			t.Fatalf("%s.guard_json: %v", table, err)
		}
		if g != "{}" {
			t.Errorf("%s.guard_json default = %q, want {}", table, g)
		}
	}
}
