package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// seedEvents writes 20 sessions × 50 tool_call events, nearly all status='ok':
// the shape where session_id is selective and status is not.
func seedEvents(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(t.TempDir() + "/opt.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for s := 1; s <= 20; s++ {
		if _, err := tx.Exec(`INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (?, 1, ?, '2026-10-01T00:00:00Z')`,
			s, fmt.Sprintf("s-%d", s)); err != nil {
			t.Fatal(err)
		}
		for e := 0; e < 50; e++ {
			status := "ok"
			if e == 0 {
				status = "error"
			}
			if _, err := tx.Exec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload) VALUES (?, ?, 'tool_call', 'Bash', ?, '{}')`,
				s, fmt.Sprintf("2026-10-01T00:%02d:00Z", e), status); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

const sessionEventsQuery = `SELECT payload FROM events
 WHERE session_id = ? AND type = 'tool_call' AND tool_name = 'Bash' AND status = 'ok'`

func eventsPlan(t *testing.T, s *sql.DB) string {
	t.Helper()
	rows, err := s.Query(`EXPLAIN QUERY PLAN `+sessionEventsQuery, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
	}
	return strings.Join(steps, " | ")
}

func TestOptimizeWritesPlannerStats(t *testing.T) {
	s := seedEvents(t)
	if err := Optimize(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.QueryRow(`SELECT count(*) FROM sqlite_stat1 WHERE tbl = 'events'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no sqlite_stat1 rows for events after Optimize")
	}
	// The periodic form must also run cleanly on an already-analyzed store.
	if err := Optimize(context.Background(), s, false); err != nil {
		t.Fatal(err)
	}
}

// With statistics the planner reaches a session's events through
// idx_events_session; without them it guessed idx_events_status_ts on the live
// store and scanned every ok event (#584).
func TestOptimizePicksSessionIndexForEvents(t *testing.T) {
	s := seedEvents(t)
	if err := Optimize(context.Background(), s, true); err != nil {
		t.Fatal(err)
	}
	if plan := eventsPlan(t, s); !strings.Contains(plan, "idx_events_session") {
		t.Fatalf("plan = %q, want a search through idx_events_session", plan)
	}
}
