package phaserun

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// ── D1 (learning-loop phase 9) on the settle loop ──
//
// The classifier is consulted only on the rules' `continue` branch with a
// stop_reason of end_turn. These tests script the SAME run as
// TestSettle_ContinuedThenDone (first turn reports progress with b unticked,
// the continuation finishes) and vary only the classifier.

type d1Stub struct {
	a     decide.Answer
	calls int
}

func (s *d1Stub) Name() string { return decide.BackendLocal }
func (s *d1Stub) Ask(context.Context, decide.Question) (decide.Answer, error) {
	s.calls++
	return s.a, nil
}

// endTurn stamps every assistant turn of the session with stop_reason end_turn,
// which is what the API reports for a plain end of turn.
func endTurn(t *testing.T, db *sql.DB, uuid string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE turns SET stop_reason='end_turn'
		WHERE session_id=(SELECT id FROM sessions WHERE session_uuid=?)`, uuid); err != nil {
		t.Fatalf("stop_reason: %v", err)
	}
}

func continuedThenDone(t *testing.T, db *sql.DB, p1 int64) *stubRunner {
	doc := phaseDocPath(t, db, p1)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		if spec.Resume {
			mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			seedTranscript(t, db, spec.SessionUUID, "Finished the second one.\n\nPHASE DONE")
		} else {
			mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")
			seedTranscript(t, db, spec.SessionUUID, "I finished the first criterion. Should I continue with the second?")
		}
		endTurn(t, db, spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	return r
}

func runWithD1(t *testing.T, eng func(db *sql.DB) *decide.Engine) (db *sql.DB, p1 int64, r *stubRunner) {
	t.Helper()
	db, _, p1, _ = fixture(t)
	r = continuedThenDone(t, db, p1)
	s := newTestService(db, r, &stubWt{})
	s.Decide = eng(db)
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return db, p1, r
}

func d1Engine(mode decide.Mode, a decide.Answer, stub **d1Stub) func(*sql.DB) *decide.Engine {
	return func(db *sql.DB) *decide.Engine {
		s := &d1Stub{a: a}
		if stub != nil {
			*stub = s
		}
		return &decide.Engine{DB: db, Local: s, DefaultModes: map[string]decide.Mode{"d1": mode},
			Thresholds: map[string]float64{"d1": 0.85}}
	}
}

func decisionCount(t *testing.T, db *sql.DB) (n, acted int, truth string) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(acted),0), COALESCE(MAX(ground_truth),'') FROM decisions`).Scan(&n, &acted, &truth); err != nil {
		t.Fatal(err)
	}
	return n, acted, truth
}

// assertUnchanged: the run behaved exactly like TestSettle_ContinuedThenDone.
func assertUnchanged(t *testing.T, db *sql.DB, p1 int64, r *stubRunner) {
	t.Helper()
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "done" || runErr.Valid {
		t.Errorf("run_state = %q (%q), want done", state, runErr.String)
	}
	if n := r.specCount(); n != 2 {
		t.Errorf("spawned %d times, want 2", n)
	}
	if k := runEventKinds(t, db, p1); strings.Join(k, ",") != "continuation,done" {
		t.Errorf("run events = %v, want [continuation done]", k)
	}
}

func TestSettleD1_UnsetURLIsIdentical(t *testing.T) {
	cfg, _ := decide.ConfigFromEnv(func(string) string { return "" })
	cfg.Modes["d1"] = decide.ModeActive // even active is inert without a backend
	db, p1, r := runWithD1(t, func(db *sql.DB) *decide.Engine { return decide.New(db, cfg) })
	assertUnchanged(t, db, p1, r)
	if n, _, _ := decisionCount(t, db); n != 0 {
		t.Errorf("%d decisions recorded without a backend", n)
	}
}

func TestSettleD1_NilEngineIsIdentical(t *testing.T) {
	db, p1, r := runWithD1(t, func(*sql.DB) *decide.Engine { return nil })
	assertUnchanged(t, db, p1, r)
}

func TestSettleD1_ShadowIsIdenticalAndRecordsGroundTruth(t *testing.T) {
	var s *d1Stub
	db, p1, r := runWithD1(t, d1Engine(decide.ModeShadow, decide.Answer{Value: decide.D1Blocked, Confidence: 0.99, Calibrated: true}, &s))
	assertUnchanged(t, db, p1, r)
	n, acted, truth := decisionCount(t, db)
	if s.calls != 1 || n != 1 || acted != 0 {
		t.Errorf("calls=%d rows=%d acted=%d, want one logged, unacted call", s.calls, n, acted)
	}
	// The continuation ticked b: continuing was right.
	if truth != decide.D1Report {
		t.Errorf("ground truth = %q, want %q", truth, decide.D1Report)
	}
}

func TestSettleD1_ActiveReportContinues(t *testing.T) {
	db, p1, r := runWithD1(t, d1Engine(decide.ModeActive, decide.Answer{Value: decide.D1Report, Confidence: 0.95, Calibrated: true}, nil))
	assertUnchanged(t, db, p1, r)
}

func TestSettleD1_ActiveBlockedStampsBlocked(t *testing.T) {
	db, p1, r := runWithD1(t, d1Engine(decide.ModeActive, decide.Answer{Value: decide.D1Blocked, Confidence: 0.95, Calibrated: true}, nil))
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "blocked" || !strings.Contains(runErr.String, "classifier") {
		t.Errorf("run_state = %q (%q), want blocked by the classifier", state, runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — a blocked run is not continued", n)
	}
	if k := runEventKinds(t, db, p1); strings.Join(k, ",") != runcore.EventBlocked {
		t.Errorf("run events = %v", k)
	}
	if _, acted, _ := decisionCount(t, db); acted != 1 {
		t.Errorf("acted = %d, want 1", acted)
	}
}

func TestSettleD1_ActiveQuestionHandsToOperator(t *testing.T) {
	var notified decide.NeedsOperator
	db, p1, r := runWithD1(t, func(db *sql.DB) *decide.Engine {
		e := d1Engine(decide.ModeActive, decide.Answer{Value: decide.D1Question, Confidence: 0.95, Calibrated: true}, nil)(db)
		e.OnNeedsOperator = func(n decide.NeedsOperator) { notified = n }
		return e
	})
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" || !strings.Contains(runErr.String, "needs operator") {
		t.Errorf("run_state = %q (%q), want partial handed to the operator", state, runErr.String)
	}
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — no automatic continuation", n)
	}
	if notified.Engine != Engine || notified.SubjectID != p1 {
		t.Errorf("operator not notified: %+v", notified)
	}
}

func TestSettleD1_ActiveBelowThresholdIsTheSafeDefault(t *testing.T) {
	db, p1, r := runWithD1(t, d1Engine(decide.ModeActive, decide.Answer{Value: decide.D1Report, Confidence: 0.4, Calibrated: true}, nil))
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "partial" || !strings.Contains(runErr.String, "classifier unsure") || r.specCount() != 1 {
		t.Errorf("run_state = %q (%q) spawns=%d, want partial, no continuation", state, runErr.String, r.specCount())
	}
}
