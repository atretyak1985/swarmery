package phaserun

import (
	"log"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// RelendDocs refreshes the lent copy of every phase doc in docPaths whose
// phase's worktree still exists and still holds a copy — called after a plan
// revision was applied (internal/planrev.Apply), so a worktree that outlived its
// run does not go on holding, and being graded against, the pre-revision text.
// The revision refuses a phase that is running, so this never races an executor.
// Returns how many copies were refreshed; every failure is logged and skipped.
func (s *Service) RelendDocs(docPaths []string) int {
	if s == nil || s.Wt == nil {
		return 0
	}
	n := 0
	for _, doc := range docPaths {
		rows, err := s.DB.Query(`SELECT id FROM epic_phases WHERE doc_path = ?`, doc)
		if err != nil {
			log.Printf("warning: phaserun: re-lend %s: %v", doc, err)
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			info, err := s.loadPhase(id)
			if err != nil || info.ProjectSlug == "" {
				continue
			}
			wt, err := s.Wt.Path(info.ProjectSlug, runcore.PhaseTaskName(id))
			if err != nil {
				continue
			}
			ok, err := worktree.RelendPlanDoc(wt, doc)
			switch {
			case err != nil:
				log.Printf("warning: phaserun: phase=%d re-lend %s into %s: %v", id, doc, wt, err)
			case ok:
				n++
				log.Printf("phaserun: phase=%d re-lent the revised doc into %s", id, wt)
			}
		}
	}
	return n
}
