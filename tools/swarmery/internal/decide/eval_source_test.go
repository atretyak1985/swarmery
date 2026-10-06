package decide

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// Under a Limit, newer agent labels never push operator-labelled subjects out
// of the replayed window, and a subject with an operator label AND a newer
// agent label keeps the rank its operator label gives it.
func TestEvalLimitRanksOperatorLabelsFirst(t *testing.T) {
	db := openDB(t)
	for _, s := range []string{"s-op-1", "s-op-2", "s-op-3", "s-ag-1", "s-ag-2", "s-ag-3"} {
		seedOneShot(t, db, s)
	}
	for _, s := range []string{"s-op-1", "s-op-2", "s-op-3"} {
		seedTruth(t, db, QD2Outcome, s, "partial", "shipped", "2026-09-29T10:00:00Z")
	}
	for _, s := range []string{"s-ag-1", "s-ag-2", "s-ag-3"} {
		seedTruth(t, db, QD2Outcome, s, "partial", "shipped", "2026-10-01T10:00:00Z")
	}
	mustExec(t, db, `UPDATE decisions SET ground_truth_source = 'agent' WHERE session_uuid LIKE 's-ag-%'`)

	rep, err := Eval(context.Background(), db, nil, EvalOptions{Questions: []string{QD2Outcome}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	q := evalQ(t, rep, QD2Outcome)
	if q.Labelled != 3 || q.Replayed != 3 {
		t.Errorf("headline = labelled %d replayed %d, want 3/3 (the operator subjects)", q.Labelled, q.Replayed)
	}
	if ag := q.Sources[1]; ag.Labelled != 0 {
		t.Errorf("agent source = %+v, want no agent subject inside a full Limit", ag)
	}
	if missed := rep.MissedFloors(map[string]float64{QD2Outcome: 0.5}); len(missed) != 0 {
		t.Errorf("missed floors = %v, want none", missed)
	}

	// s-op-1 gains a newer agent label on another question: its rank still
	// comes from its operator label, so it does not jump ahead of s-op-0.
	seedOneShot(t, db, "s-op-0")
	seedTruth(t, db, QD2Outcome, "s-op-0", "partial", "shipped", "2026-09-30T10:00:00Z")
	seedTruth(t, db, QD2TaskType, "s-op-1", "docs", "docs", "2026-10-02T10:00:00Z")
	mustExec(t, db, `UPDATE decisions SET ground_truth_source = 'agent' WHERE session_uuid = 's-op-1' AND question_id = ?`, QD2TaskType)
	subs, err := evalTruthSet(context.Background(), db, EvalQuestions, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range subs {
		order = append(order, s.uuid)
	}
	want := []string{"s-op-0", "s-op-1", "s-op-2", "s-op-3", "s-ag-1", "s-ag-2", "s-ag-3"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// An agent's label never feeds a threshold: Eval's headline agreement and
// Summary's Agreed count operator labels only, and Eval reports each source
// in Sources.
func TestEvalAndSummarySkipAgentLabels(t *testing.T) {
	db := openDB(t)
	// Rules-only replay: R4 answers outcome "shipped" for every one-shot session.
	for _, s := range []string{"s-op-1", "s-op-2", "s-ag-1", "s-ag-2", "s-ag-3"} {
		seedOneShot(t, db, s)
	}
	seedTruth(t, db, QD2Outcome, "s-op-1", "partial", "shipped", "2026-09-29T10:00:00Z") // replay agrees; stored disagrees
	seedTruth(t, db, QD2Outcome, "s-op-2", "partial", "partial", "2026-09-29T10:00:00Z") // replay disagrees; stored agrees
	for _, s := range []string{"s-ag-1", "s-ag-2", "s-ag-3"} {
		seedTruth(t, db, QD2Outcome, s, "partial", "shipped", "2026-09-29T10:00:00Z") // replay agrees; stored disagrees
	}
	mustExec(t, db, `UPDATE decisions SET ground_truth_source = 'agent' WHERE session_uuid LIKE 's-ag-%'`)

	rep, err := Eval(context.Background(), db, nil, EvalOptions{Questions: []string{QD2Outcome}})
	if err != nil {
		t.Fatal(err)
	}
	q := evalQ(t, rep, QD2Outcome)
	if q.Labelled != 2 || q.Replayed != 2 || q.Agree != 1 || q.Agreement == nil || *q.Agreement != 0.5 {
		t.Errorf("headline = labelled %d replayed %d agree %d agreement %v, want 2/2/1/0.5 (operator only)",
			q.Labelled, q.Replayed, q.Agree, q.Agreement)
	}
	if len(q.Sources) != 2 {
		t.Fatalf("sources = %+v, want operator and agent", q.Sources)
	}
	op, ag := q.Sources[0], q.Sources[1]
	if op.Source != TruthOperator || op.Labelled != 2 || op.Replayed != 2 || op.Agree != 1 {
		t.Errorf("operator source = %+v", op)
	}
	if ag.Source != TruthAgent || ag.Labelled != 3 || ag.Replayed != 3 || ag.Agree != 3 || ag.Agreement == nil || *ag.Agreement != 1 {
		t.Errorf("agent source = %+v", ag)
	}
	var text bytes.Buffer
	if err := RenderEval(&text, rep, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"source operator", "source agent"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}

	stats, err := Summary(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.QuestionID != QD2Outcome {
			continue
		}
		if s.WithTruth != 2 || s.Agreed != 1 || s.Agreement == nil || *s.Agreement != 0.5 {
			t.Errorf("summary = withTruth %d agreed %d agreement %v, want 2/1/0.5 (operator only)", s.WithTruth, s.Agreed, s.Agreement)
		}
		if s.AgentTruth != 3 || s.AgentAgreed != 0 {
			t.Errorf("summary agent = %d/%d, want 3/0", s.AgentTruth, s.AgentAgreed)
		}
	}
}
