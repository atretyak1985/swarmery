package api

// The retro report's surprises section (learning loop phase 13.6): the window's
// phase runs that landed furthest from their forecast, which the digest cites as
// [E:phase:<id>] so the system-improver can reason about plans that mis-predict.

// retroSurprisesLimit bounds the section: the digest is an evidence budget, and
// the tail of a surprise ranking is by construction the least surprising.
const retroSurprisesLimit = 10

type retroSurpriseDTO struct {
	PhaseID     int64   `json:"phaseId"`
	Plan        string  `json:"plan"`
	Phase       string  `json:"phase"`
	Index       float64 `json:"index"`
	Top         string  `json:"top"`
	Summary     string  `json:"summary"`
	SessionUUID string  `json:"sessionUuid"`
	ComputedAt  string  `json:"computedAt"`
}

type retroSurprisesDTO struct {
	Surprises []retroSurpriseDTO `json:"surprises"`
}

// buildRetroSurprises returns the most surprising scored phase runs computed in
// the window, one per phase (its most surprising run), highest first.
//
// Two exclusions keep the section honest evidence: a score of 0 (the run went as
// forecast — nothing to attend to) and a score built on a POST-HOC forecast (one
// that cannot have been a prediction, so its "surprise" measures nothing).
func (h *Handler) buildRetroSurprises(dr dateRange, pf string, pargs []any) (retroSurprisesDTO, error) {
	rows, err := h.DB.Query(`
		SELECT s.phase_id, COALESCE(t.title, ''), e.name, MAX(s.surprise_index),
		       s.top_component, s.summary, s.session_uuid, s.computed_at
		  FROM phase_surprise s
		  JOIN epic_phases e ON e.id = s.phase_id
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		 WHERE s.computed_at >= ? AND s.computed_at < ? AND p.archived = 0
		   AND s.forecast_post_hoc = 0 AND s.surprise_index > 0`+pf+`
		 GROUP BY s.phase_id
		 ORDER BY MAX(s.surprise_index) DESC, s.phase_id
		 LIMIT ?`,
		append(append([]any{dr.start, dr.end}, pargs...), retroSurprisesLimit)...)
	if err != nil {
		return retroSurprisesDTO{}, err
	}
	defer rows.Close()
	out := retroSurprisesDTO{Surprises: []retroSurpriseDTO{}}
	for rows.Next() {
		var d retroSurpriseDTO
		if err := rows.Scan(&d.PhaseID, &d.Plan, &d.Phase, &d.Index, &d.Top, &d.Summary,
			&d.SessionUUID, &d.ComputedAt); err != nil {
			return retroSurprisesDTO{}, err
		}
		out.Surprises = append(out.Surprises, d)
	}
	return out, rows.Err()
}
