package api

// GET /api/projects/{id}/first-request — how big a session's first request is
// in this project, and roughly what fills it (internal/firstreq).
//
// Two halves, labelled apart on the wire because they are different kinds of
// number: `measured` is real usage read from turns (median over WindowDays),
// `estimate` is the CLAUDE.md / MCP / skills split sized at ~bytes/4 with Other
// as the remainder of the measured whole prompt. Read-only: nothing here edits
// a config or trims anything.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/firstreq"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
)

// firstRequestDTO is the GET /api/projects/{id}/first-request payload
// (mirrored in web/src/api/types.ts as FirstRequest).
type firstRequestDTO struct {
	WindowDays int                `json:"windowDays"`
	Measured   firstreq.Measured  `json:"measured"`
	Estimate   firstreq.Breakdown `json:"estimate"`
	// MCPServers are the servers the project's config files declare — the ones
	// mcp_server_unused_30d judges. Plugin and claude.ai servers are not in any
	// local file; their tools still count in the estimate when sessions used them.
	MCPServers []mcpcfg.ConfiguredServer `json:"mcpServers"`
}

func (h *Handler) projectFirstRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return
	}
	db := h.rdb()
	var path string
	if err := db.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	} else if err != nil {
		writeErr(w, err)
		return
	}

	since := time.Now().Add(-firstreq.WindowDays * 24 * time.Hour)
	measured, err := firstreq.Measure(db, id, since)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The same claude dir the scanner indexes skills from (AttachSystemHubDir).
	claudeDir := systemHubClaudeDir
	servers := mcpcfg.ReadConfigured(claudeDir, path)
	tools, err := firstreq.ObservedMCPTools(db, id, servers, since)
	if err != nil {
		writeErr(w, err)
		return
	}
	skills, err := firstreq.VisibleSkills(db, id, firstreq.EnabledPlugins(claudeDir, path, overlaysFor(path)...))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, firstRequestDTO{
		WindowDays: firstreq.WindowDays,
		Measured:   measured,
		Estimate: firstreq.Estimate(firstreq.Sources{ClaudeDir: claudeDir, ProjectRoot: path},
			tools, skills, measured.MedianPromptTokens),
		MCPServers: servers,
	}, nil)
}
