package handoff

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
)

// rejectedSeed is the fixture every StructuredRejected test starts from.
const rejectedSeed = "../../testdata/fixtures/handoff/rejected-seed.sql"

// seededDB is testDB plus the rejected-seed fixture.
func seededDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	raw, err := os.ReadFile(rejectedSeed)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	mustExec(t, db, string(raw))
	return db
}

func lines(rs []Rejected) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Line())
	}
	return out
}

// Plan session: the three sources each contribute, in question order then
// correction order; a decision row supersedes its interview answer; an
// unanswered question with no decision rejects nothing; another plan's
// interview, another session's correction and a lesson edit stay out.
func TestStructuredRejectedPlanSession(t *testing.T) {
	db := seededDB(t)
	got, err := StructuredRejected(db, "rej-plan-session")
	if err != nil {
		t.Fatalf("StructuredRejected: %v", err)
	}
	want := []string{
		"- Redis — chose SQLite table instead [decision D-1]",
		"- LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]",
		"- Eager warm-up — chose Lazy fill instead [decision D-3]",
		"- refactor — d2.task_type: refactor -> bugfix [correction decision:900]",
		"- continue — d1.run_end: continue -> stop [correction decision:901]",
		"- Split phase 1 into a migration and a backfill — the backfill must ship with the migration [correction revision:7]",
	}
	if g := lines(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
	bySource := map[string]int{}
	for _, r := range got {
		bySource[strings.Fields(r.Source)[0]]++
	}
	for _, src := range []string{"interview", "decision", "correction"} {
		if bySource[src] == 0 {
			t.Errorf("source %q contributed nothing; got %+v", src, got)
		}
	}
}

// Without phase_decisions (main before verify-not-trust phase 2 lands) the
// interview answers stand on their own: D-1 falls back to the interview, the
// unanswered D-3 rejects nothing.
func TestStructuredRejectedWithoutDecisionsTable(t *testing.T) {
	db := seededDB(t)
	mustExec(t, db, `DROP TABLE phase_decisions`)
	got, err := StructuredRejected(db, "rej-plan-session")
	if err != nil {
		t.Fatalf("StructuredRejected: %v", err)
	}
	g := lines(got)
	want := []string{
		"- Redis — Fast shared in-memory store; chose SQLite table instead [interview D-1]",
		"- LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]",
	}
	if !reflect.DeepEqual(g[:2], want) {
		t.Fatalf("interview lines =\n%s\nwant\n%s", strings.Join(g[:2], "\n"), strings.Join(want, "\n"))
	}
	for _, l := range g {
		if strings.Contains(l, "warm-up") || strings.Contains(l, "[decision ") {
			t.Errorf("no decision source without the table; got %q", l)
		}
	}
}

// A session with no plan link and no run branch: corrections only.
func TestStructuredRejectedNoPlanCorrectionsOnly(t *testing.T) {
	db := seededDB(t)
	got, err := StructuredRejected(db, "rej-noplan-session")
	if err != nil {
		t.Fatalf("StructuredRejected: %v", err)
	}
	want := []string{"- feature — d2.task_type: feature -> docs [correction decision:902]"}
	if g := lines(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("lines = %q, want %q", g, want)
	}
}

// A phase-run session has no session_plan row: its stamped branch
// (swarm/phase-<id>) names the plan. Plan-scoped corrections apply; another
// session's correction does not.
func TestStructuredRejectedRunBranch(t *testing.T) {
	db := seededDB(t)
	got, err := StructuredRejected(db, "rej-phase-run")
	if err != nil {
		t.Fatalf("StructuredRejected: %v", err)
	}
	g := strings.Join(lines(got), "\n")
	for _, want := range []string{"[interview D-2]", "[decision D-3]", "[correction decision:901]", "[correction revision:7]"} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %q in\n%s", want, g)
		}
	}
	if strings.Contains(g, "decision:900") {
		t.Errorf("session 1's own correction leaked into the phase run:\n%s", g)
	}
}

// An unknown session yields nothing and no error.
func TestStructuredRejectedUnknownSession(t *testing.T) {
	db := seededDB(t)
	got, err := StructuredRejected(db, "no-such-session")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want nothing", got, err)
	}
}

func TestPlanDirKey(t *testing.T) {
	a := planDirKey("/ws/p/workspace/archive/2026/10/21/x/plan")
	for _, p := range []string{
		"/ws/p/workspace/working/2026/10/21/x/",
		"/ws/p/workspace/working/2026/10/21/x/plan/",
		"/ws/p/workspace/working/2026/10/21/x",
	} {
		if k := planDirKey(p); k != a {
			t.Errorf("planDirKey(%q) = %q, want %q", p, k, a)
		}
	}
	if planDirKey("") != "" {
		t.Errorf(`planDirKey("") must stay ""`)
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"One. Two.":            "One.",
		"No terminator":        "No terminator",
		"v1.2 is fine. Later.": "v1.2 is fine.",
		"Multi\nline? Yes.":    "Multi line?",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
