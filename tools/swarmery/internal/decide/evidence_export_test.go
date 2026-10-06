package decide

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// SessionEvidence is what the D2 question builder puts in front of the model:
// the task-type question's Input (opening line + digest).
func TestSessionEvidenceMatchesD2Input(t *testing.T) {
	db := openDB(t)
	seedEnding(t, db, "ev-1", testModel, "end_turn", "It tokenises the input and builds the tree.")

	got, err := SessionEvidence(db, "ev-1")
	if err != nil {
		t.Fatalf("SessionEvidence: %v", err)
	}
	s, reason, err := evalSession(context.Background(), db, "ev-1")
	if err != nil || reason != "" {
		t.Fatalf("evalSession: %q %v", reason, err)
	}
	qs := d2Questions(db, s, false)
	if qs[0].ID != QD2TaskType {
		t.Fatalf("first D2 question = %s, want %s", qs[0].ID, QD2TaskType)
	}
	if got != qs[0].Input {
		t.Fatalf("SessionEvidence differs from the D2 input\n got: %q\nwant: %q", got, qs[0].Input)
	}
	for _, want := range []string{"first request: what does the parser do?", "title: fix the parser", "builds the tree"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
}

func TestSessionEvidenceUnknownSession(t *testing.T) {
	db := openDB(t)
	if _, err := SessionEvidence(db, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestClearAgentTruth(t *testing.T) {
	db := openDB(t)
	seedTruth(t, db, QD2Outcome, "s-op", "shipped", "failed", "2026-09-29T10:00:00Z")
	mustExec(t, db, `UPDATE decisions SET ground_truth_source='operator' WHERE subject='s-op'`)
	mustExec(t, db, `INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, calibrated,
		backend, created_at) VALUES (?, 's-ag', 's-ag', 'h', 'shipped', 0.8, 1, 'local', '2026-09-29T09:00:00Z')`, QD2Outcome)
	var opID, agID int64
	if err := db.QueryRow(`SELECT id FROM decisions WHERE subject='s-op'`).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM decisions WHERE subject='s-ag'`).Scan(&agID); err != nil {
		t.Fatal(err)
	}
	if err := RecordGroundTruth(db, agID, "partial", TruthAgent, time.Now()); err != nil {
		t.Fatalf("record agent truth: %v", err)
	}

	if err := ClearAgentTruth(db, agID); err != nil {
		t.Fatalf("clear agent truth: %v", err)
	}
	var gt, gtAt sql.NullString
	var src string
	if err := db.QueryRow(`SELECT ground_truth, ground_truth_at, ground_truth_source FROM decisions WHERE id=?`, agID).
		Scan(&gt, &gtAt, &src); err != nil {
		t.Fatal(err)
	}
	if gt.Valid || gtAt.Valid || src != "" {
		t.Fatalf("agent label not cleared: truth=%v at=%v source=%q", gt, gtAt, src)
	}
	if err := ClearAgentTruth(db, agID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second clear = %v, want sql.ErrNoRows", err)
	}

	if err := ClearAgentTruth(db, opID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clear on operator label = %v, want sql.ErrNoRows", err)
	}
	if truth, source := truthSource(t, db, opID); truth != "failed" || source != TruthOperator {
		t.Fatalf("operator label changed: %q/%q", truth, source)
	}
	if err := ClearAgentTruth(db, 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clear on unknown id = %v, want sql.ErrNoRows", err)
	}
}
