package api

// memory-engineering phase 1: the stale-fact lint as an API surface.
//
//	GET /api/projects/{id}/memory/lint → memlint.Report
//
// Read-only. The project's auto-memory directory (the same root listMemory
// resolves for kind auto-memory) is linted against the project's own git
// history; a project with no memory directory answers an empty report rather
// than an error, because "nothing remembered" is the healthy state the Memory
// page should render as such. An unknown project id is 404.

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint"
)

// GET /api/projects/{id}/memory/lint
func (h *Handler) lintMemory(w http.ResponseWriter, r *http.Request) {
	var projectPath string
	err := h.DB.QueryRow(
		`SELECT path FROM projects WHERE `+projectMatchExpr(""),
		scopeArgs(r.PathValue("id"))...).Scan(&projectPath)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	clean := filepath.Clean(projectPath)
	rep, err := memlint.Lint(clean, memoryClaudeDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, memlint.Report{
				Dir:      memconsolidate.AutoMemoryDirIn(memoryClaudeDir, clean),
				Project:  clean,
				Findings: []memlint.Finding{},
			}, nil)
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, rep, nil)
}
