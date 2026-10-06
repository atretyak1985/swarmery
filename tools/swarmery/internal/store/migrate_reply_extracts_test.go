package store

import (
	"strings"
	"testing"
)

// replyExtractsVersion finds the reply_extracts migration by NAME, so a
// renumber never breaks these tests.
func replyExtractsVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_reply_extracts.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the reply_extracts migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateReplyExtractsFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustHaveColumns(t, db, "reply_extracts", "id", "session_id", "turn_id", "status",
		"question", "options_json", "recommended", "model", "error", "created_at")
	mustHaveIndex(t, db, "idx_reply_extracts_created")

	// The FK check a session delete runs must be an index seek (0073 lesson).
	plan := queryPlan(t, db, `SELECT 1 FROM reply_extracts WHERE session_id = 1`)
	if !strings.Contains(plan, "USING") || strings.Contains(plan, "SCAN reply_extracts") {
		t.Errorf("FK check on reply_extracts.session_id is not indexed — plan:\n%s", plan)
	}
}

// On a populated DB: the table is absent before, present after; the UNIQUE
// key, the status CHECK and ON DELETE CASCADE hold; a re-run is a no-op.
func TestMigrateReplyExtractsOnPopulatedDB(t *testing.T) {
	v := replyExtractsVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if len(columnSet(t, db, "reply_extracts")) != 0 {
		t.Fatal("reply_extracts exists before its migration")
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'u-pre', 'awaiting_reply', '2026-10-06T00:00:00Z', 'jsonl')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	insert := func(turn int, status string) error {
		_, err := db.Exec(`INSERT INTO reply_extracts (session_id, turn_id, status, model, created_at)
			VALUES (1, ?, ?, 'm', '2026-10-06T00:00:00.000Z')`, turn, status)
		return err
	}
	if err := insert(7, "ok"); err != nil {
		t.Fatalf("insert ok row: %v", err)
	}
	if err := insert(7, "error"); err == nil {
		t.Error("a second row for the same (session, turn) was accepted — UNIQUE missing")
	}
	if err := insert(8, "pending"); err == nil {
		t.Error("status 'pending' was accepted — CHECK missing")
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id = 1`); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reply_extracts`).Scan(&n); err != nil || n != 0 {
		t.Errorf("reply_extracts rows after session delete = %d (%v), want 0 (ON DELETE CASCADE)", n, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
