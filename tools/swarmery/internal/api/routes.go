package api

import "net/http"

// Routes registers every API route on the mux.
//
// Parallel-wave contract: each wave adds routes ONLY inside its own section
// below, so branches never conflict in one spot.
func Routes(mux *http.ServeMux, h *Handler) {
	// ── core: vertical slice (this file's owner) ──
	mux.HandleFunc("GET /api/projects", h.listProjects)
	// week-over-week health rows (cost/error-rate/duration) — literal segment,
	// so it wins over the {id} wildcard below.
	mux.HandleFunc("GET /api/projects/health", h.projectsHealth)
	mux.HandleFunc("GET /api/projects/{id}", h.getProject)
	// soft-archive a project from the list (reversible; row + sessions kept).
	mux.HandleFunc("DELETE /api/projects/{id}", requireLocalOrigin(h.hideProject))
	mux.HandleFunc("POST /api/projects/{id}/restore", requireLocalOrigin(h.restoreProject))
	// dashboard meta (migration 0015): pin/unpin + tags — {pinned?, tags?}.
	mux.HandleFunc("PATCH /api/projects/{id}", requireLocalOrigin(h.patchProject))
	// detach the swarmery plugin from a project (.claude/settings.json). Fenced
	// like onboarding: requireLocalOrigin + the SWARMERY_ONBOARD_ROOTS allow-list
	// (disabled when unset). Supports ?dryRun to preview the plan.
	mux.HandleFunc("POST /api/projects/{id}/detach", requireLocalOrigin(h.detachProject))
	// attach: the inverse — re-enable swarmery for a detached project (merge
	// settings, restore project.json from .bak, reinstall hooks). Same fence.
	mux.HandleFunc("POST /api/projects/{id}/attach", requireLocalOrigin(h.attachProject))
	// onboarding: bootstrap a new consumer project from the dashboard. Fenced
	// by requireLocalOrigin + an explicit root allow-list (disabled when unset).
	// The GET exposes defaults (workspace root, enabled state) to the modal.
	mux.HandleFunc("GET /api/projects/onboard/config", h.onboardConfig)
	mux.HandleFunc("POST /api/projects/onboard", requireLocalOrigin(h.onboardProject))
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

	// analytics wave: interactive range analytics (analytics.go).
	mux.HandleFunc("GET /api/stats/timeseries", h.statsTimeseries)
	mux.HandleFunc("GET /api/stats/breakdown", h.statsBreakdown)
	mux.HandleFunc("GET /api/stats/matrix", h.statsMatrix)

	// analytics uplift: tools / skills / durations / errors.
	mux.HandleFunc("GET /api/stats/tools", h.statsTools)
	mux.HandleFunc("GET /api/stats/skills", h.statsSkills)
	mux.HandleFunc("GET /api/stats/durations", h.statsDurations)
	mux.HandleFunc("GET /api/stats/errors", h.statsErrors)

	// retro improvement loop: per-agent scorecards + friction board (retro.go);
	// phase 2 adds the artifact-backed lessons feed + estimation table.
	mux.HandleFunc("GET /api/retro/agents", h.retroAgents)
	mux.HandleFunc("GET /api/retro/friction", h.retroFriction)
	mux.HandleFunc("GET /api/retro/lessons", h.retroLessons)
	mux.HandleFunc("GET /api/retro/tasks", h.retroTasks)

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
	// soft-hide a session from the list (reversible; row + transcript kept).
	mux.HandleFunc("DELETE /api/sessions/{id}", requireLocalOrigin(h.hideSession))
	// partial update (ops-hygiene): today only {outcome} — see session_patch.go.
	mux.HandleFunc("PATCH /api/sessions/{id}", requireLocalOrigin(h.patchSession))

	// session message: resume an idle/completed conversation headlessly
	// (`claude -r <uuid> -p`) — see internal/api/session_message.go. Same D4
	// origin hardening as the other write endpoints; live sessions are rejected.
	mux.HandleFunc("POST /api/sessions/{id}/message", requireLocalOrigin(h.PostSessionMessage))
	mux.HandleFunc("POST /api/sessions/{id}/message/cancel", requireLocalOrigin(h.CancelSessionMessage))

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
	// promotion & drift detector — read-only analysis over the registry
	// (system_insights.go). Display-only: promotion stays a manual flow.
	mux.HandleFunc("GET /api/system/insights", h.systemInsights)

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

	// global search: FTS5 over turns.text (migration 0012) + LIKE groups for
	// sessions/files/projects — powers the Cmd+K command palette.
	mux.HandleFunc("GET /api/search", h.search)
	mux.HandleFunc("GET /api/files/sessions", h.fileSessions)

	// control-plane v2: notifications & auto-approve rules. Writes carry the
	// same D4 origin hardening as every other mutating endpoint; evaluation
	// happens inside approvals.Service.Open, never here.
	mux.HandleFunc("GET /api/approval-rules", h.listApprovalRules)
	mux.HandleFunc("POST /api/approval-rules", requireLocalOrigin(h.createApprovalRule))
	mux.HandleFunc("PATCH /api/approval-rules/{id}", requireLocalOrigin(h.patchApprovalRule))
	mux.HandleFunc("DELETE /api/approval-rules/{id}", requireLocalOrigin(h.deleteApprovalRule))
}
