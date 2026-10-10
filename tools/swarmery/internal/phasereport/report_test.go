package phasereport

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec1(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// fixture is a project, a workspace epic and phases whose docs live in dir.
type fixture struct {
	db     *sql.DB
	dir    string
	taskID int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := openDB(t)
	exec1(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1, '/p', 'p', '2026-01-01T00:00:00Z')`)
	taskID := exec1(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 'Epic', 'goal', 'running', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'workspace', '2026-09-01-epic')`)
	return fixture{db: db, dir: t.TempDir(), taskID: taskID}
}

// phase inserts a phase whose doc carries body; returns its id.
func (f fixture) phase(t *testing.T, seq int, body string) int64 {
	t.Helper()
	doc := filepath.Join(f.dir, "phase-"+string(rune('0'+seq))+".md")
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return exec1(t, f.db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (?, ?, ?, ?)`,
		f.taskID, seq, "Phase", doc)
}

// actual inserts one phase_actuals row.
func (f fixture) actual(t *testing.T, phaseID int64, uuid, outcome, start, at string, cost float64) {
	t.Helper()
	exec1(t, f.db, `INSERT INTO phase_actuals (phase_id, session_uuid, outcome, start_point, cost_usd, computed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, phaseID, uuid, outcome, start, cost, at)
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(DateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func parseWin(t *testing.T, from, to string) (time.Time, time.Time) {
	t.Helper()
	lo, hi, err := ParseWindow(from, to)
	if err != nil {
		t.Fatalf("ParseWindow(%q, %q): %v", from, to, err)
	}
	return lo, hi
}

func rowN(t *testing.T, r Report, key string) int {
	t.Helper()
	row, ok := r.Row(key)
	if !ok {
		t.Fatalf("row %q missing; rows = %+v", key, r.Rows)
	}
	return row.N
}

func TestParseWindow(t *testing.T) {
	lo, hi, err := ParseWindow("2026-07-30", "2026-10-09")
	if err != nil {
		t.Fatal(err)
	}
	if got := lo.Format(time.RFC3339); got != "2026-07-30T00:00:00Z" {
		t.Errorf("from = %s", got)
	}
	if got := hi.Format(time.RFC3339); got != "2026-10-09T23:59:59Z" {
		t.Errorf("to = %s", got)
	}
	// An RFC3339 edge is the exact instant.
	_, hi, err = ParseWindow("2026-07-30", "2026-10-09T17:00:00Z")
	if err != nil || hi.Format(time.RFC3339) != "2026-10-09T17:00:00Z" {
		t.Errorf("rfc3339 to = %v, %v", hi, err)
	}
	for _, c := range [][2]string{{"", "2026-10-09"}, {"2026-07-30", ""}, {"30.07.2026", "2026-10-09"},
		{"2026-07-30", "2026-13-01"}, {"2026-10-09", "2026-07-30"}} {
		if _, _, err := ParseWindow(c[0], c[1]); !errors.Is(err, ErrBadWindow) {
			t.Errorf("ParseWindow(%q, %q) err = %v, want ErrBadWindow", c[0], c[1], err)
		}
	}
}

// TestBuildWindowEdges: 00:00:00 on `from` and 23:59:59.999 on `to` are in;
// one millisecond either side is out — for every windowed source.
func TestBuildWindowEdges(t *testing.T) {
	f := newFixture(t)
	p := f.phase(t, 1, "- [x] done\n")
	f.actual(t, p, "before", "completed", "s", "2026-09-30T23:59:59.999Z", 1)
	f.actual(t, p, "first", "completed", "s", "2026-10-01T00:00:00Z", 1)
	f.actual(t, p, "last", "failed", "s", "2026-10-02T23:59:59.999Z", 1)
	f.actual(t, p, "after", "completed", "s", "2026-10-03T00:00:00Z", 1)
	for _, at := range []string{"2026-09-30T23:59:59.999Z", "2026-10-01T00:00:00.000Z", "2026-10-02T23:59:59.999Z", "2026-10-03T00:00:00.000Z"} {
		exec1(t, f.db, `INSERT INTO route_decisions (surface, subject, mode, signals_json, score, tier, pick_model, pick_effort, created_at)
			VALUES ('phaserun', 'phase:1', 'shadow', '{}', 1, 'S', 'sonnet', 'low', ?)`, at)
		exec1(t, f.db, `INSERT INTO verification_runs (target_key, status, started_at) VALUES ('phase:1', 'pass', ?)`, at)
		exec1(t, f.db, `INSERT INTO phase_reopens (phase_id, workspace_task_id, doc_path, reason, caught_by, created_at)
			VALUES (1, '1', '/d', 'r', 'operator', ?)`, at)
	}
	lo, hi := parseWin(t, "2026-10-01", "2026-10-02")
	rep, err := Build(f.db, lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int{
		KeyRuns: 2, KeyCompleted: 1, KeyFailed: 1, KeyRouter: 2, "verifier_phase_pass": 2,
		"verifier_all_pass": 2, KeyReopens: 2, "reopens_operator": 2,
	} {
		if got := rowN(t, rep, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	if rep.From != "2026-10-01T00:00:00Z" || rep.To != "2026-10-02T23:59:59Z" {
		t.Errorf("window = %s … %s", rep.From, rep.To)
	}
}

// TestBuildRows covers every row family on one seeded store.
func TestBuildRows(t *testing.T) {
	f := newFixture(t)
	// p1: a doc still waiting on a PR. Two noops from the same base, then a
	// noop from a new base.
	p1 := f.phase(t, 1, "- [x] code\n- [ ] open the pull request\n")
	f.actual(t, p1, "a1", "noop", "base1", "2026-10-01T10:00:00Z", 1.5)
	f.actual(t, p1, "a2", "noop", "base1", "2026-10-01T11:00:00Z", 2.5)
	f.actual(t, p1, "a3", "noop", "base2", "2026-10-01T12:00:00Z", 0.5)
	// p2: everything ticked now; the run's own last message names a manual step.
	p2 := f.phase(t, 2, "- [x] all\n```\n- [ ] push (inside a fence: not a criterion)\n```\n")
	f.actual(t, p2, "b1", "noop", "base3", "2026-10-02T10:00:00Z", 1)
	sid := exec1(t, f.db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (1, 'b1', '2026-10-02T09:00:00Z')`)
	exec1(t, f.db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, 1, 'assistant', '2026-10-02T09:00:00Z', 'working')`, sid)
	exec1(t, f.db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, 2, 'assistant', '2026-10-02T09:30:00Z', 'PHASE BLOCKED: needs a check вручну on the console')`, sid)
	// p3: the run's blocked event mentions a merge; p3's doc says nothing.
	p3 := f.phase(t, 3, "- [ ] write tests\n")
	f.actual(t, p3, "c0", "completed", "base4", "2026-09-01T10:00:00Z", 1)
	f.actual(t, p3, "c1", "noop", "base4", "2026-10-03T10:00:00Z", 1)
	exec1(t, f.db, `INSERT INTO run_events (engine, subject_id, session_uuid, kind, detail, created_at)
		VALUES ('phaserun', ?, 'c1', 'blocked', 'waiting on merge of #12', '2026-10-03T09:59:00Z')`, p3)
	// An event of the EARLIER run must not count for c1.
	exec1(t, f.db, `INSERT INTO run_events (engine, subject_id, session_uuid, kind, detail, created_at)
		VALUES ('phaserun', ?, 'c0', 'blocked', 'production deploy', '2026-09-01T09:00:00Z')`, p3)
	// p4: deleted from its plan — its run still counts, but cannot be split.
	f.actual(t, 9999, "d1", "noop", "", "2026-10-03T11:00:00Z", 0)
	// Outcomes beyond the noop.
	f.actual(t, p3, "c2", "partial", "base4", "2026-10-04T10:00:00Z", 1)
	f.actual(t, p3, "c3", "idle", "base4", "2026-10-04T11:00:00Z", 1)

	// Router: one divergent (pick sonnet, ran opus), one agreeing alias/id pair, one applied.
	exec1(t, f.db, `INSERT INTO route_decisions (surface, subject, mode, signals_json, score, tier, pick_model, pick_effort, used_model, applied, created_at)
		VALUES ('phaserun', 'phase:1', 'shadow', '{}', 1, 'S', 'sonnet', 'low', 'claude-opus-5-5', 0, '2026-10-01T00:00:00.000Z'),
		       ('phaserun', 'phase:2', 'active', '{}', 1, 'S', 'opus', 'low', 'claude-opus-5-5', 1, '2026-10-01T00:00:00.000Z'),
		       ('dispatch', 'task:1', 'shadow', '{}', 1, 'S', 'haiku', 'low', 'claude-opus-5-5', 0, '2026-10-01T00:00:00.000Z')`)
	// Verifier: phase verdicts with classes, plus a task verdict.
	exec1(t, f.db, `INSERT INTO verification_runs (target_key, status, detail, started_at) VALUES
		('phase:1', 'fail', 'tests: 3 failing', '2026-10-01T00:00:00Z'),
		('phase:2', 'inconclusive', 'env: no docker', '2026-10-01T00:00:00Z'),
		('phase:3', 'fail', '', '2026-10-01T00:00:00Z'),
		('phase:3', 'pass', 'ok', '2026-10-01T00:00:00Z'),
		('task:1', 'fail', 'x', '2026-10-01T00:00:00Z')`)
	exec1(t, f.db, `INSERT INTO phase_reopens (phase_id, workspace_task_id, doc_path, reason, caught_by, created_at) VALUES
		(1, '1', '/d', 'r', 'review', '2026-10-01T00:00:00Z'), (1, '1', '/d', 'r', 'none', '2026-10-01T00:00:00Z')`)

	lo, hi := parseWin(t, "2026-10-01", "2026-10-05")
	rep, err := Build(f.db, lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		KeyRuns: 8, KeyCompleted: 0, KeyPartial: 1, KeyNoop: 6, KeyFailed: 0, KeyOther: 1,
		KeyNoopPushPR: 4, KeyNoopManual: 1, KeyNoopUnexplained: 1,
		KeyNoopRepeat: 1, KeyNoopAfterNoop: 2, KeyNoopCost: 6,
		KeyRouter: 2, KeyRouterApplied: 1, KeyRouterDivergent: 1,
		"verifier_phase_fail": 2, "verifier_phase_inconclusive": 1, "verifier_phase_pass": 1,
		"verifier_class:tests": 1, "verifier_class:env": 1, "verifier_class:unspecified": 1,
		"verifier_all_fail": 3, KeyReviewRuns: 0,
		KeyReopens: 2, "reopens_review": 1, "reopens_none": 1, "reopens_operator": 0,
	}
	for key, n := range want {
		if got := rowN(t, rep, key); got != n {
			t.Errorf("%s = %d, want %d", key, got, n)
		}
	}
	cost, _ := rep.Row(KeyNoopCost)
	if cost.CostUSD == nil || *cost.CostUSD != 6.5 {
		t.Errorf("noop cost = %v, want 6.5", cost.CostUSD)
	}
	push, _ := rep.Row(KeyNoopPushPR)
	if !push.Estimated {
		t.Error("noop push/PR row must be estimated before criteria_land_open exists")
	}
	if !hasNote(rep, "phase_reviews is absent") || !hasNote(rep, "1 noop run(s) had no phase doc") {
		t.Errorf("notes = %v", rep.Notes)
	}
}

// TestBuildRecordedNoopSplit: once epic_phases carries the recorded open-criteria
// counts, they decide and the rows stop being estimates.
func TestBuildRecordedNoopSplit(t *testing.T) {
	f := newFixture(t)
	exec1(t, f.db, `ALTER TABLE epic_phases ADD COLUMN criteria_land_open INTEGER`)
	exec1(t, f.db, `ALTER TABLE epic_phases ADD COLUMN criteria_manual_open INTEGER`)
	p1 := f.phase(t, 1, "- [ ] nothing that matches\n")
	p2 := f.phase(t, 2, "- [ ] open the PR\n")
	exec1(t, f.db, `UPDATE epic_phases SET criteria_land_open = 2, criteria_manual_open = 1 WHERE id = ?`, p1)
	exec1(t, f.db, `UPDATE epic_phases SET criteria_land_open = 0, criteria_manual_open = 1 WHERE id = ?`, p2)
	f.actual(t, p1, "a", "noop", "", "2026-10-01T00:00:00Z", 0)
	f.actual(t, p2, "b", "noop", "", "2026-10-01T00:00:00Z", 0)
	f.actual(t, 777, "c", "noop", "", "2026-10-01T00:00:00Z", 0)
	rep, err := Build(f.db, day(t, "2026-10-01"), day(t, "2026-10-01").Add(24*time.Hour-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if rowN(t, rep, KeyNoopPushPR) != 1 || rowN(t, rep, KeyNoopManual) != 1 || rowN(t, rep, KeyNoopUnexplained) != 1 {
		t.Errorf("rows = %+v", rep.Rows)
	}
	if row, _ := rep.Row(KeyNoopPushPR); row.Estimated {
		t.Error("recorded split must not be estimated")
	}
}

// TestBuildFallbackRows: a phase whose last run has no actuals row is counted
// beside the main rows, by its derived outcome; one WITH an actuals row is not.
func TestBuildFallbackRows(t *testing.T) {
	f := newFixture(t)
	p1 := f.phase(t, 1, "- [x] a\n")
	p2 := f.phase(t, 2, "- [ ] a\n")
	exec1(t, f.db, `UPDATE epic_phases SET run_state = 'done', run_session_uuid = 'u1', run_ended_at = '2026-10-01T05:00:00Z',
		checkboxes_total = 1, checkboxes_done = 1, run_checkboxes_before = 0, run_checkboxes_after = 1 WHERE id = ?`, p1)
	exec1(t, f.db, `UPDATE epic_phases SET run_state = 'done', run_session_uuid = 'u2', run_ended_at = '2026-10-01T06:00:00Z',
		checkboxes_total = 1, checkboxes_done = 0 WHERE id = ?`, p2)
	f.actual(t, p2, "u2", "noop", "", "2026-10-01T06:00:00Z", 0)
	rep, err := Build(f.db, day(t, "2026-10-01"), day(t, "2026-10-01").Add(24*time.Hour-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FallbackRows.N != 1 || rep.FallbackRows.ByOutcome["completed"] != 1 {
		t.Errorf("fallback = %+v", rep.FallbackRows)
	}
	if rowN(t, rep, KeyRuns) != 1 {
		t.Errorf("runs = %d, want 1 (fallback never folds into the main rows)", rowN(t, rep, KeyRuns))
	}
}

// TestBuildOnOldSchema: a store without the newer tables (the CLI's
// no-migrate path on an older daemon DB) reports zeros and notes, not errors.
func TestBuildOnOldSchema(t *testing.T) {
	db := openDB(t)
	for _, tbl := range []string{"phase_reopens", "phase_actuals", "route_decisions", "verification_cache", "verification_runs", "run_events"} {
		exec1(t, db, `DROP TABLE IF EXISTS `+tbl)
	}
	rep, err := Build(db, day(t, "2026-10-01"), day(t, "2026-10-02"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{KeyRuns, KeyRouter, "verifier_phase_fail", KeyReviewRuns, KeyReopens} {
		if rowN(t, rep, key) != 0 {
			t.Errorf("%s != 0", key)
		}
	}
	for _, n := range []string{"phase_actuals is absent", "route_decisions is absent", "verification_runs is absent", "phase_reopens is absent"} {
		if !hasNote(rep, n) {
			t.Errorf("note %q missing: %v", n, rep.Notes)
		}
	}
}

func TestBuildPhaseReviewsTable(t *testing.T) {
	f := newFixture(t)
	exec1(t, f.db, `CREATE TABLE phase_reviews (id INTEGER PRIMARY KEY, created_at TEXT NOT NULL)`)
	exec1(t, f.db, `INSERT INTO phase_reviews (created_at) VALUES ('2026-10-01T01:00:00Z'), ('2026-11-01T01:00:00Z')`)
	rep, err := Build(f.db, day(t, "2026-10-01"), day(t, "2026-10-02"))
	if err != nil {
		t.Fatal(err)
	}
	if rowN(t, rep, KeyReviewRuns) != 1 {
		t.Errorf("review runs = %d", rowN(t, rep, KeyReviewRuns))
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		text         string
		land, manual bool
	}{
		{"open the PR", true, false},
		{"gh pr create", true, false},
		{"Push the branch", true, false},
		{"pushed already", false, false},
		{"merge to main", true, false},
		{"reprocess", false, false},
		{"перевірити вручну", false, true},
		{"деплой на прод", false, true},
		{"продукт", false, false},
		{"check it manually in the Console", false, true},
		{"production rollout", false, true},
	}
	for _, c := range cases {
		land, man := classify([]string{c.text})
		if land != c.land || man != c.manual {
			t.Errorf("classify(%q) = %v/%v, want %v/%v", c.text, land, man, c.land, c.manual)
		}
	}
}

func TestVerdictClass(t *testing.T) {
	if got := verdictClass("  tests: 3 failing"); got != "tests" {
		t.Errorf("got %q", got)
	}
	if got := verdictClass(strings.Repeat("x", 80)); len([]rune(got)) != 61 {
		t.Errorf("long class not capped: %q", got)
	}
}

func TestRender(t *testing.T) {
	cost := 1.25
	rep := Report{From: "a", To: "b", Rows: []Row{
		{Key: "runs", Label: "phase runs", N: 3},
		{Key: "noop_cost", Label: "noop cost", N: 1, CostUSD: &cost},
		{Key: "noop_push_pr", Label: "noop · push", N: 2, Estimated: true},
	}, FallbackRows: Fallback{N: 2, ByOutcome: map[string]int{"noop": 1, "completed": 1}}, Notes: []string{"hello"}}
	var buf bytes.Buffer
	if err := Render(&buf, rep, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"phase runs        3", "$1.25", "(est.)", "fallback (no actuals row): 2 — completed 1, noop 1", "note: hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	buf.Reset()
	if err := Render(&buf, rep, true); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back.Rows) != 3 || back.FallbackRows.N != 2 {
		t.Errorf("json round trip: %v %+v", err, back)
	}
}

func hasNote(r Report, sub string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
