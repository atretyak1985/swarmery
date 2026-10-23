package phaserun

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// planReviewBlockOutput is a reviewer reply carrying a `round: plan` block.
const planReviewBlockOutput = "- P1 api/x.go:1 — reads x.id as text although D-2 chose integers.\n\n" +
	"```yaml\nkind: review\nround: plan\nverdict: FAIL\ncomplete: true\nfindings:\n" +
	"  - {id: RP-1, type: contract, file: api/x.go, line: 1, evidence: \"reads x.id as text\"}\n" +
	"resolves: []\nids:\n" +
	"  - {id: D-1, evidence: \"phase 1 @ abc store/schema.sql:1\"}\n" +
	"  - {id: D-2, evidence: missing}\n" +
	"  - {id: SC-1, evidence: \"phase 2 @ def api/x.go:1\"}\n" +
	"drift:\n  - {id: D-2, where: \"api/x.go:1\", how: \"reads x.id as text, D-2 chose integers\"}\n" +
	"checked_callers: []\n```\nVERDICT: FAIL\n"

// The plan review's prompt carries the decision table and the plan round
// contract; the reviewer's `round: plan` block lands as decision_evidence (kind
// review) and its drift as a plan-conformance finding.
func TestPlanReviewDecisionTableAndBlock(t *testing.T) {
	s, db, taskID, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, question, status, answer, created_at)
		VALUES (?, 'D-1', 1, 'interview', 'Key type?', 'decided', 'integer ids', '2026-10-01T00:00:00Z'),
		       (?, 'D-2', 2, 'interview', 'Column type of x.id?', 'decided', 'INTEGER', '2026-10-01T00:00:00Z')`, taskID, taskID)
	mustExec(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-1', 'api reads x')`, taskID)
	mustExec(t, db, `UPDATE epic_phases SET covers = '["SC-1"]', decision_deps = '["D-1"]' WHERE id = ?`, p1)
	mustExec(t, db, `UPDATE epic_phases SET decision_deps = '["D-2"]' WHERE id = ?`, p2)
	rv.outputs = []string{planReviewBlockOutput}

	s.stamp(p2, doc2, "done", "")

	calls := rv.calls()
	if len(calls) != 1 {
		t.Fatalf("plan reviews = %d, want 1", len(calls))
	}
	prompt := calls[0].Prompt
	for _, want := range []string{
		"DECISIONS AND CRITERIA (3):",
		"| id | text | status | claimed by phases |",
		"| D-1 | Key type? → integer ids | remaining | 1 |",
		"| D-2 | Column type of x.id? → INTEGER | remaining | 2 |",
		"| SC-1 | api reads x | remaining | 1 |",
		"PLAN ROUND CONTRACT",
		"drift: one entry per place the code departs",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("plan review prompt lacks %q", want)
		}
	}
	if strings.Index(prompt, "DECISIONS AND CRITERIA") > strings.Index(prompt, "PHASE DIFFS") {
		t.Error("the decision table must come before the diffs")
	}

	tb, err := wsingest.LoadDecisionTable(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range tb.Rows {
		got[r.ID] = r.Status
	}
	want := map[string]string{"D-1": wsingest.RowEvidence, "D-2": wsingest.RowRemaining, "SC-1": wsingest.RowEvidence}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("table statuses = %v, want %v", got, want)
	}
	if tb.PlanReview.Status != wsingest.PlanReviewVerified || tb.PlanReview.Verdict != "FAIL" {
		t.Errorf("plan review state = %+v, want verified FAIL", tb.PlanReview)
	}

	var typ, file, criterion, note, source string
	var line int
	if err := db.QueryRow(`SELECT type, file, line, criterion, note, source FROM review_findings
		WHERE workspace_task_id = ? AND round = 'plan' AND finding_id = 'drift-1'`, taskID).
		Scan(&typ, &file, &line, &criterion, &note, &source); err != nil {
		t.Fatalf("drift finding: %v", err)
	}
	if typ != "plan-conformance" || file != "api/x.go" || line != 1 || criterion != "D-2" || source != "daemon" ||
		note != "reads x.id as text, D-2 chose integers" {
		t.Errorf("drift finding = %s %s:%d %s %q %s", typ, file, line, criterion, note, source)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM review_findings WHERE workspace_task_id = ? AND round = 'plan'`, taskID).
		Scan(&n); err != nil || n != 2 {
		t.Errorf("plan round findings = %d (%v), want the finding + the drift", n, err)
	}
}

// An INCONCLUSIVE plan review reads "not verified" — never an empty table and
// never "remaining", which would claim a check that did not happen.
func TestPlanReviewInconclusiveIsNotVerified(t *testing.T) {
	s, db, taskID, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	mustExec(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-1', 'api reads x')`, taskID)
	mustExec(t, db, `UPDATE epic_phases SET covers = '["SC-1"]' WHERE id = ?`, p1)
	rv.outputs = []string{"The diff for phase 2 was unreadable.\nVERDICT: INCONCLUSIVE — no diff for phase 2"}

	s.stamp(p2, doc2, "done", "")

	tb, err := wsingest.LoadDecisionTable(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tb.PlanReview.Status != wsingest.PlanReviewNotVerified || tb.PlanReview.ReasonClass == "" {
		t.Fatalf("plan review state = %+v, want not-verified with a reason class", tb.PlanReview)
	}
	if len(tb.Rows) != 1 || tb.Rows[0].Status != wsingest.RowNotVerified {
		t.Errorf("rows = %+v, want SC-1 not-verified", tb.Rows)
	}
}

func TestRenderDecisionTableCapsRows(t *testing.T) {
	if renderDecisionTable(nil) != "" {
		t.Error("no rows must render nothing")
	}
	rows := make([]wsingest.DecisionRow, planReviewTableMaxRows+5)
	for i := range rows {
		rows[i] = wsingest.DecisionRow{ID: fmt.Sprintf("D-%d", i+1), Text: "a | b\nc " + strings.Repeat("x", 300), Status: "remaining"}
	}
	out := renderDecisionTable(rows)
	if !strings.Contains(out, "[… 5 more row(s) omitted") {
		t.Errorf("missing truncation marker:\n%s", out[len(out)-300:])
	}
	if strings.Contains(out, fmt.Sprintf("| D-%d |", planReviewTableMaxRows+1)) {
		t.Error("a row past the cap was rendered")
	}
	if !strings.Contains(out, "| D-1 | a \\| b c xxx") || strings.Contains(out, strings.Repeat("x", 200)) {
		t.Errorf("cell not escaped/capped: %s", strings.SplitN(out, "\n", 5)[3])
	}
}
