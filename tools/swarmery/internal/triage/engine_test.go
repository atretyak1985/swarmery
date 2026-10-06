package triage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// countingJudge returns a fixed (Answer, error) pair and counts its calls.
type countingJudge struct {
	a     Answer
	err   error
	calls int
}

func (j *countingJudge) Judge(context.Context, Item) (Answer, error) {
	j.calls++
	return j.a, j.err
}

// A2: an item whose kind differs from its source's is never judged or applied.
func TestRunRejectsItemOfForeignKind(t *testing.T) {
	j := &stubJudge{value: "a"}
	s := newTestService(t, j)
	src := &fakeSource{kind: "friction", items: []Item{item("classifier", "", "k1", "r1", "r2")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if j.calls != 0 || len(src.applied) != 0 {
		t.Fatalf("judge calls=%d applied=%v", j.calls, src.applied)
	}
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil || len(vs) != 2 || r.Rejected != 2 {
		t.Fatalf("verdicts = %+v run = %+v err=%v", vs, r, err)
	}
	for _, v := range vs {
		if v.Kind != "friction" || v.State != StateRejected ||
			v.Reason != "source friction offered an item of kind classifier" {
			t.Fatalf("verdict = %+v", v)
		}
	}
}

// A3: a failed judge call still books its cost and session on the run.
func TestRunBooksCostOfFailedJudgeCall(t *testing.T) {
	j := &countingJudge{a: Answer{SessionUUID: "sess-x", CostUSD: 0.25}, err: errors.New("unparsable")}
	s := newTestService(t, j)
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1", "r2")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if r.CostUSD != 0.25 || len(r.SessionUUIDs) != 1 || r.SessionUUIDs[0] != "sess-x" || r.Failed != 2 {
		t.Fatalf("run = %+v", r)
	}
	if st := states(t, s, r.ID); st["r1"] != StateFailed || st["r2"] != StateFailed || len(src.applied) != 0 {
		t.Fatalf("states = %v applied = %v", st, src.applied)
	}
}

// A3: ClaudeJudge returns the session id and cost together with its error.
func TestJudgeErrorEnvelopeKeepsCostAndSession(t *testing.T) {
	for name, out := range map[string]string{
		"is_error":   `{"is_error":true,"result":"rate limited","session_id":"s-err","total_cost_usd":0.07}`,
		"unparsable": `{"result":"{\"values\": oops","session_id":"s-err","total_cost_usd":0.07}`,
	} {
		j, _ := cannedJudge(out, nil)
		a, err := j.Judge(context.Background(), judgeItem())
		if err == nil || a.SessionUUID != "s-err" || a.CostUSD != 0.07 {
			t.Errorf("%s: answer = %+v, err = %v", name, a, err)
		}
	}
}

// seedVerdict stores a verdict for kind/ref in state, created at `at`.
func seedVerdict(t *testing.T, s *Service, kind, ref, state string, at time.Time) {
	t.Helper()
	now := s.now
	s.now = func() time.Time { return at }
	defer func() { s.now = now }()
	if _, err := s.insertVerdict(Verdict{RunID: mustInsertRun(t, s), Kind: kind, Ref: ref, State: state}); err != nil {
		t.Fatal(err)
	}
}

// preparingSource is a fakeSource with a Preparer; it records prepared keys.
type preparingSource struct {
	fakeSource
	prepErr  error
	prepared []string
}

func (p *preparingSource) Prepare(_ context.Context, it *Item) error {
	p.prepared = append(p.prepared, it.Key)
	if p.prepErr != nil {
		return p.prepErr
	}
	it.Evidence = "prepared"
	return nil
}

// evidenceJudge records the evidence of every item it is asked about.
type evidenceJudge struct {
	stubJudge
	seen []string
}

func (j *evidenceJudge) Judge(ctx context.Context, it Item) (Answer, error) {
	j.seen = append(j.seen, it.Key+"="+it.Evidence)
	return j.stubJudge.Judge(ctx, it)
}

// B1: blocked candidates do not eat the cap, and only judged items are prepared.
func TestRunCapCountsOnlyUnblockedItems(t *testing.T) {
	j := &evidenceJudge{stubJudge: stubJudge{value: "noise"}}
	s := newTestService(t, j)
	src := &preparingSource{fakeSource: fakeSource{kind: "friction", items: []Item{
		item("friction", "", "k1", "r1"), item("friction", "", "k2", "r2"), item("friction", "", "k3", "r3"),
		item("friction", "", "k4", "r4"), item("friction", "", "k5", "r5"),
	}}}
	s.Register(src)
	for _, ref := range []string{"r1", "r2", "r3"} {
		seedVerdict(t, s, "friction", ref, StateSuggested, s.clock())
	}
	r := mustRun(t, s, StartReq{Cap: 2})
	if r.Total != 2 || r.Applied != 2 || strings.Join(src.applied, ",") != "r4,r5" {
		t.Fatalf("run = %+v applied = %v", r, src.applied)
	}
	if got := strings.Join(src.prepared, ","); got != "k4,k5" {
		t.Fatalf("prepared = %s", got)
	}
	if got := strings.Join(j.seen, ","); got != "k4=prepared,k5=prepared" {
		t.Fatalf("judged = %s", got)
	}
}

// B1: a Prepare error fails every part of the item and applies nothing.
func TestRunPrepareErrorLeavesItemUntouched(t *testing.T) {
	j := &stubJudge{value: "noise"}
	s := newTestService(t, j)
	src := &preparingSource{prepErr: errors.New("transcript gone"),
		fakeSource: fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1", "r2")}}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if r.Failed != 2 || j.calls != 0 || len(src.applied) != 0 {
		t.Fatalf("run = %+v calls = %d applied = %v", r, j.calls, src.applied)
	}
	vs, _ := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	for _, v := range vs {
		if v.State != StateFailed || v.Reason != "prepare: transcript gone" {
			t.Fatalf("verdict = %+v", v)
		}
	}
	// A foreign-kind item is rejected before Prepare is ever called.
	s2 := newTestService(t, j)
	foreign := &preparingSource{fakeSource: fakeSource{kind: "friction", items: []Item{item("classifier", "", "k1", "r1")}}}
	s2.Register(foreign)
	mustRun(t, s2, StartReq{})
	if len(foreign.prepared) != 0 {
		t.Fatalf("foreign item prepared: %v", foreign.prepared)
	}
}

// B2: one case per blocking rule (and its boundary).
func TestPartBlockedRules(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	recent, old := now.Add(-time.Hour), now.Add(-8*24*time.Hour)
	type seed struct {
		state string
		at    time.Time
	}
	cases := map[string]struct {
		seeds []seed
		want  bool
	}{
		"nothing":                  {nil, false},
		"suggested":                {[]seed{{StateSuggested, old}}, true},
		"sample":                   {[]seed{{StateSample, old}}, true},
		"undone is permanent":      {[]seed{{StateUndone, now.AddDate(-1, 0, 0)}}, true},
		"undoing":                  {[]seed{{StateUndoing, old}}, true},
		"skipped recently":         {[]seed{{StateSkipped, recent}}, true},
		"skipped long ago":         {[]seed{{StateSkipped, old}}, false},
		"applied":                  {[]seed{{StateApplied, recent}}, false},
		"two recent failures":      {[]seed{{StateFailed, recent}, {StateFailed, recent}}, false},
		"three recent failures":    {[]seed{{StateFailed, recent}, {StateFailed, recent}, {StateFailed, recent}}, true},
		"failed+rejected mix of 3": {[]seed{{StateFailed, recent}, {StateRejected, recent}, {StateRejected, recent}}, true},
		"one of three is old":      {[]seed{{StateFailed, recent}, {StateFailed, recent}, {StateFailed, old}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t, nil)
			for _, sd := range c.seeds {
				seedVerdict(t, s, "friction", "r1", sd.state, sd.at)
			}
			seedVerdict(t, s, "lesson", "r1", StateSuggested, recent) // other kind never counts
			got, err := s.partBlocked("friction", "r1")
			if err != nil || got != c.want {
				t.Fatalf("partBlocked = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

// BlockedBefore is the blocking rule seen from one run's start: only the
// verdicts of earlier runs count, and the windows are counted back from that
// start — while partBlocked (the engine, now) counts every run.
func TestBlockedBeforeCountsOnlyEarlierRuns(t *testing.T) {
	start := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s := newTestService(t, nil)
	s.now = func() time.Time { return start.Add(2 * time.Hour) }
	seedVerdict(t, s, "friction", "held", StateSample, start.Add(-time.Hour))
	seedVerdict(t, s, "friction", "old-skip", StateSkipped, start.Add(-8*24*time.Hour))
	seedVerdict(t, s, "friction", "recent-skip", StateSkipped, start.Add(-time.Hour))
	this := mustInsertRun(t, s) // the audited run
	seedVerdict(t, s, "friction", "later", StateSample, start.Add(time.Hour))

	for ref, want := range map[string]bool{"held": true, "old-skip": false, "recent-skip": true, "later": false, "none": false} {
		got, err := BlockedBefore(s.DB, "friction", ref, this, start)
		if err != nil || got != want {
			t.Errorf("BlockedBefore(%q) = %v, %v; want %v", ref, got, err, want)
		}
	}
	// The engine's own view still blocks the part a later run holds.
	if got, err := s.partBlocked("friction", "later"); err != nil || !got {
		t.Errorf("partBlocked(later) = %v, %v; want true", got, err)
	}
}

// B2: one blocked part blocks the whole item.
func TestRunItemBlockedByAnyPart(t *testing.T) {
	j := &stubJudge{value: "noise"}
	s := newTestService(t, j)
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1", "r2")}}
	s.Register(src)
	seedVerdict(t, s, "friction", "r2", StateUndone, s.clock())
	if r := mustRun(t, s, StartReq{}); r.Total != 0 || j.calls != 0 || len(src.applied) != 0 {
		t.Fatalf("run = %+v calls = %d applied = %v", r, j.calls, src.applied)
	}
}

// B2: three failures within 7 days block re-judging; older ones stop counting.
func TestRunStopsAfterThreeFailures(t *testing.T) {
	j := &stubJudge{err: errors.New("boom")}
	s := newTestService(t, j)
	s.Register(&fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}})
	for range 4 {
		mustRun(t, s, StartReq{})
	}
	if j.calls != 3 {
		t.Fatalf("judge calls = %d, want 3", j.calls)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	mustRun(t, s, StartReq{})
	if j.calls != 4 {
		t.Fatalf("judge calls after a week = %d, want 4", j.calls)
	}
}

// B2: undone is permanent — the operator took the action back.
func TestRunNeverRedoesUndonePart(t *testing.T) {
	j := &stubJudge{value: "noise"}
	s := newTestService(t, j)
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil || len(vs) != 1 || vs[0].State != StateApplied {
		t.Fatalf("first run verdicts = %+v err = %v", vs, err)
	}
	if _, err := s.Undo(context.Background(), vs[0].ID); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	if r2 := mustRun(t, s, StartReq{}); r2.Total != 0 || j.calls != 1 || len(src.applied) != 1 {
		t.Fatalf("redo: run=%+v calls=%d applied=%v", r2, j.calls, src.applied)
	}
}

// decidingSource is a fakeSource that answers every item by rule.
type decidingSource struct {
	fakeSource
	ans Answer
}

func (d *decidingSource) Decide(Item) (Answer, bool) { return d.ans, true }

// B3: a part the answer has no value for is skipped; the others proceed.
func TestRunMissingPartIsSkipped(t *testing.T) {
	check := func(t *testing.T, s *Service, src *fakeSource) {
		t.Helper()
		r := mustRun(t, s, StartReq{})
		if r.Applied != 2 || r.Skipped != 1 || r.Failed != 0 || len(src.applied) != 2 {
			t.Fatalf("run = %+v applied=%v", r, src.applied)
		}
		vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
		if err != nil || len(vs) != 3 {
			t.Fatalf("verdicts = %+v err = %v", vs, err)
		}
		for _, v := range vs {
			if v.Ref == "r3" && (v.State != StateSkipped || v.Reason != "no answer") {
				t.Fatalf("r3 = %+v", v)
			}
		}
	}
	t.Run("judge", func(t *testing.T) {
		s := newTestService(t, &stubJudge{values: map[string]string{"r1": "noise", "r2": "noise"}})
		src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1", "r2", "r3")}}
		s.Register(src)
		check(t, s, src)
	})
	t.Run("decider", func(t *testing.T) {
		s := newTestService(t, nil)
		src := &decidingSource{fakeSource: fakeSource{kind: "friction",
			items: []Item{item("friction", "", "k1", "r1", "r2", "r3")}},
			ans: Answer{Values: map[string]string{"r1": "noise", "r2": "noise", "zz": "noise"}}}
		s.Register(src)
		check(t, s, &src.fakeSource)
	})
	t.Run("no usable ref", func(t *testing.T) {
		s := newTestService(t, nil)
		src := &decidingSource{fakeSource: fakeSource{kind: "friction",
			items: []Item{item("friction", "", "k1", "r1", "r2")}},
			ans: Answer{Values: map[string]string{"zz": "noise"}}}
		s.Register(src)
		r := mustRun(t, s, StartReq{})
		if r.Failed != 2 || r.Skipped != 0 || len(src.applied) != 0 {
			t.Fatalf("run = %+v", r)
		}
		vs, _ := s.ListVerdicts(VerdictFilter{RunID: r.ID})
		for _, v := range vs {
			if v.State != StateFailed || !strings.Contains(v.Reason, "no value for any part") {
				t.Fatalf("verdict = %+v", v)
			}
		}
	})
}
