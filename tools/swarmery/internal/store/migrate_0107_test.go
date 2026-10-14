package store

import (
	"strings"
	"testing"
)

// linkSessionsVersion finds the link_sessions migration by NAME, so a
// renumber never breaks these tests.
func linkSessionsVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_link_sessions.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the link_sessions migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateLinkSessionsFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustHaveColumns(t, db, "sessions",
		"link_status", "link_seen_at", "link_context_pct", "link_rate_limits", "link_cost_usd")
	mustHaveColumns(t, db, "link_events", "id", "session_id", "kind", "at", "payload")
	mustHaveIndex(t, db, "idx_link_events_session_at")

	// The UNIQUE(session_id, kind, at) key is the dedup contract.
	ins := `INSERT INTO link_events (session_id, kind, at, payload) VALUES (1, 'turn.start', '2026-10-13T20:00:00.000Z', '{}')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ins); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second identical insert err = %v, want a UNIQUE violation", err)
	}
	// No FK: a session id with no sessions row is accepted (0079 rationale).
	if _, err := db.Exec(`INSERT INTO link_events (session_id, kind, at) VALUES (999, 'turn.start', '2026-10-13T20:00:00.000Z')`); err != nil {
		t.Fatalf("link_events must carry no FK: %v", err)
	}
}

// Columns absent before, present after; a pre-existing row reads NULL link_*
// and the new 'mod' source value is storable (sessions.source has no CHECK).
func TestMigrateLinkSessionsOnPopulatedDB(t *testing.T) {
	v := linkSessionsVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if columnSet(t, db, "sessions")["link_status"] {
		t.Fatal("link_status exists before its migration")
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		VALUES (1, 'u-pre', 'idle', '2026-10-13T00:00:00Z', 'jsonl')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE session_uuid = 'u-pre'
		AND link_status IS NULL AND link_seen_at IS NULL AND link_context_pct IS NULL
		AND link_rate_limits IS NULL AND link_cost_usd IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("pre-existing row link_* not all NULL: n=%d err=%v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		VALUES (1, 'u-mod', 'active', '2026-10-13T00:00:00Z', 'mod')`); err != nil {
		t.Fatalf("source='mod' must be storable: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
