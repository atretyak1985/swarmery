package decide

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A cause added after labelling began agrees with the label an operator used
// before it existed — and never the other way round.
func TestAgrees(t *testing.T) {
	cases := []struct {
		name                    string
		question, truth, answer string
		want                    bool
	}{
		{"equal", QD2Failure, "timeout", "timeout", true},
		{"equal ignores case", QD2Outcome, "shipped", "Shipped", true},
		{"different", QD2Failure, "timeout", "refusal", false},
		{"truth other agrees with answer auth", QD2Failure, "other", "auth", true},
		{"truth other agrees with answer quota", QD2Failure, "other", "quota", true},
		{"truth other agrees with answer api-error", QD2Failure, "other", "api-error", true},
		{"parent match ignores case", QD2Failure, "Other", "API-Error", true},
		{"truth blocked-on-operator agrees with answer auth", QD2Failure, "blocked-on-operator", "auth", true},
		{"truth tool-error agrees with answer api-error", QD2Failure, "tool-error", "api-error", true},
		{"truth blocked-on-operator does not agree with answer quota", QD2Failure, "blocked-on-operator", "quota", false},
		{"truth tool-error does not agree with answer auth", QD2Failure, "tool-error", "auth", false},
		{"truth auth does not agree with answer blocked-on-operator", QD2Failure, "auth", "blocked-on-operator", false},
		{"truth auth does not agree with answer other", QD2Failure, "auth", "other", false},
		{"truth quota does not agree with answer other", QD2Failure, "quota", "other", false},
		{"truth api-error does not agree with answer other", QD2Failure, "api-error", "other", false},
		{"siblings do not agree", QD2Failure, "auth", "quota", false},
		{"a cause with no parent is not other", QD2Failure, "other", "timeout", false},
		{"the parent rule is d2.failure_cause only", QD2TaskType, "other", "auth", false},
		{"an empty truth agrees with nothing", QD2Failure, "", "", false},
		{"an empty answer agrees with nothing", QD2Failure, "other", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Agrees(tc.question, tc.truth, tc.answer); got != tc.want {
				t.Errorf("Agrees(%q, truth=%q, answer=%q) = %v, want %v", tc.question, tc.truth, tc.answer, got, tc.want)
			}
		})
	}
}

// The taxonomy grew at the end, every new cause names a parent that is itself a
// cause, and ground truth may take the new values.
func TestFailureCausesTaxonomy(t *testing.T) {
	want := []string{"none", "tool-error", "test-failure", "blocked-on-operator", "refusal", "timeout",
		"context-exhausted", "scope-misread", "other", "auth", "quota", "api-error"}
	if !slices.Equal(FailureCauses, want) {
		t.Fatalf("FailureCauses = %v, want the old order with auth, quota, api-error appended", FailureCauses)
	}
	for child, parents := range FailureParent {
		if !slices.Contains(FailureCauses, child) || len(parents) == 0 {
			t.Errorf("FailureParent[%q] = %v: the child must be a failure cause with a parent", child, parents)
		}
		for _, parent := range parents {
			if !slices.Contains(FailureCauses, parent) || FailureParent[parent] != nil {
				t.Errorf("FailureParent[%q] names %q: a parent must be an original failure cause", child, parent)
			}
		}
	}
	if !slices.Equal(OptionsFor(QD2Failure), FailureCauses) {
		t.Error("ground truth for d2.failure_cause must accept the whole taxonomy")
	}
}

// Summary counts a new cause against an old `other` label as agreement, and an
// old `other` answer against a new-cause label as a miss.
func TestSummaryParentAgreement(t *testing.T) {
	db := openDB(t)
	for _, row := range []struct{ q, answer, truth string }{
		{QD2Failure, "auth", "other"},      // parent: agrees
		{QD2Failure, "quota", "other"},     // parent: agrees
		{QD2Failure, "api-error", "other"}, // parent: agrees
		{QD2Failure, "other", "auth"},      // never the reverse
		{QD2Failure, "timeout", "timeout"}, // equal
		{QD2Failure, "timeout", "other"},   // no parent
		{QD2TaskType, "other", "other"},    // equal, another question
		{QD2TaskType, "bugfix", "other"},   // the parent rule does not leak
	} {
		mustExec(t, db, `INSERT INTO decisions (question_id, subject, input_hash, answer, confidence, backend, ground_truth, created_at)
			VALUES (?, 's', 'h', ?, 0.9, 'local', ?, '2026-09-30T10:00:00Z')`, row.q, row.answer, row.truth)
	}
	stats, err := Summary(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]QuestionStats{}
	for _, s := range stats {
		byID[s.QuestionID] = s
	}
	if f := byID[QD2Failure]; f.WithTruth != 6 || f.Agreed != 4 || f.Agreement == nil || *f.Agreement != 4.0/6.0 {
		t.Errorf("d2.failure_cause = %+v, want 4 of 6 agreed", f)
	}
	if tt := byID[QD2TaskType]; tt.WithTruth != 2 || tt.Agreed != 1 {
		t.Errorf("d2.task_type = %+v, want 1 of 2 agreed", tt)
	}
}

// A session with no turn has nothing to read: the labeler never asks about it,
// while a session with a turn beside it is labelled as before.
func TestLabelerSkipsZeroTurn(t *testing.T) {
	db := openDB(t)
	seedEmptySession(t, db, "s-empty", "2026-09-20T11:00:00.000Z", "")
	seedSession(t, db, "s-full", "2026-09-20T11:30:00.000Z", "")
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	s := &stub{name: BackendLocal, a: Answer{Value: "other", Confidence: 0.9, Calibrated: true},
		byQ: map[string]Answer{QD2Outcome: {Value: "partial", Confidence: 0.9, Calibrated: true}}}
	e := &Engine{DB: db, Local: s, Now: now}

	n, err := (&Labeler{E: e}).Run(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("run: labelled %d (%v), want only the session that has a turn", n, err)
	}
	if s.calls != 3 {
		t.Errorf("backend calls = %d, want 3 (one session, three questions)", s.calls)
	}
	for _, r := range decisionRows(t, db) {
		if r["subject"] != "s-full" {
			t.Errorf("a decision was recorded for %v, want s-full only", r["subject"])
		}
	}
	// It stays skipped on later passes instead of being retried for ever.
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
		t.Errorf("second pass labelled %d, want 0", n)
	}
	// The moment a turn is ingested the session becomes a candidate.
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = 's-empty'), 1, 'assistant', '2026-09-20T10:30:00.000Z', 'hello')`)
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 1 {
		t.Errorf("after its first turn the session was labelled %d times, want 1", n)
	}
}

// The labelling queue hides the decisions of a session that has no turn —
// nothing to judge them from — without deleting them; a decision with no
// session row at all stays.
func TestLabelQueueHidesZeroTurn(t *testing.T) {
	db := openDB(t)
	seedEmptySession(t, db, "s-empty", "2026-09-20T11:00:00.000Z", "")
	seedSession(t, db, "s-full", "2026-09-20T11:30:00.000Z", "")
	for _, row := range []struct{ q, subject, sess string }{
		{QD2TaskType, "s-empty", "s-empty"}, // id 1: hidden
		{QD2Outcome, "s-empty", "s-empty"},  // id 2: hidden
		{QD2Failure, "s-empty", "s-empty"},  // id 3: hidden
		{QD2TaskType, "s-full", "s-full"},   // id 4: listed
		{QD1, "phaserun:7", ""},             // id 5: no session — listed
		{QD2TaskType, "s-gone", "s-gone"},   // id 6: session row absent — listed
	} {
		mustExec(t, db, `INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
			VALUES (?, ?, ?, 'h', ?, 0.8, 'local', '2026-09-24T18:00:00Z')`, row.q, row.subject, row.sess, OptionsFor(row.q)[0])
	}
	ids := func(projectID int64) []int64 {
		t.Helper()
		items, err := LabelQueue(db, -1, "", projectID, false)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out
	}
	if got := ids(0); !slices.Equal(got, []int64{6, 5, 4}) {
		t.Errorf("queue = %v, want [6 5 4] (the zero-turn session's three rows hidden)", got)
	}
	if got := ids(1); !slices.Equal(got, []int64{4}) {
		t.Errorf("project queue = %v, want [4]", got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&n); err != nil || n != 6 {
		t.Errorf("decisions = %d (%v), want all 6 kept — hidden, not deleted", n, err)
	}
	// Once the session has a turn its rows are back.
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = 's-empty'), 1, 'user', '2026-09-20T10:30:00.000Z', 'hi')`)
	if got := ids(0); len(got) != 6 {
		t.Errorf("queue after the first turn = %v, want all 6", got)
	}
}
