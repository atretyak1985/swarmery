package store

import "testing"

// TestMigrate0067FreshDB verifies the wizard model column is registered.
func TestMigrate0067FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 67`).Scan(&name); err != nil {
		t.Fatalf("migration 67 not recorded: %v", err)
	}
	if name != "0067_planning_model.sql" {
		t.Errorf("migration 67 name: want 0067_planning_model.sql, got %s", name)
	}
	mustHaveColumns(t, db, "planning_sessions", "model")
}

// TestMigrate0067OnPopulatedDB: a pre-0067 wizard row survives and reads NULL —
// "the planner default", which is the model every historical wizard started on.
func TestMigrate0067OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 66)

	if columnSet(t, db, "planning_sessions")["model"] {
		t.Fatal("model exists before 0067 — migrateUpTo applied too much")
	}
	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO planning_sessions (id, project_id, session_uuid, status, idea, created_at, updated_at)
		 VALUES (1, 1, 'uuid-pre-0067', 'awaiting_answer', 'an idea', '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert planning session: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}
	mustHaveColumns(t, db, "planning_sessions", "model")

	var model *string
	if err := db.QueryRow(`SELECT model FROM planning_sessions WHERE id = 1`).Scan(&model); err != nil {
		t.Fatalf("read model: %v", err)
	}
	if model != nil {
		t.Errorf("model = %q, want NULL for a pre-0067 row", *model)
	}
}
