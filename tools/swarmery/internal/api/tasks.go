package api

// phase 3.5: workspaces (E-lite) — task endpoints. Workspace-ingested tasks
// (tasks.source='workspace') carry card metadata plus their linked sessions
// (task_sessions) and the aggregate cost those sessions burned.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// taskSessionDTO is one linked session inside a task detail.
type taskSessionDTO struct {
	SessionID   int64    `json:"sessionId"`
	SessionUUID string   `json:"sessionUuid"`
	Title       *string  `json:"title"`
	StartedAt   string   `json:"startedAt"`
	EndedAt     *string  `json:"endedAt"`
	LinkSource  string   `json:"linkSource"` // explicit | heuristic
	Confidence  *float64 `json:"confidence"`
	CostUSD     *float64 `json:"costUsd"`
}

// taskSummaryDTO is one row of GET /api/tasks (the Overview 14-day table).
type taskSummaryDTO struct {
	ID            int64    `json:"id"`
	ExternalID    string   `json:"externalId"`
	WorkspaceSlug string   `json:"workspaceSlug"`
	ProjectSlug   string   `json:"projectSlug"`
	ProjectName   *string  `json:"projectName"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	Outcome       string   `json:"outcome"` // active | done | archived
	StartedAt     *string  `json:"startedAt"`
	ArchivedAt    *string  `json:"archivedAt"`
	Sessions      int64    `json:"sessions"`
	CostUSD       *float64 `json:"costUsd"`
}

// taskDetailDTO is GET /api/tasks/{id}: card metadata + linked sessions +
// total cost + outcome.
type taskDetailDTO struct {
	taskSummaryDTO
	Goal         *string          `json:"goal"` // card **Ціль** (stored in tasks.prompt)
	SessionLinks []taskSessionDTO `json:"sessionLinks"`
}

// outcome derives the human word for a task's state.
func outcome(status string, archivedAt *string) string {
	switch {
	case archivedAt != nil:
		return "archived"
	case status == "done":
		return "done"
	default:
		return "active"
	}
}

const taskSummarySelect = `
	SELECT t.id, t.external_id, w.slug, p.slug, p.name, t.title, t.status,
	       t.started_at, t.archived_at,
	       COALESCE(agg.sessions, 0), agg.cost_usd
	FROM tasks t
	JOIN workspaces w ON w.id = t.workspace_id
	JOIN projects p   ON p.id = t.project_id
	LEFT JOIN (
		-- Per-session cost is a CORRELATED subquery, not a
		-- "LEFT JOIN (SELECT ... FROM turns GROUP BY session_id)" derived table:
		-- SQLite materializes the latter with a FULL scan of turns (fat text
		-- column, ~100k rows) on every call. Correlated, it rides
		-- idx_turns_session_usage once per task_sessions row -- a table two
		-- orders of magnitude smaller. Same reasoning as sessionSelect.
		SELECT ts.task_id,
		       COUNT(*) AS sessions,
		       SUM((SELECT SUM(tu.cost_usd) FROM turns tu
		             WHERE tu.session_id = ts.session_id)) AS cost_usd
		FROM task_sessions ts
		GROUP BY ts.task_id
	) agg ON agg.task_id = t.id
	WHERE t.source = 'workspace'`

func scanTaskSummary(scan func(...any) error, t *taskSummaryDTO) error {
	if err := scan(&t.ID, &t.ExternalID, &t.WorkspaceSlug, &t.ProjectSlug, &t.ProjectName,
		&t.Title, &t.Status, &t.StartedAt, &t.ArchivedAt, &t.Sessions, &t.CostUSD); err != nil {
		return err
	}
	t.Outcome = outcome(t.Status, t.ArchivedAt)
	return nil
}

// GET /api/tasks?days=<n> — workspace tasks recently active: started within
// the window, finished within it, or still open. Default 14 days.
func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	days := 14
	if q := r.URL.Query().Get("days"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n <= 0 {
			http.Error(w, `{"error":"invalid days, want a positive integer"}`, http.StatusBadRequest)
			return
		}
		days = n
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	// Tasks of archived projects drop out of the list too (consistent with
	// sessions/analytics); getTask by id stays reachable so the hide is reversible.
	rows, err := h.DB.Query(taskSummarySelect+`
		AND p.archived = 0
		AND (t.started_at >= ? OR t.finished_at >= ? OR t.finished_at IS NULL)
		ORDER BY agg.cost_usd DESC, t.started_at DESC`, cutoff, cutoff)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	tasks := []taskSummaryDTO{}
	for rows.Next() {
		var t taskSummaryDTO
		if err := scanTaskSummary(rows.Scan, &t); err != nil {
			writeErr(w, err)
			return
		}
		tasks = append(tasks, t)
	}
	writeJSON(w, tasks, rows.Err())
}

// GET /api/tasks/{id} — id is the numeric row id or the card external_id.
func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	idArg := r.PathValue("id")
	where := ` AND t.external_id = ?`
	if _, err := strconv.ParseInt(idArg, 10, 64); err == nil {
		where = ` AND t.id = ?`
	}

	var d taskDetailDTO
	err := scanTaskSummary(h.DB.QueryRow(taskSummarySelect+where, idArg).Scan, &d.taskSummaryDTO)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	var goal sql.NullString
	if err := h.DB.QueryRow(`SELECT prompt FROM tasks WHERE id = ?`, d.ID).Scan(&goal); err != nil {
		writeErr(w, err)
		return
	}
	if goal.Valid && goal.String != "" {
		d.Goal = &goal.String
	}

	d.SessionLinks = []taskSessionDTO{}
	rows, err := h.DB.Query(`
		SELECT s.id, s.session_uuid, s.title, s.started_at, s.ended_at,
		       ts.link_source, ts.confidence,
		       -- correlated, not a materialized full scan of turns (see
		       -- taskSummarySelect) -- this runs for one task's sessions only.
		       (SELECT SUM(tu.cost_usd) FROM turns tu WHERE tu.session_id = s.id)
		FROM task_sessions ts
		JOIN sessions s ON s.id = ts.session_id
		WHERE ts.task_id = ?
		ORDER BY s.started_at`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ls taskSessionDTO
		if err := rows.Scan(&ls.SessionID, &ls.SessionUUID, &ls.Title, &ls.StartedAt,
			&ls.EndedAt, &ls.LinkSource, &ls.Confidence, &ls.CostUSD); err != nil {
			writeErr(w, err)
			return
		}
		d.SessionLinks = append(d.SessionLinks, ls)
	}
	writeJSON(w, d, rows.Err())
}
