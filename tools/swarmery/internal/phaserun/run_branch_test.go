package phaserun

import (
	"database/sql"
	"errors"

	"testing"
)

// epic_phases identity is doc_path (wsingest applyEpics), so a renamed or regenerated
// phase doc replaces the row and mints a new id. A branch derived from that id is
// therefore only valid until the next plan rescan — which the phase's own executor
// triggers by editing its doc. These tests pin the stamped branch (migration 0043) as
// the single source of truth.

func runBranchOf(t *testing.T, db *sql.DB, phaseID int64) sql.NullString {
	t.Helper()
	var b sql.NullString
	if err := db.QueryRow(`SELECT run_branch FROM epic_phases WHERE id=?`, phaseID).Scan(&b); err != nil {
		t.Fatalf("read run_branch: %v", err)
	}
	return b
}

// Start must record the branch in the SAME statement that opens the run: a crash between
// two separate writes would leave commits on a branch nothing names.
func TestStartStampsRunBranch(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	got := runBranchOf(t, db, p1)
	want := "swarm/phase-" + itoa64(p1)
	if !got.Valid || got.String != want {
		t.Errorf("run_branch = %v, want %q", got, want)
	}
}

// A run performed under a PREVIOUS row id left its commits on a branch the deterministic
// name no longer reaches. Start must reclaim that branch too — and refuse when it holds
// work, rather than stranding it for ever.
func TestStartRefusesWhenPreviousRunBranchHasCommits(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const orphan = "swarm/phase-1280" // the id this phase ran under, before a doc rename
	mustExec(t, db, `UPDATE epic_phases SET run_state='done', run_branch=? WHERE id=?`, orphan, p1)

	wt := &stubWt{reclaimAheadBy: map[string]int{orphan: 2}}
	s := newTestService(db, &stubRunner{}, wt)

	_, err := s.Start(p1, "")
	if !errors.Is(err, ErrBranchDirty) {
		t.Fatalf("err = %v, want ErrBranchDirty", err)
	}
	var dirty *BranchDirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("err = %v, want a *BranchDirtyError carrying the fields", err)
	}
	if dirty.Branch != orphan {
		t.Errorf("Branch = %q, want the orphaned %q — naming the derived branch sends the operator to a branch that does not exist",
			dirty.Branch, orphan)
	}
	if dirty.CommitsAhead != 2 {
		t.Errorf("CommitsAhead = %d, want 2", dirty.CommitsAhead)
	}
	if wt.acquiredCount() != 0 {
		t.Error("acquired a worktree despite refusing — the refusal must come first")
	}
}

// An orphaned branch that is EMPTY is just a leftover name: reclaimed silently, the run
// proceeds, and the new branch is stamped over the old one.
func TestStartReclaimsEmptyPreviousRunBranch(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const orphan = "swarm/phase-1280"
	mustExec(t, db, `UPDATE epic_phases SET run_state='done', run_branch=? WHERE id=?`, orphan, p1)

	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	reclaimed := wt.reclaimedList()
	if len(reclaimed) != 2 {
		t.Fatalf("reclaimed = %v, want both the deterministic name and the orphan", reclaimed)
	}
	if reclaimed[1] != orphan {
		t.Errorf("second reclaim = %q, want %q", reclaimed[1], orphan)
	}
	if got, want := runBranchOf(t, db, p1), "swarm/phase-"+itoa64(p1); got.String != want {
		t.Errorf("run_branch = %v, want it re-stamped to %q", got, want)
	}
}

// DeleteRunBranch follows the stamp, never the row id.
func TestDeleteRunBranchUsesStampedBranch(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const orphan = "swarm/phase-1280"
	mustExec(t, db, `UPDATE epic_phases SET run_state='done', run_branch=? WHERE id=?`, orphan, p1)

	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	branch, existed, err := s.DeleteRunBranch(p1)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if branch != orphan {
		t.Errorf("branch = %q, want %q", branch, orphan)
	}
	if !existed {
		t.Error("existed = false, want true — the stub reports the branch was there")
	}
	if len(wt.deleted) != 1 || wt.deleted[0] != orphan {
		t.Errorf("deleted = %v, want [%s]", wt.deleted, orphan)
	}
}

// No stamp, no guess: a phase with no recorded branch gets a refusal, not a derived name
// whose deletion would report success while destroying nothing.
func TestDeleteRunBranchWithoutStampRefuses(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	if _, _, err := s.DeleteRunBranch(p1); !errors.Is(err, ErrNoRunBranch) {
		t.Fatalf("err = %v, want ErrNoRunBranch", err)
	}
	if len(wt.deleted) != 0 {
		t.Errorf("deleted = %v, want no delete attempted", wt.deleted)
	}
}
