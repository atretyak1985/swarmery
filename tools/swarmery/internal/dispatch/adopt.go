package dispatch

// Adoption — a dispatched run that outlived the daemon. The loop lives in
// internal/runcore (why adoption exists at all is documented there); what stays
// here is how dispatch differs from the other two engines, and it differs
// deliberately.
//
// Dispatch already owns an evidence-based reclaim path: procwatch marks the run's
// session proc_state='dead', and HealDeadProcess then requeues the task with retry
// accounting, progress high-water and worktree handling all in one place.
// Duplicating that here would mean two reclaim policies to keep in step — so
// adoption does the one thing that path cannot do for itself: hold the concurrency
// slot while the orphan runs, so the scheduler does not dispatch over the cap, and
// poke it when the process is finally gone.
//
// Hence no Kill hook (a board run has no cancel path) and no terminal write. The
// Ended hook does two things, in this order:
//
//   - returnOrphanOutput: bring the run's Completion Report home. runPlaybook
//     returns the lent plan doc in a defer, and a daemon restart is the one exit
//     no defer survives — so an adopted card's report stayed in the worktree and
//     the dashboard read "no summary of the work written" over work that shipped.
//     Worse, the re-admission that follows lends the stale workspace doc back IN,
//     overwriting the report for good. phaserun closed the same hole in
//     settleAdopted; this is the dispatch half.
//   - Poke, which lets HealDeadProcess act on the evidence procwatch has by then
//     written. The return comes FIRST: the Poke is what hands the row on to the
//     code that reads (and re-lends) the workspace copy.

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// tracked adapts this service to runcore.Tracked.
type tracked struct{ s *Service }

func (tracked) Engine() string { return Engine }

func (t tracked) ScanRunning() ([]runcore.Candidate, error) {
	return runcore.ScanCandidates(t.s.DB, `
		SELECT id, COALESCE(dispatch_session_uuid,'')
		  FROM tasks
		 WHERE source='queue' AND board_column='in_progress'`)
}

func (t tracked) Adopt(c runcore.Candidate, _ int) (runcore.AdoptHooks, bool) {
	return runcore.AdoptHooks{Ended: func(bool) {
		t.s.returnOrphanOutput(c.ID, fmt.Sprintf("dispatch adopted task=%d", c.ID))
		t.s.Poke()
	}}, true
}

// returnOrphanOutput is runPlaybook's deferred return trip for a run whose
// runPlaybook died with the previous daemon — an adopted survivor when it ends,
// or a dead-at-boot run HealStale is about to requeue.
//
// Nothing of that run survives in memory, so what LendPlanDoc returned is
// re-derived from the row: tasks.worktree_path is the checkout (admit writes it),
// tasks.workspace_dir is the micro-plan dir mintMicroPlan lent the doc from, and
// worktree.LentPlanDocRel is where the lend put it. A lent copy that is not in
// the worktree means the lend never happened (docless card, or a lend failure),
// which is exactly when the normal path collects worktree.ReportPath instead —
// so the same returnRunOutput decides between the two.
//
// Best-effort like the defer it stands in for: the work is already in the
// branch, so nothing here can fail the heal.
func (s *Service) returnOrphanOutput(id int64, what string) {
	var wtPath, wsDir sql.NullString
	err := s.DB.QueryRow(`SELECT worktree_path, workspace_dir FROM tasks WHERE id=?`, id).Scan(&wtPath, &wsDir)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("warning: %s: cannot load the run's worktree, so its report is not returned: %v", what, err)
		}
		return
	}
	if wtPath.String == "" {
		return // no worktree, nothing was lent and nothing was written
	}
	var taskDoc, docRel string
	if wsDir.String != "" {
		taskDoc = taskdir.PhaseDocPath(wsDir.String)
		rel := worktree.LentPlanDocRel(taskDoc)
		if _, err := os.Stat(filepath.Join(wtPath.String, rel)); err == nil {
			docRel = rel
		}
	}
	s.returnRunOutput(id, what, wtPath.String, docRel, taskDoc)
}

// adoptSurvivors probes every in_progress queue task and adopts the ones whose
// executor is still alive, returning their ids so HealStale leaves them alone.
func (s *Service) adoptSurvivors() ([]int64, error) {
	return runcore.Adopter{
		Slots:     s.Slots,
		Tracked:   tracked{s},
		FindRun:   s.FindRun,
		ProcAlive: s.ProcAlive,
		Poll:      s.adoptPoll,
		Go:        s.Go,
	}.AdoptSurvivors()
}

// returnHealedOutput runs returnOrphanOutput for every task HealStale is about to
// requeue — the same predicate as its UPDATE, minus the survivors just adopted
// (theirs is returned when they end).
func (s *Service) returnHealedOutput(adopted []int64) {
	skip := make(map[int64]bool, len(adopted))
	for _, id := range adopted {
		skip[id] = true
	}
	rows, err := s.DB.Query(`
		SELECT id FROM tasks
		 WHERE source='queue' AND board_column='in_progress'
		   AND worktree_path IS NOT NULL`)
	if err != nil {
		log.Printf("warning: swarmery dispatch: orphaned reports not returned: %v", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && !skip[id] {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: swarmery dispatch: orphaned reports scan: %v", err)
	}
	_ = rows.Close()
	for _, id := range ids {
		s.returnOrphanOutput(id, fmt.Sprintf("dispatch healed task=%d", id))
	}
}
