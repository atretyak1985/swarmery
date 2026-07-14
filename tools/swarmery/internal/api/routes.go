package api

import "net/http"

// Routes registers every API route on the mux.
//
// Parallel-wave contract: each wave adds routes ONLY inside its own section
// below, so branches never conflict in one spot.
func Routes(mux *http.ServeMux, h *Handler) {
	// ── core: vertical slice (this file's owner) ──
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)

	// wave A: WS
	mux.HandleFunc("GET /api/ws", h.ws)

	// wave C: stats
	mux.HandleFunc("GET /api/stats/today", h.statsToday)

	// parity: docs/stats/health (design-parity wave — dashboard endpoints)
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/docs", h.listDocs)
	mux.HandleFunc("GET /api/docs/{slug}", h.getDoc)
	mux.HandleFunc("GET /api/stats/overview", h.statsOverview)

	// phase 3.5: workspaces
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)

	// phase 2: approvals (frozen contract — docs/hooks-protocol.md).
	// All write endpoints reject foreign browser Origins (D4); requests
	// without an Origin (the swarmery hook shim, curl) pass.
	mux.HandleFunc("POST /api/hooks/permission-request", requireLocalOrigin(h.hookPermissionRequest))
	mux.HandleFunc("POST /api/hooks/stop", requireLocalOrigin(h.hookStop))
	mux.HandleFunc("POST /api/approvals/{id}", requireLocalOrigin(h.resolveApproval))
	mux.HandleFunc("GET /api/approvals", h.listApprovals)

	// process liveness + kill (phase 4 step-07+)
	mux.HandleFunc("POST /api/hooks/session-start", requireLocalOrigin(h.hookSessionStart))
	mux.HandleFunc("POST /api/sessions/{id}/kill", requireLocalOrigin(h.KillSession))

	// phase 4: system — read-only registry surface over the sysscan tables
	// (step-05). GET only; every write flow is Stage 2.
	mux.HandleFunc("GET /api/system/summary", h.systemSummary)
	mux.HandleFunc("GET /api/system/agents", h.listSystemAgents)
	mux.HandleFunc("GET /api/system/agents/{id}", h.getSystemAgent)
	mux.HandleFunc("GET /api/system/agents/{id}/history", h.getSystemAgentHistory)
	mux.HandleFunc("GET /api/system/agents/{id}/versions/{v}", h.getSystemAgentVersion)
	mux.HandleFunc("GET /api/system/agents/{id}/diff", h.diffSystemAgent)
	mux.HandleFunc("GET /api/system/skills", h.listSystemSkills)
	mux.HandleFunc("GET /api/system/skills/{id}", h.getSystemSkill)
	mux.HandleFunc("GET /api/system/skills/{id}/versions/{v}", h.getSystemSkillVersion)
	mux.HandleFunc("GET /api/system/skills/{id}/diff", h.diffSystemSkill)
	mux.HandleFunc("GET /api/system/hooks", h.listSystemHooks)
	mux.HandleFunc("GET /api/system/commands", h.listSystemCommands)
	mux.HandleFunc("GET /api/system/overlays", h.listSystemOverlays)

	// phase 4: system, Stage 2 write surface (step-09) — agents/skills PUT +
	// rollback through internal/sysedit. Same D4 origin hardening as the
	// approvals write endpoints. Deletes are step-11.
	mux.HandleFunc("PUT /api/system/agents/{id}", requireLocalOrigin(h.putSystemAgent))
	mux.HandleFunc("POST /api/system/agents/{id}/rollback", requireLocalOrigin(h.rollbackSystemAgent))
	mux.HandleFunc("PUT /api/system/skills/{id}", requireLocalOrigin(h.putSystemSkill))
	mux.HandleFunc("POST /api/system/skills/{id}/rollback", requireLocalOrigin(h.rollbackSystemSkill))
	// step-10: hooks toggle/edit — the only settings.json write surface.
	mux.HandleFunc("POST /api/system/hooks/{id}/toggle", requireLocalOrigin(h.toggleSystemHook))
	mux.HandleFunc("PUT /api/system/hooks/{id}", requireLocalOrigin(h.updateSystemHook))
	// step-11: agent create (canonical template, O_EXCL through sysedit) +
	// soft delete (file → config-backups, deleted=1) + restore.
	mux.HandleFunc("POST /api/system/agents", requireLocalOrigin(h.createSystemAgent))
	mux.HandleFunc("DELETE /api/system/agents/{id}", requireLocalOrigin(h.deleteSystemAgent))
	mux.HandleFunc("POST /api/system/agents/{id}/restore", requireLocalOrigin(h.restoreSystemAgent))
}
