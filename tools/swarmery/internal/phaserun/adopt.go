package phaserun

// Adoption — a phase run that outlived the daemon. The loop lives in
// internal/runcore (why adoption exists at all, and what it can and cannot
// recover, is documented there); what stays here is this engine's policy:
//
//   - which rows are 'running' (epic_phases),
//   - what a Stop does to an orphan (kill its process group),
//   - what to write when the pid finally goes away — settleAdopted below, which
//     runs the SAME evidence through the SAME classifier the normal exit path
//     uses. It used to write 'done' unconditionally, which made adoption the one
//     place in the daemon where a run's completion was still decided by the fact
//     that a process ended rather than by what it achieved.

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// tracked adapts this service to runcore.Tracked.
type tracked struct{ s *Service }

func (tracked) Engine() string { return Engine }

func (t tracked) ScanRunning() ([]runcore.Candidate, error) {
	return runcore.ScanCandidates(t.s.DB,
		`SELECT id, COALESCE(run_session_uuid,'') FROM epic_phases WHERE run_state='running'`)
}

func (t tracked) Adopt(c runcore.Candidate, pid int) (runcore.AdoptHooks, bool) {
	// The row has to be loadable: the terminal stamp needs its doc path, and the UI
	// nudge needs its workspace task id. A row we cannot load is left to the heal
	// sweep — an adopted run we could never stamp would be stuck 'running' for ever.
	info, err := t.s.loadPhase(c.ID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d has a live process (pid=%d) but is unloadable (%v) — healing it instead",
			c.ID, pid, err)
		return runcore.AdoptHooks{}, false
	}
	return runcore.AdoptHooks{
		Kill: func() {
			// The run leads its own group (procgroup.Isolate at spawn), so this reaches
			// its children too — the same kill a cancel of our own child does.
			if err := procgroup.Kill(pid); err != nil {
				log.Printf("warning: phaserun: kill adopted run phase=%d pid=%d: %v", c.ID, pid, err)
			}
		},
		Adopted: func() { t.s.notify(info.WorkspaceTaskID) },
		Ended: func(cancelled bool) {
			state, note := t.s.settleAdopted(c.ID, c.UUID, info)
			if cancelled {
				state, note = "failed", "cancelled"
			}
			t.s.stamp(c.ID, info.DocPath, state, note)
			// A survivor's actuals are measured like any run's, after the stamp.
			// loadPhase does not resolve the repo (Start does), so resolve it here;
			// a failure only costs the git half of the row.
			if t.s.Actuals != nil {
				root, err := t.s.runRoot(info)
				if err != nil {
					log.Printf("warning: phaserun: phase=%d actuals: run repository unresolved: %v", c.ID, err)
				}
				t.s.Actuals(c.ID, c.UUID, root)
			}
			t.s.notify(info.WorkspaceTaskID)
		},
	}, true
}

// settleAdopted decides an adopted phase run's ending from the same two inputs
// settle() weighs — the criteria ticked in the doc RIGHT NOW and the transcript's
// ending — so a run that outlived a daemon restart is graded by the same rule as
// one we reaped ourselves.
//
// It deliberately does NOT continue. The continuation counter lives in settle's
// loop variable and died with the previous daemon, so resuming here could take a
// run past runcore.MaxContinuations, which is a money bound and holds
// unconditionally. A survivor that would have been nudged settles `partial` and
// records why (runcore.AdoptedNotContinuedNote).
//
// Every note keeps AdoptedExitNote's fact attached: the exit status really is
// unknown, whatever the evidence says about the work.
func (s *Service) settleAdopted(phaseID int64, uuid string, info phaseInfo) (state, note string) {
	// FIRST, before any of the evidence is read: bring the executor's copy of the
	// phase doc home. Start lends the doc INTO the worktree (LendPlanDoc) and the
	// orphan ticks THAT file; the workspace copy it was cut from is untouched. The
	// normal exit path knows this and calls returnDocNow before it counts anything
	// — adoption did not, so it graded the pre-run copy: a survivor that ticked all
	// 11 criteria and wrote its Completion Report was stamped
	// `partial / 0 of 11 criteria ticked`, a statement that is false about a
	// finished phase. Worse, the next Start's LendPlanDoc overwrites the worktree
	// copy from that stale workspace doc, so the ticks AND the report are gone.
	//
	// Same helper and same rules as returnDocNow (worktree.ReturnPlanDocLogged): a
	// missing copy and an unchanged copy are both no-ops, an empty one is refused,
	// and no failure here is fatal — the work is already in the branch.
	docPath := info.DocPath
	located := s.returnAdoptedDoc(phaseID, info)

	text := runcore.LastAssistantText(s.DB, uuid)
	// Same evidence pair as settle: an adopted run can have ended in a safeguard
	// refusal exactly as an attended one can, and the adoption path is the one
	// with NO continuation loop to notice later.
	stop := runcore.LastStopReason(s.DB, uuid)
	refusalCat := runcore.RefusalCategory(s.DB, uuid)

	c, ok := criteriaInDoc(docPath)
	if !ok {
		if reason, blocked := runcore.BlockedOrRefused(text, stop, refusalCat); blocked {
			s.event(phaseID, uuid, runcore.EventBlocked, 0, reason)
			log.Printf("phaserun: adopted phase=%d blocked: %s", phaseID, reason)
			return "blocked", reason
		}
		note = fmt.Sprintf("%s; phase doc unreadable at exit: %s", runcore.AdoptedExitNote, docPath)
		s.event(phaseID, uuid, runcore.EventPartial, 0, note)
		return "partial", note
	}

	switch end, reason := runcore.ClassifyRunEnd(text, stop, refusalCat, c.Done, c.Total); end {
	case runcore.EndBlocked:
		s.event(phaseID, uuid, runcore.EventBlocked, 0, reason)
		log.Printf("phaserun: adopted phase=%d blocked: %s", phaseID, reason)
		return "blocked", reason
	case runcore.EndDone:
		return "done", runcore.AdoptedExitNote
	default:
		// The count is only worth printing when the doc it was counted from is the
		// one the executor actually edited. When the lent copy could not even be
		// located, `c` describes the PRE-RUN document, and stamping "0 of 11" from
		// it asserts a measurement that was never taken — the very falsehood this
		// path existed to produce. Name the gap instead.
		if located {
			note = fmt.Sprintf("%d of %d criteria ticked; %s", c.Done, c.Total, runcore.AdoptedNotContinuedNote)
		} else {
			note = fmt.Sprintf("the executor's copy of the phase doc could not be recovered, so its ticked criteria are unknown; %s",
				runcore.AdoptedNotContinuedNote)
		}
		s.event(phaseID, uuid, runcore.EventPartial, 0, note)
		log.Printf("phaserun: adopted phase=%d partial: %s", phaseID, note)
		return "partial", note
	}
}

// returnAdoptedDoc copies the phase doc out of the orphan's worktree and back
// over the workspace document, exactly the way runAndHandle's returnDocNow does
// for a run we reaped ourselves — same helper, same tolerance for a missing or
// unchanged file.
//
// Adoption holds no worktree.Acquired (this daemon never acquired anything), so
// the checkout is re-derived from the pair Start handed Acquire: the project slug
// and runcore.PhaseTaskName(phaseID). The lent copy's location inside it is
// LendPlanDoc's own contract, PlanDocDir/<basename>.
//
// The bool is whether the copy-back could be ATTEMPTED at all — false only when
// the worktree path cannot be named, which is the one case in which the caller
// must not quote a tick count it never measured.
func (s *Service) returnAdoptedDoc(phaseID int64, info phaseInfo) bool {
	if info.DocPath == "" {
		return false
	}
	wtPath, err := s.Wt.Path(info.ProjectSlug, runcore.PhaseTaskName(phaseID))
	if err != nil {
		log.Printf("warning: phaserun: adopted phase=%d: cannot locate the run's worktree, so its copy of %s is unreachable: %v",
			phaseID, info.DocPath, err)
		return false
	}
	worktree.ReturnPlanDocLogged(fmt.Sprintf("phaserun adopted phase=%d", phaseID),
		wtPath, filepath.Join(worktree.PlanDocDir, filepath.Base(info.DocPath)), info.DocPath)
	return true
}

// adoptSurvivors probes every 'running' row and adopts the live ones, returning
// their phase ids so HealStale can exclude them from the fail sweep.
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
