package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "decisions", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseDecisionsBasic(t *testing.T) {
	got := ParseDecisions(readFixture(t, "basic.md"))
	want := []DecisionLine{
		{Seq: 1, Cost: "cheap", Status: "decided", Question: "Which key shape?", Answer: "orders/line-items id"},
		{Seq: 2, Cost: "cheap", Status: "recommended", Question: "TTL default?",
			Answer: "5 minutes " + RecommendedSuffix, Reason: "a config value, changed in one line"},
		{Seq: 3, Cost: "expensive", Status: "decided", Question: "Which store backs the cache?",
			Answer: "SQLite table", Reason: "a data migration to undo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDecisions =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseDecisionsUndecidedAndDuplicates(t *testing.T) {
	got := ParseDecisions(readFixture(t, "undecided.md"))
	if len(got) != 2 {
		t.Fatalf("lines = %+v, want 2 (duplicate D-2 and prose ignored)", got)
	}
	if d := got[1]; d.ID() != "D-2" || d.Cost != "expensive" || d.Status != "undecided" ||
		d.Answer != UndecidedAnswer || d.Reason != "every writer has to change" {
		t.Errorf("D-2 = %+v", d)
	}
}

func TestParseDecisionsLegacyPlanYieldsNothing(t *testing.T) {
	if got := ParseDecisions("# Plan\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n| 1 | A | `phase-1.md` | — |\n"); got != nil {
		t.Errorf("legacy README parsed to %+v, want nil", got)
	}
}

// The renderer and the parser are one format: whatever FormatDecisionLine
// writes, ParseDecisions reads back unchanged.
func TestFormatDecisionLineRoundTrips(t *testing.T) {
	in := []DecisionLine{
		{Seq: 4, Cost: "expensive", Status: "undecided", Question: "Split the\nservice?", Answer: "", Reason: "two deploys"},
		{Seq: 5, Cost: "cheap", Status: "decided", Question: "Name?", Answer: "orders-cache"},
	}
	readme := "# P\n\n## Decisions\n\n"
	for _, d := range in {
		readme += FormatDecisionLine(d) + "\n"
	}
	got := ParseDecisions(readme)
	if len(got) != 2 || got[0].Question != "Split the service?" || got[0].Answer != UndecidedAnswer ||
		got[0].Reason != "two deploys" || got[1].Answer != "orders-cache" || got[1].Reason != "" {
		t.Fatalf("round trip = %+v\nfrom:\n%s", got, readme)
	}
}

func TestParseDecisionDeps(t *testing.T) {
	cases := map[string][]string{
		"# P\n**Depends on decisions:** D-1, D-3\n":                                                {"D-1", "D-3"},
		"# P\n**Repo:** x · **Depends on:** — · **Depends on decisions:** D-2 · **Estimate:** S\n": {"D-2"},
		"# P\n| **Depends on decisions** | D-04, d-4, D-7 |\n":                                     {"D-4", "D-7"},
		"# P\n**Depends on:** 1, 2\n":                                                              nil,
		"# P\n## Goal\n**Depends on decisions:** D-1\n":                                            nil,
	}
	for md, want := range cases {
		if got := ParseDecisionDeps(md); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseDecisionDeps(%q) = %v, want %v", md, got, want)
		}
	}
}

func decisionRows(t *testing.T, db *sql.DB) []DecisionLine {
	t.Helper()
	rows, err := db.Query(`SELECT seq, cost, status, question, answer, cost_reason FROM phase_decisions
		WHERE workspace_task_id = ? ORDER BY seq`, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []DecisionLine
	for rows.Next() {
		var d DecisionLine
		if err := rows.Scan(&d.Seq, &d.Cost, &d.Status, &d.Question, &d.Answer, &d.Reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func scanDecisionsOnce(t *testing.T, db *sql.DB, planDir string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDecisions(tx, carryTaskID, planDir, parseDecisionsFile(planDir)); err != nil {
		tx.Rollback()
		t.Fatalf("applyDecisions: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Two scans of the same README leave the same rows — the upsert on
// (workspace_task_id, decision_id) never duplicates.
func TestApplyDecisionsIdempotent(t *testing.T) {
	db := carryFixture(t)
	planDir := writePlan(t, map[string]string{"README.md": readFixture(t, "basic.md")})
	scanDecisionsOnce(t, db, planDir)
	first := decisionRows(t, db)
	var firstDecided sql.NullString
	db.QueryRow(`SELECT decided_at FROM phase_decisions WHERE decision_id='D-1'`).Scan(&firstDecided)
	scanDecisionsOnce(t, db, planDir)
	second := decisionRows(t, db)
	if len(first) != 3 || !reflect.DeepEqual(first, second) {
		t.Fatalf("scan 1 = %+v\nscan 2 = %+v — want the same 3 rows", first, second)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM phase_decisions`).Scan(&n)
	if n != 3 {
		t.Errorf("phase_decisions rows = %d after two scans, want 3", n)
	}
	var again sql.NullString
	db.QueryRow(`SELECT decided_at FROM phase_decisions WHERE decision_id='D-1'`).Scan(&again)
	if !firstDecided.Valid || again != firstDecided {
		t.Errorf("decided_at moved on rescan: %v → %v", firstDecided, again)
	}

	// The README is the truth for what it states: an edit flips the row.
	edited := readFixture(t, "undecided.md")
	if err := os.WriteFile(filepath.Join(planDir, "README.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	scanDecisionsOnce(t, db, planDir)
	var status, cost string
	db.QueryRow(`SELECT status, cost FROM phase_decisions WHERE decision_id='D-2'`).Scan(&status, &cost)
	if status != "undecided" || cost != "expensive" {
		t.Errorf("D-2 after edit = (%s, %s), want (undecided, expensive)", status, cost)
	}
}

// PROCEED's rows (keyed by planning session, no task yet) are adopted onto the
// task the wizard's plan became, and the README then upserts over them without
// losing their daemon-owned fields.
func TestApplyDecisionsAdoptsSessionRows(t *testing.T) {
	db := carryFixture(t)
	planDir := writePlan(t, map[string]string{"README.md": readFixture(t, "undecided.md")})
	mustExec(t, db, `INSERT INTO planning_sessions (id, project_id, session_uuid, idea, plan_dir, status, created_at, updated_at)
		VALUES (5, 1, 'u-5', 'idea', ?, 'done', 'x', 'x')`, filepath.Dir(planDir))
	mustExec(t, db, `INSERT INTO phase_decisions (planning_session_id, decision_id, seq, question_id, question, cost, status, option_ids_json, created_at)
		VALUES (5, 'D-2', 2, 'invalidate', 'Invalidate on write or on read?', 'expensive', 'undecided', '["opt-a"]', 'x'),
		       (5, 'D-7', 7, 'extra', 'Only in the DB', 'cheap', 'recommended', '[]', 'x')`)
	scanDecisionsOnce(t, db, planDir)
	scanDecisionsOnce(t, db, planDir)

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM phase_decisions`).Scan(&n)
	if n != 3 { // D-1 (README-born) + adopted D-2 + adopted D-7
		t.Fatalf("rows = %d, want 3", n)
	}
	var qid, opts string
	var sess sql.NullInt64
	if err := db.QueryRow(`SELECT question_id, option_ids_json, planning_session_id FROM phase_decisions
		WHERE workspace_task_id = ? AND decision_id = 'D-2'`, carryTaskID).Scan(&qid, &opts, &sess); err != nil {
		t.Fatal(err)
	}
	if qid != "invalidate" || opts != `["opt-a"]` || sess.Int64 != 5 {
		t.Errorf("adopted D-2 lost daemon-owned fields: qid=%q opts=%s session=%v", qid, opts, sess)
	}
}

// The header line lands on epic_phases.decision_deps, and deleting it retracts it.
func TestApplyEpicsStoresDecisionDeps(t *testing.T) {
	db := carryFixture(t)
	warn, _ := collectWarn(t)
	planDir := writePlan(t, map[string]string{
		"README.md":  readFixture(t, "undecided.md"),
		"phase-1.md": "# Phase 1\n**Depends on decisions:** D-2\n\n## Acceptance criteria\n- [ ] a\n",
		"phase-2.md": "# Phase 2\n\n## Acceptance criteria\n- [ ] a\n",
	})
	applyPhases(t, db, parsePlan(planDir, warn))
	deps := func(doc string) string {
		var s string
		if err := db.QueryRow(`SELECT decision_deps FROM epic_phases WHERE doc_path LIKE ?`, "%"+doc).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := deps("phase-1.md"); got != `["D-2"]` {
		t.Errorf("phase-1 decision_deps = %s", got)
	}
	if got := deps("phase-2.md"); got != `[]` {
		t.Errorf("phase-2 decision_deps = %s", got)
	}
	os.WriteFile(filepath.Join(planDir, "phase-1.md"), []byte("# Phase 1\n\n- [ ] a\n"), 0o644)
	applyPhases(t, db, parsePlan(planDir, warn))
	if got := deps("phase-1.md"); got != `[]` {
		t.Errorf("phase-1 decision_deps after the line was deleted = %s, want []", got)
	}
}
