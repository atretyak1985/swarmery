package triage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// fakeSource is an in-memory Source; it records every Apply/Undo call.
type fakeSource struct {
	kind       string
	items      []Item
	collectErr error
	applyErr   error
	follow     []Suggestion
	open       map[string]bool
	applied    []string // part refs
	undone     []int64  // every Source.Undo call, failed ones included
	undoErr    error
}

func (f *fakeSource) Kind() string { return f.kind }
func (f *fakeSource) Collect(_ context.Context, _ Scope, limit int) ([]Item, error) {
	if f.collectErr != nil {
		return nil, f.collectErr
	}
	if limit > 0 && len(f.items) > limit { // 0 = every candidate
		return f.items[:limit], nil
	}
	return f.items, nil
}
func (f *fakeSource) Apply(_ context.Context, _ Item, p Part, value, _ string, _ json.RawMessage) (Applied, error) {
	if f.applyErr != nil {
		return Applied{}, f.applyErr
	}
	f.applied = append(f.applied, p.Ref)
	return Applied{Prior: json.RawMessage(`{"was":"` + p.Ref + `"}`), Follow: f.follow}, nil
}
func (f *fakeSource) Undo(_ context.Context, v Verdict) error {
	f.undone = append(f.undone, v.ID)
	return f.undoErr
}
func (f *fakeSource) Open(_ context.Context, ref string) (bool, error) { return f.open[ref], nil }

// stubJudge answers every part with value (or values[ref]); err fails every call.
type stubJudge struct {
	value  string
	values map[string]string
	err    error
	calls  int
}

func (j *stubJudge) Judge(_ context.Context, it Item) (Answer, error) {
	j.calls++
	if j.err != nil {
		return Answer{}, j.err
	}
	vals := map[string]string{}
	for _, p := range it.Parts {
		if j.values != nil {
			if v, ok := j.values[p.Ref]; ok {
				vals[p.Ref] = v
			}
			continue
		}
		vals[p.Ref] = j.value
	}
	return Answer{Values: vals, Reason: "because", SessionUUID: "sess-" + it.Key, CostUSD: 0.01}, nil
}

func newTestService(t *testing.T, j Judge) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewService(db, j)
	s.Go = func(fn func()) { fn() }
	s.perm = func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	s.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	return s
}

func item(kind, class, key string, refs ...string) Item {
	it := Item{Kind: kind, Class: class, Key: key, Title: "t-" + key}
	for _, r := range refs {
		it.Parts = append(it.Parts, Part{Ref: r, Allowed: []string{"noise", "fixable", "a", "accept"}})
	}
	return it
}

func mustRun(t *testing.T, s *Service, req StartReq) Run {
	t.Helper()
	id, err := s.Start(req)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	r, err := s.GetRun(id)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return r
}

func states(t *testing.T, s *Service, runID int64) map[string]string {
	t.Helper()
	vs, err := s.ListVerdicts(VerdictFilter{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, v := range vs {
		out[v.Ref] = v.State
	}
	return out
}

func TestRunRecordsCountersCostSessions(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1"), item("friction", "", "k2", "r2")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{Scope: Scope{ProjectID: 7}, Trigger: TriggerSchedule})
	if r.Status != StatusOK || r.Trigger != TriggerSchedule || r.ScopeProjectID == nil || *r.ScopeProjectID != 7 {
		t.Fatalf("run = %+v", r)
	}
	if r.Total != 2 || r.Done != 2 || r.Applied != 2 || r.FinishedAt == nil {
		t.Fatalf("counters = %+v", r)
	}
	if r.CostUSD < 0.019 || len(r.SessionUUIDs) != 2 || r.SessionUUIDs[0] != "sess-k1" {
		t.Fatalf("cost/sessions = %v %v", r.CostUSD, r.SessionUUIDs)
	}
	if len(src.applied) != 2 {
		t.Fatalf("applied = %v", src.applied)
	}
	vs, _ := s.ListVerdicts(VerdictFilter{States: []string{StateApplied}, Kind: "friction", ProjectID: 7})
	if len(vs) != 2 || string(vs[0].Prior) == "{}" {
		t.Fatalf("verdicts = %+v", vs)
	}
	runs, _ := s.ListRuns(0)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	if a, _ := s.ActiveRun(); a != nil {
		t.Fatalf("active after finish = %+v", a)
	}
}

func TestRunBusyAndHealStale(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	var pending func()
	s.Go = func(fn func()) { pending = fn } // hold the run open
	id, err := s.Start(StartReq{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(StartReq{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start = %v, want ErrBusy", err)
	}
	if a, _ := s.ActiveRun(); a == nil || a.ID != id {
		t.Fatalf("active = %+v", a)
	}
	pending()
	if _, err := s.Start(StartReq{Trigger: "bogus"}); err == nil {
		t.Fatal("bogus trigger accepted")
	}
	// A 'running' row left by a crashed daemon blocks Start until healed.
	if _, err := s.DB.Exec(`INSERT INTO triage_runs(trigger, status, started_at) VALUES('operator','running','x')`); err != nil {
		t.Fatal(err)
	}
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	var st, msg string
	if err := s.DB.QueryRow(`SELECT status, error FROM triage_runs WHERE started_at='x'`).Scan(&st, &msg); err != nil {
		t.Fatal(err)
	}
	if st != StatusFailed || msg != "interrupted by daemon restart" {
		t.Fatalf("healed = %s %q", st, msg)
	}
	if _, err := s.Start(StartReq{}); err != nil {
		t.Fatalf("start after heal: %v", err)
	}
}

func TestRunJudgeFailureWritesNothing(t *testing.T) {
	for name, j := range map[string]*stubJudge{
		"error":      {err: errors.New("boom")},
		"unparsable": {values: map[string]string{}}, // omits every part
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t, j)
			src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1", "r2")}}
			s.Register(src)
			r := mustRun(t, s, StartReq{})
			if r.Status != StatusOK || r.Failed != 2 || r.Applied != 0 || len(src.applied) != 0 {
				t.Fatalf("run = %+v applied=%v", r, src.applied)
			}
			if st := states(t, s, r.ID); st["r1"] != StateFailed || st["r2"] != StateFailed {
				t.Fatalf("states = %v", st)
			}
		})
	}
}

func TestRunCollectErrorFailsRun(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	s.Register(&fakeSource{kind: "friction", collectErr: errors.New("db gone")})
	r := mustRun(t, s, StartReq{Kinds: []string{"friction"}})
	if r.Status != StatusFailed || r.Error == "" {
		t.Fatalf("run = %+v", r)
	}
}

func TestRunSkipsItemsWithOpenVerdict(t *testing.T) {
	j := &stubJudge{value: "accept"}
	s := newTestService(t, j)
	src := &fakeSource{kind: "lesson", items: []Item{item("lesson", "", "k1", "r1")}}
	s.Register(src)
	r1 := mustRun(t, s, StartReq{})
	if r1.Suggested != 1 {
		t.Fatalf("first run = %+v", r1)
	}
	r2 := mustRun(t, s, StartReq{})
	if r2.Total != 0 || j.calls != 1 {
		t.Fatalf("second run judged again: %+v calls=%d", r2, j.calls)
	}
	// A recent skip also keeps the item out.
	s2 := newTestService(t, &stubJudge{value: ValueSkip})
	s2.Register(&fakeSource{kind: "lesson", items: []Item{item("lesson", "", "k1", "r1")}})
	if r := mustRun(t, s2, StartReq{}); r.Skipped != 1 {
		t.Fatalf("skip run = %+v", r)
	}
	if r := mustRun(t, s2, StartReq{}); r.Total != 0 {
		t.Fatalf("skipped item re-collected: %+v", r)
	}
}

func TestRunSampleItemsNeverApplied(t *testing.T) {
	t.Setenv("SWARMERY_TRIAGE_SAMPLE", "2")
	s := newTestService(t, &stubJudge{value: "a"})
	src := &fakeSource{kind: "classifier", items: []Item{
		item("classifier", "", "k1", "r1"), item("classifier", "", "k2", "r2"), item("classifier", "", "k3", "r3"),
	}}
	s.Register(src)
	r := mustRun(t, s, StartReq{Cap: 10})
	if r.Applied != 1 || r.Suggested != 2 {
		t.Fatalf("run = %+v", r)
	}
	st := states(t, s, r.ID)
	if st["r1"] != StateSample || st["r2"] != StateSample || st["r3"] != StateApplied {
		t.Fatalf("states = %v", st)
	}
	if len(src.applied) != 1 || src.applied[0] != "r3" {
		t.Fatalf("applied = %v", src.applied)
	}
}

func TestDeniedKindsRejected(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "a"})
	var srcs []*fakeSource
	for _, k := range []string{"approval", "proposal", "alert"} {
		src := &fakeSource{kind: k, items: []Item{item(k, "", "k-"+k, "r-"+k)}}
		srcs = append(srcs, src)
		// Denied kinds are not in the run order, so drive them directly.
		run := &Run{ID: mustInsertRun(t, s)}
		s.triageItem(context.Background(), run, k, src, src.items[0], false)
		if run.Rejected != 1 || run.Applied != 0 {
			t.Fatalf("%s run = %+v", k, run)
		}
		if st := states(t, s, run.ID); st["r-"+k] != StateRejected {
			t.Fatalf("%s states = %v", k, st)
		}
	}
	for _, src := range srcs {
		if len(src.applied) != 0 {
			t.Fatalf("%s applied %v", src.kind, src.applied)
		}
	}
}

func mustInsertRun(t *testing.T, s *Service) int64 {
	t.Helper()
	id, err := s.insertRun(StartReq{Trigger: TriggerOperator})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunFollowSuggestionsPassPolicy(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}, follow: []Suggestion{
		{Kind: "lesson", Ref: "l1", Value: "accept"},   // suggest → stored suggested
		{Kind: "approval", Ref: "a1", Value: "accept"}, // denied → rejected
		{Kind: "friction", Ref: "f1", Value: "noise"},  // auto → never auto-applied from a follow
	}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	st := states(t, s, r.ID)
	if st["l1"] != StateSuggested || st["a1"] != StateRejected || st["f1"] != StateRejected {
		t.Fatalf("states = %v", st)
	}
	if len(src.applied) != 1 {
		t.Fatalf("applied = %v", src.applied)
	}
}

func TestRunApplyErrorAndRejectedValue(t *testing.T) {
	s := newTestService(t, &stubJudge{values: map[string]string{"r1": "noise", "r2": "zzz"}})
	src := &fakeSource{kind: "friction", applyErr: errors.New("nope"),
		items: []Item{item("friction", "", "k1", "r1", "r2")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if r.Failed != 1 || r.Rejected != 1 {
		t.Fatalf("run = %+v", r)
	}
}

func TestUndoExactlyOnceAndSweepOpen(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	vs, _ := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	v, err := s.Undo(context.Background(), vs[0].ID)
	if err != nil || v.State != StateUndone || v.DecidedAt == nil {
		t.Fatalf("undo = %+v, %v", v, err)
	}
	if _, err := s.Undo(context.Background(), vs[0].ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("second undo = %v", err)
	}
	if _, err := s.Undo(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing undo = %v", err)
	}
	if len(src.undone) != 1 {
		t.Fatalf("Source.Undo calls = %d", len(src.undone))
	}

	// SweepOpen: suggested → stale, sample → audited once the ref stops waiting.
	ls := &fakeSource{kind: "lesson", open: map[string]bool{"keep": true}}
	s.Register(ls)
	run := &Run{ID: mustInsertRun(t, s)}
	for ref, state := range map[string]string{"gone": StateSuggested, "keep": StateSuggested} {
		s.record(run, Verdict{Kind: "lesson", Ref: ref, Value: "accept", State: state})
	}
	s.record(run, Verdict{Kind: "lesson", Ref: "smp", Value: "accept", State: StateSample})
	s.record(run, Verdict{Kind: "ghost", Ref: "g", Value: "x", State: StateSuggested}) // no source
	if err := s.SweepOpen(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := states(t, s, run.ID)
	if st["gone"] != StateStale || st["keep"] != StateSuggested || st["smp"] != StateAudited || st["g"] != StateSuggested {
		t.Fatalf("swept = %v", st)
	}
}
