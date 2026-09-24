package phaserun

import (
	"errors"
	"testing"
)

// TestActualsHookOrdering pins where the actuals hook sits on the exit path:
// AFTER the terminal stamp and the verdict (so the row it measures is this run's
// finished row, verdict included), BEFORE the worktree teardown and the slot
// release (so no retry can overwrite the row or clear its run_events while it is
// being measured).
func TestActualsHookOrdering(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	setVerifyMode(t, db, p1, "strict")

	var order []string
	s.Verify = &stubVerifier{onVerify: func() { order = append(order, "verify") }}
	wt.onRemove = func() { order = append(order, "remove") }

	var (
		gotID              int64
		gotUUID, gotRoot   string
		stateAtHook        string
		slotHeldDuringHook bool
		calls              int
	)
	s.Actuals = func(phaseID int64, sessionUUID, repoRoot string) {
		calls++
		order = append(order, "actuals")
		gotID, gotUUID, gotRoot = phaseID, sessionUUID, repoRoot
		if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, phaseID).Scan(&stateAtHook); err != nil {
			t.Errorf("read run_state inside the hook: %v", err)
		}
		slotHeldDuringHook = s.Slots.IsActive(s.slotKey(phaseID))
	}

	uuid, err := s.Start(p1, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if calls != 1 {
		t.Fatalf("actuals hook called %d times, want exactly 1", calls)
	}
	want := []string{"verify", "actuals", "remove"}
	if len(order) != len(want) {
		t.Fatalf("exit order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("exit order = %v, want %v", order, want)
		}
	}
	if gotID != p1 || gotUUID != uuid {
		t.Errorf("hook got (phase %d, uuid %q), want (%d, %q)", gotID, gotUUID, p1, uuid)
	}
	// The identity resolver in the harness maps the project path to itself.
	if gotRoot != "/repo/p" {
		t.Errorf("hook repoRoot = %q, want the run's resolved repository /repo/p", gotRoot)
	}
	if stateAtHook == "running" || stateAtHook == "" {
		t.Errorf("run_state at hook time = %q — the hook ran before the terminal stamp", stateAtHook)
	}
	if !slotHeldDuringHook {
		t.Error("slot was already released when the hook ran — a retry could overwrite the row mid-measurement")
	}
}

// A service with no recorder wired (every unit test, and a daemon that does not
// record actuals) runs exactly as before.
func TestActualsHookNilIsInert(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state == "running" {
		t.Errorf("run_state = running after a completed run")
	}
}

// RunRoot answers with the same resolution Start used, and refuses the same
// inputs Start refuses.
func TestRunRoot(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	root, err := s.RunRoot(p1)
	if err != nil || root != "/repo/p" {
		t.Fatalf("RunRoot = (%q, %v), want (/repo/p, nil)", root, err)
	}
	if _, err := s.RunRoot(99999); !errors.Is(err, ErrPhaseNotFound) {
		t.Errorf("RunRoot(unknown) err = %v, want ErrPhaseNotFound", err)
	}
	mustExec(t, db, `UPDATE projects SET path = '' WHERE id = 1`)
	if _, err := s.RunRoot(p1); !errors.Is(err, ErrNoPath) {
		t.Errorf("RunRoot(no project path) err = %v, want ErrNoPath", err)
	}
}
