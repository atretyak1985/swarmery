package runcore

import (
	"database/sql"
	"strconv"
	"strings"
)

// Branch minting. These three functions own the `swarm/…` literals for the whole
// daemon: worktree.Manager derives BOTH the checkout path and the branch from the
// taskName handed to Acquire, so every other place that wants to name a run's
// branch has to reproduce that derivation exactly.
//
// Before this, the literals were spelled out in phaserun.Start, planrun.Start,
// phasediag, and a migration backfill. Drift there does not fail loudly — it
// produces a delete that reports success and destroys nothing, or a reclaim that
// looks at a branch no run ever used.
const branchPrefix = "swarm/"

// BranchPrefix, PhaseBranchPrefix and PlanBranchPrefix are for CONSUMERS — code
// that lists or parses branch names (a `branch --list` glob, a TrimPrefix) rather
// than minting one. They are exported separately from the helpers above because the
// two directions fail differently: a minting drift produces a branch nobody can
// find, a parsing drift produces a branch nobody recognises as ours.
const (
	BranchPrefix      = branchPrefix
	PhaseBranchPrefix = branchPrefix + "phase-"
	PlanBranchPrefix  = branchPrefix + "plan-"
)

// TaskBranch is a board task's branch: the external id IS the taskName Acquire
// receives (dispatch/service.go admit).
func TaskBranch(externalID string) string { return branchPrefix + externalID }

// PhaseBranch is a phase run's branch. NOTE: this is the DETERMINISTIC name for
// the current row id. A phase that ran under a previous row id (epic_phases
// identity is doc_path, so a renamed doc mints a new id) has its real branch in
// epic_phases.run_branch — the stamped value, which is the only record of it.
// Never derive when a stamped one exists.
func PhaseBranch(phaseID int64) string {
	return branchPrefix + PhaseTaskName(phaseID)
}

// PlanBranch is a whole-plan run's branch.
func PlanBranch(workspaceTaskID int64) string {
	return branchPrefix + PlanTaskName(workspaceTaskID)
}

// PhaseTaskName / PlanTaskName are the taskName arguments handed to
// worktree.Acquire, which derives `<root>/<slug>/<taskName>` and
// `swarm/<taskName>` from them. Exported so the callers pass the same string to
// Acquire that these branch helpers name.
func PhaseTaskName(phaseID int64) string { return "phase-" + strconv.FormatInt(phaseID, 10) }
func PlanTaskName(taskID int64) string   { return "plan-" + strconv.FormatInt(taskID, 10) }

// PhaseTaskNameFromBranch inverts PhaseBranch: "swarm/phase-<digits>" → "phase-<digits>".
// ok=false for anything else, so a hand-made or foreign branch is never guessed at.
// It is how a STAMPED run_branch (which may name a previous row id — see PhaseBranch)
// is turned back into the taskName worktree.Manager needs to reach that branch.
func PhaseTaskNameFromBranch(branch string) (taskName string, ok bool) {
	digits, found := strings.CutPrefix(branch, PhaseBranchPrefix)
	if !found || digits == "" {
		return "", false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return strings.TrimPrefix(branch, branchPrefix), true
}

// WorktreeKey identifies the CHECKOUT a run will land in, which is not the same
// thing as the run. worktree.Manager derives both the path and the branch from
// (projectSlug, taskName), so two runs that agree on (project, branch) resolve to
// ONE directory — and Acquire warm-REUSES a path whose branch already matches
// rather than refusing it (worktree.go invariant 4), deliberately, so a crashed
// run can be resumed instead of destroyed.
//
// That is precisely why admission has to do the rejecting: sharing acquisition
// without sharing rejection is how two agents end up in one directory.
//
// Keyed on the BRANCH rather than on dispatch's original external-id, because the
// three engines number their runs differently (external id, phase-<id>,
// plan-<id>) and the branch is the one identity all three derive from. A board
// task and a phase run can now be compared at all, which the old key could not do.
type WorktreeKey struct {
	ProjectID int64
	Branch    string
}

// WorktreeCount counts every live worktree the daemon is responsible for: board
// runs, phase runs and plan runs.
//
// dispatch's MaxWorktrees used to be enforced against `tasks` rows alone, so a
// machine with four phase runs and a plan run in flight reported ZERO worktrees to
// the cap that exists to bound them. The cap was real; the total it measured was
// not.
//
// A running row is counted as holding a worktree because Start acquires before it
// stamps 'running'. The reverse edge is not exact: teardown stamps the terminal
// state BEFORE removing the worktree (worktree first, slot last — see each
// engine's runAndHandle defer), so a run in teardown is uncounted for the tens of
// milliseconds the git shell-out takes. Under-counting there is the safe
// direction: it can only admit one extra run at the moment another is already
// releasing its checkout.
func WorktreeCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT
		  (SELECT COUNT(*) FROM tasks
		    WHERE source='queue' AND worktree_path IS NOT NULL
		      AND board_column='in_progress')
		+ (SELECT COUNT(*) FROM epic_phases WHERE run_state='running')
		+ (SELECT COUNT(*) FROM plan_runs   WHERE run_state='running')`).Scan(&n)
	return n, err
}

// WorktreeKeys returns the checkout identity of every run currently holding one —
// the set a new admission must not collide with, across all three engines.
//
// Read from the DB rather than from the in-memory slot registry, so it also sees a
// run this daemon did not start (adopted after a restart).
//
// Rows that cannot name a checkout are skipped: a board task with no external id
// has no deterministic path, and a phase/plan whose project row is gone cannot
// collide with anything a live project would acquire.
func WorktreeKeys(db *sql.DB) (map[WorktreeKey]bool, error) {
	held := make(map[WorktreeKey]bool)

	// Board runs: worktree.Acquire was handed the external id as its taskName. The
	// prefixes are interpolated from the same constants the Go helpers use, so a
	// rename cannot leave these queries naming branches nothing mints.
	if err := scanKeys(db, held, `
		SELECT project_id, '`+BranchPrefix+`' || external_id FROM tasks
		 WHERE source='queue' AND board_column='in_progress'
		   AND worktree_path IS NOT NULL
		   AND external_id IS NOT NULL AND external_id <> ''`); err != nil {
		return nil, err
	}

	// Phase runs: run_branch is the branch STAMPED at spawn (migration 0043) and is
	// authoritative — after a doc rename the derived name points at a branch that
	// does not exist, while the one actually holding the checkout survives. The
	// derived name is the fallback for rows the backfill never reached.
	if err := scanKeys(db, held, `
		SELECT t.project_id,
		       COALESCE(NULLIF(p.run_branch,''), '`+PhaseBranchPrefix+`' || p.id)
		  FROM epic_phases p
		  JOIN tasks t ON t.id = p.workspace_task_id
		 WHERE p.run_state='running'`); err != nil {
		return nil, err
	}

	// Plan runs: the deterministic plan-<taskID> name, which is what Start reclaims
	// and Acquire derives.
	if err := scanKeys(db, held, `
		SELECT t.project_id, '`+PlanBranchPrefix+`' || r.workspace_task_id
		  FROM plan_runs r
		  JOIN tasks t ON t.id = r.workspace_task_id
		 WHERE r.run_state='running'`); err != nil {
		return nil, err
	}

	return held, nil
}

// scanKeys adds one query's (projectID, branch) pairs to held.
func scanKeys(db *sql.DB, held map[WorktreeKey]bool, query string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k WorktreeKey
		if err := rows.Scan(&k.ProjectID, &k.Branch); err != nil {
			return err
		}
		held[k] = true
	}
	return rows.Err()
}
