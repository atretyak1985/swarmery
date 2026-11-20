package landpoll

// Run branches the dashboard did not land.
//
// The land flow knows only the change request it opened (or discovered by the
// run branch's name). Work that reached the base by another road — a PR opened
// from a hand-made branch, a local merge — leaves the phase `ready` forever, and
// once the operator deletes the run branch the Push button fails on a branch that
// is not there. Two passes close that gap:
//
//   - settle: a finished phase with no change request recorded whose run branch
//     is gone (neither refs/heads nor refs/remotes/origin) is stored as `merged`,
//     with landed_at NULL — the merge time is unknown, and a guessed one would
//     skew the land-latency numbers. This is the rule phaserun.resolveBase already
//     stacks dependencies on: a deleted run branch is the operator saying its work
//     is accounted for. Its [LAND] criteria are ticked as a merge's are, but the
//     plan review is NOT started: there is no branch left to review, and a backlog
//     of old phases settling in one tick must not start a review per plan.
//   - cleanup: when a change request is seen merged (store's pr_open → merged
//     flip), the run branch is deleted locally. The merge is the proof the
//     branch's work is on the base; the remote branch is left to the host
//     (`gh pr merge -d`, the repo's auto-delete setting).
//
// Both go through the RunBranches seam and are off while it is nil.

import (
	"context"
	"fmt"
	"time"
)

// DefaultSettleMax caps the phases one RunOnce probes for a gone run branch when
// Poller.SettleMax is unset. Each probe is one or two local `git rev-parse`, no
// network, so the cap only bounds a tick over a large backlog.
const DefaultSettleMax = 200

// RunBranches is the local git seam for a phase's run branch (production:
// *phaserun.Service).
type RunBranches interface {
	// RunBranchGone reports whether the phase's stamped run branch is absent from
	// its run repo, both as a local branch and as origin/<branch>.
	RunBranchGone(phaseID int64) (bool, error)
	// DeleteRunBranch deletes the phase's run branch; existed is false for one
	// that was already gone.
	DeleteRunBranch(phaseID int64) (branch string, existed bool, err error)
}

// settleWhere selects the phases settle may store as merged: a run branch, no
// change request recorded, nothing landed yet (none or pushed), a run that
// finished (done, or blocked by its verification), every executable criterion
// ticked (only [LAND] / [MANUAL] may be open), and a live plan.
const settleWhere = `
	 WHERE COALESCE(e.run_branch, '') <> ''
	   AND COALESCE(e.pr_url, '') = '' AND COALESCE(e.pr_number, 0) = 0
	   AND COALESCE(e.landing_state, 'none') IN ('none', 'pushed')
	   AND e.run_state IN ('done', 'blocked')
	   AND e.checkboxes_total > 0
	   AND e.checkboxes_done + COALESCE(e.criteria_land_open, 0) + COALESCE(e.criteria_manual_open, 0) >= e.checkboxes_total
	   AND t.archived_at IS NULL`

// settlePass probes up to SettleMax candidates, newest first, and settles every
// one whose run branch is gone. changed counts the settled phases.
func (p *Poller) settlePass(ctx context.Context) (changed int, err error) {
	if p.Branches == nil {
		return 0, nil
	}
	limit := p.SettleMax
	if limit <= 0 {
		limit = DefaultSettleMax
	}
	rows, err := p.selectRows(ctx, "settle candidates", selectColumns+settleWhere+`
		 ORDER BY e.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		ok, err := p.settle(r)
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

// SettleOne settles one phase now if it qualifies (the land and refresh clicks):
// settled is true when this call stored it as merged. A phase that does not
// qualify, or whose run branch is still there, is (false, nil).
func (p *Poller) SettleOne(ctx context.Context, phaseID int64) (settled bool, err error) {
	if p.Branches == nil {
		return false, nil
	}
	rows, err := p.selectRows(ctx, "settle candidate", selectColumns+settleWhere+` AND e.id = ?`, phaseID)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return p.settle(rows[0])
}

// settle stores r as merged when its run branch is gone. A probe git could not
// answer (the repo moved, a lock) is logged and skipped: it says nothing about
// the branch. The UPDATE is guarded on the same landing it was selected with, so
// a land or a discovery that got there first wins.
func (p *Poller) settle(r phaseRow) (bool, error) {
	gone, err := p.Branches.RunBranchGone(r.phaseID)
	if err != nil {
		p.logf("landpoll: phase %d (task %d): probe run branch %s: %v", r.phaseID, r.taskID, r.runBranch, err)
		return false, nil
	}
	if !gone {
		return false, nil
	}
	out, err := p.DB.Exec(`UPDATE epic_phases
		    SET landing_state = ?, landing_error = NULL, pr_checked_at = ?
		  WHERE id = ? AND COALESCE(landing_state, ?) IN (?, ?)
		    AND COALESCE(pr_url, '') = '' AND COALESCE(pr_number, 0) = 0`,
		StateMerged, p.now().UTC().Format(time.RFC3339), r.phaseID, StateNone, StateNone, StatePushed)
	if err != nil {
		return false, fmt.Errorf("landpoll: settle phase %d: %w", r.phaseID, err)
	}
	if n, _ := out.RowsAffected(); n == 0 {
		return false, nil
	}
	p.logf("landpoll: phase %d (task %d): run branch %s is gone — counted as landed", r.phaseID, r.taskID, r.runBranch)
	p.tickLandOnly(r)
	p.publish(r.taskID)
	return true, nil
}

// cleanupMerged deletes the run branch of a phase whose change request was just
// seen merged. Best effort: the merge is stored; a branch git refuses to delete
// (checked out somewhere, a lock) is logged and left for the operator.
func (p *Poller) cleanupMerged(r phaseRow) {
	if p.Branches == nil || r.runBranch == "" {
		return
	}
	branch, existed, err := p.Branches.DeleteRunBranch(r.phaseID)
	switch {
	case err != nil:
		p.logf("landpoll: phase %d (task %d) merged, but deleting run branch %s failed: %v", r.phaseID, r.taskID, r.runBranch, err)
	case existed:
		p.logf("landpoll: phase %d (task %d) merged: deleted run branch %s", r.phaseID, r.taskID, branch)
	}
}
