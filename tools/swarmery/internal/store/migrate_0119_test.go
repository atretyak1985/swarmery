package store

import "testing"

// TestMigrate0119AddsPlainSummaries: the retelling cache table lands keyed by
// its content hash, so a second insert of the same key conflicts.
func TestMigrate0119AddsPlainSummaries(t *testing.T) {
	db, err := Open(t.TempDir() + "/m119.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 119`).Scan(&name); err != nil {
		t.Fatalf("migration 119 not recorded: %v", err)
	}
	if name != "0119_plain_summaries.sql" {
		t.Errorf("migration 119 name: want 0119_plain_summaries.sql, got %s", name)
	}
	const ins = `INSERT INTO plain_summaries (key, kind, lang, text, model, created_at)
		VALUES ('k', 'phase', 'uk', 'text', 'm', '2026-10-21T00:00:00Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("duplicate key accepted")
	}
}
