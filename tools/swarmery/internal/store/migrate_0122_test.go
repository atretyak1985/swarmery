package store

import "testing"

// TestMigrate0122AddsColdRestart: turns gains cold_restart defaulting to 0, and
// session_cache_cold lands keyed by session_uuid with the kind constrained to
// the two billing channels.
func TestMigrate0122AddsColdRestart(t *testing.T) {
	db, err := Open(t.TempDir() + "/m122.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 122`).Scan(&name); err != nil {
		t.Fatalf("migration 122 not recorded: %v", err)
	}
	if name != "0122_turns_cold_restart.sql" {
		t.Errorf("migration 122 name: want 0122_turns_cold_restart.sql, got %s", name)
	}

	if _, err := db.Exec(`INSERT INTO projects (path, slug, name, first_seen, last_activity)
		VALUES ('/p', '-p', 'p', '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at, status)
		VALUES (1, 'u1', '2026-10-22T00:00:00Z', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at) VALUES (1, 0, 'assistant', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var cold int
	if err := db.QueryRow(`SELECT cold_restart FROM turns WHERE session_id = 1`).Scan(&cold); err != nil {
		t.Fatal(err)
	}
	if cold != 0 {
		t.Errorf("cold_restart default: want 0, got %d", cold)
	}

	const ins = `INSERT INTO session_cache_cold (session_uuid, since, kind, footprint) VALUES ('u1', '2026-10-22T01:00:00Z', ?, 200000)`
	if _, err := db.Exec(ins, "subscription"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins, "subscription"); err == nil {
		t.Fatal("duplicate session_uuid accepted")
	}
	if _, err := db.Exec(`INSERT INTO session_cache_cold (session_uuid, since, kind, footprint) VALUES ('u2', 'x', 'bogus', 1)`); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
