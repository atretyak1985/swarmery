package prune

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// RetentionDays resolves the env knob. The load-bearing cases are the two
// silent-corruption guards: "0" must disable (not fall through to a default
// that starts deleting), and a sub-floor value must be raised, never honoured.
func TestRetentionDaysEnvTable(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{name: "unset", set: false, want: DefaultRetentionDays},
		{name: "empty", set: true, val: "", want: DefaultRetentionDays},
		{name: "explicit 60", set: true, val: "60", want: 60},
		{name: "zero disables", set: true, val: "0", want: 0},
		{name: "below floor is raised", set: true, val: "7", want: MinRetentionDays},
		{name: "floor itself is kept", set: true, val: "14", want: 14},
		{name: "non-integer falls back", set: true, val: "abc", want: DefaultRetentionDays},
		{name: "negative falls back", set: true, val: "-5", want: DefaultRetentionDays},
		{name: "large value is kept", set: true, val: "365", want: 365},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// t.Setenv registers the restore even when we then unset, so the
			// "unset" case cannot leak into the rest of the suite.
			t.Setenv(RetentionDaysEnv, tc.val)
			if !tc.set {
				if err := os.Unsetenv(RetentionDaysEnv); err != nil {
					t.Fatal(err)
				}
			}
			if got := RetentionDays(); got != tc.want {
				t.Errorf("RetentionDays() = %d, want %d", got, tc.want)
			}
		})
	}
}

// Tick prunes across the cutoff on a fixture store, keeps the session HEADER
// rows flagged pruned=1, and — the reason RunWithOptions exists — never
// VACUUMs: the daemon runs this under load against its own live WAL.
func TestTickPrunesAcrossCutoffWithoutVacuum(t *testing.T) {
	db := seedDB(t)

	var vacuumed bool
	orig := vacuum
	t.Cleanup(func() { vacuum = orig })
	vacuum = func(*sql.DB) error { vacuumed = true; return nil }

	// now - 60d = 2026-05-02: session 1 (ended 2026-03-10) is past it,
	// session 2 (ended 2026-06-20) is not, session 3 never ended.
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	st, err := Tick(db, now, DefaultRetentionDays)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if vacuumed {
		t.Error("Tick VACUUMed; the daemon path must never rewrite the live DB file")
	}
	if st.Sessions != 1 {
		t.Errorf("st.Sessions = %d, want 1 (only the pre-cutoff ended session)", st.Sessions)
	}
	if st.Turns != 2 || st.Events != 3 || st.FileChanges != 1 {
		t.Errorf("stats = %+v, want turns=2 events=3 file_changes=1", st)
	}
	if st.RollupRows == 0 {
		t.Error("st.RollupRows = 0, want the pruned session's aggregates preserved")
	}

	// Header rows survive, flagged; the recent and still-open ones are untouched.
	for _, tc := range []struct{ id, want int }{{1, 1}, {2, 0}, {3, 0}} {
		var pruned int
		if err := db.QueryRow(`SELECT pruned FROM sessions WHERE id = ?`, tc.id).Scan(&pruned); err != nil {
			t.Fatalf("session %d: %v", tc.id, err)
		}
		if pruned != tc.want {
			t.Errorf("session %d pruned = %d, want %d", tc.id, pruned, tc.want)
		}
	}
	// Session 2's child rows must still be there.
	var turns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns WHERE session_id = 2`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 1 {
		t.Errorf("session 2 turns = %d, want 1 (kept)", turns)
	}
}

// days <= 0 is the "disabled" contract: it must not touch the database at all.
func TestTickDisabledIsNoop(t *testing.T) {
	db := seedDB(t)
	for _, days := range []int{0, -1} {
		st, err := Tick(db, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), days)
		if err != nil {
			t.Fatalf("Tick(days=%d): %v", days, err)
		}
		if st != (Stats{}) {
			t.Errorf("Tick(days=%d) = %+v, want zero Stats", days, st)
		}
	}
	var pruned int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE pruned = 1`).Scan(&pruned); err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Errorf("%d sessions pruned with retention disabled, want 0", pruned)
	}
}

// Run keeps the CLI's VACUUM; RunWithOptions honours the flag. These are the
// two halves of the split, asserted against the same fixture.
func TestRunAndRunWithOptionsVacuumBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*sql.DB) (Stats, error)
		want bool
	}{
		{"Run vacuums", func(db *sql.DB) (Stats, error) { return Run(db, cutoff, false) }, true},
		{"RunWithOptions Vacuum true", func(db *sql.DB) (Stats, error) {
			return RunWithOptions(db, cutoff, false, Options{Vacuum: true})
		}, true},
		{"RunWithOptions Vacuum false", func(db *sql.DB) (Stats, error) {
			return RunWithOptions(db, cutoff, false, Options{Vacuum: false})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := seedDB(t)
			var vacuumed bool
			orig := vacuum
			t.Cleanup(func() { vacuum = orig })
			vacuum = func(*sql.DB) error { vacuumed = true; return nil }

			st, err := tc.call(db)
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			if st.Sessions != 1 {
				t.Fatalf("st.Sessions = %d, want 1 (fixture precondition)", st.Sessions)
			}
			if vacuumed != tc.want {
				t.Errorf("vacuum called = %v, want %v", vacuumed, tc.want)
			}
		})
	}
}
