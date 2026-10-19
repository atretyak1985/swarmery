package decide

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Run's candidate query held the single store connection for the whole pass
// when its per-session decision counts were planned through
// idx_decisions_question (see d2CandidatesQuery).
func TestD2CandidatesQueryUsesSubjectIndex(t *testing.T) {
	db := openDB(t)
	plan := explainPlan(t, db, d2CandidatesQuery(3),
		"2026-01-01T00:00:00.000Z", QD2TaskType, QD2Outcome, QD2Failure, 3,
		QD2TaskType, QD2Outcome, QD2Failure, d2MaxFailures, 20)
	var decisions []string
	for _, d := range plan {
		if strings.Contains(d, " d ") {
			decisions = append(decisions, d)
		}
	}
	if len(decisions) != 2 {
		t.Fatalf("want 2 decisions steps, got %q (plan: %q)", decisions, plan)
	}
	for _, d := range decisions {
		if !strings.Contains(d, "idx_decisions_subject") {
			t.Errorf("decisions step = %q, want a search through idx_decisions_subject (plan: %q)", d, plan)
		}
	}
}

// failOn errors only on the listed questions, answering the rest.
type failOn struct {
	stub
	bad map[string]bool
}

func (f *failOn) Ask(ctx context.Context, q Question) (Answer, error) {
	f.calls++
	if f.bad[q.ID] {
		return Answer{}, errors.New("timeout")
	}
	v := map[string]string{QD2TaskType: "bugfix", QD2Outcome: "partial", QD2Failure: "none"}[q.ID]
	return Answer{Value: v, Confidence: 0.9, Calibrated: true}, nil
}

func (f *failOn) Name() string { return BackendLocal }

// A pass that answered task_type and outcome and then timed out on
// failure_cause must come back for the session; and a session the backend can
// never answer is given up after d2MaxFailures errored calls.
func TestLabelerRetriesAPartialSessionAndGivesUpOnAPoisonedOne(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-partial", "2026-09-20T11:00:00.000Z", "")
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	b := &failOn{bad: map[string]bool{QD2Failure: true}}
	e := &Engine{DB: db, Local: b, Now: now, Thresholds: map[string]float64{"d2": 0.6}}

	for pass := 1; pass <= d2MaxFailures; pass++ {
		if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
			t.Fatalf("pass %d: labelled %d (%v), want the partially answered session retried", pass, n, err)
		}
	}
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
		t.Fatalf("after %d failures the session is still asked about (%d)", d2MaxFailures, n)
	}

	// Once the backend recovers on a fresh session, all three answers mark it done.
	seedSession(t, db, "s-ok", "2026-09-21T11:00:00.000Z", "")
	b.bad = nil
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 1 {
		t.Fatalf("recovered backend labelled %d, want 1", n)
	}
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
		t.Fatalf("a fully answered session was asked again (%d)", n)
	}
}
