package lessons

// The generation log as the dashboard reads it: one lesson_generations row per
// run that was eligible for a lesson. Shown under the review queue, so a run
// that produced no candidate still says why — a failure, every lesson rejected,
// or the model's own "nothing transferable here" (no_entry_reason, 0133).

import (
	"database/sql"
	"encoding/json"
)

// Generation is one lesson_generations row.
type Generation struct {
	SourcePhaseRun string      `json:"sourcePhaseRun"`
	PhaseID        int64       `json:"phaseId"`
	PhaseName      string      `json:"phaseName"`
	PlanID         string      `json:"planId"`
	State          string      `json:"state"` // running | done | failed
	Inserted       int         `json:"inserted"`
	Linked         int         `json:"linked"`
	Rejected       []Rejection `json:"rejected"`
	Error          string      `json:"error"`
	NoEntryReason  string      `json:"noEntryReason"`
	Model          string      `json:"model"`
	CreatedAt      string      `json:"createdAt"`
	FinishedAt     *string     `json:"finishedAt"`
}

// maxGenerations caps one ListGenerations read.
const maxGenerations = 200

// ListGenerations returns the newest generation rows, at most limit (clamped to
// 1..maxGenerations), narrowed to one project by slug ("" is every project).
func ListGenerations(db *sql.DB, project string, limit int) ([]Generation, error) {
	if limit <= 0 || limit > maxGenerations {
		limit = maxGenerations
	}
	q := `SELECT g.source_phase_run, g.phase_id, COALESCE(e.name, ''), COALESCE(t.external_id, ''), g.state,
		g.inserted, g.linked, g.rejected_json, g.error, g.no_entry_reason, g.model, g.created_at, g.finished_at
		FROM lesson_generations g
		LEFT JOIN epic_phases e ON e.id = g.phase_id
		LEFT JOIN tasks t ON t.id = e.workspace_task_id`
	args := []any{}
	if project != "" {
		q += ` WHERE t.project_id IN (SELECT id FROM projects WHERE slug = ?)`
		args = append(args, project)
	}
	args = append(args, limit)
	rows, err := db.Query(q+` ORDER BY g.created_at DESC, g.source_phase_run LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Generation{}
	for rows.Next() {
		var (
			g        Generation
			rejected string
			finished sql.NullString
		)
		if err := rows.Scan(&g.SourcePhaseRun, &g.PhaseID, &g.PhaseName, &g.PlanID, &g.State, &g.Inserted,
			&g.Linked, &rejected, &g.Error, &g.NoEntryReason, &g.Model, &g.CreatedAt, &finished); err != nil {
			return nil, err
		}
		g.Rejected = []Rejection{}
		_ = json.Unmarshal([]byte(rejected), &g.Rejected)
		g.FinishedAt = nullStr(finished)
		out = append(out, g)
	}
	return out, rows.Err()
}
