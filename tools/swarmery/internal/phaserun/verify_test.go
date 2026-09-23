package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// stubVerifier records every phase-verify request AND the order it was called in
// relative to the worktree teardown. The ordering is the whole contract: the verifier
// grades a live checkout, so a call that lands after Remove grades a directory that no
// longer exists.
type stubVerifier struct {
	mu   sync.Mutex
	reqs []runcore.PhaseVerifyRequest
	err  error
	// onVerify runs inside VerifyPhase, which is how a test observes what was still
	// true at that moment (e.g. "the worktree had not been removed yet").
	onVerify func()
}

func (v *stubVerifier) VerifyPhase(_ context.Context, req runcore.PhaseVerifyRequest) error {
	v.mu.Lock()
	v.reqs = append(v.reqs, req)
	hook, err := v.onVerify, v.err
	v.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (v *stubVerifier) calls() []runcore.PhaseVerifyRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]runcore.PhaseVerifyRequest(nil), v.reqs...)
}

// setVerifyMode writes the doc-owned opt-in onto a phase row, the way a wsingest
// rescan of a doc carrying `**Verify:** strict` would.
func setVerifyMode(t *testing.T, db *sql.DB, phaseID int64, mode string) {
	t.Helper()
	mustExec(t, db, `UPDATE epic_phases SET verify_mode=? WHERE id=?`, mode, phaseID)
}

func phaseStartPoint(t *testing.T, db *sql.DB, phaseID int64) sql.NullString {
	t.Helper()
	var sp sql.NullString
	if err := db.QueryRow(
		`SELECT run_start_point FROM epic_phases WHERE id=?`, phaseID).Scan(&sp); err != nil {
		t.Fatalf("read run_start_point: %v", err)
	}
	return sp
}

// TestStartPersistsRunStartPoint: the SHA Acquire pinned the worktree to is RECORDED
// at spawn, not re-derived later. Without it the verifier has no honest diff base and
// falls back to the branch — which diffed against itself is empty by construction, so
// landed work grades as "nothing was done" (the defect 0051 fixed for board cards).
func TestStartPersistsRunStartPoint(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{startPoint: "deadbeef123"}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sp := phaseStartPoint(t, db, p1)
	if !sp.Valid || sp.String != "deadbeef123" {
		t.Errorf("run_start_point = %v, want deadbeef123", sp)
	}
	// It is the branch's BASE, never the branch: those two being equal is the bug.
	if sp.String == runcore.PhaseBranch(p1) {
		t.Error("run_start_point is the run branch — a diff against itself is always empty")
	}
}

// TestVerifyRunsBeforeWorktreeRemoval pins the defer ordering §5.3 calls out: verify,
// THEN remove the worktree, THEN release the slot. Each step depends on the previous
// one still holding.
func TestVerifyRunsBeforeWorktreeRemoval(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	setVerifyMode(t, db, p1, "strict")

	var order []string
	v := &stubVerifier{onVerify: func() { order = append(order, "verify") }}
	wt.onRemove = func() { order = append(order, "remove") }
	s.Verify = v

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Go is synchronous in the harness, so the run goroutine (and its defer) is done.
	want := []string{"verify", "remove"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("call order = %v, want %v — a verdict on a reclaimed worktree grades nothing", order, want)
	}
	// The slot is free again: verification blocks the run goroutine but must not
	// leak the single-flight hold.
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("slot still held after the run goroutine returned")
	}
}

// TestVerifyRequestCarriesTheRunsFacts: the request describes the run that just
// ended — its worktree, its branch, its recorded base, and the doc as it now stands
// (the executor's ticks are part of what is graded).
func TestVerifyRequestCarriesTheRunsFacts(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	wt := &stubWt{startPoint: "cafe1234"}
	s := newTestService(db, &stubRunner{}, wt)
	setVerifyMode(t, db, p1, "normal")
	v := &stubVerifier{}
	s.Verify = v

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("verify calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.PhaseID != p1 {
		t.Errorf("PhaseID = %d, want %d", got.PhaseID, p1)
	}
	if got.WorkspaceTaskID != taskID {
		t.Errorf("WorkspaceTaskID = %d, want %d — the epic the Plans page refetches", got.WorkspaceTaskID, taskID)
	}
	if got.Mode != "normal" {
		t.Errorf("Mode = %q, want normal", got.Mode)
	}
	if got.StartPoint != "cafe1234" {
		t.Errorf("StartPoint = %q, want cafe1234", got.StartPoint)
	}
	if got.WorktreePath != "/wt/p/"+runcore.PhaseTaskName(p1) {
		t.Errorf("WorktreePath = %q, want the run's own worktree", got.WorktreePath)
	}
	if got.Branch != runcore.PhaseBranch(p1) {
		t.Errorf("Branch = %q, want %q", got.Branch, runcore.PhaseBranch(p1))
	}
	if got.Title != "Phase 1 — Schema" {
		t.Errorf("Title = %q, want the phase name", got.Title)
	}
	// `- [x] a`, not `- [ ] a`: verifyRun reads info.DocPath AFTER the executor's
	// edits have been copied back (service.go's returnDoc ordering), so the
	// verifier grades the document as it stands NOW — which is exactly what the
	// service comment there promises. The stub run ticks the doc, so a successful
	// run's verify request carries ticked criteria. Asserting the unticked form
	// would be asserting that the verifier sees the pre-run document.
	if !strings.Contains(got.Prompt, "- [x] a") {
		t.Errorf("Prompt does not carry the phase doc's criteria:\n%s", got.Prompt)
	}
	if got.ProjectPath != "/repo/p" {
		t.Errorf("ProjectPath = %q, want the project root (never the worktree)", got.ProjectPath)
	}
}

// TestVerifySkippedWhenDocDidNotOptIn is the "plans keep today's behaviour" gate:
// verify_mode defaults to off, so an untouched plan is never graded.
func TestVerifySkippedWhenDocDidNotOptIn(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	v := &stubVerifier{}
	s.Verify = v

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(v.calls()); n != 0 {
		t.Errorf("verify called %d time(s) for a phase whose doc never asked", n)
	}
}

// TestVerifySkippedWhenRunDidNotEndCleanly: a cancelled or crashed executor may have
// left the tree mid-edit, so a verdict there measures the interruption, not the work.
func TestVerifySkippedWhenRunDidNotEndCleanly(t *testing.T) {
	cases := []struct {
		name   string
		runner *stubRunner
	}{
		{"start error", &stubRunner{startErr: errors.New("claude not found")}},
		{"nonzero exit", &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
		}}},
		{"timeout", &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
			return &Run{SessionUUID: spec.SessionUUID, TimedOut: true, ExitCode: -1}, nil
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, _, p1, _ := fixture(t)
			s := newTestService(db, c.runner, &stubWt{})
			setVerifyMode(t, db, p1, "strict")
			v := &stubVerifier{}
			s.Verify = v

			if _, err := s.Start(p1, "", ""); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if state, _, _, _ := phaseRow(t, db, p1); state != "failed" {
				t.Fatalf("run_state = %q, want failed (test premise)", state)
			}
			if n := len(v.calls()); n != 0 {
				t.Errorf("verify called %d time(s) on a run that did not end cleanly", n)
			}
		})
	}
}

// TestVerifyErrorDoesNotFailTheRun: a grading failure is not evidence about the work.
// The run keeps the state its own exit earned, and the worktree is still reclaimed.
func TestVerifyErrorDoesNotFailTheRun(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	setVerifyMode(t, db, p1, "normal")
	s.Verify = &stubVerifier{err: errors.New("verifier could not start")}

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state, _, _, runErr := phaseRow(t, db, p1); state != "done" || runErr.Valid {
		t.Errorf("run_state=%q run_error=%v — a verify failure must not rewrite the run's own outcome", state, runErr)
	}
	if wt.removedCount() != 1 {
		t.Errorf("worktree removals = %d, want 1 — teardown must survive a verify error", wt.removedCount())
	}
}

// TestVerifyNotWiredIsSilent: nil seam is a valid production state (and every unit
// test's state) — no panic, no skipped teardown.
func TestVerifyNotWiredIsSilent(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	setVerifyMode(t, db, p1, "strict")
	s.Verify = nil

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if wt.removedCount() != 1 {
		t.Errorf("worktree removals = %d, want 1", wt.removedCount())
	}
}
