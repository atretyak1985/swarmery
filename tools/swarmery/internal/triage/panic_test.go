package triage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// panicSource panics in Collect, or in Apply from the second call on.
type panicSource struct {
	fakeSource
	collectPanic bool
}

func (p *panicSource) Collect(ctx context.Context, sc Scope, limit int) ([]Item, error) {
	if p.collectPanic {
		panic("collect boom")
	}
	return p.fakeSource.Collect(ctx, sc, limit)
}

func (p *panicSource) Apply(ctx context.Context, it Item, pt Part, v, r string, pl json.RawMessage) (Applied, error) {
	if len(p.applied) >= 1 {
		panic("apply boom")
	}
	return p.fakeSource.Apply(ctx, it, pt, v, r, pl)
}

func TestRunPanicEndsRowAndUnblocks(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	s.Register(&panicSource{fakeSource: fakeSource{kind: "friction"}, collectPanic: true})
	r := mustRun(t, s, StartReq{})
	if r.Status != StatusFailed || !strings.HasPrefix(r.Error, "panic: ") {
		t.Fatalf("collect panic run = %+v", r)
	}

	s.Register(&panicSource{fakeSource: fakeSource{kind: "friction",
		items: []Item{item("friction", "", "k1", "r1"), item("friction", "", "k2", "r2")}}})
	r = mustRun(t, s, StartReq{})
	if r.Status != StatusFailed || r.Applied != 1 {
		t.Fatalf("apply panic run = %+v", r)
	}
	if st := states(t, s, r.ID); st["r1"] != StateApplied {
		t.Fatalf("first item verdict lost: %v", st)
	}
	if _, err := s.Start(StartReq{}); err != nil {
		t.Fatalf("start after panic: %v", err)
	}
}

func TestRunStartHealsOrphanRunningRow(t *testing.T) {
	s := newTestService(t, nil)
	if _, err := s.DB.Exec(`INSERT INTO triage_runs(trigger, status, started_at) VALUES('operator','running','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(StartReq{}); err != nil {
		t.Fatalf("start over orphan row: %v", err)
	}
	var st, msg string
	if err := s.DB.QueryRow(`SELECT status, error FROM triage_runs WHERE started_at='x'`).Scan(&st, &msg); err != nil {
		t.Fatal(err)
	}
	if st != StatusFailed || msg != "run ended without a result" {
		t.Fatalf("orphan = %s %q", st, msg)
	}
}
