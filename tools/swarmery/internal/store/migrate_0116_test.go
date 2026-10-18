package store

import "testing"

// TestMigrate0116AddsSessionPlan: the terminal plan-choice table lands keyed one
// row per session uuid, with the task index, and chosen_by / task_id / at are
// required while phase_id may be NULL ("the plan's active phase").
func TestMigrate0116AddsSessionPlan(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 116`).Scan(&name); err != nil {
		t.Fatalf("migration 116 not recorded: %v", err)
	}
	if name != "0116_session_plan.sql" {
		t.Errorf("migration 116 name: want 0116_session_plan.sql, got %s", name)
	}
	mustHaveColumns(t, db, "session_plan", "session_id", "task_id", "phase_id", "chosen_by", "at")

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_session_plan_task'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("idx_session_plan_task missing")
	}

	ins := `INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at)
		VALUES ('s-1', '2026-10-13-x', ?, 'terminal', '2026-10-14T00:00:00.000Z')`
	if _, err := db.Exec(ins, nil); err != nil {
		t.Fatalf("insert with NULL phase_id: %v", err)
	}
	if _, err := db.Exec(ins, "7"); err == nil {
		t.Fatal("second row for the same session succeeded; want a PRIMARY KEY conflict")
	}
	for _, q := range []string{
		`INSERT INTO session_plan (session_id, phase_id, chosen_by, at) VALUES ('s-2', NULL, 'auto', '2026-10-14T00:00:00.000Z')`,
		`INSERT INTO session_plan (session_id, task_id, phase_id, at) VALUES ('s-3', 't', NULL, '2026-10-14T00:00:00.000Z')`,
		`INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by) VALUES ('s-4', 't', NULL, 'auto')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s succeeded; want NOT NULL", q)
		}
	}
}
