package triage

import (
	"errors"
	"testing"
)

// TestStoreMigrationConstraints proves migration 0100 applied with its CHECKs.
func TestStoreMigrationConstraints(t *testing.T) {
	s := newTestService(t, nil)
	if _, err := s.DB.Exec(`INSERT INTO triage_runs(trigger, started_at) VALUES('cron','x')`); err == nil {
		t.Fatal("bad trigger accepted")
	}
	if _, err := s.DB.Exec(`INSERT INTO triage_runs(trigger, status, started_at) VALUES('operator','done','x')`); err == nil {
		t.Fatal("bad status accepted")
	}
	id := mustInsertRun(t, s)
	if _, err := s.DB.Exec(`INSERT INTO triage_verdicts(run_id, kind, ref, value, state, created_at)
		VALUES(?, 'k', 'r', 'v', 'bogus', 'x')`, id); err == nil {
		t.Fatal("bad state accepted")
	}
	for _, st := range []string{StateApplied, StateSuggested, StateSample, StateAccepted, StateAudited,
		StateStale, StateUndone, StateRejected, StateFailed, StateSkipped} {
		if _, err := s.insertVerdict(Verdict{RunID: id, Kind: "k", Ref: "r", Value: "v", State: st}); err != nil {
			t.Fatalf("state %s rejected: %v", st, err)
		}
	}
}

func TestStoreReadsAndDefaults(t *testing.T) {
	s := newTestService(t, nil)
	if _, err := s.GetRun(42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run = %v", err)
	}
	if _, err := s.GetVerdict(42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing verdict = %v", err)
	}
	id := mustInsertRun(t, s)
	r, err := s.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	// Fleet scope stores NULL; empty arrays decode as [] (never null) for the UI.
	if r.ScopeProjectID != nil || r.Kinds == nil || r.SessionUUIDs == nil || r.Status != StatusRunning {
		t.Fatalf("run = %+v", r)
	}
	vid, err := s.insertVerdict(Verdict{RunID: id, Kind: "k", Ref: "r", Value: "v", State: StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.GetVerdict(vid)
	if err != nil || string(v.Payload) != "{}" || string(v.Prior) != "{}" || v.DecidedAt != nil {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	if runs, err := s.ListRuns(10000); err != nil || len(runs) != 1 {
		t.Fatalf("list runs = %d, %v", len(runs), err)
	}
}
