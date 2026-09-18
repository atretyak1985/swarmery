package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// getLatestHandoff serves GET /api/handoffs/latest?cwd=<abs path> — the newest
// handoff brief belonging to the PROJECT that cwd resolves to, so a cold
// session can be handed last session's state without knowing any session id.
//
// cwd is canonicalized with the ingest attribution resolver
// (ingest.CanonicalProjectPath), which is what mints sessions.project_id in the
// first place: a dispatcher worktree and an in-repo subdirectory both resolve
// to their parent project, so a session started in a worktree still finds the
// brief written for the repo.
//
// 204, never 404 or 500, whenever there is simply nothing to inject: no project
// row, no handoff row, or a row whose file has since been deleted or become
// unreadable for any other reason. The caller
// is a SessionStart hook — an error status there is noise an operator cannot
// act on, and the hook must never block a session start.
func (h *Handler) getLatestHandoff(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" || !filepath.IsAbs(cwd) {
		writeClientErr(w, http.StatusBadRequest, "cwd must be an absolute path")
		return
	}
	canon := ingest.CanonicalProjectPath(h.DB, filepath.Clean(cwd))

	var (
		sessionUUID string
		path        string
		tokens      int64
		createdAt   string
	)
	err := h.DB.QueryRow(`
		SELECT s.session_uuid, ho.path, ho.context_tokens, ho.created_at
		FROM handoffs ho
		JOIN sessions s ON s.id = ho.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE p.path = ?
		ORDER BY ho.created_at DESC, ho.id DESC
		LIMIT 1`, canon).Scan(&sessionUUID, &path, &tokens, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	body, err := os.ReadFile(path)
	if err != nil {
		// EVERY read failure is "nothing to inject", not an error: the row
		// outlived its file (pruned handoffs dir), the directory lost its
		// permissions, the path is now a directory. The caller is a SessionStart
		// hook that can only act on 204, and a 500 would echo the absolute brief
		// path back to it — so the cause is logged here instead.
		log.Printf("handoffs: latest brief %q is unreadable: %v", path, err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeJSON(w, map[string]any{
		"session_uuid":   sessionUUID,
		"created_at":     createdAt,
		"context_tokens": tokens,
		"brief":          string(body),
	}, nil)
}
