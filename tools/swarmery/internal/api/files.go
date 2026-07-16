package api

import (
	"net/http"
	"strings"
)

// Reverse file lookup: which sessions touched files matching a path
// substring. Backs the command palette's file drill-in (a File search result
// expands into this list in place — no dedicated /files page, by design).

const fileSessionsLimit = 50

type fileSessionDTO struct {
	SessionID   int64   `json:"sessionId"`
	Title       *string `json:"title"`
	ProjectSlug string  `json:"projectSlug"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"startedAt"`
	Changes     int64   `json:"changes"`
	LastTouched string  `json:"lastTouched"`
}

type fileSessionsResponseDTO struct {
	Path     string           `json:"path"`
	Sessions []fileSessionDTO `json:"sessions"`
}

// GET /api/files/sessions?path=<substr>&project=<slug>
func (h *Handler) fileSessions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}
	project := r.URL.Query().Get("project")

	query := `
		SELECT s.id, s.title, p.slug, s.status, s.started_at,
		       COUNT(fc.id) AS changes, MAX(e.ts) AS last_touched
		FROM file_changes fc
		JOIN events e ON e.id = fc.event_id
		JOIN sessions s ON s.id = fc.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE fc.file_path LIKE ? ESCAPE '\' AND s.hidden = 0 AND p.archived = 0`
	args := []any{likePattern(path)}
	if project != "" {
		query += ` AND p.slug = ?`
		args = append(args, project)
	}
	query += `
		GROUP BY s.id
		ORDER BY last_touched DESC
		LIMIT ?`
	args = append(args, fileSessionsLimit)

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	resp := fileSessionsResponseDTO{Path: path, Sessions: []fileSessionDTO{}}
	for rows.Next() {
		var fs fileSessionDTO
		if err := rows.Scan(&fs.SessionID, &fs.Title, &fs.ProjectSlug, &fs.Status,
			&fs.StartedAt, &fs.Changes, &fs.LastTouched); err != nil {
			writeErr(w, err)
			return
		}
		resp.Sessions = append(resp.Sessions, fs)
	}
	writeJSON(w, resp, rows.Err())
}
