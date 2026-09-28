package advisor

import (
	"database/sql"
	"testing"
)

func seedOpenRec(t *testing.T, db *sql.DB, rule, kind, target, status string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, ?, ?, 't', 'd', '{}', ?, ?, ?, ?)`,
		rule, kind, target, status, rule+":"+target, ago(20), ago(20))
}

func statusOf(t *testing.T, db *sql.DB, rule, target string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM recommendations WHERE rule=? AND target=?`,
		rule, target).Scan(&s); err != nil {
		t.Fatalf("read status %s/%s: %v", rule, target, err)
	}
	return s
}

// A recommendation whose condition stopped reproducing must close itself.
//
// The live failure this pins: after both architecture maps were refreshed, a
// fresh advisor pass proposed nothing — and both R7 rows stayed open and
// actionable, one of them three weeks old. The Retro page could only accumulate,
// so an operator who had just executed an improvement plan saw an unchanged
// screen and concluded nothing had happened.
func TestResolveVanishedConditions(t *testing.T) {
	db := testDB(t)

	// Self-checking rules with no live finding: the world was re-examined and the
	// condition is gone, whatever the row's status.
	seedOpenRec(t, db, "R7", "project", "-work-alpha", "proposed")
	seedOpenRec(t, db, "R9", "session", "uuid-gone", "accepted")
	seedOpenRec(t, db, "R8", "agent", "main", "proposed")
	// A rate rule: silence can mean "no traffic this window", so a COMMITMENT
	// made on it is not closed by guessing — only the uncommitted proposal is.
	seedOpenRec(t, db, "R1", "tool", "SomeQuietTool", "accepted")
	seedOpenRec(t, db, "R2", "agent", "quiet-agent", "proposed")

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Resolved == 0 {
		t.Fatal("nothing was resolved; the sweep did not run")
	}

	for _, tc := range []struct{ rule, target, want string }{
		{"R7", "-work-alpha", "resolved"},
		{"R9", "uuid-gone", "resolved"},
		{"R8", "main", "resolved"},
		{"R2", "quiet-agent", "resolved"},
		// The one that must survive: an accepted rate-rule row. Absence of data
		// is not evidence of repair.
		{"R1", "SomeQuietTool", "accepted"},
	} {
		if got := statusOf(t, db, tc.rule, tc.target); got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.rule, tc.target, got, tc.want)
		}
	}
}

// A condition that STILL reproduces keeps its row open — the sweep must not
// close live findings.
func TestResolveLeavesLiveFindingsOpen(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied+2, 0)

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := statusOf(t, db, "R1", "Bash"); got != "proposed" {
		t.Errorf("a live finding was closed: status = %q, want proposed", got)
	}
}

// Dismissed and verified rows are terminal already; the sweep must not touch
// them, or a dismissal would silently become something else.
func TestResolveIgnoresTerminalRows(t *testing.T) {
	db := testDB(t)
	seedOpenRec(t, db, "R7", "project", "-work-dismissed", "dismissed")
	seedOpenRec(t, db, "R9", "session", "uuid-verified", "verified")

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := statusOf(t, db, "R7", "-work-dismissed"); got != "dismissed" {
		t.Errorf("dismissed row became %q", got)
	}
	if got := statusOf(t, db, "R9", "uuid-verified"); got != "verified" {
		t.Errorf("verified row became %q", got)
	}
}

// A resolved condition that COMES BACK is proposed again, on a fresh row —
// the contract resolveVanished states ("if the condition returns the rule
// re-proposes it"). Live, upsert had no case for `resolved`, so recommendation
// 89 (R3) failed every pass with `unknown status "resolved"`, and because Run
// returns on the first upsert error, R4–R12, adoption, verification and the
// resolve sweep itself never ran again.
func TestResolvedConditionThatReturnsIsReproposed(t *testing.T) {
	db := testDB(t)
	seedOpenRec(t, db, "R1", "tool", "Bash", "resolved")
	seedDenied(t, db, "Bash", R1MinDenied+2, 0)

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	var total, resolved, proposed int
	if err := db.QueryRow(`SELECT COUNT(*),
			SUM(status = 'resolved'), SUM(status = 'proposed')
		  FROM recommendations WHERE rule = 'R1' AND target = 'Bash'`).Scan(&total, &resolved, &proposed); err != nil {
		t.Fatal(err)
	}
	if total != 2 || resolved != 1 || proposed != 1 {
		t.Errorf("R1/Bash rows: total=%d resolved=%d proposed=%d, want the resolved row kept and one fresh proposed row",
			total, resolved, proposed)
	}
}
