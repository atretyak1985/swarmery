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

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
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
			state, note := t.s.settleAdopted(c.ID, c.UUID, info.DocPath)
			if cancelled {
				state, note = "failed", "cancelled"
			}
			t.s.stamp(c.ID, info.DocPath, state, note)
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
func (s *Service) settleAdopted(phaseID int64, uuid, docPath string) (state, note string) {
	text := runcore.LastAssistantText(s.DB, uuid)

	c, ok := criteriaInDoc(docPath)
	if !ok {
		if reason, blocked := runcore.BlockedReason(text); blocked {
			s.event(phaseID, uuid, runcore.EventBlocked, 0, reason)
			log.Printf("phaserun: adopted phase=%d blocked: %s", phaseID, reason)
			return "blocked", reason
		}
		note = fmt.Sprintf("%s; phase doc unreadable at exit: %s", runcore.AdoptedExitNote, docPath)
		s.event(phaseID, uuid, runcore.EventPartial, 0, note)
		return "partial", note
	}

	switch end, reason := runcore.ClassifyEnd(text, c.Done, c.Total); end {
	case runcore.EndBlocked:
		s.event(phaseID, uuid, runcore.EventBlocked, 0, reason)
		log.Printf("phaserun: adopted phase=%d blocked: %s", phaseID, reason)
		return "blocked", reason
	case runcore.EndDone:
		return "done", runcore.AdoptedExitNote
	default:
		note = fmt.Sprintf("%d of %d criteria ticked; %s", c.Done, c.Total, runcore.AdoptedNotContinuedNote)
		s.event(phaseID, uuid, runcore.EventPartial, 0, note)
		log.Printf("phaserun: adopted phase=%d partial: %s", phaseID, note)
		return "partial", note
	}
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
