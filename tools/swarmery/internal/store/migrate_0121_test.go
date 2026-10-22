package store

import "testing"

// TestMigrate0121AddsRedactionProgress: the rescrub cursor table lands keyed by
// table name, and Open alone leaves it empty (the worker is the daemon's).
func TestMigrate0121AddsRedactionProgress(t *testing.T) {
	db, err := Open(t.TempDir() + "/m121.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 121`).Scan(&name); err != nil {
		t.Fatalf("migration 121 not recorded: %v", err)
	}
	if name != "0121_redaction_progress.sql" {
		t.Errorf("migration 121 name: want 0121_redaction_progress.sql, got %s", name)
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
