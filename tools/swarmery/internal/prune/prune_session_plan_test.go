package prune

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// session_plan (migration 0111) ages on the link_events cutoff by its own `at`,
// but only once its session has ended — ended_at set, the mod reported
// session.end, or no sessions row is left. A live terminal keeps its pick
// however old the choice is.
func TestPrune_SessionPlanAgesOnceItsSessionEnded(t *testing.T) {
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "prune-sp.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			// Sessions end AFTER the cutoff, so none is a prune candidate itself:
			// the point is that session_plan does not need one.
			if _, err := db.Exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES (1, '/p', 'p', '2026-01-01T00:00:00Z');
				INSERT INTO sessions (project_id, session_uuid, status, started_at, ended_at, link_status) VALUES
				(1, 'ended',    'completed', '2026-01-01T00:00:00.000Z', '2026-07-01T00:00:00.000Z', NULL),
				(1, 'mod-done', 'active',    '2026-01-01T00:00:00.000Z', NULL, 'done'),
				(1, 'live',     'active',    '2026-01-01T00:00:00.000Z', NULL, 'waiting'),
				(1, 'fresh',    'completed', '2026-01-01T00:00:00.000Z', '2026-07-01T00:00:00.000Z', NULL);
				INSERT INTO session_plan (session_id, task_id, phase_id, chosen_by, at) VALUES
				('ended',    't', NULL, 'terminal', '2026-01-01T00:00:00.000Z'),
				('mod-done', 't', '3',  'auto',     '2026-05-31T23:59:59.999Z'),
				('gone',     't', NULL, 'terminal', '2026-02-01T00:00:00.000Z'),
				('live',     't', NULL, 'terminal', '2026-01-01T00:00:00.000Z'),
				('fresh',    't', NULL, 'auto',     '2026-06-01T00:00:00.000Z')`); err != nil {
				t.Fatalf("seed: %v", err)
			}

			st, err := Run(db, "2026-06-01T00:00:00.000Z", dry)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if st.Sessions != 0 {
				t.Fatalf("fixture has %d expiring sessions; the point is to have none", st.Sessions)
			}
			if st.SessionPlans != 3 {
				t.Errorf("SessionPlans = %d, want 3 (ended, mod-done, gone)", st.SessionPlans)
			}
			rows, err := db.Query(`SELECT session_id FROM session_plan ORDER BY session_id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var left []string
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					t.Fatal(err)
				}
				left = append(left, s)
			}
			want := "fresh,live"
			if dry {
				want = "ended,fresh,gone,live,mod-done"
			}
			if got := strings.Join(left, ","); got != want {
				t.Errorf("rows left = %s, want %s", got, want)
			}
		})
	}
}
