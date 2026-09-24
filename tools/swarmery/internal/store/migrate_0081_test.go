package store

import "testing"

// TestMigrate0081AddsPhaseActuals: the actuals table lands with its natural key
// (one row per run, keyed by the run's session uuid) and the phase lookup index.
func TestMigrate0081AddsPhaseActuals(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 81`).Scan(&name); err != nil {
		t.Fatalf("migration 81 not recorded: %v", err)
	}
	if name != "0081_phase_actuals.sql" {
		t.Errorf("migration 81 name: want 0081_phase_actuals.sql, got %s", name)
	}
	mustHaveColumns(t, db, "phase_actuals",
		"phase_id", "session_uuid", "run_state", "branch", "start_point",
		"files_json", "areas_json", "area_depth", "lines_added", "lines_removed",
		"size_band", "duration_s", "cost_usd", "outcome", "verify_verdict",
		"test_failures", "test_failures_unexpected", "continuations",
		"model_fallback", "source", "computed_at")

	var idx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_phase_actuals_phase'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Errorf("idx_phase_actuals_phase missing")
	}

	// One row per run: a second insert for the same session is a conflict, which
	// is what makes the writer's recompute an upsert rather than an append.
	ins := `INSERT INTO phase_actuals (phase_id, session_uuid, computed_at) VALUES (1, 'u-1', '2026-09-23T00:00:00Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("second insert for the same session_uuid succeeded; want a UNIQUE conflict")
	}

	// Unmeasured columns default to NULL, never to a fabricated zero.
	var lines, cost, verdict any
	if err := db.QueryRow(`SELECT lines_added, cost_usd, verify_verdict FROM phase_actuals WHERE session_uuid = 'u-1'`).
		Scan(&lines, &cost, &verdict); err != nil {
		t.Fatal(err)
	}
	if lines != nil || cost != nil || verdict != nil {
		t.Errorf("unmeasured columns = %v/%v/%v, want all NULL", lines, cost, verdict)
	}
}
