package phasediag

import (
	"strconv"
	"testing"
)

// A FAIL from an opt-in surprise auto-verify on a verify-off doc belongs to the run
// it graded. A later run that nobody graded must not inherit it as a blocker; a doc
// that asked for verification keeps today's behaviour.
func TestStaleFailOnAVerifyOffDocIsNotABlocker(t *testing.T) {
	cases := []struct {
		name, mode, verifiedAt string
		want                   bool
	}{
		{"verify-off, graded during this run", "off", "2026-07-28T10:05:00.500Z", true},
		{"verify-off, graded during an earlier run", "off", "2026-07-27T09:00:00.000Z", false},
		{"verify-normal, old grade still stands", "normal", "2026-07-27T09:00:00.000Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.addPhase(t, 1, "Phase 1", "[]", 2, 2)
			f.setRun(t, p, "done", "", 0) // run_started_at 2026-07-28T10:00:00Z
			mustExec(t, f.db, `UPDATE epic_phases SET verify_mode=? WHERE id=?`, tc.mode, p)
			f.setVerdict(t, p, "fail", "surprise verify said no")
			mustExec(t, f.db, `INSERT INTO verification_runs (target_key, status, started_at)
				VALUES (?, 'fail', ?)`, "phase:"+strconv.FormatInt(p, 10), tc.verifiedAt)

			d, err := Diagnose(f.db, nil, nil, p)
			if err != nil {
				t.Fatalf("Diagnose: %v", err)
			}
			got := false
			for _, b := range d.Blockers {
				if b.Kind == KindVerifyFailed {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("verify-failed blocker = %v, want %v (kinds=%v)", got, tc.want, kinds(d.Blockers))
			}
		})
	}
}
