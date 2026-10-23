package wsingest

import (
	"strings"
	"testing"
)

func TestRenderDecisionTableTruncates(t *testing.T) {
	if RenderDecisionTable(nil, 1<<10) != "" {
		t.Error("no rows rendered a table")
	}
	var rows []DecisionTableRow
	for i := 1; i <= 60; i++ {
		rows = append(rows, DecisionTableRow{ID: "D-" + string(rune('0'+i%10)), Kind: TableKindDecision,
			Text: strings.Repeat("long question ", 20), Status: "decided", Answer: "yes", Phases: []int{2, 1}})
	}
	got := RenderDecisionTable(rows, 2<<10)
	if len(got) > 2<<10+200 {
		t.Errorf("table is %d bytes, cap 2 KB", len(got))
	}
	if !strings.Contains(got, "more row(s) truncated") {
		t.Errorf("no truncation marker:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Error("a long cell was not capped")
	}
}

func TestRowChipPrecedence(t *testing.T) {
	phase := func(id int64) *int64 { return &id }
	report := EvidenceItem{Kind: EvidenceKindReport, Status: EvidenceStatusEvidence}
	notImpl := EvidenceItem{Kind: EvidenceKindReport, Status: EvidenceStatusNotImplemented, Reason: "out of scope"}
	notImpl3 := EvidenceItem{Kind: EvidenceKindReport, Status: EvidenceStatusNotImplemented, Reason: "deferred to phase 6", PhaseID: phase(3)}
	report3 := EvidenceItem{Kind: EvidenceKindReport, Status: EvidenceStatusEvidence, PhaseID: phase(3)}
	report6 := EvidenceItem{Kind: EvidenceKindReport, Status: EvidenceStatusEvidence, PhaseID: phase(6)}
	revOK := EvidenceItem{Kind: EvidenceKindReview, Status: EvidenceStatusEvidence}
	revMissing := EvidenceItem{Kind: EvidenceKindReview, Status: EvidenceStatusMissing}
	none := PlanReviewState{Status: PlanReviewNone}
	nv := PlanReviewState{Status: PlanReviewNotVerified, Reason: "reviewer-timed-out"}
	cases := []struct {
		name   string
		ev     []EvidenceItem
		pr     PlanReviewState
		chip   string
		reason string
	}{
		{"nothing", nil, none, ChipRemaining, ""},
		{"report", []EvidenceItem{report}, none, ChipEvidence, ""},
		{"not implemented alone", []EvidenceItem{notImpl}, none, ChipNotImplemented, "out of scope"},
		{"not implemented, unverified", []EvidenceItem{notImpl}, nv, ChipNotImplemented, "out of scope"},
		{"deferral overtaken by a later phase's report", []EvidenceItem{notImpl3, report6}, none, ChipEvidence, ""},
		{"later phase's report, either order", []EvidenceItem{report6, notImpl3}, none, ChipEvidence, ""},
		{"the deferring phase's own mention proves nothing", []EvidenceItem{notImpl3, report3}, none, ChipNotImplemented, "deferred to phase 6"},
		{"phaseless report vs a deferral", []EvidenceItem{notImpl3, report}, none, ChipNotImplemented, "deferred to phase 6"},
		{"review evidence beats a deferral", []EvidenceItem{notImpl3, revOK}, none, ChipEvidence, ""},
		{"review missing confirms a deferral", []EvidenceItem{notImpl3, revMissing}, none, ChipNotImplemented, "deferred to phase 6"},
		{"review missing confirms a deferral over a later report", []EvidenceItem{notImpl3, report6, revMissing}, none, ChipNotImplemented, "deferred to phase 6"},
		{"review evidence beats missing on a deferral", []EvidenceItem{notImpl3, revMissing, revOK}, none, ChipEvidence, ""},
		{"review missing beats a report claim", []EvidenceItem{report, revMissing}, none, ChipRemaining, ""},
		{"review evidence", []EvidenceItem{revMissing, revOK}, none, ChipEvidence, ""},
		{"inconclusive plan review", nil, nv, ChipNotVerified, "reviewer-timed-out"},
		{"inconclusive but cited", []EvidenceItem{report}, nv, ChipEvidence, ""},
	}
	for _, c := range cases {
		chip, reason := rowChip(c.ev, c.pr)
		if chip != c.chip || reason != c.reason {
			t.Errorf("%s: chip=%s reason=%q, want %s %q", c.name, chip, reason, c.chip, c.reason)
		}
	}
}

// TestLoadDecisionTable: rows are the declared decisions then criteria, with
// the claiming phases and their evidence; an undeclared id is not a row; a doc's
// INCONCLUSIVE plan round makes the uncited rows not-verified.
func TestLoadDecisionTable(t *testing.T) {
	db := carryFixture(t)
	doc := "# Phase 1 — One\n**Covers:** SC-1, SC-2\n**Depends on decisions:** D-1\n\n## Acceptance Criteria\n- [x] a\n\n" +
		"## Completion Report\nDid D-1 and SC-1; also D-77.\nnot implemented: SC-2 — no fixture data\n"
	planDir, _ := reviewPlan(t, doc)
	scanReviewPlan(t, db, planDir)
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, question, status, answer, created_at)
		VALUES (?, 'D-1', 1, 'Q1', 'decided', 'A1', '2026-10-23T00:00:00Z'), (?, 'D-3', 3, 'Q3', 'undecided', '', '2026-10-23T00:00:00Z')`,
		carryTaskID, carryTaskID)
	mustExec(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text, done) VALUES (?, 0, 'SC-1', 'one', 1), (?, 1, 'SC-2', 'two', 0)`,
		carryTaskID, carryTaskID)

	tb, err := LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]DecisionTableRow{}
	for _, r := range tb.Rows {
		ids = append(ids, r.ID)
		byID[r.ID] = r
	}
	if strings.Join(ids, ",") != "D-1,D-3,SC-1,SC-2" {
		t.Fatalf("rows = %v", ids)
	}
	if r := byID["D-1"]; r.Chip != ChipEvidence || len(r.Phases) != 1 || r.Phases[0] != 1 || len(r.Evidence) != 1 ||
		r.Evidence[0].PhaseSeq == nil || *r.Evidence[0].PhaseSeq != 1 || r.Evidence[0].Source != "doc" {
		t.Errorf("D-1 = %+v", r)
	}
	if r := byID["SC-2"]; r.Chip != ChipNotImplemented || r.Reason != "no fixture data" {
		t.Errorf("SC-2 = %+v", r)
	}
	if r := byID["D-3"]; r.Chip != ChipRemaining || len(r.Phases) != 0 || r.Evidence == nil {
		t.Errorf("D-3 = %+v", r)
	}
	if byID["SC-1"].Status != "done" || byID["D-1"].Answer != "A1" {
		t.Errorf("declared state lost: %+v %+v", byID["SC-1"], byID["D-1"])
	}
	if tb.PlanReview.Status != PlanReviewNone || tb.Counts != (DecisionTableCounts{Evidence: 2, Remaining: 1, NotImplemented: 1}) {
		t.Errorf("review=%+v counts=%+v", tb.PlanReview, tb.Counts)
	}

	// An interactive plan round, INCONCLUSIVE, in the doc.
	doc2 := doc + "\n## Review\n\n```yaml\nkind: review\nround: plan\nverdict: INCONCLUSIVE\ncomplete: false\n```\n"
	planDir2, _ := reviewPlan(t, doc2)
	_ = planDir2
	mustExec(t, db, `DELETE FROM epic_phases`)
	scanReviewPlan(t, db, planDir2)
	tb, err = LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tb.PlanReview.Status != PlanReviewNotVerified {
		t.Errorf("plan review = %+v, want not-verified", tb.PlanReview)
	}
	for _, r := range tb.Rows {
		if r.ID == "D-3" && r.Chip != ChipNotVerified {
			t.Errorf("D-3 = %+v, want not-verified", r)
		}
	}
}
