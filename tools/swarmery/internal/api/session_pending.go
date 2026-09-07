package api

// Pending sessions: the answer GET /api/sessions/{uuid} gives BEFORE the
// transcript exists.
//
// Every run the daemon spawns (phase, plan, dispatch, verify, planning) mints its
// session uuid up front and parks it on its own row, and the dashboard links to
// /sessions/<uuid> the moment the run is admitted. The sessions row, however, is
// created by ingest when `claude -p` writes its first transcript line — several
// seconds later. In that window the detail page used to get a bare 404 ("session
// not found") that looked exactly like a pruned or hidden session, with nothing to
// wait for. A uuid the daemon itself minted is not "not found": it is a session that
// has not started writing yet, and this file says so (202) with enough context for
// the page to wait, and to stop waiting when the run ended without ever producing a
// transcript.
//
// pendingSessionSources is the registry of every column that parks a minted uuid.
// TestPendingSessionSources_CoverEverySessionUUIDColumn scans the live schema and
// fails when a new `*session_uuid` column is added without either a source here or
// an explicit exemption with its reason — the only way this list stays complete.

import (
	"database/sql"
	"errors"
)

// pendingSessionSource maps one uuid-bearing column to the query that describes the
// run parked on it.
type pendingSessionSource struct {
	Table, Column string // schema identity, for the coverage test
	Kind          string // wire value: what kind of run minted the uuid
	// query takes the uuid and yields exactly:
	//   running (0/1), started_at, label, task_id (nullable), ref_id, project_slug
	query string
}

var pendingSessionSources = []pendingSessionSource{
	{Table: "epic_phases", Column: "run_session_uuid", Kind: "phase", query: `
		SELECT e.run_state = 'running', COALESCE(e.run_started_at, ''), e.name,
		       e.workspace_task_id, e.id, COALESCE(p.slug, '')
		  FROM epic_phases e
		  LEFT JOIN tasks t ON t.id = e.workspace_task_id
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE e.run_session_uuid = ?`},
	{Table: "plan_runs", Column: "run_session_uuid", Kind: "plan", query: `
		SELECT r.run_state = 'running', COALESCE(r.run_started_at, ''), COALESCE(t.title, ''),
		       r.workspace_task_id, r.workspace_task_id, COALESCE(p.slug, '')
		  FROM plan_runs r
		  LEFT JOIN tasks t ON t.id = r.workspace_task_id
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE r.run_session_uuid = ?`},
	{Table: "tasks", Column: "dispatch_session_uuid", Kind: "dispatch", query: `
		SELECT t.board_column = 'in_progress', COALESCE(t.started_at, ''), COALESCE(t.title, ''),
		       t.id, t.id, COALESCE(p.slug, '')
		  FROM tasks t
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.dispatch_session_uuid = ?`},
	{Table: "verification_runs", Column: "verify_session_uuid", Kind: "verify", query: `
		SELECT v.status = 'running', COALESCE(v.started_at, ''), 'verification of ' || v.target_key,
		       v.task_id, v.id, COALESCE(p.slug, '')
		  FROM verification_runs v
		  LEFT JOIN tasks t ON t.id = v.task_id
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE v.verify_session_uuid = ?`},
	{Table: "planning_sessions", Column: "session_uuid", Kind: "planning", query: `
		SELECT s.status IN ('generating', 'awaiting_answer', 'proceeding'), COALESCE(s.created_at, ''),
		       COALESCE(s.idea, ''), NULL, s.id, COALESCE(p.slug, '')
		  FROM planning_sessions s
		  LEFT JOIN projects p ON p.id = s.project_id
		 WHERE s.session_uuid = ?`},
	// A revision row is written once its generating session has finished, so it is
	// never "running" — but its uuid is still one the daemon minted, and a missing
	// transcript is worth naming rather than reporting as "not found".
	{Table: "plan_revisions", Column: "session_uuid", Kind: "revision", query: `
		SELECT 0, COALESCE(r.created_at, ''), COALESCE(r.reason, ''),
		       r.workspace_task_id, r.id, COALESCE(p.slug, '')
		  FROM plan_revisions r
		  LEFT JOIN tasks t ON t.id = r.workspace_task_id
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE r.session_uuid = ?`},
}

// pendingSessionExempt lists `*session_uuid` columns that deliberately have no
// source above, each with the reason. The coverage test fails on a column that is
// neither a source nor listed here, and on an entry here that no longer exists.
var pendingSessionExempt = map[string]string{
	"sessions.session_uuid":                "the ingested session row itself — a hit here is a 200, not a pending answer",
	"retro_analyses.planning_session_uuid": "a copy of planning_sessions.session_uuid stamped on the analysis it planned; the planning row is the source",
}

// pendingSessionDTO is the 202 body for GET /api/sessions/{uuid}. `pending` is the
// discriminator the client narrows on against the 200 session detail.
type pendingSessionDTO struct {
	Pending     bool    `json:"pending"`
	SessionUUID string  `json:"sessionUuid"`
	Source      string  `json:"source"`
	Running     bool    `json:"running"`
	StartedAt   *string `json:"startedAt"`
	Label       string  `json:"label"`
	TaskID      *int64  `json:"taskId"`
	RefID       int64   `json:"refId"`
	ProjectSlug string  `json:"projectSlug"`
}

// pendingSession looks the uuid up across every source, in registry order. ok is
// false when no run parked this uuid — the plain 404 case.
func pendingSession(db *sql.DB, uuid string) (pendingSessionDTO, bool, error) {
	for _, src := range pendingSessionSources {
		var (
			running   bool
			startedAt string
			label     string
			taskID    sql.NullInt64
			refID     int64
			slug      string
		)
		err := db.QueryRow(src.query, uuid).Scan(&running, &startedAt, &label, &taskID, &refID, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return pendingSessionDTO{}, false, err
		}
		d := pendingSessionDTO{
			Pending:     true,
			SessionUUID: uuid,
			Source:      src.Kind,
			Running:     running,
			Label:       label,
			RefID:       refID,
			ProjectSlug: slug,
		}
		if startedAt != "" {
			d.StartedAt = &startedAt
		}
		if taskID.Valid {
			d.TaskID = &taskID.Int64
		}
		return d, true, nil
	}
	return pendingSessionDTO{}, false, nil
}
