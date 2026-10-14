package prune

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// link_commands (the link control channel's queue) ages on the same cutoff as
// link_events, by its own created_at, with no expiring session in the pass.
func TestPrune_LinkCommandsAgeOnTheCutoff(t *testing.T) {
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "prune-link-cmd.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`INSERT INTO link_commands (session_id, kind, payload, state, created_at) VALUES
				(1, 'stop',   '{}',             'expired', '2026-01-01T00:00:00.000Z'),
				(1, 'prompt', '{"text":"a"}',   'done',    '2026-05-31T23:59:59.999Z'),
				(2, 'prompt', '{"text":"b"}',   'done',    '2026-06-01T00:00:00.000Z'),
				(2, 'stop',   '{}',             'queued',  '2026-09-01T00:00:00.000Z')`); err != nil {
				t.Fatalf("seed: %v", err)
			}

			st, err := Run(db, "2026-06-01T00:00:00.000Z", dry)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if st.Sessions != 0 {
				t.Fatalf("fixture has %d expiring sessions; the point is to have none", st.Sessions)
			}
			if st.LinkCommands != 2 {
				t.Errorf("LinkCommands = %d, want 2 (the two before the cutoff)", st.LinkCommands)
			}
			var left int
			if err := db.QueryRow(`SELECT COUNT(*) FROM link_commands`).Scan(&left); err != nil {
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
