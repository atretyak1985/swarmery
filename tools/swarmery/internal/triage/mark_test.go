package triage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// seedMarkVerdict inserts one verdict in state with a stored payload and returns its id.
func seedMarkVerdict(t *testing.T, s *Service, runID int64, ref, state string) int64 {
	t.Helper()
	id, err := s.insertVerdict(Verdict{RunID: runID, Kind: "lesson", Ref: ref, Value: "accept",
		Reason: "because", Payload: json.RawMessage(`{"old":true}`), State: state})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestListSuggestedAfterAndCount(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	run := mustInsertRun(t, s)
	p1, p2 := int64(1), int64(2)
	var ids []int64
	for i, p := range []*int64{&p1, &p2, nil, &p1, &p1} {
		id, err := s.insertVerdict(Verdict{RunID: run, Kind: "lesson", Ref: string(rune('a' + i)), Value: "accept",
			State: StateSuggested, ProjectID: p})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	seedMarkVerdict(t, s, run, "z", StateAccepted) // never listed

	page, err := s.ListSuggestedAfter(0, 2, 0)
	if err != nil || len(page) != 2 || page[0].ID != ids[0] || page[1].ID != ids[1] {
		t.Fatalf("first page = %+v (%v), want ids %v ascending", page, err, ids[:2])
	}
	page, err = s.ListSuggestedAfter(page[1].ID, 10, 0)
	if err != nil || len(page) != 3 || page[0].ID != ids[2] {
		t.Fatalf("second page = %d rows (%v), want 3 from %d", len(page), err, ids[2])
	}
	// Project 1: its own verdicts plus the project-less one.
	page, err = s.ListSuggestedAfter(0, 0, 1)
	if err != nil || len(page) != 4 || page[1].ID != ids[2] {
		t.Fatalf("project page = %d rows (%v), want 4", len(page), err)
	}
	if n, err := s.CountSuggested(0); err != nil || n != 5 {
		t.Fatalf("count all = %d (%v), want 5", n, err)
	}
	if n, err := s.CountSuggested(2); err != nil || n != 2 {
		t.Fatalf("count project 2 = %d (%v), want 2", n, err)
	}
}

func TestMarkAccepted(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	run := mustInsertRun(t, s)
	id := seedMarkVerdict(t, s, run, "1", StateSuggested)

	v, err := s.MarkAccepted(id, json.RawMessage(`{"cardId":"C-12"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateAccepted || v.DecidedAt == nil || *v.DecidedAt != "2026-10-06T12:00:00.000Z" {
		t.Fatalf("after accept: state %q decided %v", v.State, v.DecidedAt)
	}
	if string(v.Payload) != `{"cardId":"C-12"}` {
		t.Fatalf("payload = %s, want the new payload", v.Payload)
	}
	if _, err := s.MarkAccepted(id, nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("second accept err = %v, want ErrNotOpen", err)
	}
	if _, err := s.MarkAccepted(9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}

	// A nil payload keeps the stored one.
	keep := seedMarkVerdict(t, s, run, "2", StateSuggested)
	if v, err := s.MarkAccepted(keep, nil); err != nil || string(v.Payload) != `{"old":true}` {
		t.Fatalf("accept with nil payload = %s, %v; want the stored payload", v.Payload, err)
	}
	// An invalid payload is refused and the verdict stays open.
	bad := seedMarkVerdict(t, s, run, "3", StateSuggested)
	if _, err := s.MarkAccepted(bad, json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid payload accepted")
	}
	if v, _ := s.GetVerdict(bad); v.State != StateSuggested {
		t.Fatalf("verdict after refused payload = %q, want suggested", v.State)
	}

	for _, st := range []string{StateSample, StateApplied, StateStale, StateUndone} {
		other := seedMarkVerdict(t, s, run, "x-"+st, st)
		if _, err := s.MarkAccepted(other, nil); !errors.Is(err, ErrNotOpen) {
			t.Errorf("accept of a %s verdict err = %v, want ErrNotOpen", st, err)
		}
		if v, _ := s.GetVerdict(other); v.State != st {
			t.Errorf("%s verdict changed to %q", st, v.State)
		}
	}
}

func TestVerdictOpen(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	s.Register(&fakeSource{kind: "lesson", open: map[string]bool{"1": true}})
	ctx := context.Background()

	if open, err := s.VerdictOpen(ctx, Verdict{Kind: "lesson", Ref: "1"}); err != nil || !open {
		t.Fatalf("open ref = %v, %v; want true", open, err)
	}
	if open, err := s.VerdictOpen(ctx, Verdict{Kind: "lesson", Ref: "2"}); err != nil || open {
		t.Fatalf("closed ref = %v, %v; want false", open, err)
	}
	if open, err := s.VerdictOpen(ctx, Verdict{Kind: "nope", Ref: "1"}); !errors.Is(err, ErrNotFound) || open {
		t.Fatalf("unregistered kind = %v, %v; want false, ErrNotFound", open, err)
	}
}

func TestReopenSuggestion(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	run := mustInsertRun(t, s)
	id := seedMarkVerdict(t, s, run, "1", StateSuggested)
	if _, err := s.MarkAccepted(id, nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReopenSuggestion(id)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateSuggested || v.DecidedAt != nil {
		t.Fatalf("after reopen: state %q decided %v", v.State, v.DecidedAt)
	}
	if _, err := s.ReopenSuggestion(id); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("reopen of a suggestion err = %v, want ErrNotOpen", err)
	}
	if _, err := s.ReopenSuggestion(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
	for _, st := range []string{StateSample, StateApplied, StateStale} {
		other := seedMarkVerdict(t, s, run, "x-"+st, st)
		if _, err := s.ReopenSuggestion(other); !errors.Is(err, ErrNotOpen) {
			t.Errorf("reopen of a %s verdict err = %v, want ErrNotOpen", st, err)
		}
	}
}

func TestSetVerdictPayload(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	run := mustInsertRun(t, s)
	id := seedMarkVerdict(t, s, run, "1", StateAccepted)
	if err := s.SetVerdictPayload(id, json.RawMessage(`{"cardId":"T-1"}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetVerdict(id); string(v.Payload) != `{"cardId":"T-1"}` || v.State != StateAccepted {
		t.Fatalf("after set: payload %s state %q", v.Payload, v.State)
	}
	if err := s.SetVerdictPayload(id, json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid payload stored")
	}
	if err := s.SetVerdictPayload(9999, json.RawMessage(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
}

func TestMarkStale(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	run := mustInsertRun(t, s)
	id := seedMarkVerdict(t, s, run, "1", StateSuggested)

	v, err := s.MarkStale(id)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateStale || v.DecidedAt == nil || string(v.Payload) != `{"old":true}` {
		t.Fatalf("after stale: state %q decided %v payload %s", v.State, v.DecidedAt, v.Payload)
	}
	if _, err := s.MarkStale(id); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("second stale err = %v, want ErrNotOpen", err)
	}
	if _, err := s.MarkStale(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
	for _, st := range []string{StateSample, StateApplied, StateAccepted} {
		other := seedMarkVerdict(t, s, run, "x-"+st, st)
		if _, err := s.MarkStale(other); !errors.Is(err, ErrNotOpen) {
			t.Errorf("stale of a %s verdict err = %v, want ErrNotOpen", st, err)
		}
	}
}
