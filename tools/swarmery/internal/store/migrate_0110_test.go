package store

import "testing"

// TestMigrate0110AddsLinkSwarm: the swarm tables land with their columns and
// indexes; link_claims is keyed one row per (session, path); link_messages
// numbers rows with AUTOINCREMENT and leaves delivered NULL ("unknown").
func TestMigrate0110AddsLinkSwarm(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 110`).Scan(&name); err != nil {
		t.Fatalf("migration 110 not recorded: %v", err)
	}
	if name != "0110_link_swarm.sql" {
		t.Errorf("migration 110 name: want 0110_link_swarm.sql, got %s", name)
	}
	mustHaveColumns(t, db, "link_messages",
		"id", "kind", "from_session", "to_session", "to_address", "body", "truncated",
		"origin", "at", "delivered", "reason", "command_id", "matched_id")
	mustHaveColumns(t, db, "link_claims", "session_id", "path", "claimed_at", "released_at")

	for _, idx := range []string{"idx_link_messages_at", "idx_link_messages_from", "idx_link_messages_to", "idx_link_claims_path"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s missing", idx)
		}
	}

	if _, err := db.Exec(`INSERT INTO link_messages (kind, body, at) VALUES ('send', 'hi', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	var delivered any
	var truncated int
	if err := db.QueryRow(`SELECT delivered, truncated FROM link_messages`).Scan(&delivered, &truncated); err != nil {
		t.Fatal(err)
	}
	if delivered != nil || truncated != 0 {
		t.Errorf("delivered/truncated = %v/%d, want NULL/0", delivered, truncated)
	}
	if _, err := db.Exec(`INSERT INTO link_messages (kind, at) VALUES ('send', '2026-10-14T00:00:00.000Z')`); err == nil {
		t.Error("insert without body succeeded; want NOT NULL")
	}

	ins := `INSERT INTO link_claims (session_id, path, claimed_at) VALUES ('s1', '/a', '2026-10-14T00:00:00.000Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("second claim of the same (session, path) succeeded; want a PRIMARY KEY conflict")
	}
	if _, err := db.Exec(`INSERT INTO link_claims (session_id, path, claimed_at) VALUES ('s2', '/a', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatalf("another session's claim on the same path: %v", err)
	}
}
