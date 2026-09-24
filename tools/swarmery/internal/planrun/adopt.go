package planrun

// Adoption — a plan run that outlived the daemon. The loop lives in
// internal/runcore (why adoption exists at all, and what it can and cannot
// recover, is documented there); what stays here is this engine's policy:
//
//   - which rows are 'running' (plan_runs),
//   - what a Stop does to an orphan (kill its process group — condemning a live
//     orchestrator to 'failed / daemon restart' would both lie on the Plans page and
//     free the slot, so a Retry would put a second orchestrator into the same
//     worktree while the first is still committing),
//   - what to write when the pid finally goes away — settleAdopted below, which
//     reads the plan's phase checkboxes and the transcript's ending and runs them
//     through the SAME classifier the normal exit path uses. Writing 'done'
//     because a process ended is the exit-code rule this phase removed
//     everywhere else.

import (
	"fmt"
	"log"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// tracked adapts this service to runcore.Tracked.
type tracked struct{ s *Service }

func (tracked) Engine() string { return Engine }

func (t tracked) ScanRunning() ([]runcore.Candidate, error) {
	return runcore.ScanCandidates(t.s.DB,
		`SELECT workspace_task_id, COALESCE(run_session_uuid,'') FROM plan_runs WHERE run_state='running'`)
}

func (t tracked) Adopt(c runcore.Candidate, pid int) (runcore.AdoptHooks, bool) {
	return runcore.AdoptHooks{
		Kill: func() {
			if err := procgroup.Kill(pid); err != nil {
				log.Printf("warning: planrun: kill adopted run plan=%d pid=%d: %v", c.ID, pid, err)
			}
		},
		Adopted: func() { t.s.notify(c.ID) },
		Ended: func(cancelled bool) {
			state, note := t.s.settleAdopted(c.ID, c.UUID)
			if cancelled {
				state, note = "failed", "cancelled"
			}
			t.s.stamp(c.ID, state, note)
			t.s.notify(c.ID)
		},
	}, true
}

// settleAdopted decides an adopted plan run's ending from the same evidence
// settle() uses — the criteria ticked across the plan's phase docs right now and
// the transcript's ending — rather than from the disappearance of a pid.
//
// The phases are re-read HERE, at exit, not at adoption: an orphan keeps ticking
// for however long it outlives the daemon, and the counts read at startup would
// be the state it was already past.
//
// It never continues: the continuation counter died with the previous daemon and
// runcore.MaxContinuations must hold unconditionally, so a run that would have
// been nudged settles `partial` with runcore.AdoptedNotContinuedNote.
func (s *Service) settleAdopted(taskID int64, uuid string) (state, note string) {
	text := runcore.LastAssistantText(s.DB, uuid)
	stop := runcore.LastStopReason(s.DB, uuid)
	refusalCat := runcore.RefusalCategory(s.DB, uuid)

	phases, err := s.loadPhases(taskID)
	if err != nil {
		log.Printf("warning: planrun: adopted plan=%d phases unreadable at exit: %v", taskID, err)
	}
	done, total, _, ok := planCriteria(phases)
	if !ok {
		if reason, blocked := runcore.BlockedOrRefused(text, stop, refusalCat); blocked {
			s.event(taskID, uuid, runcore.EventBlocked, 0, reason)
			log.Printf("planrun: adopted plan=%d blocked: %s", taskID, reason)
			return "blocked", reason
		}
		note = runcore.AdoptedExitNote + "; no readable phase doc at exit"
		s.event(taskID, uuid, runcore.EventPartial, 0, note)
		return "partial", note
	}

	switch end, reason := runcore.ClassifyRunEnd(text, stop, refusalCat, done, total); end {
	case runcore.EndBlocked:
		s.event(taskID, uuid, runcore.EventBlocked, 0, reason)
		log.Printf("planrun: adopted plan=%d blocked: %s", taskID, reason)
		return "blocked", reason
	case runcore.EndDone:
		return "done", runcore.AdoptedExitNote
	default:
		note = fmt.Sprintf("%d of %d criteria ticked; %s", done, total, runcore.AdoptedNotContinuedNote)
		s.event(taskID, uuid, runcore.EventPartial, 0, note)
		log.Printf("planrun: adopted plan=%d partial: %s", taskID, note)
		return "partial", note
	}
}

// adoptSurvivors probes every 'running' plan run and adopts the live ones,
// returning their workspace task ids so HealStale can exclude them.
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
