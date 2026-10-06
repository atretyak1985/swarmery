package triage

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The engine never hands its own item to code it does not own: every Decide,
// Judge and Apply call receives a fresh deep copy, so an in-place change made
// there never reaches what the engine validates, applies or records.

// rewritingDecider rewrites the item it is handed: Parts[0].Ref becomes "gone".
type rewritingDecider struct{ fakeSource }

func (d *rewritingDecider) Decide(it Item) (Answer, bool) {
	orig := it.Parts[0].Ref
	it.Parts[0].Ref = "gone"
	return Answer{Values: map[string]string{"gone": "noise", orig: "noise"}, Reason: "rule"}, true
}

// T1a: a Decider cannot redirect a part to another (undone) ref.
func TestIsolationDeciderCannotRewriteRef(t *testing.T) {
	j := &stubJudge{value: "noise"}
	s := newTestService(t, j)
	seedVerdict(t, s, "friction", "gone", StateUndone, s.clock())
	src := &rewritingDecider{fakeSource: fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if got := strings.Join(src.applied, ","); got != "r1" {
		t.Fatalf("applied = %q, want r1", got)
	}
	if st := states(t, s, r.ID); len(st) != 1 || st["r1"] != StateApplied {
		t.Fatalf("states = %v", st)
	}
	if j.calls != 0 {
		t.Fatalf("judge called %d times for a decided item", j.calls)
	}
}

// crossPartApplier rewrites part 1 of the item it is handed while applying part 0.
type crossPartApplier struct {
	fakeSource
	setRef     string
	setAllowed []string
}

func (c *crossPartApplier) Apply(ctx context.Context, it Item, p Part, value, reason string, payload json.RawMessage) (Applied, error) {
	if len(it.Parts) > 1 && p.Ref == it.Parts[0].Ref {
		if c.setRef != "" {
			it.Parts[1].Ref = c.setRef
		}
		if c.setAllowed != nil {
			it.Parts[1].Allowed = c.setAllowed
		}
	}
	return c.fakeSource.Apply(ctx, it, p, value, reason, payload)
}

// T1b: an Apply for part 0 cannot change part 1's ref or Allowed.
func TestIsolationApplyCannotRewriteOtherPart(t *testing.T) {
	newItem := func() Item {
		return Item{Kind: "friction", Key: "k1", Parts: []Part{
			{Ref: "r1", Allowed: []string{"noise"}},
			{Ref: "r2", Allowed: []string{"fixable"}}, // the judge's "noise" is not allowed here
		}}
	}
	cases := map[string]crossPartApplier{
		"ref and allowed": {setRef: "evil", setAllowed: []string{"noise"}},
		"allowed only":    {setAllowed: []string{"noise"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t, &stubJudge{value: "noise"})
			src := &crossPartApplier{fakeSource: fakeSource{kind: "friction", items: []Item{newItem()}},
				setRef: c.setRef, setAllowed: c.setAllowed}
			s.Register(src)
			r := mustRun(t, s, StartReq{})
			if got := strings.Join(src.applied, ","); got != "r1" {
				t.Fatalf("applied = %q, want r1", got)
			}
			st := states(t, s, r.ID)
			if len(st) != 2 || st["r1"] != StateApplied || st["r2"] != StateRejected {
				t.Fatalf("states = %v", st)
			}
		})
	}
}

// mutatingJudge changes the item it is asked about, then answers for both the
// original and the rewritten ref.
type mutatingJudge struct{ calls int }

func (j *mutatingJudge) Judge(_ context.Context, it Item) (Answer, error) {
	j.calls++
	orig := it.Parts[0].Ref
	it.Kind = "classifier"
	it.Parts[0].Ref = "gone"
	if len(it.Parts[0].Allowed) > 0 {
		it.Parts[0].Allowed[0] = "hacked" // in-place write
	}
	return Answer{Values: map[string]string{orig: "noise", "gone": "noise"}, Reason: "j"}, nil
}

// T1c: a Judge that mutates its item changes nothing the engine validates,
// applies or records.
func TestIsolationJudgeMutationHasNoEffect(t *testing.T) {
	s := newTestService(t, &mutatingJudge{})
	seedVerdict(t, s, "friction", "gone", StateUndone, s.clock())
	src := &fakeSource{kind: "friction", items: []Item{{Kind: "friction", Key: "k1",
		Parts: []Part{{Ref: "r1", Allowed: []string{"noise"}}}}}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if got := strings.Join(src.applied, ","); got != "r1" {
		t.Fatalf("applied = %q, want r1", got)
	}
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Ref != "r1" || vs[0].Kind != "friction" || vs[0].State != StateApplied || vs[0].Value != "noise" {
		t.Fatalf("verdicts = %+v", vs)
	}
}

// allowedScribbler writes into the Allowed slice of the Part it applies.
type allowedScribbler struct{ fakeSource }

func (a *allowedScribbler) Apply(ctx context.Context, it Item, p Part, value, reason string, payload json.RawMessage) (Applied, error) {
	for i := range p.Allowed {
		p.Allowed[i] = "hacked"
	}
	return a.fakeSource.Apply(ctx, it, p, value, reason, payload)
}

// T1d: an Apply that writes into its Part's Allowed slice changes neither the
// engine's Allowed (a later part sharing that backing array is still validated
// against the original values) nor what Collect returned (a second run).
func TestIsolationApplyCannotMutateAllowed(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	shared := []string{"noise"} // both parts alias one backing array, as a Source may build them
	src := &allowedScribbler{fakeSource: fakeSource{kind: "friction", items: []Item{{Kind: "friction", Key: "k1",
		Parts: []Part{{Ref: "r1", Allowed: shared}, {Ref: "r2", Allowed: shared}}}}}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if st := states(t, s, r.ID); st["r1"] != StateApplied || st["r2"] != StateApplied {
		t.Fatalf("first run states = %v", st)
	}
	if !slices.Equal(shared, []string{"noise"}) {
		t.Fatalf("Collect's Allowed was written through: %v", shared)
	}
	r2 := mustRun(t, s, StartReq{})
	if st := states(t, s, r2.ID); st["r1"] != StateApplied || st["r2"] != StateApplied {
		t.Fatalf("second run states = %v", st)
	}
}

// shiftingKindSource answers Kind() differently on later calls: "lesson" when
// it is registered (a suggest-only kind), "classifier" every time after.
type shiftingKindSource struct {
	fakeSource
	kindCalls int
}

func (s *shiftingKindSource) Kind() string {
	s.kindCalls++
	if s.kindCalls == 1 {
		return "lesson"
	}
	return "classifier"
}

// The engine reads a Source's kind once, at registration. A Kind() that later
// answers "classifier" cannot move the source's items under the classifier's
// auto rule, past the blocking check, or into another kind's verdict rows.
func TestIsolationKindIsReadOnceAtRegistration(t *testing.T) {
	j := &stubJudge{value: "a"}
	s := newTestService(t, j)
	// classifier/r1 was undone by the operator: it must never be applied again.
	seedVerdict(t, s, "classifier", "r1", StateUndone, s.clock())
	src := &shiftingKindSource{fakeSource: fakeSource{items: []Item{item("classifier", "", "k1", "r1")}}}
	s.Register(src)
	if src.kindCalls != 1 {
		t.Fatalf("Register read Kind() %d times, want 1", src.kindCalls)
	}
	r := mustRun(t, s, StartReq{})
	if src.kindCalls != 1 {
		t.Fatalf("the run read Kind() again: %d calls in total, want 1", src.kindCalls)
	}
	if len(src.applied) != 0 {
		t.Fatalf("applied = %v, want nothing: the source is registered as lesson", src.applied)
	}
	if j.calls != 0 {
		t.Fatalf("judge called %d times for an item of a foreign kind", j.calls)
	}
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Kind != "lesson" || vs[0].Ref != "r1" || vs[0].State != StateRejected {
		t.Fatalf("verdicts = %+v, want one rejected lesson/r1 row", vs)
	}
}
