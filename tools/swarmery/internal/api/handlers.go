package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// Handler bundles the API dependencies.
type Handler struct {
	DB *sql.DB
	// Watching reports whether the live ingest pipeline is attached
	// (serve without --no-ingest); surfaced by GET /api/health.
	Watching bool
	// Docs is the markdown source for /api/docs — the embedded docsfs
	// snapshot in production, overridable with any fs.FS in tests.
	Docs fs.FS
}

type projectDTO struct {
	ID           int64   `json:"id"`
	Path         string  `json:"path"`
	Slug         string  `json:"slug"`
	Name         *string `json:"name"`
	FirstSeen    string  `json:"firstSeen"`
	LastActivity *string `json:"lastActivity"`
	Archived     bool    `json:"archived"`
	Sessions     int64   `json:"sessions"`
}

type sessionDTO struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"projectId"`
	ProjectSlug string  `json:"projectSlug"`
	ProjectName *string `json:"projectName"`
	SessionUUID string  `json:"sessionUuid"`
	Model       *string `json:"model"`
	GitBranch   *string `json:"gitBranch"`
	CWD         *string `json:"cwd"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"startedAt"`
	EndedAt     *string `json:"endedAt"`
	Title       *string `json:"title"`
	Source      string  `json:"source"`
	// Parity contract: per-session aggregates over deduped turns.
	// tokens = SUM(tokens_in + tokens_out), null while the session has no
	// turns; costUsd = SUM(cost_usd), null while no turn is priced.
	Tokens  *int64   `json:"tokens"`
	CostUSD *float64 `json:"costUsd"`
	// phase 3.5: workspaces — best task link (explicit beats heuristic,
	// then highest confidence); all null while the session is unlinked.
	TaskID         *int64   `json:"taskId"`
	TaskExternalID *string  `json:"taskExternalId"`
	TaskLinkSource *string  `json:"taskLinkSource"` // explicit | heuristic
	TaskConfidence *float64 `json:"taskConfidence"`
	// process liveness (migration 0009): proc_state and pid, null when untracked.
	ProcState *string `json:"procState"`
	ProcPID   *int64  `json:"procPid"`
	// why: a one-line intent summary derived from the first user turn's prose
	// (additive optional — absent until the session has a user turn with text).
	Why *string `json:"why,omitempty"`
}

type turnDTO struct {
	ID               int64    `json:"id"`
	Seq              int64    `json:"seq"`
	Role             string   `json:"role"`
	MessageID        *string  `json:"messageId"`
	Model            *string  `json:"model"`
	StartedAt        string   `json:"startedAt"`
	EndedAt          *string  `json:"endedAt"`
	TokensIn         *int64   `json:"tokensIn"`
	TokensOut        *int64   `json:"tokensOut"`
	TokensCacheRead  *int64   `json:"tokensCacheRead"`
	TokensCacheWrite *int64   `json:"tokensCacheWrite"`
	CostUSD          *float64 `json:"costUsd"`
	Text             *string  `json:"text"`
}

type eventDTO struct {
	ID            int64           `json:"id"`
	TurnID        *int64          `json:"turnId"`
	TS            string          `json:"ts"`
	Type          string          `json:"type"`
	ToolName      *string         `json:"toolName"`
	ParentEventID *int64          `json:"parentEventId"`
	Status        *string         `json:"status"`
	DurationMs    *int64          `json:"durationMs"`
	Payload       json.RawMessage `json:"payload"`
}

type fileChangeDTO struct {
	ID         int64   `json:"id"`
	EventID    int64   `json:"eventId"`
	FilePath   string  `json:"filePath"`
	ChangeType string  `json:"changeType"`
	Additions  *int64  `json:"additions"`
	Deletions  *int64  `json:"deletions"`
	Diff       *string `json:"diff"`
	OutOfScope bool    `json:"outOfScope"`
}

type sessionDetailDTO struct {
	sessionDTO
	Turns       []turnDTO       `json:"turns"`
	Events      []eventDTO      `json:"events"`
	FileChanges []fileChangeDTO `json:"fileChanges"`
	// recovered: count of tool errors in this session that a later same-tool
	// success cleared — the "auto-recovered" count in the detail header.
	// Always present (0 when nothing errored or nothing recovered).
	Recovered int64 `json:"recovered"`
}

// GET /api/projects
func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(`
		SELECT p.id, p.path, p.slug, p.name, p.first_seen, p.last_activity, p.archived,
		       (SELECT COUNT(*) FROM sessions s WHERE s.project_id = p.id) AS sessions
		FROM projects p ORDER BY p.last_activity DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	projects := []projectDTO{}
	for rows.Next() {
		var p projectDTO
		var archived int
		if err := rows.Scan(&p.ID, &p.Path, &p.Slug, &p.Name, &p.FirstSeen,
			&p.LastActivity, &archived, &p.Sessions); err != nil {
			writeErr(w, err)
			return
		}
		p.Archived = archived != 0
		projects = append(projects, p)
	}
	writeJSON(w, projects, rows.Err())
}

// sessionSelect is the shared session projection: entity columns plus the
// per-session token/cost aggregates (parity contract) computed in ONE
// aggregate JOIN — never per-row subqueries (no N+1).
const sessionSelect = `
	SELECT s.id, s.project_id, p.slug, p.name, s.session_uuid, s.model, s.git_branch, s.cwd,
	       s.status, s.started_at, s.ended_at, s.title, s.source,
	       agg.tokens, agg.cost_usd,
	       tl.task_id, tl.external_id, tl.link_source, tl.confidence,
	       s.proc_state, s.pid,
	       why.text
	FROM sessions s
	JOIN projects p ON p.id = s.project_id
	LEFT JOIN (
		-- Session totals are the TRUE cost: every turn including subagents
		-- (phase 2). The Chat tab still shows the orchestrator turns only, but
		-- the card's cost/tokens reflect the whole session — consistent with
		-- the overview/today and analytics aggregates.
		SELECT session_id,
		       SUM(COALESCE(tokens_in, 0) + COALESCE(tokens_out, 0)) AS tokens,
		       SUM(cost_usd) AS cost_usd
		FROM turns GROUP BY session_id
	) agg ON agg.session_id = s.id
	LEFT JOIN (
		-- phase 3.5: one best task link per session, picked in a single
		-- window pass (explicit first, then highest confidence) — no N+1.
		SELECT ts.session_id, ts.task_id, t.external_id, ts.link_source, ts.confidence,
		       ROW_NUMBER() OVER (
		           PARTITION BY ts.session_id
		           ORDER BY (ts.link_source = 'explicit') DESC, ts.confidence DESC
		       ) AS rn
		FROM task_sessions ts
		JOIN tasks t ON t.id = ts.task_id
	) tl ON tl.session_id = s.id AND tl.rn = 1
	LEFT JOIN (
		-- "why": the first user turn's prose per session, in one window pass.
		SELECT session_id, text,
		       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq) AS rn
		FROM turns
		WHERE role = 'user' AND text IS NOT NULL AND TRIM(text) != ''
	) why ON why.session_id = s.id AND why.rn = 1`

// GET /api/sessions?project=<slug|id>&status=<status>
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	query := sessionSelect + ` WHERE 1=1`
	args := []any{}
	if project := r.URL.Query().Get("project"); project != "" {
		query += ` AND (p.slug = ? OR CAST(p.id AS TEXT) = ?)`
		args = append(args, project, project)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` AND s.status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY s.started_at DESC`

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	sessions := []sessionDTO{}
	for rows.Next() {
		var s sessionDTO
		if err := scanSession(rows.Scan, &s); err != nil {
			writeErr(w, err)
			return
		}
		sessions = append(sessions, s)
	}
	writeJSON(w, sessions, rows.Err())
}

// GET /api/sessions/{id} — id is the numeric row id or the session UUID.
func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	idArg := r.PathValue("id")
	where := `s.session_uuid = ?`
	if _, err := strconv.ParseInt(idArg, 10, 64); err == nil {
		where = `s.id = ?`
	}

	var d sessionDetailDTO
	err := scanSession(h.DB.QueryRow(sessionSelect+` WHERE `+where, idArg).Scan, &d.sessionDTO)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	d.Turns = []turnDTO{}
	// Chat/transcript is the ORCHESTRATOR conversation only: subagent turns
	// (agent_name set, no prose — phase 2) exist for aggregate analytics and
	// the session's total cost, but would render as empty rows here. Subagent
	// activity is surfaced via the subagent_start/stop events in the timeline.
	rows, err := h.DB.Query(`
		SELECT id, seq, role, message_id, model, started_at, ended_at,
		       tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, text
		FROM turns WHERE session_id = ? AND agent_name IS NULL ORDER BY seq`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var t turnDTO
		if err := rows.Scan(&t.ID, &t.Seq, &t.Role, &t.MessageID, &t.Model, &t.StartedAt, &t.EndedAt,
			&t.TokensIn, &t.TokensOut, &t.TokensCacheRead, &t.TokensCacheWrite, &t.CostUSD, &t.Text); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		d.Turns = append(d.Turns, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	d.Events = []eventDTO{}
	rows, err = h.DB.Query(`
		SELECT id, turn_id, ts, type, tool_name, parent_event_id, status, duration_ms, payload
		FROM events WHERE session_id = ? ORDER BY ts, id`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var e eventDTO
		var payload sql.NullString
		if err := rows.Scan(&e.ID, &e.TurnID, &e.TS, &e.Type, &e.ToolName,
			&e.ParentEventID, &e.Status, &e.DurationMs, &payload); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		if payload.Valid {
			e.Payload = json.RawMessage(payload.String)
		}
		d.Events = append(d.Events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	d.FileChanges = []fileChangeDTO{}
	rows, err = h.DB.Query(`
		SELECT id, event_id, file_path, change_type, additions, deletions, diff, out_of_scope
		FROM file_changes WHERE session_id = ? ORDER BY id`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var fc fileChangeDTO
		var oos int
		if err := rows.Scan(&fc.ID, &fc.EventID, &fc.FilePath, &fc.ChangeType,
			&fc.Additions, &fc.Deletions, &fc.Diff, &oos); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		fc.OutOfScope = oos != 0
		d.FileChanges = append(d.FileChanges, fc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// recovered: tool errors this session later cleared with a same-tool
	// success (the "auto-recovered" header stat). A best-effort heuristic —
	// each errored tool that has any later ok call on the same tool counts once.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		WHERE e.session_id = ? AND e.status = 'error' AND e.tool_name IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM events e2
		      WHERE e2.session_id = e.session_id AND e2.tool_name = e.tool_name
		        AND e2.status = 'ok' AND (e2.ts > e.ts OR (e2.ts = e.ts AND e2.id > e.id))
		  )`, d.ID).Scan(&d.Recovered); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, d, nil)
}

func scanSession(scan func(...any) error, s *sessionDTO) error {
	var whyRaw sql.NullString
	if err := scan(&s.ID, &s.ProjectID, &s.ProjectSlug, &s.ProjectName, &s.SessionUUID, &s.Model,
		&s.GitBranch, &s.CWD, &s.Status, &s.StartedAt, &s.EndedAt, &s.Title, &s.Source,
		&s.Tokens, &s.CostUSD,
		&s.TaskID, &s.TaskExternalID, &s.TaskLinkSource, &s.TaskConfidence,
		&s.ProcState, &s.ProcPID,
		&whyRaw); err != nil {
		return err
	}
	if whyRaw.Valid {
		if w := summarizeWhy(whyRaw.String); w != "" {
			s.Why = &w
		}
	}
	return nil
}

// summarizeWhy condenses a first user turn into a one-line intent: the first
// non-empty line, inner whitespace collapsed, capped at whyMaxLen with an
// ellipsis. Returns "" when nothing usable remains.
func summarizeWhy(text string) string {
	line := ""
	for _, raw := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(raw); t != "" {
			line = t
			break
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) > whyMaxLen {
		return strings.TrimSpace(string(runes[:whyMaxLen])) + "…"
	}
	return line
}

const whyMaxLen = 160

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("warn: encode response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	log.Printf("error: api: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
