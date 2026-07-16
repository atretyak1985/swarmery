package api

import (
	"net/http"
	"strconv"
	"strings"
)

// Global search (Cmd+K palette): one endpoint, four independently-queried
// result groups. Turns match through the turns_fts external-content FTS5
// index (migration 0012), ranked by bm25; sessions/files/projects are escaped
// LIKE matches (tiny cardinality — rationale in 0012_fts.sql). Hidden
// sessions and archived projects are excluded everywhere, matching
// listSessions' scoping.

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
)

// Snippet highlight markers — deliberately NOT HTML: the client splits on
// them and renders styled spans (no dangerouslySetInnerHTML).
const (
	snipOpen  = "⟦"
	snipClose = "⟧"
)

type searchSessionDTO struct {
	ID          int64   `json:"id"`
	Title       *string `json:"title"`
	GitBranch   *string `json:"gitBranch"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"startedAt"`
	ProjectSlug string  `json:"projectSlug"`
	ProjectName *string `json:"projectName"`
}

type searchTurnDTO struct {
	TurnID       int64   `json:"turnId"`
	SessionID    int64   `json:"sessionId"`
	SessionTitle *string `json:"sessionTitle"`
	ProjectSlug  string  `json:"projectSlug"`
	StartedAt    string  `json:"startedAt"`
	Role         string  `json:"role"`
	AgentName    *string `json:"agentName"`
	Snippet      string  `json:"snippet"`
}

type searchFileDTO struct {
	Path        string `json:"path"`
	Sessions    int64  `json:"sessions"`
	LastTouched string `json:"lastTouched"`
}

type searchProjectDTO struct {
	ID   int64   `json:"id"`
	Slug string  `json:"slug"`
	Name *string `json:"name"`
}

type searchResponseDTO struct {
	Query    string             `json:"query"`
	Sessions []searchSessionDTO `json:"sessions"`
	Turns    []searchTurnDTO    `json:"turns"`
	Files    []searchFileDTO    `json:"files"`
	Projects []searchProjectDTO `json:"projects"`
}

// ftsQuery converts raw user input into a SAFE FTS5 MATCH expression: every
// whitespace-separated term becomes a quoted phrase (embedded quotes doubled
// per FTS5 string rules), joined with implicit AND; the last term gets a `*`
// prefix suffix so the palette matches mid-word while typing. User input can
// never inject FTS operators (OR/NEAR/column filters) or unbalanced quotes.
func ftsQuery(raw string) string {
	terms := strings.Fields(raw)
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	parts[len(parts)-1] += "*"
	return strings.Join(parts, " ")
}

// likePattern wraps raw input in %…% for a substring LIKE, escaping the LIKE
// metacharacters. Every LIKE below uses ESCAPE '\'.
func likePattern(raw string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(raw)
	return "%" + escaped + "%"
}

// GET /api/search?q=<query>&project=<slug>&limit=<n>
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, `{"error":"missing q"}`, http.StatusBadRequest)
		return
	}
	limit := searchDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			http.Error(w, `{"error":"invalid limit"}`, http.StatusBadRequest)
			return
		}
		limit = min(n, searchMaxLimit)
	}
	project := r.URL.Query().Get("project")

	resp := searchResponseDTO{Query: q}
	var err error
	if resp.Sessions, err = h.searchSessions(q, project, limit); err != nil {
		writeErr(w, err)
		return
	}
	if resp.Turns, err = h.searchTurns(q, project, limit); err != nil {
		writeErr(w, err)
		return
	}
	if resp.Files, err = h.searchFiles(q, project, limit); err != nil {
		writeErr(w, err)
		return
	}
	if resp.Projects, err = h.searchProjects(q, limit); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, resp, nil)
}

func (h *Handler) searchSessions(q, project string, limit int) ([]searchSessionDTO, error) {
	query := `
		SELECT s.id, s.title, s.git_branch, s.status, s.started_at, p.slug, p.name
		FROM sessions s
		JOIN projects p ON p.id = s.project_id
		WHERE s.hidden = 0 AND p.archived = 0
		  AND (s.title LIKE ? ESCAPE '\' OR s.git_branch LIKE ? ESCAPE '\')`
	pat := likePattern(q)
	args := []any{pat, pat}
	if project != "" {
		query += ` AND p.slug = ?`
		args = append(args, project)
	}
	query += ` ORDER BY s.started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []searchSessionDTO{}
	for rows.Next() {
		var s searchSessionDTO
		if err := rows.Scan(&s.ID, &s.Title, &s.GitBranch, &s.Status,
			&s.StartedAt, &s.ProjectSlug, &s.ProjectName); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *Handler) searchTurns(q, project string, limit int) ([]searchTurnDTO, error) {
	query := `
		SELECT t.id, s.id, s.title, p.slug, t.started_at, t.role, t.agent_name,
		       snippet(turns_fts, 0, '` + snipOpen + `', '` + snipClose + `', '…', 12)
		FROM turns_fts
		JOIN turns t ON t.id = turns_fts.rowid
		JOIN sessions s ON s.id = t.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE turns_fts MATCH ? AND s.hidden = 0 AND p.archived = 0`
	args := []any{ftsQuery(q)}
	if project != "" {
		query += ` AND p.slug = ?`
		args = append(args, project)
	}
	query += ` ORDER BY bm25(turns_fts) LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []searchTurnDTO{}
	for rows.Next() {
		var t searchTurnDTO
		if err := rows.Scan(&t.TurnID, &t.SessionID, &t.SessionTitle, &t.ProjectSlug,
			&t.StartedAt, &t.Role, &t.AgentName, &t.Snippet); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (h *Handler) searchFiles(q, project string, limit int) ([]searchFileDTO, error) {
	query := `
		SELECT fc.file_path, COUNT(DISTINCT fc.session_id) AS sessions, MAX(e.ts) AS last_touched
		FROM file_changes fc
		JOIN events e ON e.id = fc.event_id
		JOIN sessions s ON s.id = fc.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE fc.file_path LIKE ? ESCAPE '\' AND s.hidden = 0 AND p.archived = 0`
	args := []any{likePattern(q)}
	if project != "" {
		query += ` AND p.slug = ?`
		args = append(args, project)
	}
	query += `
		GROUP BY fc.file_path
		ORDER BY last_touched DESC
		LIMIT ?`
	args = append(args, limit)

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []searchFileDTO{}
	for rows.Next() {
		var f searchFileDTO
		if err := rows.Scan(&f.Path, &f.Sessions, &f.LastTouched); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// searchProjects ignores the project= scope on purpose: scoping a project
// search to one project is self-contradictory.
func (h *Handler) searchProjects(q string, limit int) ([]searchProjectDTO, error) {
	pat := likePattern(q)
	rows, err := h.DB.Query(`
		SELECT id, slug, name FROM projects
		WHERE archived = 0 AND (slug LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\')
		ORDER BY last_activity DESC
		LIMIT ?`, pat, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []searchProjectDTO{}
	for rows.Next() {
		var p searchProjectDTO
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
