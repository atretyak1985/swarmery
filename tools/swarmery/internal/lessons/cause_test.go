package lessons

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// causeStub is a scripted decide backend.
type causeStub struct {
	a     decide.Answer
	err   error
	calls int
	input string
}

func (s *causeStub) Name() string { return decide.BackendLocal }
func (s *causeStub) Ask(_ context.Context, q decide.Question) (decide.Answer, error) {
	s.calls++
	s.input = q.Input
	return s.a, s.err
}

func newClassifier(db *sql.DB, b decide.Backend, mode decide.Mode) *CauseClassifier {
	e := &decide.Engine{DB: db, Local: b, DefaultModes: map[string]decide.Mode{"d3": mode},
		Thresholds: map[string]float64{"d3": 0.6}}
	return &CauseClassifier{DB: db, E: e, Threshold: threshold(0.6), Now: func() time.Time { return fixedNow }}
}

type causeRow struct {
	cause      sql.NullString
	conf       sql.NullFloat64
	decisionID sql.NullInt64
	at         sql.NullString
}

func readCause(t *testing.T, db *sql.DB, uuid string) causeRow {
	t.Helper()
	var r causeRow
	if err := db.QueryRow(`SELECT divergence_cause, divergence_cause_confidence, divergence_cause_decision_id,
		divergence_cause_at FROM phase_surprise WHERE session_uuid = ?`, uuid).
		Scan(&r.cause, &r.conf, &r.decisionID, &r.at); err != nil {
		t.Fatal(err)
	}
	return r
}

func d3Decisions(t *testing.T, db *sql.DB, uuid string) (n int, ruleValue, mode string) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(rule_value), ''), COALESCE(MAX(mode), '')
		FROM decisions WHERE question_id = ? AND subject = ?`, decide.QD3Cause, uuid).
		Scan(&n, &ruleValue, &mode); err != nil {
		t.Fatal(err)
	}
	return n, ruleValue, mode
}

func TestClassifyShadowRecordsTheDecisionOnly(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "u-shadow", 0.8, report)
	b := &causeStub{a: decide.Answer{Value: "code-differs", Confidence: 0.95, Calibrated: true}}
	c := newClassifier(db, b, decide.ModeShadow)

	out, err := c.Classify(context.Background(), phaseID, "u-shadow")
	if err != nil || !out.Asked || out.Label != "" {
		t.Fatalf("shadow outcome = %+v, %v; want asked, no label", out, err)
	}
	if n, _, mode := d3Decisions(t, db, "u-shadow"); n != 1 || mode != "shadow" {
		t.Fatalf("decisions = %d (mode %q), want 1 shadow row", n, mode)
	}
	if r := readCause(t, db, "u-shadow"); r.cause.Valid {
		t.Fatalf("shadow wrote divergence_cause=%q onto the surprise row", r.cause.String)
	}
	// The classifier saw the executor's own explanation, not a transcript.
	if !containsAll(b.input, "usage.cache_creation", "Phase One", "touched ingest, not cost") {
		t.Fatalf("input misses the divergence evidence:\n%s", b.input)
	}
	// Once per run: an error-free decision is never asked again.
	if out, _ := c.Classify(context.Background(), phaseID, "u-shadow"); out.Asked || b.calls != 1 {
		t.Fatalf("second classify asked again (calls=%d)", b.calls)
	}
}

func TestClassifyActiveStoresLabelAndSurvivesRescore(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "u-active", 0.8, report)
	b := &causeStub{a: decide.Answer{Value: "code-differs", Confidence: 0.9, Calibrated: true}}
	c := newClassifier(db, b, decide.ModeActive)

	out, err := c.Classify(context.Background(), phaseID, "u-active")
	if err != nil || out.Label != "code-differs" {
		t.Fatalf("active outcome = %+v, %v", out, err)
	}
	r := readCause(t, db, "u-active")
	if r.cause.String != "code-differs" || !r.conf.Valid || r.conf.Float64 != 0.9 ||
		r.decisionID.Int64 != out.DecisionID || out.DecisionID == 0 || r.at.String == "" {
		t.Fatalf("row = %+v, decision %d", r, out.DecisionID)
	}
	// A rescore of the run (the settled pass) upserts phase_surprise: the label
	// must survive it, like notified_at does.
	st, err := surprise.NewScorer(db, surprise.DefaultConfig()).Score(phaseID, "u-active")
	if err != nil || st == nil {
		t.Fatalf("rescore: %v, %v", st, err)
	}
	if r := readCause(t, db, "u-active"); r.cause.String != "code-differs" {
		t.Fatalf("rescore erased the label: %+v", r)
	}
}

func TestClassifyActiveBelowThresholdIsUnknown(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "u-low", 0.8, report)
	for _, a := range []decide.Answer{
		{Value: "scope-grew", Confidence: 0.4, Calibrated: true},
		{Value: "scope-grew", Confidence: 0.99, Calibrated: false}, // a self-report never clears the floor
	} {
		mustExec(t, db, `DELETE FROM decisions`)
		c := newClassifier(db, &causeStub{a: a}, decide.ModeActive)
		if _, err := c.Classify(context.Background(), phaseID, "u-low"); err != nil {
			t.Fatal(err)
		}
		r := readCause(t, db, "u-low")
		if r.cause.String != decide.LabelUnknown || r.conf.Valid {
			t.Fatalf("answer %+v stored %+v, want unknown with NULL confidence", a, r)
		}
	}
}

func TestClassifyModelFallbackIsTheRulesViewNotTheAnswer(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "u-fb", 0.8, report)
	mustExec(t, db, `UPDATE phase_actuals SET model_fallback = 1 WHERE session_uuid = 'u-fb'`)
	b := &causeStub{a: decide.Answer{Value: "code-differs", Confidence: 0.9, Calibrated: true}}
	c := newClassifier(db, b, decide.ModeActive)
	if _, err := c.Classify(context.Background(), phaseID, "u-fb"); err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 {
		t.Fatalf("a fallback must not short-circuit the classifier (calls=%d)", b.calls)
	}
	if _, rule, _ := d3Decisions(t, db, "u-fb"); rule != "model-fallback" {
		t.Fatalf("rule_value = %q, want model-fallback", rule)
	}
	if r := readCause(t, db, "u-fb"); r.cause.String != "code-differs" {
		t.Fatalf("label = %q, want the classifier's answer", r.cause.String)
	}
}

func TestClassifySkipsWithoutParagraphAndCapsFailures(t *testing.T) {
	db := openDB(t)
	noPara := seedRun(t, db, "u-nopara", 0.8, "Shipped it. Nothing diverged worth a paragraph.")
	b := &causeStub{a: decide.Answer{Value: "other", Confidence: 0.9, Calibrated: true}}
	c := newClassifier(db, b, decide.ModeActive)
	if out, err := c.Classify(context.Background(), noPara, "u-nopara"); err != nil || out.Asked || b.calls != 0 {
		t.Fatalf("no paragraph: %+v, %v, calls=%d", out, err, b.calls)
	}

	failing := seedRun(t, db, "u-fail", 0.8, report)
	bad := &causeStub{err: errors.New("backend down")}
	c = newClassifier(db, bad, decide.ModeActive)
	for i := 0; i < causeMaxFailures+2; i++ {
		if _, err := c.Classify(context.Background(), failing, "u-fail"); err != nil {
			t.Fatal(err)
		}
	}
	if bad.calls != causeMaxFailures {
		t.Fatalf("errored run asked %d times, want the cap %d", bad.calls, causeMaxFailures)
	}
	if r := readCause(t, db, "u-fail"); r.cause.Valid {
		t.Fatalf("a failed call wrote %q", r.cause.String)
	}
}

func TestCauseAfterScoreEligibility(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "u-hook", 0.8, report)
	st := &surprise.Stored{PhaseID: phaseID, SessionUUID: "u-hook"}
	st.Index = 0.8

	var jobs []func()
	enqueue := func(f func()) { jobs = append(jobs, f) }

	cases := []struct {
		name   string
		c      *CauseClassifier
		st     *surprise.Stored
		source string
		want   int
	}{
		{"unconfigured engine", &CauseClassifier{DB: db, E: &decide.Engine{DB: db}, Threshold: threshold(0.6)}, st, "run-end", 0},
		{"question off", newClassifier(db, &causeStub{}, decide.ModeOff), st, "run-end", 0},
		{"backfill", newClassifier(db, &causeStub{}, decide.ModeShadow), st, sourceBackfill, 0},
		{"no threshold", func() *CauseClassifier {
			c := newClassifier(db, &causeStub{}, decide.ModeShadow)
			c.Threshold = nil
			return c
		}(), st, "run-end", 0},
		{"below threshold", newClassifier(db, &causeStub{}, decide.ModeShadow),
			&surprise.Stored{Result: surprise.Result{Index: 0.3}, PhaseID: phaseID, SessionUUID: "u-hook"}, "run-end", 0},
		{"eligible", newClassifier(db, &causeStub{}, decide.ModeShadow), st, "run-end", 1},
	}
	for _, tc := range cases {
		jobs = nil
		tc.c.Go = enqueue
		tc.c.AfterScore(tc.st, tc.source)
		if len(jobs) != tc.want {
			t.Fatalf("%s: %d jobs, want %d", tc.name, len(jobs), tc.want)
		}
	}
	// The queued job runs the classification in the background.
	b := &causeStub{a: decide.Answer{Value: "tooling-env", Confidence: 0.9, Calibrated: true}}
	c := newClassifier(db, b, decide.ModeActive)
	jobs = nil
	c.Go = enqueue
	c.AfterScore(st, "run-end")
	jobs[0]()
	if r := readCause(t, db, "u-hook"); r.cause.String != "tooling-env" {
		t.Fatalf("background job stored %+v", r)
	}
	// A nil classifier is a no-op (the daemon's hook tolerates it).
	var nilC *CauseClassifier
	nilC.AfterScore(st, "run-end")
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
