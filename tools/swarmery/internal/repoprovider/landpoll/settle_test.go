package landpoll

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// fakeBranches is the RunBranches seam: gone lists the phases whose run branch
// is absent, probeErr the ones git cannot answer for, deleteErr a failing delete.
type fakeBranches struct {
	mu        sync.Mutex
	gone      map[int64]bool
	probeErr  map[int64]error
	deleteErr error
	probed    []int64
	deleted   []int64
}

func (f *fakeBranches) RunBranchGone(id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, id)
	if err := f.probeErr[id]; err != nil {
		return false, err
	}
	return f.gone[id], nil
}

func (f *fakeBranches) DeleteRunBranch(id int64) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return "", false, f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return fmt.Sprintf("swarm/phase-%d", id), true, nil
}

// finishedPhase is a phase whose run finished in runState with every executable
// criterion ticked, a stamped run branch and no change request recorded.
func (h *harness) finishedPhase(seq int, runState string) int64 {
	h.t.Helper()
	id := h.phase(seq, StateNone, "", 0, "")
	h.exec(`UPDATE epic_phases
		   SET run_branch = ?, run_state = ?, checkboxes_total = 3, checkboxes_done = 3, landed_at = NULL
		 WHERE id = ?`, fmt.Sprintf("swarm/phase-%d", seq), runState, id)
	return id
}

func TestRunOnceSettlesFinishedPhaseWithGoneBranch(t *testing.T) {
	h := newHarness(t)
	gone := h.finishedPhase(1, "blocked")
	kept := h.finishedPhase(2, "done")
	fb := &fakeBranches{gone: map[int64]bool{gone: true}}
	h.poller.Branches = fb
	review := &fakePlanReview{}
	h.poller.PlanReview = review

	if _, changed := h.runOnce(); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	l := h.landing(gone)
	if l.state != StateMerged || l.landedAt.Valid || l.landingError.Valid {
		t.Fatalf("gone branch: %+v, want merged with landed_at NULL", l)
	}
	if got := h.landing(kept).state; got != StateNone {
		t.Errorf("branch still there: landing_state = %q, want none", got)
	}
	if h.publishedCount() != 1 {
		t.Errorf("published = %v, want one", h.published)
	}
	if len(review.seen()) != 0 {
		t.Errorf("a settle started a plan review: %v", review.seen())
	}
	// A settled phase is not probed again.
	fb.probed = nil
	h.runOnce()
	for _, id := range fb.probed {
		if id == gone {
			t.Fatalf("settled phase probed again: %v", fb.probed)
		}
	}
}

func TestSettleSkipsUnfinishedAndAlreadyLanded(t *testing.T) {
	h := newHarness(t)
	failed := h.finishedPhase(1, "failed")
	open := h.finishedPhase(2, "done")
	h.exec(`UPDATE epic_phases SET checkboxes_done = 1 WHERE id = ?`, open)
	landOnly := h.finishedPhase(3, "done")
	h.exec(`UPDATE epic_phases SET checkboxes_done = 2, criteria_land_open = 1 WHERE id = ?`, landOnly)
	withPR := h.finishedPhase(4, "done")
	h.exec(`UPDATE epic_phases SET landing_state = ?, pr_number = 9, pr_url = 'u' WHERE id = ?`, StatePROpen, withPR)
	archived := h.finishedPhase(5, "done")
	fb := &fakeBranches{gone: map[int64]bool{failed: true, open: true, landOnly: true, withPR: true, archived: true}}
	h.poller.Branches = fb

	// Archive a second plan holding phase 5.
	res := h.exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id, archived_at)
		VALUES (7, 'Old', 'goal', 'done', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'workspace', '2026-09-01-old', '2026-09-30T00:00:00Z')`)
	oldTask, _ := res.LastInsertId()
	h.exec(`UPDATE epic_phases SET workspace_task_id = ? WHERE id = ?`, oldTask, archived)

	h.runOnce()
	for id, want := range map[int64]string{failed: StateNone, open: StateNone, landOnly: StateMerged, withPR: StatePROpen, archived: StateNone} {
		if got := h.landing(id).state; got != want {
			t.Errorf("phase %d: landing_state = %q, want %q", id, got, want)
		}
	}
}

func TestSettleProbeErrorIsLoggedAndSkipped(t *testing.T) {
	h := newHarness(t)
	id := h.finishedPhase(1, "done")
	h.poller.Branches = &fakeBranches{probeErr: map[int64]error{id: errors.New("not a git repository")}}
	if _, changed := h.runOnce(); changed != 0 {
		t.Fatalf("changed = %d", changed)
	}
	if got := h.landing(id).state; got != StateNone {
		t.Fatalf("landing_state = %q after a failed probe", got)
	}
	if len(h.logs) != 1 {
		t.Fatalf("logs = %q, want one probe warning", h.logs)
	}
}

func TestRefreshOneSettlesGoneBranch(t *testing.T) {
	h := newHarness(t)
	id := h.finishedPhase(1, "done")
	h.poller.Branches = &fakeBranches{gone: map[int64]bool{id: true}}
	_, state, err := h.poller.RefreshOne(context.Background(), id)
	if err != nil || state != StateMerged {
		t.Fatalf("RefreshOne = %q, %v; want merged, nil", state, err)
	}
	if calls := h.fake.Calls; len(calls) != 0 {
		t.Errorf("a settle asked the host: %v", calls)
	}
}

func TestSettleOneWithoutSeamIsNoop(t *testing.T) {
	h := newHarness(t)
	id := h.finishedPhase(1, "done")
	if ok, err := h.poller.SettleOne(context.Background(), id); ok || err != nil {
		t.Fatalf("SettleOne without Branches = %v, %v", ok, err)
	}
}

func TestMergedChangeRequestDeletesRunBranch(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 21, "")
	h.exec(`UPDATE epic_phases SET run_branch = 'swarm/phase-1' WHERE id = ?`, id)
	fb := &fakeBranches{}
	h.poller.Branches = fb

	h.reply(21, reply{out: prJSON("OPEN", "", "REVIEW_REQUIRED", runPending)})
	h.runOnce()
	if len(fb.deleted) != 0 {
		t.Fatalf("an open PR deleted its branch: %v", fb.deleted)
	}
	h.reply(21, reply{out: prJSON("MERGED", "2026-10-09T12:00:30Z", "APPROVED", runOK)})
	h.runOnce()
	if len(fb.deleted) != 1 || fb.deleted[0] != id {
		t.Fatalf("deleted = %v, want [%d]", fb.deleted, id)
	}
}

func TestMergedDeleteFailureIsLogged(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 22, "")
	h.exec(`UPDATE epic_phases SET run_branch = 'swarm/phase-1' WHERE id = ?`, id)
	h.poller.Branches = &fakeBranches{deleteErr: errors.New("branch is checked out")}
	h.reply(22, reply{out: prJSON("MERGED", "2026-10-09T12:00:30Z", "APPROVED", runOK)})
	h.runOnce()
	if got := h.landing(id).state; got != StateMerged {
		t.Fatalf("landing_state = %q, want merged despite the failed delete", got)
	}
	if len(h.logs) != 1 {
		t.Fatalf("logs = %q, want one delete warning", h.logs)
	}
}
