package store

import "testing"

// TestMigrate0027FreshDB verifies 0027 creates the routines + routine_runs
// tables with their declared defaults and indexes on a brand-new database.
func TestMigrate0027FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 27`).Scan(&name); err != nil {
		t.Fatalf("migration 27 not recorded: %v", err)
	}
	if name != "0027_routines.sql" {
		t.Errorf("migration 27 name: want 0027_routines.sql, got %s", name)
	}

	mustHaveColumns(t, db, "routines",
		"id", "project_id", "name", "cron_expr", "enabled", "catch_up",
		"steps", "webhook_token", "timeout_sec", "created_at", "updated_at",
		"last_run_at", "next_run_at")
	mustHaveColumns(t, db, "routine_runs",
		"id", "routine_id", "trigger", "status", "detail", "started_at", "finished_at")
	mustHaveIndex(t, db, "idx_routines_due")
	mustHaveIndex(t, db, "idx_routine_runs")

	// A minimal insert lands with the declared defaults (global routine, no cron).
	if _, err := db.Exec(`INSERT INTO routines (id, name, created_at, updated_at)
		VALUES ('R-abc123', 'nightly', '2026-07-24T00:00:00.000Z', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert minimal routine: %v", err)
	}
	var enabled, timeout int
	var catchUp, steps string
	var projectID, cron any
	if err := db.QueryRow(`SELECT enabled, catch_up, steps, timeout_sec, project_id, cron_expr
		FROM routines WHERE id = 'R-abc123'`).Scan(&enabled, &catchUp, &steps, &timeout, &projectID, &cron); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Errorf("default enabled = %d, want 1", enabled)
	}
	if catchUp != "skip" {
		t.Errorf("default catch_up = %q, want skip", catchUp)
	}
	if steps != "[]" {
		t.Errorf("default steps = %q, want []", steps)
	}
	if timeout != 900 {
		t.Errorf("default timeout_sec = %d, want 900", timeout)
	}
	if projectID != nil {
		t.Errorf("default project_id = %v, want NULL (global)", projectID)
	}
	if cron != nil {
		t.Errorf("default cron_expr = %v, want NULL", cron)
	}

	// catch_up CHECK constraint rejects an unknown policy.
	if _, err := db.Exec(`INSERT INTO routines (id, name, catch_up, created_at, updated_at)
		VALUES ('R-bad', 'x', 'bogus', '2026-07-24T00:00:00.000Z', '2026-07-24T00:00:00.000Z')`); err == nil {
		t.Error("expected CHECK violation for catch_up='bogus', got nil")
	}

	// routine_runs FK + CHECKs: a valid run inserts; a bad status is rejected.
	if _, err := db.Exec(`INSERT INTO routine_runs (routine_id, trigger, status, started_at)
		VALUES ('R-abc123', 'cron', 'running', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert routine_run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO routine_runs (routine_id, trigger, status, started_at)
		VALUES ('R-abc123', 'cron', 'bogus', '2026-07-24T00:00:00.000Z')`); err == nil {
		t.Error("expected CHECK violation for status='bogus', got nil")
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}
