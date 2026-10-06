package triage

import (
	"errors"
	"testing"
	"time"
)

var muteT0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestMuteIsActiveWithReasonAndUntil(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if err := Mute(s.DB, "err: boom", "noise", 7, muteT0); err != nil {
		t.Fatal(err)
	}
	act, err := ActiveMutes(s.DB, muteT0)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := act["err: boom"]
	if !ok || m.Reason != "noise" || m.VerdictID != 7 ||
		m.MutedAt != fmtTS(muteT0) || m.MutedUntil != fmtTS(muteT0.AddDate(0, 0, MuteDays)) {
		t.Fatalf("active = %+v", act)
	}
}

func TestReMuteReplacesReasonAndExtends(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if err := Mute(s.DB, "k", "first", 3, muteT0); err != nil {
		t.Fatal(err)
	}
	later := muteT0.AddDate(0, 0, 10)
	if err := Mute(s.DB, "k", "second", 0, later); err != nil {
		t.Fatal(err)
	}
	act, err := ActiveMutes(s.DB, later)
	if err != nil {
		t.Fatal(err)
	}
	m := act["k"]
	if len(act) != 1 || m.Reason != "second" || m.VerdictID != 0 ||
		m.MutedUntil != fmtTS(later.AddDate(0, 0, MuteDays)) {
		t.Fatalf("active = %+v", act)
	}
}

func TestMuteExpiresAfterMuteDays(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if err := Mute(s.DB, "k", "noise", 0, muteT0); err != nil {
		t.Fatal(err)
	}
	if act, err := ActiveMutes(s.DB, muteT0.AddDate(0, 0, 29)); err != nil || len(act) != 1 {
		t.Fatalf("day 29: active = %+v err=%v, want the mute", act, err)
	}
	if act, err := ActiveMutes(s.DB, muteT0.AddDate(0, 0, 31)); err != nil || len(act) != 0 {
		t.Fatalf("day 31: active = %+v err=%v, want none", act, err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM friction_mutes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d (%v): an expired mute must not be deleted", n, err)
	}
}

func TestUnmuteReturnsRowThenNone(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if err := Mute(s.DB, "k", "noise", 9, muteT0); err != nil {
		t.Fatal(err)
	}
	m, ok, err := Unmute(s.DB, "k")
	if err != nil || !ok || m.Key != "k" || m.Reason != "noise" || m.VerdictID != 9 {
		t.Fatalf("first unmute = %+v ok=%v err=%v", m, ok, err)
	}
	if _, ok, err := Unmute(s.DB, "k"); err != nil || ok {
		t.Fatalf("second unmute ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestMuteRejectsEmptyKeyOrReason(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if err := Mute(s.DB, "", "noise", 0, muteT0); err == nil {
		t.Error("empty key accepted")
	}
	if err := Mute(s.DB, "k", "  ", 0, muteT0); err == nil {
		t.Error("empty reason accepted")
	}
}

// LatestApplied finds the newest applied verdict of kind/ref/value and ignores
// other states, values and refs.
func TestLatestApplied(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	if _, ok, err := s.LatestApplied("friction", "k", "noise"); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	run := mustInsertRun(t, s)
	var want int64
	for _, v := range []Verdict{
		{RunID: run, Kind: "friction", Ref: "k", Value: "noise", State: StateApplied},
		{RunID: run, Kind: "friction", Ref: "k", Value: "noise", State: StateApplied},
		{RunID: run, Kind: "friction", Ref: "k", Value: "noise", State: StateUndone},
		{RunID: run, Kind: "friction", Ref: "k", Value: "fixable", State: StateApplied},
		{RunID: run, Kind: "friction", Ref: "other", Value: "noise", State: StateApplied},
	} {
		id, err := s.insertVerdict(v)
		if err != nil {
			t.Fatal(err)
		}
		if v.Ref == "k" && v.Value == "noise" && v.State == StateApplied {
			want = id
		}
	}
	v, ok, err := s.LatestApplied("friction", "k", "noise")
	if err != nil || !ok || v.ID != want {
		t.Fatalf("LatestApplied = %+v ok=%v err=%v, want id %d", v, ok, err, want)
	}
}

func TestMarkUndone(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	seedVerdict(t, s, "friction", "k", StateApplied, muteT0)
	seedVerdict(t, s, "friction", "k2", StateSuggested, muteT0)
	vs, err := s.ListVerdicts(VerdictFilter{})
	if err != nil || len(vs) != 2 {
		t.Fatalf("verdicts = %+v err=%v", vs, err)
	}
	ids := map[string]int64{}
	for _, v := range vs {
		ids[v.Ref] = v.ID
	}
	v, err := s.MarkUndone(ids["k"])
	if err != nil || v.State != StateUndone || v.DecidedAt == nil {
		t.Fatalf("MarkUndone = %+v err=%v", v, err)
	}
	if _, err := s.MarkUndone(ids["k"]); !errors.Is(err, ErrNotUndoable) {
		t.Errorf("second MarkUndone err = %v, want ErrNotUndoable", err)
	}
	if _, err := s.MarkUndone(ids["k2"]); !errors.Is(err, ErrNotUndoable) {
		t.Errorf("MarkUndone on a suggestion err = %v, want ErrNotUndoable", err)
	}
	if _, err := s.MarkUndone(99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkUndone on unknown id err = %v, want ErrNotFound", err)
	}
}
