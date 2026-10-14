package prune

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// turn_forecasts (migration 0113) age on the same cutoff as link_events, by
// their own started_at, with no expiring session needed — scored rows and half
// rows alike.
func TestPrune_TurnForecastsAgeOnTheCutoff(t *testing.T) {
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "prune-tf.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`INSERT INTO turn_forecasts (session_id, run_id, turn_seq, started_at, forecast, actual, surprise) VALUES
				(1, 'r1', 1, '2026-01-01T00:00:00.000Z', '{}', '{}', 0.4),
				(1, 'r1', 2, '2026-05-31T23:59:59.999Z', '{}', NULL, NULL),
				(2, 'r2', 1, '2026-06-01T00:00:00.000Z', '{}', '{}', 0.1),
				(2, 'r2', 2, '2026-09-01T00:00:00.000Z', NULL, '{}', NULL)`); err != nil {
				t.Fatalf("seed: %v", err)
			}

			st, err := Run(db, "2026-06-01T00:00:00.000Z", dry)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if st.Sessions != 0 {
				t.Fatalf("fixture has %d expiring sessions; the point is to have none", st.Sessions)
			}
			if st.TurnForecasts != 2 {
				t.Errorf("TurnForecasts = %d, want 2 (the two before the cutoff)", st.TurnForecasts)
			}
			var left int
			if err := db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&left); err != nil {
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
