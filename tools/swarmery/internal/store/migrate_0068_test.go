package store

import "testing"

// TestMigrate0068FreshDB verifies the terminal-identity columns are registered.
func TestMigrate0068FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 68`).Scan(&name); err != nil {
		t.Fatalf("migration 68 not recorded: %v", err)
	}
	if name != "0068_session_terminal.sql" {
		t.Errorf("migration 68 name: want 0068_session_terminal.sql, got %s", name)
	}
	mustHaveColumns(t, db, "sessions", "term_program", "term_focus_url", "term_bundle_id", "term_tty")
}

// TestMigrate0068OnPopulatedDB: a pre-0068 session row survives and reads NULL
// for all four new columns — no terminal was ever recorded for it.
func TestMigrate0068OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 67)

	cols := columnSet(t, db, "sessions")
	for _, c := range []string{"term_program", "term_focus_url", "term_bundle_id", "term_tty"} {
		if cols[c] {
			t.Fatalf("%s exists before 0068 — migrateUpTo applied too much", c)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		 VALUES (1, 1, 'uuid-pre-0068', 'completed', '2026-08-01T00:00:00Z', 'jsonl')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}
	mustHaveColumns(t, db, "sessions", "term_program", "term_focus_url", "term_bundle_id", "term_tty")

	var program, focusURL, bundleID, tty *string
	if err := db.QueryRow(
		`SELECT term_program, term_focus_url, term_bundle_id, term_tty FROM sessions WHERE id = 1`,
	).Scan(&program, &focusURL, &bundleID, &tty); err != nil {
		t.Fatalf("read terminal columns: %v", err)
	}
	if program != nil || focusURL != nil || bundleID != nil || tty != nil {
		t.Errorf("terminal columns = %v/%v/%v/%v, want all NULL for a pre-0068 row",
			program, focusURL, bundleID, tty)
	}
}
