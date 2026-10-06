package store

import "testing"

// entrypointVersion finds the session_entrypoint migration by NAME, so a
// renumber never breaks this test.
func entrypointVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_session_entrypoint.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the session_entrypoint migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateSessionEntrypointFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name LIKE '%_session_entrypoint.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recorded %d time(s) (%v), want 1", n, err)
	}
	mustHaveColumns(t, db, "sessions", "entrypoint")
}

// Column absent before, present after, and a pre-existing row reads the empty
// string (unknown).
func TestMigrateSessionEntrypointOnPopulatedDB(t *testing.T) {
	v := entrypointVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if columnSet(t, db, "sessions")["entrypoint"] {
		t.Fatal("entrypoint exists before its migration")
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		VALUES (1, 'u-pre', 'idle', '2026-10-06T00:00:00Z', 'jsonl')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT entrypoint FROM sessions WHERE session_uuid = 'u-pre'`).Scan(&got); err != nil || got != "" {
		t.Errorf("pre-existing row entrypoint = %q (%v), want ''", got, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
