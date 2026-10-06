package triage

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	dec "github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
)

// seedAuditVerdict is one classifier verdict on decision id in state, under a
// fresh run.
func seedAuditVerdict(t *testing.T, db *sql.DB, id int64, value, state string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO triage_runs(trigger, status, started_at) VALUES('operator','ok','2026-10-06T12:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()
	mustExecT(t, db, `INSERT INTO triage_verdicts(run_id, kind, ref, value, state, created_at)
		VALUES(?, ?, ?, ?, ?, '2026-10-06T12:00:00Z')`, run, kindClassifier, ref(id), value, state)
}

func label(t *testing.T, db *sql.DB, id int64, truth, source string) {
	t.Helper()
	if source == dec.TruthOperator {
		if err := dec.RecordGroundTruth(db, id, truth, source, time.Now()); err != nil {
			t.Fatal(err)
		}
		return
	}
	mustExecT(t, db, `UPDATE decisions SET ground_truth = ?, ground_truth_source = ? WHERE id = ?`, truth, source, id)
}

func mustAudit(t *testing.T, db *sql.DB) []AuditRow {
	t.Helper()
	rows, err := ClassifierAudit(db)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestClassifierAuditEmpty(t *testing.T) {
	s, _ := newClassifierEnv(t, &stubJudge{})
	rows := mustAudit(t, s.DB)
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want an empty non-nil slice", rows)
	}
	if b, _ := json.Marshal(rows); string(b) != "[]" {
		t.Fatalf("json = %s, want []", b)
	}
}

func TestClassifierAuditCounts(t *testing.T) {
	s, _ := newClassifierEnv(t, &stubJudge{})
	db := s.DB
	seedClassifierSession(t, db, "audit-1", 1, "x")
	mk := func(q, answer string) int64 { return seedDecision(t, db, q, "audit-1", answer, "2026-10-02T10:00:00Z") }

	unlabelled := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, unlabelled, "shipped", StateSample)

	agree := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, agree, "shipped", StateAudited)
	label(t, db, agree, "shipped", dec.TruthOperator)

	disagree := mk(dec.QD2TaskType, "feature")
	seedAuditVerdict(t, db, disagree, "feature", StateSample)
	label(t, db, disagree, "bugfix", dec.TruthOperator)

	byAgent := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, byAgent, "shipped", StateSample)
	label(t, db, byAgent, "shipped", dec.TruthAgent)

	observed := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, observed, "shipped", StateAudited)
	label(t, db, observed, "shipped", dec.TruthObserved)

	applied := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, applied, "shipped", StateApplied)
	label(t, db, applied, "shipped", dec.TruthOperator)

	// Two sample verdicts on one decision: only the newest (disagreeing) counts.
	twice := mk(dec.QD2Outcome, "failed")
	seedAuditVerdict(t, db, twice, "shipped", StateSample)
	seedAuditVerdict(t, db, twice, "failed", StateSample)
	label(t, db, twice, "shipped", dec.TruthOperator)

	got := mustAudit(t, db)
	want := []AuditRow{
		{QuestionID: dec.QD2Outcome, Answered: 2, Agree: 1},
		{QuestionID: dec.QD2TaskType, Answered: 1, Agree: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
