package store

import "testing"

// TestMigrate0076AddsPlanningEffort: the wizard's depth column lands beside the
// model column 0067 added, for the same end-to-end-consistency reason.
func TestMigrate0076AddsPlanningEffort(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 76`).Scan(&name); err != nil {
		t.Fatalf("migration 76 not recorded: %v", err)
	}
	if name != "0076_planning_effort.sql" {
		t.Errorf("migration 76 name: want 0076_planning_effort.sql, got %s", name)
	}

	mustHaveColumns(t, db, "planning_sessions", "model", "effort")
}

// TestMigrate0076PreservesLegacyWizards is the data-safety case. A wizard row
// written before the column existed must come out with NULL — not "" and not a
// baked-in rung — because NULL is what makes planning.Service.Effort fall
// through to the planner's ladder. An empty string would be indistinguishable
// from it here, but a literal default written by the migration would freeze
// every historical wizard at whatever today's default happens to be.
func TestMigrate0076PreservesLegacyWizards(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 75)

	if _, err := db.Exec(`
		INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/p', '-p', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO planning_sessions (id, project_id, session_uuid, status, idea, mode, model, created_at, updated_at)
		VALUES (1, 1, 'legacy-uuid', 'awaiting_answer', 'an idea', 'plan', 'claude-opus-5-5',
		        '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy wizard: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	var model string
	var effort *string
	if err := db.QueryRow(`
		SELECT model, effort FROM planning_sessions WHERE id = 1`).Scan(&model, &effort); err != nil {
		t.Fatalf("read migrated wizard: %v", err)
	}
	if model != "claude-opus-5-5" {
		t.Errorf("model = %q, want claude-opus-5-5 (the pin must survive)", model)
	}
	if effort != nil {
		t.Errorf("effort = %v, want NULL on a pre-0076 row", *effort)
	}
}
