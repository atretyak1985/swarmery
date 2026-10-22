package store

import "testing"

// TestMigrate0120AddsRedactionProgress: the rescrub cursor table lands keyed by
// table name, and Open alone leaves it empty (the worker is the daemon's).
func TestMigrate0120AddsRedactionProgress(t *testing.T) {
	db, err := Open(t.TempDir() + "/m120.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 120`).Scan(&name); err != nil {
		t.Fatalf("migration 120 not recorded: %v", err)
	}
	if name != "0120_redaction_progress.sql" {
		t.Errorf("migration 120 name: want 0120_redaction_progress.sql, got %s", name)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM redaction_progress`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("redaction_progress has %d rows after Open, want 0", n)
	}
	const ins = `INSERT INTO redaction_progress (table_name, last_rowid, rules_version) VALUES ('events', 0, 1)`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("duplicate table_name accepted")
	}
}
