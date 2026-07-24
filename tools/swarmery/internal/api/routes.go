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
	// plugins: the marketplace catalog × this project's enabledPlugins, plus
	// a fenced per-pack toggle (PUT added in step 03).
	mux.HandleFunc("GET /api/projects/{id}/plugins", h.projectPlugins)
	mux.HandleFunc("PUT /api/projects/{id}/plugins/{name}", requireLocalOrigin(h.putProjectPlugin))
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
	// phase 3: internal/advisor recommendations. The writes carry the same D4
	// origin hardening as every other mutating endpoint.
	mux.HandleFunc("GET /api/retro/recommendations", h.retroRecommendations)
	mux.HandleFunc("PATCH /api/retro/recommendations/{id}", requireLocalOrigin(h.patchRecommendation))
	mux.HandleFunc("POST /api/retro/advise", requireLocalOrigin(h.retroAdvise))
	// self-improvement phase 3: internal/improve agent-rewriter proposals
	// (improve.go). Validation is synchronous, generation async (202).
	mux.HandleFunc("POST /api/retro/recommendations/{id}/improve", requireLocalOrigin(h.improveRecommendation))
	mux.HandleFunc("POST /api/retro/agents/{agent}/improve", requireLocalOrigin(h.improveAgent))
	// read-only preview of the evidence bundle the rewriter would send the model
	// — no origin fence, it mutates nothing.
	mux.HandleFunc("GET /api/retro/agents/{agent}/evidence", h.agentEvidence)
	mux.HandleFunc("GET /api/retro/proposals", h.listProposals)
	mux.HandleFunc("POST /api/retro/proposals/{id}/retry", requireLocalOrigin(h.retryProposal))
	// self-improvement phase 4: human gate + apply/PR pipeline (apply.go).
	// PATCH decides (approved fires Apply async); the manual apply re-runs a
	// stuck-approved proposal after a gh outage. Same D4 origin hardening.
	mux.HandleFunc("PATCH /api/retro/proposals/{id}", requireLocalOrigin(h.patchProposal))
	mux.HandleFunc("POST /api/retro/proposals/{id}/apply", requireLocalOrigin(h.applyProposal))

	// phase 3.5: workspaces
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)

	// fusion phase 1: task board (dispatchable queue — writes are localhost-only).
	mux.HandleFunc("GET /api/board/tasks", h.listBoardTasks)
	mux.HandleFunc("POST /api/board/tasks", requireLocalOrigin(h.createBoardTask))
	mux.HandleFunc("PATCH /api/board/tasks/{id}", requireLocalOrigin(h.patchBoardTask))

	// fusion phase 3: dispatcher control — status + pause/resume (global or
	// per-project). The pause write carries the same D4 origin hardening.
	mux.HandleFunc("GET /api/dispatch", h.getDispatch)
	mux.HandleFunc("POST /api/dispatch/pause", requireLocalOrigin(h.pauseDispatch))

	// fusion phase 6: auto-verification — manual re-run of the read-only verifier
	// for a task (the auto trigger fires from the dispatcher's exit path). 202 +
	// async seam; a headless spawn write, so the same D4 origin hardening.
	mux.HandleFunc("POST /api/tasks/{id}/verify", requireLocalOrigin(h.verifyTask))

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
	// graceful stop — ends the session as 'completed', not 'killed'; also the
	// only way to close a zombie row with no known PID.
	mux.HandleFunc("POST /api/sessions/{id}/stop", requireLocalOrigin(h.StopSession))
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

	// tool dashboards (step 02): sidebar feed + fenced serena process control
	// (tools_dash.go). The POSTs carry the same D4 origin hardening as every
	// other mutating endpoint; the roots fence lives in the handler.
	mux.HandleFunc("GET /api/tools", h.toolsDash)
	mux.HandleFunc("POST /api/projects/{id}/serena/start", requireLocalOrigin(h.serenaStart))
	mux.HandleFunc("POST /api/projects/{id}/serena/stop", requireLocalOrigin(h.serenaStop))
	// tool dashboards (step 03): same-origin embedding surfaces (tools_embed.go)
	// — serena reverse proxy (incl. ws upgrade; start/stop above stay more
	// specific and win) + the graphify/architecture static jails. The jails
	// register method-less so the handler can 405 non-GET/HEAD itself: a
	// "GET …" pattern would let other methods fall through to the "/" SPA
	// catch-all instead.
	mux.HandleFunc("/api/projects/{id}/serena/{rest...}", h.serenaProxy)
	mux.HandleFunc("/api/projects/{id}/graphify/{rest...}", h.graphifyStatic)
	mux.HandleFunc("/api/projects/{id}/architecture/{rest...}", h.architectureStatic)

	// control-plane v2: notifications & auto-approve rules. Writes carry the
	// same D4 origin hardening as every other mutating endpoint; evaluation
	// happens inside approvals.Service.Open, never here.
	mux.HandleFunc("GET /api/approval-rules", h.listApprovalRules)
	mux.HandleFunc("POST /api/approval-rules", requireLocalOrigin(h.createApprovalRule))
	mux.HandleFunc("PATCH /api/approval-rules/{id}", requireLocalOrigin(h.patchApprovalRule))
	mux.HandleFunc("DELETE /api/approval-rules/{id}", requireLocalOrigin(h.deleteApprovalRule))

	// fusion phase 7: routines (scheduled automation). CRUD + manual run +
	// run history carry the same D4 origin hardening as every other mutating
	// endpoint; the WEBHOOK trigger is the sole exception — it is meant for
	// external callers, so it is token-gated (constant-time compare, 404 on any
	// miss) instead of origin-fenced.
	mux.HandleFunc("GET /api/routines", h.listRoutines)
	mux.HandleFunc("POST /api/routines", requireLocalOrigin(h.createRoutine))
	mux.HandleFunc("PATCH /api/routines/{id}", requireLocalOrigin(h.patchRoutine))
	mux.HandleFunc("DELETE /api/routines/{id}", requireLocalOrigin(h.deleteRoutine))
	mux.HandleFunc("POST /api/routines/{id}/run", requireLocalOrigin(h.runRoutine))
	mux.HandleFunc("GET /api/routines/{id}/runs", h.listRoutineRuns)
	mux.HandleFunc("POST /api/hooks/routine/{id}/{token}", h.hookRoutine)
}
