package actuals

import (
	"testing"
	"time"
)

type hookCall struct {
	phase         int64
	uuid, source  string
	rowSourceSeen any
}

// OnRecorded fires after every stored pass — run-end and settled — with the
// source that wrote the row, AFTER the row exists (the scorer reads it), and a
// panicking hook never fails the recording.
func TestOnRecordedFiresAfterEachStoredPass(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)

	var calls []hookCall
	var scheduled func()
	r := newRecorder(db)
	r.SettleDelay = time.Minute
	r.After = func(_ time.Duration, f func()) { scheduled = f }
	r.OnRecorded = func(p int64, uuid, source string) {
		calls = append(calls, hookCall{p, uuid, source, readRow(t, db, uuid)["source"]})
	}

	r.AfterRun(phaseID, runUUID, repo.dir)
	scheduled()
	if len(calls) != 2 {
		t.Fatalf("hook called %d times, want once per pass", len(calls))
	}
	for i, want := range []string{SourceRunEnd, SourceRunEndSettled} {
		c := calls[i]
		if c.phase != phaseID || c.uuid != runUUID || c.source != want || c.rowSourceSeen != want {
			t.Errorf("call %d = %+v, want phase %d uuid %s source %s with its row already stored", i, c, phaseID, runUUID, want)
		}
	}

	r.OnRecorded = func(int64, string, string) { panic("downstream bug") }
	if _, err := r.Record(phaseID, runUUID, repo.dir, SourceRunEnd, true); err != nil {
		t.Errorf("a panicking hook failed the recording: %v", err)
	}
}

// The backfill fires the hook for every row it stores, and never on a dry run.
func TestOnRecordedFiresFromBackfill(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	seedPhase(t, db, repo.dir, repo.start)
	root := func(int64) (string, error) { return repo.dir, nil }

	var sources []string
	r := newRecorder(db)
	r.OnRecorded = func(_ int64, _ string, source string) { sources = append(sources, source) }

	if _, err := r.Backfill(BackfillOptions{RepoRoot: root, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("a dry run fired the hook %d times", len(sources))
	}
	if _, err := r.Backfill(BackfillOptions{RepoRoot: root}); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0] != SourceBackfill {
		t.Errorf("backfill hook sources = %v, want [%s]", sources, SourceBackfill)
	}
}
