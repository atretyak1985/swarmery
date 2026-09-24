package api

// Surprise scores on the Plans page and the attention frame (learning loop
// phase 13). The scorer lives in internal/surprise; this file only READS what it
// stored and hydrates the one WS frame it publishes.
//
// A phase carries the score of its CURRENT run only (epic_phases.run_session_uuid):
// the chip beside the run chips must describe the same run they do. A phase with
// no scored current run serves `surprise: null` — never a zero score — and the UI
// says "no forecast" or nothing.

import (
	"database/sql"
	"errors"
	"log"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// phaseSurprises returns, per phase id of this task, the stored score of the
// phase's current run. One query for the whole task, issued AFTER the phase
// cursor is closed (single SQLite connection — see phaseForecasts). Errors
// degrade to an empty map: a score is a decoration on a page that must render.
func (h *Handler) phaseSurprises(taskID int64) map[int64]*surprise.Stored {
	out := map[int64]*surprise.Stored{}
	rows, err := h.DB.Query(surprise.SelectStored+` s
		 WHERE EXISTS (SELECT 1 FROM epic_phases e
		                WHERE e.id = s.phase_id AND e.run_session_uuid = s.session_uuid
		                  AND e.workspace_task_id = ?)`, taskID)
	if err != nil {
		log.Printf("warning: epics: phase surprise unreadable (task %d): %v", taskID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		st, err := surprise.ScanStored(rows.Scan)
		if err != nil {
			log.Printf("warning: epics: phase surprise row (task %d): %v", taskID, err)
			return out
		}
		out[st.PhaseID] = st
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: epics: phase surprise (task %d): %v", taskID, err)
	}
	return out
}

// wsPhaseSurprisePayload is the phase_surprise frame: enough for the notch and
// the dashboard to say "phase X of plan Y surprised us, mostly on Z" without a
// second request, plus the ids to navigate to it.
type wsPhaseSurprisePayload struct {
	TaskID      int64   `json:"taskId"`
	ProjectID   int64   `json:"projectId"`
	PhaseID     int64   `json:"phaseId"`
	PhaseName   string  `json:"phaseName"`
	PlanTitle   string  `json:"planTitle"`
	SessionUUID string  `json:"sessionUuid"`
	Index       float64 `json:"index"`
	Top         string  `json:"top"`
	Summary     string  `json:"summary"`
}

// phaseSurprisePayload hydrates a phase_surprise frame from the phase's current
// run's stored score. (nil, nil) when the phase or its score is gone.
func (h *Handler) phaseSurprisePayload(phaseID int64) (*wsPhaseSurprisePayload, error) {
	var (
		p     wsPhaseSurprisePayload
		uuid  sql.NullString
		title sql.NullString
	)
	err := h.DB.QueryRow(`
		SELECT e.id, e.workspace_task_id, COALESCE(t.project_id, 0), e.name, t.title, e.run_session_uuid
		  FROM epic_phases e LEFT JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE e.id = ?`, phaseID).Scan(&p.PhaseID, &p.TaskID, &p.ProjectID, &p.PhaseName, &title, &uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.PlanTitle = title.String
	if !uuid.Valid || uuid.String == "" {
		return nil, nil
	}
	st, err := surprise.LoadBySession(h.DB, uuid.String)
	if err != nil || st == nil {
		return nil, err
	}
	p.SessionUUID, p.Index, p.Top, p.Summary = st.SessionUUID, st.Index, st.Top, st.Summary
	return &p, nil
}
