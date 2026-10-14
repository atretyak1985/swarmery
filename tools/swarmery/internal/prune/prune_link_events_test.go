package prune

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// link_events (the swarmery link's dedup ledger) ages on the same cutoff as
// everything else, by its own `at` — including rows of a session that never
// ended (a mod-minted row may never get ended_at) and with no expiring
// session at all in the pass.
func TestPrune_LinkEventsAgeOnTheCutoff(t *testing.T) {
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "prune-link.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`INSERT INTO link_events (session_id, kind, at, payload) VALUES
				(1, 'turn.start',    '2026-01-01T00:00:00.000Z', NULL),
				(1, 'turn.complete', '2026-05-31T23:59:59.999Z', NULL),
				(2, 'turn.start',    '2026-06-01T00:00:00.000Z', NULL),
				(2, 'session.measure','2026-09-01T00:00:00.000Z', '{}')`); err != nil {
				t.Fatalf("seed: %v", err)
			}

			st, err := Run(db, "2026-06-01T00:00:00.000Z", dry)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if st.Sessions != 0 {
				t.Fatalf("fixture has %d expiring sessions; the point is to have none", st.Sessions)
			}
			if st.LinkEvents != 2 {
				t.Errorf("LinkEvents = %d, want 2 (the two before the cutoff)", st.LinkEvents)
			}
			var left int
			if err := db.QueryRow(`SELECT COUNT(*) FROM link_events`).Scan(&left); err != nil {
				t.Fatal(err)
			}
			want := 2
			if dry {
				want = 4
			}
			if left != want {
				t.Errorf("rows left = %d, want %d", left, want)
			}
		})
	}
}
