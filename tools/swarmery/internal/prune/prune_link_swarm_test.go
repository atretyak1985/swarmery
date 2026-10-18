package prune

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// link_messages age on the link_events cutoff by their own at; link_claims go
// 24 h after their release, or 24 h after a claim never released (a crashed
// session) — independent of the cutoff, with no expiring session in the pass.
func TestPrune_LinkSwarmTables(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "prune-link-swarm.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`INSERT INTO link_messages (kind, body, at) VALUES
				('send',    'old',   '2026-01-01T00:00:00.000Z'),
				('receive', 'edge',  '2026-05-31T23:59:59.999Z'),
				('send',    'kept',  '2026-06-01T00:00:00.000Z'),
				('dashboard','new',  '2026-10-14T11:00:00.000Z')`); err != nil {
				t.Fatalf("seed messages: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO link_claims (session_id, path, claimed_at, released_at) VALUES
				('s1', '/released-old',   '2026-10-13T09:00:00.000Z', '2026-10-13T11:59:59.999Z'),
				('s1', '/released-fresh', '2026-10-13T09:00:00.000Z', '2026-10-13T12:00:00.000Z'),
				('s2', '/crashed',        '2026-10-13T10:00:00.000Z', NULL),
				('s2', '/live',           '2026-10-14T11:30:00.000Z', NULL)`); err != nil {
				t.Fatalf("seed claims: %v", err)
			}

			st, err := RunWithOptions(db, "2026-06-01T00:00:00.000Z", dry, Options{Now: now})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if st.Sessions != 0 {
				t.Fatalf("fixture has %d expiring sessions; the point is to have none", st.Sessions)
			}
			if st.LinkMessages != 2 {
				t.Errorf("LinkMessages = %d, want 2 (the two before the cutoff)", st.LinkMessages)
			}
			if st.LinkClaims != 2 {
				t.Errorf("LinkClaims = %d, want 2 (released > 24 h ago, never released and claimed > 24 h ago)", st.LinkClaims)
			}
			var msgs, claims int
			if err := db.QueryRow(`SELECT COUNT(*) FROM link_messages`).Scan(&msgs); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM link_claims`).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			wantMsgs, wantClaims := 2, 2
			if dry {
				wantMsgs, wantClaims = 4, 4
			}
			if msgs != wantMsgs || claims != wantClaims {
				t.Errorf("rows left: messages %d claims %d, want %d / %d", msgs, claims, wantMsgs, wantClaims)
			}
			if !dry {
				var left []string
				rows, err := db.Query(`SELECT path FROM link_claims ORDER BY path`)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var p string
					if err := rows.Scan(&p); err != nil {
						t.Fatal(err)
					}
					left = append(left, p)
				}
				rows.Close()
				if len(left) != 2 || left[0] != "/live" || left[1] != "/released-fresh" {
					t.Errorf("claims left = %v, want [/live /released-fresh]", left)
				}
			}
		})
	}
}
