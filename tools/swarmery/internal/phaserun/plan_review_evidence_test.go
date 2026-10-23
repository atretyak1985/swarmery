package phaserun

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// seedPlanIDs gives the fixture plan one decision (D-2, claimed by phase 2's
// `**Depends on decisions:**`) and two spec criteria (SC-3 and SC-4, both
// covered by phase 1).
func seedPlanIDs(t *testing.T, s *Service, taskID, p1, p2 int64) {
	t.Helper()
	db := s.DB
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, question, status, answer, created_at)
		VALUES (?, 'D-2', 2, 'Column or second table for x?', 'decided', 'one column', '2026-10-23T00:00:00Z')`, taskID)
	mustExec(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-3', 'x is stored'), (?, 1, 'SC-4', 'x is | indexed')`, taskID, taskID)
	mustExec(t, db, `UPDATE epic_phases SET covers = '["SC-3","SC-4"]' WHERE id = ?`, p1)
	mustExec(t, db, `UPDATE epic_phases SET decision_deps = '["D-2"]' WHERE id = ?`, p2)
}

// The plan review is handed the decisions-and-criteria table and the plan
// round's ids/drift contract; its `round: plan` block lands as decision
// evidence (kind review) and plan-conformance findings.
func TestPlanReviewDecisionTableAndBlock(t *testing.T) {
	s, db, taskID, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	seedPlanIDs(t, s, taskID, p1, p2)
	rv.outputs = []string{"- P1 store/schema.sql:1 — x has no index although SC-4 asks for one.\n\n" +
		"```yaml\nkind: review\nround: plan\nverdict: FAIL\nfindings: []\n" +
		"ids:\n  - {id: D-2, evidence: \"phase 1 @ abc1234\"}\n  - {id: SC-3, evidence: \"phase 1\"}\n  - {id: SC-4, evidence: missing}\n" +
		"drift:\n  - {id: D-2, where: \"api/x.go:1\", how: \"reads a second table D-2 ruled out\"}\n```\nVERDICT: FAIL"}

	s.stamp(p2, doc2, "done", "")

	calls := rv.calls()
	if len(calls) != 1 {
		t.Fatalf("plan reviews = %d, want 1", len(calls))
	}
	p := calls[0].Prompt
	for _, want := range []string{
		"DECISIONS AND CRITERIA",
		"| id | text | status | phases |",
		"| D-2 | Column or second table for x? | decided → one column | 2 |",
		"| SC-3 | x is stored | open | 1 |",
		`| SC-4 | x is \| indexed | open | 1 |`,
		"ON THE PLAN ROUND",
		"evidence: missing",
		"{id: D-4, where:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plan review prompt lacks %q", want)
		}
	}

	table, err := wsingest.LoadDecisionTable(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if table.PlanReview.Status != wsingest.PlanReviewFail {
		t.Errorf("plan review status = %+v, want fail", table.PlanReview)
	}
	chips := map[string]string{}
	for _, r := range table.Rows {
		chips[r.ID] = r.Chip
	}
	if chips["D-2"] != wsingest.ChipEvidence || chips["SC-3"] != wsingest.ChipEvidence || chips["SC-4"] != wsingest.ChipRemaining {
		t.Errorf("chips = %v, want D-2/SC-3 evidence, SC-4 remaining", chips)
	}
	var typ, criterion, file string
	if err := db.QueryRow(`SELECT type, criterion, file FROM review_findings WHERE doc_path = ?`,
		wsingest.PlanReviewDocPath(taskID)).Scan(&typ, &criterion, &file); err != nil {
		t.Fatalf("drift finding: %v", err)
	}
	if typ != "plan-conformance" || criterion != "D-2" || file != "api/x.go:1" {
		t.Errorf("drift finding = %s %s %s", typ, criterion, file)
	}
}

// An INCONCLUSIVE plan review renders not-verified: the epic's table carries the
// status with its reason class, and the rows nothing cites say not-verified
// rather than remaining. Its evidence rows are cleared.
func TestPlanReviewInconclusiveIsNotVerified(t *testing.T) {
	s, db, taskID, p1, p2, doc2, _, rv, _ := planReviewFixture(t)
	seedPlanIDs(t, s, taskID, p1, p2)
	if err := wsingest.ApplyPlanReview(db, taskID, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO decision_evidence (workspace_task_id, ref_id, phase_id, doc_path, kind, status, excerpt, created_at)
		VALUES (?, 'SC-3', ?, '/elsewhere/phase-1.md', 'report', 'evidence', 'SC-3 shipped', '2026-10-23T00:00:00Z')`, taskID, p1)
	rv.outputs = []string{"I could not finish reading the diffs."} // no VERDICT line

	s.stamp(p2, doc2, "done", "")
	if n := len(rv.calls()); n != 1 {
		t.Fatalf("plan reviews = %d, want 1", n)
	}
	table, err := wsingest.LoadDecisionTable(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if table.PlanReview.Status != wsingest.PlanReviewNotVerified || table.PlanReview.Reason != classReviewerNoVerdict {
		t.Errorf("plan review = %+v, want not-verified / %s", table.PlanReview, classReviewerNoVerdict)
	}
	chips := map[string]wsingest.DecisionTableRow{}
	for _, r := range table.Rows {
		chips[r.ID] = r
	}
	if chips["SC-3"].Chip != wsingest.ChipEvidence {
		t.Errorf("SC-3 = %+v, want evidence (a report cites it)", chips["SC-3"])
	}
	for _, id := range []string{"D-2", "SC-4"} {
		if r := chips[id]; r.Chip != wsingest.ChipNotVerified || r.Reason != classReviewerNoVerdict {
			t.Errorf("%s = %+v, want not-verified with the reason class", id, r)
		}
	}
	if table.Counts.NotVerified != 2 || table.Counts.Evidence != 1 {
		t.Errorf("counts = %+v", table.Counts)
	}
}
