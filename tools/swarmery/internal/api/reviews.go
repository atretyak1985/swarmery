package api

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The review stage's read and acknowledge surface (phase_reviews, migration 0106).
// The phase review stage (internal/phaserun/review.go) writes scope='phase' rows,
// and the plan branch review (internal/phaserun/plan_review.go) writes
// scope='plan' rows. These endpoints only read them and stamp acked_at.
//
//	GET  /api/reviews?scope=plan&unacked=1[&taskId=N][&limit=N]   newest first
//	POST /api/reviews/{id}/ack                                    requireLocalOrigin
//	GET  /api/epics/{taskId}/phases/{phaseId}/reviews             phase scope, newest first

// reviewDTO is one phase_reviews row. Plan-scope rows have phaseId null and a
// branchSetKey. Phase-scope rows have a runSessionUuid. finishedAt, ackedAt and
// costUsd are null until known (cost is not recorded yet).
type reviewDTO struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"` // phase | plan
	TaskID    int64  `json:"taskId"`
	PlanTitle string `json:"planTitle"`
	// ProjectSlug is the plan's project ("" once the task is gone): the Inbox
	// builds the link to the plan from it and narrows a project scope by it.
	ProjectSlug string `json:"projectSlug"`
	PhaseID     *int64 `json:"phaseId"`
	PhaseName   string `json:"phaseName"`
	// SessionUUID is the reviewer's own session.
	SessionUUID string `json:"sessionUuid"`
	// RunSessionUUID is the run the review graded (scope=phase), "" for a plan.
	RunSessionUUID string `json:"runSessionUuid"`
	// BranchSetKey identifies the set of run-branch tips a plan review read
	// (scope=plan), "" for a phase.
	BranchSetKey string   `json:"branchSetKey"`
	Verdict      string   `json:"verdict"` // pass | fail | inconclusive
	Detail       string   `json:"detail"`
	Findings     string   `json:"findings"`
	FixRound     int      `json:"fixRound"`
	CostUSD      *float64 `json:"costUsd"`
	TreeBefore   string   `json:"treeBefore"`
	TreeAfter    string   `json:"treeAfter"`
	StartedAt    string   `json:"startedAt"`
	FinishedAt   *string  `json:"finishedAt"`
	AckedAt      *string  `json:"ackedAt"`
}

// branchSetPrefix mirrors phaserun.planReviewKeyPrefix: the plan review stores
// its dedupe key in run_session_uuid under this prefix.
const branchSetPrefix = "branchset:"

const reviewSelect = `
	SELECT r.id, r.scope, CAST(r.workspace_task_id AS INTEGER), COALESCE(t.title, ''), COALESCE(p.slug, ''),
	       r.phase_id, COALESCE(e.name, ''), r.session_uuid, r.run_session_uuid,
	       r.verdict, r.detail, r.findings, r.fix_round, r.cost_usd,
	       r.tree_before, r.tree_after, r.started_at, r.finished_at, r.acked_at
	  FROM phase_reviews r
	  LEFT JOIN tasks t ON t.id = CAST(r.workspace_task_id AS INTEGER)
	  LEFT JOIN projects p ON p.id = t.project_id
	  LEFT JOIN epic_phases e ON e.id = r.phase_id`

func scanReviews(rows *sql.Rows) ([]reviewDTO, error) {
	defer rows.Close()
	out := make([]reviewDTO, 0)
	for rows.Next() {
		var (
			d        reviewDTO
			phaseID  sql.NullInt64
			cost     sql.NullFloat64
			finished sql.NullString
			acked    sql.NullString
			runUUID  string
		)
		if err := rows.Scan(&d.ID, &d.Scope, &d.TaskID, &d.PlanTitle, &d.ProjectSlug, &phaseID, &d.PhaseName,
			&d.SessionUUID, &runUUID, &d.Verdict, &d.Detail, &d.Findings, &d.FixRound, &cost,
			&d.TreeBefore, &d.TreeAfter, &d.StartedAt, &finished, &acked); err != nil {
			return nil, err
		}
		if phaseID.Valid {
			v := phaseID.Int64
			d.PhaseID = &v
		}
		if cost.Valid {
			v := cost.Float64
			d.CostUSD = &v
		}
		if finished.Valid {
			v := finished.String
			d.FinishedAt = &v
		}
		if acked.Valid {
			v := acked.String
			d.AckedAt = &v
		}
		if d.Scope == "plan" {
			d.BranchSetKey = strings.TrimPrefix(runUUID, branchSetPrefix)
		} else {
			d.RunSessionUUID = runUUID
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const (
	reviewListDefault = 100
	reviewListMax     = 500
)

// listReviews — GET /api/reviews?scope=phase|plan&unacked=1&taskId=N&limit=N.
// 200 []reviewDTO, newest first, never null; 400 on an unknown scope, a bad
// unacked flag, a bad taskId or a bad limit.
func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		where []string
		args  []any
	)
	switch scope := q.Get("scope"); scope {
	case "":
	case "phase", "plan":
		where = append(where, "r.scope = ?")
		args = append(args, scope)
	default:
		writeClientErr(w, http.StatusBadRequest, "scope must be phase or plan")
		return
	}
	switch q.Get("unacked") {
	case "", "0", "false":
	case "1", "true":
		where = append(where, "r.acked_at IS NULL")
	default:
		writeClientErr(w, http.StatusBadRequest, "unacked must be 1 or 0")
		return
	}
	if v := q.Get("taskId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeClientErr(w, http.StatusBadRequest, "invalid taskId")
			return
		}
		where = append(where, "r.workspace_task_id = ?")
		args = append(args, strconv.FormatInt(id, 10))
	}
	limit := reviewListDefault
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeClientErr(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = min(n, reviewListMax)
	}
	query := reviewSelect
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY r.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := scanReviews(rows)
	writeJSON(w, out, err)
}

// ackReview — POST /api/reviews/{id}/ack. requireLocalOrigin. 200 the acked
// reviewDTO; 400 bad id; 404 unknown. Idempotent: a second ack keeps the first
// acked_at.
func (h *Handler) ackReview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientErr(w, http.StatusBadRequest, "invalid review id")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.DB.Exec(`UPDATE phase_reviews SET acked_at = COALESCE(acked_at, ?) WHERE id = ?`, now, id); err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.DB.Query(reviewSelect+" WHERE r.id = ?", id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := scanReviews(rows)
	switch {
	case err != nil:
		writeErr(w, err)
	case len(out) == 0:
		writeClientErr(w, http.StatusNotFound, "review not found")
	default:
		writeJSON(w, out[0], nil)
	}
}

// listPhaseReviews — GET /api/epics/{taskId}/phases/{phaseId}/reviews. 200
// []reviewDTO, the phase's scope='phase' reviews newest first, never null; 404
// for an unknown phase or one outside that epic.
func (h *Handler) listPhaseReviews(w http.ResponseWriter, r *http.Request) {
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(reviewSelect+`
		 WHERE r.scope = 'phase' AND r.phase_id = ?
		 ORDER BY r.id DESC LIMIT ?`, phaseID, reviewListMax)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := scanReviews(rows)
	writeJSON(w, out, err)
}
