package phaserun

// The phase-run model ladder (Service.Start → resolveModel → RunSpec.Model):
//
//	request model  → VALIDATED through planning.ResolveModel; unknown ⇒ no run at all
//	SWARMERY_PHASERUN_MODEL → VERBATIM, never validated
//	neither        → "" ⇒ no --model flag, the account default
//
// The asymmetry between the first two rungs is the property these tests exist to
// pin: the env knob legitimately holds IDs outside planning.Models, and running it
// through the validator would drop every phase run back to the account default.

import (
	"errors"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

func TestStart_RequestModelIsResolvedToFullID(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// A request model must WIN over the env knob, not be merged with it.
	t.Setenv(modelEnv, "claude-sonnet-5")
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "opus", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Model; got != planning.DefaultModel {
		t.Errorf("RunSpec.Model = %q, want the resolved full ID %q", got, planning.DefaultModel)
	}
}

// An unknown model is an admission verdict resolved before anything is acquired
// or stamped — asserted on the seams (worktree + row), not only on the error,
// because a later rejection would also return ErrUnknownModel while leaving a
// worktree and a 'running' row behind.
func TestStart_UnknownRequestModelStartsNothing(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)

	_, err := s.Start(p1, "gpt-9", "")
	if !errors.Is(err, planning.ErrUnknownModel) {
		t.Fatalf("Start err = %v, want planning.ErrUnknownModel", err)
	}
	if len(r.specs) != 0 {
		t.Errorf("runner was invoked %d time(s), want 0", len(r.specs))
	}
	if len(wt.acquired) != 0 {
		t.Errorf("worktrees acquired = %v, want none", wt.acquired)
	}
	// 'idle' is the column default a never-run phase carries — untouched.
	state, uuid, started, _ := phaseRow(t, db, p1)
	if state != "idle" || uuid.Valid || started.Valid {
		t.Errorf("phase row stamped: state=%q uuid=%v started=%v, want an untouched idle row", state, uuid, started)
	}
}

// The regression this whole ladder exists for: the operator's live plist pins
// SWARMERY_PHASERUN_MODEL=claude-opus-5[1m], which planning.ResolveModel rejects
// (the [1m] context-window suffix is not in planning.Models). It must reach
// --model byte-for-byte, unvalidated and unmodified.
func TestStart_EnvModelPassedVerbatim(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const pinned = "claude-opus-5[1m]"
	t.Setenv(modelEnv, pinned)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Model; got != pinned {
		t.Errorf("RunSpec.Model = %q, want the env value verbatim %q", got, pinned)
	}
}

func TestStart_NoModelAnywhereUsesTheHouseDefault(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// This rung used to be "" — no --model flag at all — and that was the bug,
	// not the feature: omitting --model does not fall back to a house default,
	// it falls back to the ACCOUNT default, which is Fable at roughly twice the
	// Opus price. So the one rung an operator never picks, and the one every
	// un-picked "Run phase" lands on, was the most expensive of the four.
	if got := r.lastSpec().Model; got != planning.DefaultModel {
		t.Errorf("RunSpec.Model = %q, want %q — an empty model here means the account default (Fable), not a cheap one",
			got, planning.DefaultModel)
	}
}
