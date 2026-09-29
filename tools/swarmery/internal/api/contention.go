package api

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// File contention: files that two or more LIVE sessions touched inside a
// recent window — the shared-checkout collision parallel sessions keep
// hitting when they edit one repo. Visibility only: nothing is locked.
//
// Paths are compared after normalisation (a relative file_path is joined with
// the session's cwd), so a worktree — which has its own absolute root — never
// collides with the main checkout. Only a truly shared checkout is flagged.

const (
	contentionDefaultHours = 6
	contentionMinHours     = 1
	contentionMaxHours     = 72
	contentionPathsLimit   = 50
	// contentionTSFormat matches events.ts ("2026-09-29T09:15:53.364Z"), so the
	// cutoff compares lexically against the stored strings.
	contentionTSFormat = "2006-01-02T15:04:05.000Z"
)

type contentionSessionDTO struct {
	SessionID   int64   `json:"sessionId"`
	Title       *string `json:"title"`
	Status      string  `json:"status"`
	ProjectSlug string  `json:"projectSlug"`
	Changes     int64   `json:"changes"`
	LastTouched string  `json:"lastTouched"`
}

type contentionPathDTO struct {
	Path     string                 `json:"path"`
	Sessions []contentionSessionDTO `json:"sessions"`
}

type contentionResponseDTO struct {
	Hours int                 `json:"hours"`
	Paths []contentionPathDTO `json:"paths"`
}

// contentionRow is one (session, raw file_path) aggregate from the query.
type contentionRow struct {
	session contentionSessionDTO
	cwd     sql.NullString
	path    string
}

// GET /api/files/contention?project=<slug|id>&hours=<n>
func (h *Handler) fileContention(w http.ResponseWriter, r *http.Request) {
	hours, ok := parseContentionHours(r.URL.Query().Get("hours"))
	if !ok {
		http.Error(w, `{"error":"invalid hours"}`, http.StatusBadRequest)
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(contentionTSFormat)

	query := `
		SELECT s.id, s.title, s.status, p.slug, s.cwd, fc.file_path,
		       COUNT(fc.id) AS changes, MAX(e.ts) AS last_touched
		FROM file_changes fc
		JOIN events e ON e.id = fc.event_id
		JOIN sessions s ON s.id = fc.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE s.status NOT IN ('completed','killed') AND s.hidden = 0 AND p.archived = 0
		  AND e.ts >= ?`
	args := []any{cutoff}
	if project := r.URL.Query().Get("project"); project != "" {
		query += projectScopePredicate // slug OR id OR name — the shared global-scope match (scope.go)
		args = append(args, scopeArgs(project)...)
	}
	query += `
		GROUP BY s.id, fc.file_path`

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	var raw []contentionRow
	for rows.Next() {
		var cr contentionRow
		if err := rows.Scan(&cr.session.SessionID, &cr.session.Title, &cr.session.Status,
			&cr.session.ProjectSlug, &cr.cwd, &cr.path, &cr.session.Changes,
			&cr.session.LastTouched); err != nil {
			writeErr(w, err)
			return
		}
		raw = append(raw, cr)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, contentionResponseDTO{Hours: hours, Paths: groupContention(raw)}, nil)
}

// parseContentionHours reads ?hours=: empty → the default; any integer is
// clamped to [contentionMinHours, contentionMaxHours]; a non-integer is invalid.
func parseContentionHours(v string) (int, bool) {
	if v == "" {
		return contentionDefaultHours, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return min(max(n, contentionMinHours), contentionMaxHours), true
}

// normaliseContentionPath resolves a file_path to the absolute path it names:
// a relative path is joined with the session's cwd (when known); an absolute
// one is only cleaned.
func normaliseContentionPath(path string, cwd sql.NullString) string {
	if filepath.IsAbs(path) || !cwd.Valid || cwd.String == "" {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd.String, path)
}

// groupContention folds (session, raw path) rows into per-normalised-path
// groups, keeps the ones at least two distinct sessions touched, and orders
// them newest touch first (capped at contentionPathsLimit). Always non-nil.
func groupContention(rows []contentionRow) []contentionPathDTO {
	// path → sessionID → merged aggregate. A session can reach one path under
	// two spellings (relative and absolute): its changes add up and the newest
	// touch wins.
	byPath := map[string]map[int64]contentionSessionDTO{}
	for _, cr := range rows {
		p := normaliseContentionPath(cr.path, cr.cwd)
		sessions, ok := byPath[p]
		if !ok {
			sessions = map[int64]contentionSessionDTO{}
			byPath[p] = sessions
		}
		merged, seen := sessions[cr.session.SessionID]
		if !seen {
			sessions[cr.session.SessionID] = cr.session
			continue
		}
		merged.Changes += cr.session.Changes
		if cr.session.LastTouched > merged.LastTouched {
			merged.LastTouched = cr.session.LastTouched
		}
		sessions[cr.session.SessionID] = merged
	}

	out := []contentionPathDTO{}
	for p, sessions := range byPath {
		if len(sessions) < 2 {
			continue
		}
		group := contentionPathDTO{Path: p, Sessions: make([]contentionSessionDTO, 0, len(sessions))}
		for _, s := range sessions {
			group.Sessions = append(group.Sessions, s)
		}
		sort.Slice(group.Sessions, func(i, j int) bool {
			a, b := group.Sessions[i], group.Sessions[j]
			if a.LastTouched != b.LastTouched {
				return a.LastTouched > b.LastTouched
			}
			return a.SessionID < b.SessionID
		})
		out = append(out, group)
	}
	// Sessions are sorted newest first, so Sessions[0] carries the group's
	// newest touch.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Sessions[0].LastTouched, out[j].Sessions[0].LastTouched
		if a != b {
			return a > b
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > contentionPathsLimit {
		out = out[:contentionPathsLimit]
	}
	return out
}
