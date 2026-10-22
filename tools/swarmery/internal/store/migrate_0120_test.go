package store

import "testing"

// TestMigrate0120AddsWorkaroundHits: the hits table lands with its dedup key
// (turn, word, sentence hash) and the two link indexes the ingest tail probes.
func TestMigrate0120AddsWorkaroundHits(t *testing.T) {
	db, err := Open(t.TempDir() + "/m120.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 120`).Scan(&name); err != nil {
		t.Fatalf("migration 120 not recorded: %v", err)
	}
	if name != "0120_workaround_hits.sql" {
		t.Errorf("migration 120 name: want 0120_workaround_hits.sql, got %s", name)
	}
	const ins = `INSERT INTO workaround_hits (session_uuid, turn_id, word, sentence, sentence_hash, created_at)
		VALUES ('s', 7, 'instead', 'I used Edit instead.', 'h1', '2026-10-22T00:00:00Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("duplicate (turn, word, sentence) accepted")
	}
	for _, idx := range []string{"idx_workaround_hits_phase", "idx_workaround_hits_session",
		"idx_epic_phases_run_session", "idx_task_sessions_session"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing (%v)", idx, err)
		}
	}
}
