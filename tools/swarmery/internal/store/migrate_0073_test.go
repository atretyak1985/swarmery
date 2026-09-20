package store

import (
	"database/sql"
	"strings"
	"testing"
)

// Migration 0073 — the foreign-key child indexes the retention prune depends on.
//
// The store runs with PRAGMA foreign_keys=ON, so `DELETE FROM events` must prove
// for every deleted row that nothing still references it. Without an index on
// the referencing column that proof is a full scan of the child table per row —
// the shape that wedged the live daemon for 11 hours on its first retention
// pass. This test pins the CONSEQUENCE, not the DDL: the planner must answer
// each FK-check lookup with an index, on every column that references events
// or turns.
func TestMigrate0073IndexesEveryFKChildOfEventsAndTurns(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 73`).Scan(&name); err != nil {
		t.Fatalf("migration 73 not recorded: %v", err)
	}
	if name != "0073_fk_child_indexes.sql" {
		t.Errorf("migration 73 name: want 0073_fk_child_indexes.sql, got %s", name)
	}

	// Every (child table, column) that references events or turns, discovered
	// from the schema itself so a future FK added without an index fails here
	// rather than in production.
	rows, err := db.Query(`
		SELECT m.name, p."from"
		  FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) p
		 WHERE m.type = 'table' AND p."table" IN ('events', 'turns')`)
	if err != nil {
		t.Fatalf("list foreign keys: %v", err)
	}
	type fk struct{ table, col string }
	var fks []fk
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.table, &f.col); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, f)
	}
	rows.Close()
	if len(fks) < 4 {
		t.Fatalf("expected at least the four known FK children of events/turns, found %v", fks)
	}
	for _, f := range fks {
		plan := queryPlan(t, db, `SELECT 1 FROM `+f.table+` WHERE `+f.col+` = 1`)
		if !strings.Contains(plan, "USING") || strings.Contains(plan, "SCAN "+f.table) {
			t.Errorf("FK check on %s.%s is not indexed — plan:\n%s", f.table, f.col, plan)
		}
	}
}

// queryPlan renders EXPLAIN QUERY PLAN for q as one string.
func queryPlan(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + q)
	if err != nil {
		t.Fatalf("explain %q: %v", q, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail)
		b.WriteString("\n")
	}
	return b.String()
}
