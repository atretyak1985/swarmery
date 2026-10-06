package advisor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

// mustBaseline is BaselineFor at testNow, decoded.
func mustBaseline(t *testing.T, db *sql.DB, rule, target string) baseline {
	t.Helper()
	j, err := BaselineFor(db, rule, target, testNow)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	var b baseline
	if err := json.Unmarshal([]byte(j), &b); err != nil {
		t.Fatalf("unmarshal %q: %v", j, err)
	}
	return b
}

// T1 (triage's error-group rule) is measured exactly like R3.
func TestBaselineForT1MatchesR3(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 3; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'error', 'error', ?, ?)`,
			ago(1+i), fmt.Sprintf(`{"error":{"message":"API Error 529 overloaded (req_%03d)"}}`, i),
			fmt.Sprintf("t1-%d", i))
	}
	fs, err := r3RecurringErrors(db, evalWindow())
	if err != nil || len(fs) != 1 {
		t.Fatalf("r3 findings = %+v err=%v", fs, err)
	}
	key := fs[0].target

	r3 := mustBaseline(t, db, "R3", key)
	t1 := mustBaseline(t, db, "T1", key)
	if t1.Metric == "" || t1.Metric != r3.Metric || t1.Value != r3.Value || t1.Value == 0 {
		t.Errorf("T1 baseline = %+v, want R3's %+v (non-zero)", t1, r3)
	}
	if !t1.PerDay || t1.PerDay != r3.PerDay {
		t.Errorf("T1 per_day = %v, want true like R3", t1.PerDay)
	}
}

// T2 (triage's agent rule) is measured exactly like R2.
func TestBaselineForT2MatchesR2(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "tech-lead", R2MinRuns+2, 3)

	r2 := mustBaseline(t, db, "R2", "tech-lead")
	t2 := mustBaseline(t, db, "T2", "tech-lead")
	if t2.Metric == "" || t2.Metric != r2.Metric || t2.Value != r2.Value || t2.PerDay != r2.PerDay {
		t.Errorf("T2 baseline = %+v, want R2's %+v", t2, r2)
	}
	if t2.PerDay {
		t.Error("T2 is a ratio metric, per_day must be false")
	}
}

// The advisor sweep must not resolve open recommendations of rules it did not
// evaluate: T1/T2 rows are triage's, not the advisor's.
func TestAdvisorRunLeavesTriageRulesOpen(t *testing.T) {
	db := testDB(t)
	seedOpenRec(t, db, "T1", "error_group", "api error # overloaded", "proposed")
	seedOpenRec(t, db, "T2", "agent", "quiet-agent", "proposed")
	// Control: an advisor rule with no live finding IS resolved by the same pass.
	seedOpenRec(t, db, "R2", "agent", "quiet-agent", "proposed")

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := statusOf(t, db, "R2", "quiet-agent"); got != "resolved" {
		t.Fatalf("control R2 = %q, want resolved (the sweep did not run)", got)
	}
	for _, tc := range []struct{ rule, target string }{
		{"T1", "api error # overloaded"},
		{"T2", "quiet-agent"},
	} {
		if got := statusOf(t, db, tc.rule, tc.target); got != "proposed" {
			t.Errorf("%s/%s = %q, want proposed (not evaluated, must stay open)", tc.rule, tc.target, got)
		}
	}
}
