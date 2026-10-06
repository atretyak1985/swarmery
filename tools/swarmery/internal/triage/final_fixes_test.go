package triage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// hijackingPreparer tries to change what Collect returned: it rewrites the
// item's kind, mutates the original part's Allowed in place, swaps the parts
// and fills Evidence. Only Evidence (and Instruction) may survive.
type hijackingPreparer struct {
	fakeSource
	setKind string
	swapTo  []Part
}

func (h *hijackingPreparer) Prepare(_ context.Context, it *Item) error {
	if h.setKind != "" {
		it.Kind = h.setKind
	}
	if len(it.Parts) > 0 && len(it.Parts[0].Allowed) > 0 {
		it.Parts[0].Allowed[0] = "hacked" // in-place write into the engine's slice
	}
	if h.swapTo != nil {
		it.Parts = h.swapTo
	}
	it.Evidence = "e"
	return nil
}

// R1: Prepare cannot change kind, class or parts — only Evidence/Instruction.
func TestPrepareMayFillOnlyInstructionAndEvidence(t *testing.T) {
	t.Run("kind and parts rewrite ignored", func(t *testing.T) {
		j := &evidenceJudge{stubJudge: stubJudge{values: map[string]string{"orig": "fix-card", "other": "x"}}}
		s := newTestService(t, j)
		src := &hijackingPreparer{
			fakeSource: fakeSource{kind: "advisor", items: []Item{{Kind: "advisor", Class: "card", Key: "k1",
				Parts: []Part{{Ref: "orig", Allowed: []string{"fix-card", "dismiss"}}}}}},
			setKind: "classifier",
			swapTo:  []Part{{Ref: "other", Allowed: []string{"x"}}},
		}
		s.Register(src)
		r := mustRun(t, s, StartReq{})
		if len(src.applied) != 0 {
			t.Fatalf("applied = %v, want none", src.applied)
		}
		vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 1 || vs[0].Ref != "orig" || vs[0].Kind != "advisor" || vs[0].State != StateSuggested || vs[0].Value != "fix-card" {
			t.Fatalf("verdicts = %+v", vs)
		}
		if got := strings.Join(j.seen, ","); got != "k1=e" {
			t.Fatalf("judge saw %s", got)
		}
	})
	t.Run("swap to an undone ref never applies it", func(t *testing.T) {
		s := newTestService(t, &stubJudge{value: "noise"})
		seedVerdict(t, s, "friction", "gone", StateUndone, s.clock())
		src := &hijackingPreparer{
			fakeSource: fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}},
			swapTo:     []Part{{Ref: "gone", Allowed: []string{"noise"}}},
		}
		s.Register(src)
		mustRun(t, s, StartReq{})
		for _, ref := range src.applied {
			if ref == "gone" {
				t.Fatalf("undone ref re-applied: %v", src.applied)
			}
		}
	})
}

// R2: a row left 'undoing' by a crash is healed back to 'applied' and can be undone.
func TestHealStaleRestoresUndoingToApplied(t *testing.T) {
	s := newTestService(t, &stubJudge{value: "noise"})
	src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	vs, _ := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	id := vs[0].ID
	if _, err := s.DB.Exec(`UPDATE triage_verdicts SET state=? WHERE id=?`, StateUndoing, id); err != nil {
		t.Fatal(err)
	}
	// T3: a row stuck in 'undoing' blocks re-judging until it is healed.
	if blocked, err := s.partBlocked("friction", "r1"); err != nil || !blocked {
		t.Fatalf("undoing does not block the part: %v %v", blocked, err)
	}
	if _, err := s.Undo(context.Background(), id); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo of undoing row = %v", err)
	}
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetVerdict(id); v.State != StateApplied {
		t.Fatalf("healed state = %s", v.State)
	}
	if v, err := s.Undo(context.Background(), id); err != nil || v.State != StateUndone || v.DecidedAt == nil {
		t.Fatalf("undo after heal = %+v, %v", v, err)
	}
	if len(src.undone) != 1 {
		t.Fatalf("Source.Undo calls = %d", len(src.undone))
	}
}

// failUndoneWrites makes the next n writes of state 'undone' to triage_verdicts
// fail (RAISE(FAIL) keeps the counter decrement made before the error).
func failUndoneWrites(t *testing.T, s *Service, n int) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE test_fail_undone(n INTEGER)`,
		fmt.Sprintf(`INSERT INTO test_fail_undone VALUES (%d)`, n),
		`CREATE TRIGGER test_fail_undone_trg BEFORE UPDATE OF state ON triage_verdicts
		 WHEN NEW.state='undone' AND (SELECT n FROM test_fail_undone) > 0
		 BEGIN UPDATE test_fail_undone SET n = n - 1; SELECT RAISE(FAIL, 'mark boom'); END`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// T3: marking a reverted verdict undone is retried once; if it still fails the
// row stays 'undoing', which blocks the ref from being judged and applied again.
func TestUndoMarkUndoneRetriesAndBlocks(t *testing.T) {
	setup := func(t *testing.T, failures int) (*Service, *fakeSource, int64) {
		s := newTestService(t, &stubJudge{value: "noise"})
		src := &fakeSource{kind: "friction", items: []Item{item("friction", "", "k1", "r1")}}
		s.Register(src)
		r := mustRun(t, s, StartReq{})
		vs, _ := s.ListVerdicts(VerdictFilter{RunID: r.ID})
		if len(vs) != 1 || vs[0].State != StateApplied {
			t.Fatalf("verdicts = %+v", vs)
		}
		failUndoneWrites(t, s, failures)
		return s, src, vs[0].ID
	}
	t.Run("one failure is retried", func(t *testing.T) {
		s, src, id := setup(t, 1)
		v, err := s.Undo(context.Background(), id)
		if err != nil || v.State != StateUndone || len(src.undone) != 1 {
			t.Fatalf("undo = %+v, %v; Source.Undo calls = %d", v, err, len(src.undone))
		}
	})
	t.Run("two failures leave the ref blocked", func(t *testing.T) {
		s, src, id := setup(t, 2)
		if _, err := s.Undo(context.Background(), id); err == nil || !strings.Contains(err.Error(), "could not mark undone") {
			t.Fatalf("undo err = %v", err)
		}
		if v, _ := s.GetVerdict(id); v.State != StateUndoing {
			t.Fatalf("state = %s, want undoing", v.State)
		}
		mustRun(t, s, StartReq{})
		if got := strings.Join(src.applied, ","); got != "r1" {
			t.Fatalf("applied = %q: the reverted ref was applied again", got)
		}
	})
}

// refUndoSource records the ref of every Source.Undo call.
type refUndoSource struct {
	fakeSource
	undoneRefs []string
}

func (r *refUndoSource) Undo(ctx context.Context, v Verdict) error {
	r.undoneRefs = append(r.undoneRefs, v.Kind+"/"+v.Ref+"="+v.Value)
	return r.fakeSource.Undo(ctx, v)
}

// R3: an applied write whose verdict cannot be recorded is reverted and fails the run.
func TestAppliedVerdictInsertFailure(t *testing.T) {
	setup := func(t *testing.T, failures int) (*Service, *stubJudge, *refUndoSource) {
		j := &stubJudge{value: "noise"}
		s := newTestService(t, j)
		src := &refUndoSource{fakeSource: fakeSource{kind: "friction", items: []Item{
			item("friction", "", "k1", "r1"), item("friction", "", "k2", "r2")}}}
		s.Register(src)
		left := failures
		s.insertVerdictFn = func(v Verdict) (int64, error) {
			if v.State == StateApplied && left > 0 {
				left--
				return 0, errors.New("insert boom")
			}
			return s.insertVerdict(v)
		}
		return s, j, src
	}
	t.Run("fails twice", func(t *testing.T) {
		s, j, src := setup(t, 2)
		r := mustRun(t, s, StartReq{})
		if got := strings.Join(src.undoneRefs, ","); got != "friction/r1=noise" {
			t.Fatalf("Source.Undo calls = %q", got)
		}
		want := "applied friction/r1 but could not record the verdict: insert boom; reverted"
		if r.Status != StatusFailed || r.Error != want {
			t.Fatalf("run = %s %q", r.Status, r.Error)
		}
		if j.calls != 1 || r.Failed != 1 || r.Applied != 0 {
			t.Fatalf("judge calls = %d run = %+v", j.calls, r)
		}
	})
	t.Run("fails twice and the revert fails", func(t *testing.T) {
		s, _, src := setup(t, 2)
		src.undoErr = errors.New("undo boom")
		r := mustRun(t, s, StartReq{})
		want := "applied friction/r1 but could not record the verdict: insert boom; REVERT FAILED: undo boom"
		if r.Status != StatusFailed || r.Error != want {
			t.Fatalf("run = %s %q", r.Status, r.Error)
		}
		if !strings.Contains(r.Error, "REVERT FAILED") || r.Failed != 1 || r.Applied != 0 {
			t.Fatalf("run = %+v", r)
		}
	})
	t.Run("fails once", func(t *testing.T) {
		s, _, src := setup(t, 1)
		r := mustRun(t, s, StartReq{})
		if r.Status != StatusOK || r.Applied != 2 || len(src.undoneRefs) != 0 {
			t.Fatalf("run = %+v undo = %v", r, src.undoneRefs)
		}
		if st := states(t, s, r.ID); st["r1"] != StateApplied || st["r2"] != StateApplied {
			t.Fatalf("states = %v", st)
		}
	})
}

// R4: an item with no parts is dropped before Prepare and the judge.
func TestRunDropsItemWithoutParts(t *testing.T) {
	j := &stubJudge{value: "noise"}
	s := newTestService(t, j)
	src := &preparingSource{fakeSource: fakeSource{kind: "friction", items: []Item{{Kind: "friction", Key: "empty"}}}}
	s.Register(src)
	r := mustRun(t, s, StartReq{})
	if j.calls != 0 || len(src.prepared) != 0 || r.Skipped != 1 {
		t.Fatalf("judge calls = %d prepared = %v run = %+v", j.calls, src.prepared, r)
	}
}

// R5: a sweep transition does not overwrite a state another writer set after its read.
func TestSetVerdictStateGuardsCurrentState(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	id, err := s.insertVerdict(Verdict{RunID: mustInsertRun(t, s), Kind: "lesson", Ref: "r", Value: "accept", State: StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE triage_verdicts SET state=? WHERE id=?`, StateAccepted, id); err != nil {
		t.Fatal(err)
	}
	if err := s.setVerdictState(id, StateSuggested, StateStale); err != nil {
		t.Fatalf("zero-row transition = %v", err)
	}
	if v, _ := s.GetVerdict(id); v.State != StateAccepted {
		t.Fatalf("state = %s, want accepted", v.State)
	}
}

// R7: a run never judges more than MaxCap items.
func TestStartClampsCapToMax(t *testing.T) {
	j := &stubJudge{err: errors.New("no")}
	s := newTestService(t, j)
	items := make([]Item, 1200)
	for i := range items {
		items[i] = item("friction", "", fmt.Sprintf("k%d", i), fmt.Sprintf("r%d", i))
	}
	s.Register(&fakeSource{kind: "friction", items: items})
	r := mustRun(t, s, StartReq{Cap: 5000})
	if j.calls != MaxCap || r.Total != MaxCap {
		t.Fatalf("judge calls = %d total = %d", j.calls, r.Total)
	}
}
