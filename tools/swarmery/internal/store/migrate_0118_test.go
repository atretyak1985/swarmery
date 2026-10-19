package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestMigrate0118AddsTurnsStartedAtIndex: the index lands, and with planner
// statistics the overview's cost_by_model reads a day's turns through it
// instead of walking every session's turns and looking each one up by rowid.
func TestMigrate0118AddsTurnsStartedAtIndex(t *testing.T) {
	db, err := Open(t.TempDir() + "/m118.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 118`).Scan(&name); err != nil {
		t.Fatalf("migration 118 not recorded: %v", err)
	}
	if name != "0118_turns_started_at.sql" {
		t.Errorf("migration 118 name: want 0118_turns_started_at.sql, got %s", name)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// 30 days × 4 sessions × 25 priced turns: a day is a thin slice of turns.
	for s := 1; s <= 120; s++ {
		day := fmt.Sprintf("2026-09-%02dT", (s-1)/4+1)
		if _, err := tx.Exec(`INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (?, 1, ?, ?)`,
			s, fmt.Sprintf("s-%d", s), day+"00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 25; k++ {
			if _, err := tx.Exec(`INSERT INTO turns (session_id, seq, role, started_at, cost_usd) VALUES (?, ?, 'assistant', ?, 0.01)`,
				s, k, fmt.Sprintf("%s%02d:%02d:00Z", day, k/2, k%60)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := Optimize(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}

	// The cost_by_model shape from internal/api/overview.go.
	rows, err := db.Query(`EXPLAIN QUERY PLAN
		SELECT COALESCE(t.model, 'unknown') AS mdl, SUM(t.cost_usd) AS c
		FROM turns t
		JOIN sessions s ON s.id = t.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE t.cost_usd IS NOT NULL AND t.started_at >= ? AND t.started_at < ? AND p.archived = 0
		GROUP BY mdl ORDER BY c DESC, mdl`, "2026-09-10T00:00:00Z", "2026-09-11T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, " | "), "idx_turns_started_at") {
		t.Fatalf("cost_by_model plan = %q, want a range seek through idx_turns_started_at", plan)
	}
}
