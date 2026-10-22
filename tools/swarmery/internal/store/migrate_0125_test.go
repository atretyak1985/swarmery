package store

import "testing"

// TestMigrate0125AddsStallNotices: the stall ledger is keyed by (phase, reason),
// so a second notice for the same pair conflicts, and the review stage's session
// columns land on epic_phases.
func TestMigrate0125AddsStallNotices(t *testing.T) {
	db, err := Open(t.TempDir() + "/m125.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 125`).Scan(&name); err != nil {
		t.Fatalf("migration 125 not recorded: %v", err)
	}
	if name != "0125_stall_notices.sql" {
		t.Errorf("migration 125 name: want 0125_stall_notices.sql, got %s", name)
	}
	const ins = `INSERT INTO stall_notices (phase_id, reason, stage, since, first_seen)
		VALUES (7, 'no-turns', 'executor', '2026-10-22T00:00:00Z', '2026-10-22T00:20:00Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("duplicate (phase, reason) accepted")
	}
	if _, err := db.Exec(`SELECT review_session_uuid, review_started_at FROM epic_phases`); err != nil {
		t.Fatalf("review session columns missing: %v", err)
	}
}
