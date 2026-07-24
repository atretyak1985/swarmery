package store

import "testing"

// TestMigrate0029FreshDB verifies 0029 creates project_permission_presets with
// its fail-closed default, adds approval_rules.source (default 'manual'), and
// enforces the preset CHECK — on a brand-new database.
func TestMigrate0029FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 29`).Scan(&name); err != nil {
		t.Fatalf("migration 29 not recorded: %v", err)
	}
	if name != "0029_permission_presets.sql" {
		t.Errorf("migration 29 name: want 0029_permission_presets.sql, got %s", name)
	}

	mustHaveColumns(t, db, "project_permission_presets",
		"project_id", "preset", "overrides", "updated_at")
	mustHaveColumns(t, db, "approval_rules", "source")
	mustHaveIndex(t, db, "idx_approval_rules_source")

	// A project row is required for the FK.
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/x', 'x', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Fail-closed default: a minimal insert lands 'approval-required' with '{}'.
	if _, err := db.Exec(`INSERT INTO project_permission_presets (project_id, updated_at)
		VALUES (1, '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert minimal preset: %v", err)
	}
	var preset, overrides string
	if err := db.QueryRow(`SELECT preset, overrides FROM project_permission_presets
		WHERE project_id = 1`).Scan(&preset, &overrides); err != nil {
		t.Fatal(err)
	}
	if preset != "approval-required" {
		t.Errorf("default preset = %q, want approval-required (fail closed)", preset)
	}
	if overrides != "{}" {
		t.Errorf("default overrides = %q, want {}", overrides)
	}

	// preset CHECK rejects an unknown value (a bad write must be a hard error,
	// never a silent fail-open).
	if _, err := db.Exec(`UPDATE project_permission_presets SET preset = 'wide-open' WHERE project_id = 1`); err == nil {
		t.Error("expected CHECK violation for preset='wide-open', got nil")
	}

	// Existing approval_rules rows backfill to source='manual'.
	if _, err := db.Exec(`INSERT INTO approval_rules (tool_pattern, created_at)
		VALUES ('Read', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	var source string
	if err := db.QueryRow(`SELECT source FROM approval_rules WHERE tool_pattern = 'Read'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "manual" {
		t.Errorf("default rule source = %q, want manual", source)
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}
