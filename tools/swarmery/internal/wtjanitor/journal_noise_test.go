package wtjanitor

import (
	"strings"
	"testing"
	"time"
)

// The janitor runs every 15 minutes forever. A row per project per tick for the
// main checkout would be 96 rows a day of "skip: main checkout" and would bury
// the decisions an operator actually opens the panel to read.
func TestSweep_MainCheckoutIsNotJournalled(t *testing.T) {
	db := testDB(t)
	main := Worktree{Path: "/repo", Branch: "main", IsMain: true,
		NewestMTime: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{main}}, rem, idleLive{})
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rows := journalRows(t, db); len(rows) != 0 {
		t.Errorf("journal = %+v, want no rows for a main-checkout skip", rows)
	}
}

// Every other skip is a real observation about a real worktree and IS recorded:
// "we saw it, someone was inside, we left it alone" is exactly what the panel
// needs to explain why nothing happened.
func TestSweep_InformativeSkipsAreJournalled(t *testing.T) {
	db := testDB(t)
	wt := sweepable()
	wt.Live = true
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{})
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	rows := journalRows(t, db)
	if len(rows) != 1 || rows[0]["verdict"] != string(VerdictSkip) {
		t.Errorf("journal = %+v, want one skip row for a live worktree", rows)
	}
}

// A locked worktree used to reach the Remover and come back as a "redundant"
// row carrying git's refusal, every tick. Now it is a plain skip naming the lock,
// and the Remover never sees it.
func TestSweep_LockedWorktreeIsSkippedNotRemoved(t *testing.T) {
	db := testDB(t)
	wt := sweepable()
	wt.Locked = true
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{})
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rem.removed) != 0 {
		t.Errorf("Remover got %v, want nothing for a locked worktree", rem.removed)
	}
	rows := journalRows(t, db)
	if len(rows) != 1 || rows[0]["verdict"] != string(VerdictSkip) || !strings.Contains(rows[0]["reason"].(string), "locked") || rows[0]["error"] != "" {
		t.Errorf("journal = %+v, want one error-free skip row naming the lock", rows)
	}
}
