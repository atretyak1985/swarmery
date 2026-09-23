package phaserun

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
)

// slowD1 answers like a shadow classifier but takes `lag` of the service clock
// to do it — a LAN backend that is slow or down until its timeout.
type slowD1 struct {
	advance func()
}

func (s *slowD1) Name() string { return decide.BackendLocal }
func (s *slowD1) Ask(context.Context, decide.Question) (decide.Answer, error) {
	s.advance()
	return decide.Answer{Value: decide.D1Blocked, Confidence: 0.99, Calibrated: true}, nil
}

// A shadow classifier's latency must never count against the continuation
// time-left guard: with 5m30s of budget and a one-minute classifier call, the
// run still continues and finishes exactly as it would with no classifier.
func TestSettleD1_ShadowLatencyDoesNotEatTheTimeGuard(t *testing.T) {
	t.Setenv("SWARMERY_PHASERUN_TIMEOUT", "5m30s")
	db, _, p1, _ := fixture(t)
	r := continuedThenDone(t, db, p1)
	s := newTestService(db, r, &stubWt{})
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.Decide = func(db *sql.DB) *decide.Engine {
		return &decide.Engine{DB: db, Local: &slowD1{advance: func() { now = now.Add(time.Minute) }},
			DefaultModes: map[string]decide.Mode{"d1": decide.ModeShadow},
			Thresholds:   map[string]float64{"d1": 0.85}}
	}(db)
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertUnchanged(t, db, p1, r)
}
