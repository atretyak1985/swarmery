package api

import "net/http"

// Routes registers every API route on the mux.
//
// Parallel-wave contract: each wave adds routes ONLY inside its own section
// below, so branches never conflict in one spot.
func Routes(mux *http.ServeMux, h *Handler) {
	// ── core: vertical slice (this file's owner) ──
	// The PreModelSwitch gate's only read: has this model a recorded verdict?
	mux.HandleFunc("GET /api/models/{id}/validation", h.modelValidation)

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
	// repair: `claude plugin install|update <id> --scope project`; {name} is the
	// FULL plugin id (core@swarmery) — "@" is a legal path-segment character.
	// Falls back to a user-scope install when the project's .claude is a
	// symlinked overlay (onboard.SymlinkedClaudeDir → repairViaUserScope,
	// project_plugins.go) — the CLI cannot write project-scope settings through
	// a symlink there.
	mux.HandleFunc("POST /api/projects/{id}/plugins/{name}/repair", requireLocalOrigin(h.repairProjectPlugin))
	// project config: the dashboard writes ONE top-level key of
	// .claude/project.json — the key a pack declared in its requirements.json.
	// Same fence as the plugin toggle: requireLocalOrigin here,
	// SWARMERY_ONBOARD_ROOTS + resolveUnderRoots inside the handler.
	mux.HandleFunc("PUT /api/projects/{id}/config/{key}", requireLocalOrigin(h.putProjectConfig))
	// …and asks a live `claude` session for real candidate values for the
	// fields that pack nominated (project_config_probe.go). Same fence — it
	// spawns a process with the project as cwd — but it writes nothing, and it
	// answers 200 with a reason for every runtime failure rather than a 5xx.
	mux.HandleFunc("POST /api/projects/{id}/config/{key}/probe", requireLocalOrigin(h.probeProjectConfig))
	// canvas v2 parity: project editorial aggregate (rightNow + thisWeek + attention).
	mux.HandleFunc("GET /api/projects/{id}/overview", h.projectOverview)
	// onboarding: bootstrap a new consumer project from the dashboard. Fenced
	// by requireLocalOrigin + an explicit root allow-list (disabled when unset).
	// The GET exposes defaults (workspace root, enabled state) to the modal.
	mux.HandleFunc("GET /api/projects/onboard/config", h.onboardConfig)
	mux.HandleFunc("POST /api/projects/onboard", requireLocalOrigin(h.onboardProject))
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/handoff", h.getSessionHandoff)
	// per-tool context attribution: parses the session transcript on demand and
	// ranks the tools driving context growth (context_hogs.go). 404 when no
	// transcript is on disk.
	mux.HandleFunc("GET /api/sessions/{id}/context-hogs", h.getSessionContextHogs)
	// on-demand LLM task extraction (extract.go): a paid model pass that turns a
	// session into suggested Triage cards. Manual trigger only — nothing here is
	// automatic, unlike ingest's deterministic capture.
	mux.HandleFunc("POST /api/sessions/{id}/extract-tasks", requireLocalOrigin(h.extractSessionTasks))

	// wave A: WS
	mux.HandleFunc("GET /api/ws", h.ws)

	// wave C: stats
	mux.HandleFunc("GET /api/stats/today", h.statsToday)

	// parity: docs/stats/health (design-parity wave — dashboard endpoints)
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/docs", h.listDocs)
	mux.HandleFunc("GET /api/docs/{slug}", h.getDoc)
	mux.HandleFunc("GET /api/stats/overview", h.statsOverview)

	// fusion phase 9 (Console/DX): the in-memory structured log ring
	// (internal/logbuf) for `swarmery console` / `swarmery status`. Read-only,
	// localhost-only; empty snapshot when the ring is not attached.
	mux.HandleFunc("GET /api/logs", h.logs)

	// analytics wave: interactive range analytics (analytics.go).
	mux.HandleFunc("GET /api/stats/timeseries", h.statsTimeseries)
	mux.HandleFunc("GET /api/stats/breakdown", h.statsBreakdown)
	mux.HandleFunc("GET /api/stats/matrix", h.statsMatrix)
	// verification contour v2: per-agent first-pass success rate (analytics.go).
	mux.HandleFunc("GET /api/analytics/first-pass", h.firstPassRates)
	// graft context layer phase 1: exploration share of tool calls (analytics.go).
	mux.HandleFunc("GET /api/analytics/exploration", h.analyticsExploration)
	// trajjudge phase 2: LLM-judge verdicts for a session (analytics.go).
	mux.HandleFunc("GET /api/analytics/trajectory-judgments", h.trajectoryJudgments)

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
	// One consistent snapshot of the whole window + its deterministic digest —
	// the evidence the improver loop reads (retro_report.go). A read, like its
	// per-section neighbours, so no requireLocalOrigin.
	mux.HandleFunc("GET /api/retro/report", h.retroReport)
	// Page-level improver (retro_analysis.go): one saved, human-gated analysis
	// of the whole system per window. The writes go through requireLocalOrigin
	// like every other mutation here; the poll is a plain read.
	mux.HandleFunc("POST /api/retro/analysis", requireLocalOrigin(h.startRetroAnalysis))
	mux.HandleFunc("GET /api/retro/analysis", h.latestRetroAnalysis)
	mux.HandleFunc("PATCH /api/retro/analysis/{id}", requireLocalOrigin(h.patchRetroAnalysis))
	// The handoff into the EXISTING Planning Mode. Gated on status='accepted'.
	mux.HandleFunc("POST /api/retro/analysis/{id}/plan", requireLocalOrigin(h.planFromRetroAnalysis))
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
	// Permanent removal of a queue row (a task that stopped being relevant).
	// Archive keeps it on the board; this drops it. Same D4 origin hardening.
	mux.HandleFunc("DELETE /api/board/tasks/{id}", requireLocalOrigin(h.deleteBoardTask))
	// Inbox amnesty: archive every captured Triage card older than a caller-
	// supplied cutoff in one call (?dryRun counts without writing). The literal
	// segment cannot collide with the {id} routes above — those are PATCH/DELETE
	// and this is POST. Same D4 origin hardening as every other board write.
	mux.HandleFunc("POST /api/board/tasks/bulk-archive", requireLocalOrigin(h.bulkArchiveBoardTasks))

	// board redesign phase 3: the review loop — the evidence and the three exits
	// out of in_review (tasks_diff.go, tasks_review.go). The diff is a read and
	// carries no origin fence, matching every other board GET; the exits all
	// mutate (two of them destroy) and carry the same D4 hardening as the writes
	// above.
	//
	// There is deliberately NO …/verify route here: POST /api/tasks/{id}/verify
	// (below) already IS the manual re-verify trigger, with the exact preflight
	// this surface would have had to duplicate. The review UI calls that one.
	mux.HandleFunc("GET /api/board/tasks/{id}/diff", h.boardTaskDiff)
	mux.HandleFunc("POST /api/board/tasks/{id}/rerun", requireLocalOrigin(h.rerunBoardTask))
	mux.HandleFunc("POST /api/board/tasks/{id}/discard", requireLocalOrigin(h.discardBoardTask))
	mux.HandleFunc("POST /api/board/tasks/{id}/land", requireLocalOrigin(h.landBoardTask))

	// fusion phase 3: dispatcher control — status + pause/resume (global or
	// per-project). The pause write carries the same D4 origin hardening.
	mux.HandleFunc("GET /api/dispatch", h.getDispatch)
	mux.HandleFunc("POST /api/dispatch/pause", requireLocalOrigin(h.pauseDispatch))

	// fusion phase 6: auto-verification — manual re-run of the read-only verifier
	// for a task (the auto trigger fires from the dispatcher's exit path). 202 +
	// async seam; a headless spawn write, so the same D4 origin hardening.
	mux.HandleFunc("POST /api/tasks/{id}/verify", requireLocalOrigin(h.verifyTask))

	// fusion phase 8: planning mode — turn an idea into a plan. POST spawns a
	// headless planner run in the project dir (202 + async seam), GET reads its
	// live status, cancel aborts it. The writes carry the same D4 origin
	// hardening; POST 409s when a run is already active for the project.
	mux.HandleFunc("GET /api/projects/{id}/planning", h.getPlanning)
	mux.HandleFunc("POST /api/projects/{id}/planning", requireLocalOrigin(h.startPlanning))
	mux.HandleFunc("POST /api/projects/{id}/planning/cancel", requireLocalOrigin(h.cancelPlanning))
	// interactive planning v2 (phase 2): the wizard verbs — answer the current
	// question, refine with free-form instructions, or proceed to plan writing.
	// Each resumes the planner session headlessly (startResume) and answers 202.
	mux.HandleFunc("POST /api/projects/{id}/planning/answer", requireLocalOrigin(h.answerPlanning))
	mux.HandleFunc("POST /api/projects/{id}/planning/refine", requireLocalOrigin(h.refinePlanning))
	mux.HandleFunc("POST /api/projects/{id}/planning/proceed", requireLocalOrigin(h.proceedPlanning))

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

	// worktree janitor inventory (worktrees.go): live checkouts + the sweep
	// journal. Read-only on purpose — the janitor owns the decision, this shows
	// what it decided.
	mux.HandleFunc("GET /api/worktrees", h.listWorktrees)
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
	// More specific than the static jail, so POST …/rebuild wins over {rest...}.
	mux.HandleFunc("POST /api/projects/{id}/architecture/rebuild", requireLocalOrigin(h.architectureRebuild))

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

	// fusion phase 11: permission presets — a project's human-readable policy
	// (unrestricted | approval-required | locked-down + per-category overrides)
	// compiled into managed auto-approve rules. GET reads the effective policy;
	// PUT sets it (D4 origin-fenced; escalations gated behind confirm → 428).
	mux.HandleFunc("GET /api/projects/{id}/permission-preset", h.getPermissionPreset)
	mux.HandleFunc("PUT /api/projects/{id}/permission-preset", requireLocalOrigin(h.putPermissionPreset))

	// fusion phase 10: epic rollups + plan-doc editor. A workspace plan IS an
	// epic; GET lists epics (workspace tasks with parsed plan/ phases) + their
	// checkbox rollups; the docs GET/PUT/PATCH read/edit/checkbox-flip the plan
	// markdown, path-confined to that task's plan/ dir. The writes carry the same
	// D4 origin hardening as every other mutating endpoint.
	// plans-page-lifecycle phase 1: lifecycle actions (pause|resume|archive|
	// restore) as workspace file operations + a plan_updated WS publish.
	// NOTE: POST /activate was removed in interactive-planning-v2 phase 4 —
	// Board is exclusively for tasks created on the board; phase 5 adds a direct
	// phase-run mechanism. Route is intentionally absent (→ 404).
	mux.HandleFunc("GET /api/epics", h.listEpics)
	mux.HandleFunc("POST /api/epics/{taskId}/lifecycle", requireLocalOrigin(h.epicLifecycle))
	mux.HandleFunc("GET /api/epics/{taskId}/docs", h.getPlanDoc)
	mux.HandleFunc("PUT /api/epics/{taskId}/docs", requireLocalOrigin(h.putPlanDoc))
	mux.HandleFunc("PATCH /api/epics/{taskId}/docs", requireLocalOrigin(h.patchPlanDoc))
	// interactive planning v2 phase 5: run ONE plan phase headlessly in an
	// isolated worktree straight from its phase doc (no board task). 503 until
	// AttachPhaseRun wires the service.
	mux.HandleFunc("POST /api/epics/{taskId}/phases/{phaseId}/run", requireLocalOrigin(h.runPhase))
	mux.HandleFunc("POST /api/epics/{taskId}/phases/{phaseId}/run/cancel", requireLocalOrigin(h.cancelPhaseRun))
	// Why a run achieved nothing (derived outcome + deterministic blockers), and
	// the escape hatch for the branch-dirty 409 the run endpoint returns.
	mux.HandleFunc("GET /api/epics/{taskId}/phases/{phaseId}/diagnosis", h.phaseDiagnosis)
	mux.HandleFunc("DELETE /api/epics/{taskId}/phases/{phaseId}/branch", requireLocalOrigin(h.deletePhaseRunBranch))
	// The cleanup action behind phasediag's orphan-branch blocker: a swarm/phase-<id>
	// branch whose id matches no phase row — work stranded under a previous id
	// generation, which the phase-scoped route above structurally cannot name. Kept a
	// SIBLING route so that route stays incapable of naming an arbitrary branch.
	mux.HandleFunc("DELETE /api/epics/{taskId}/orphan-branch", requireLocalOrigin(h.deleteOrphanBranch))
	// Plan runs: hand the WHOLE plan to one agent (one headless session driving
	// core's run-plan skill), state on plan_runs. 503 until AttachPlanRun wires
	// the service.
	mux.HandleFunc("POST /api/epics/{taskId}/run", requireLocalOrigin(h.runPlan))
	mux.HandleFunc("POST /api/epics/{taskId}/run/cancel", requireLocalOrigin(h.cancelPlanRun))
	// plan-revision phase 3: staged plan revisions — start a revise wizard,
	// list a task's revision history, render one revision's live diffs, and
	// decide it (apply/reject). Writes carry the same D4 origin hardening;
	// everything serves 503 until AttachPlanning wires the service.
	mux.HandleFunc("POST /api/epics/{taskId}/revisions", requireLocalOrigin(h.startRevision))
	mux.HandleFunc("GET /api/epics/{taskId}/revisions", h.listRevisions)
	mux.HandleFunc("GET /api/revisions/{revisionId}", h.getRevision)
	mux.HandleFunc("POST /api/revisions/{revisionId}/apply", requireLocalOrigin(h.applyRevision))
	mux.HandleFunc("POST /api/revisions/{revisionId}/reject", requireLocalOrigin(h.rejectRevision))
	// The escape hatch for the branch-dirty 409 POST /run returns — the plan-level
	// twin of the phase branch delete above.
	mux.HandleFunc("DELETE /api/epics/{taskId}/branch", requireLocalOrigin(h.deletePlanRunBranch))

	// fusion phase 13: playbooks — selectable execution recipes. GET lists the
	// registry (built-ins overlaid by a project's own files); the duplicate POST
	// copies a built-in's markdown into the project so its prompts become editable
	// (O_EXCL → 409 on repeat). The write carries the same D4 origin hardening.
	mux.HandleFunc("GET /api/playbooks", h.listPlaybooks)
	mux.HandleFunc("POST /api/projects/{id}/playbooks/{name}/duplicate", requireLocalOrigin(h.duplicatePlaybook))

	// fusion phase 12: memory surface — the project's durable memory (CLAUDE.md,
	// Claude Code auto-memory, serena memories) made listable/readable/editable.
	// The PUT carries the same D4 origin hardening as every other mutating
	// endpoint; the traversal fence + versioned backup live in memory.go. GET
	// .../memory/file is more specific than .../memory so it wins the match.
	mux.HandleFunc("GET /api/projects/{id}/memory", h.listMemory)
	mux.HandleFunc("GET /api/projects/{id}/memory/file", h.getMemoryFile)
	mux.HandleFunc("PUT /api/projects/{id}/memory/file", requireLocalOrigin(h.putMemoryFile))

	// fusion phase 14: analytics uplift — Command-Center adoptions our store
	// already backs (stats_uplift.go): autonomy ratio, productivity
	// (LOC/languages/durations/hours-saved-ESTIMATE), the SDLC funnel snapshot,
	// and the per-playbook rollup (degrades to an empty list pre-Phase-13). All
	// read-only, range-scoped by the shared ?project= filter. Plus /api/usage —
	// a provider array: the operator's LIVE Claude subscription windows (read via
	// internal/usage; the earlier "OAuth is out of policy" call is reversed, see
	// that package's doc) plus an optional telemetry-estimate card when
	// SWARMERY_USAGE_LIMITS is set. /api/usage is unscoped (quota is global).
	mux.HandleFunc("GET /api/stats/autonomy", h.statsAutonomy)
	mux.HandleFunc("GET /api/stats/productivity", h.statsProductivity)
	mux.HandleFunc("GET /api/stats/funnel", h.statsFunnel)
	mux.HandleFunc("GET /api/stats/playbooks", h.statsPlaybooks)
	mux.HandleFunc("GET /api/usage", h.usage)

	// …plus the two POSTs that CONNECT an account whose credential the daemon
	// cannot read on its own (usage_login.go) — the macOS non-default case. The
	// browser does the authorization; the daemon holds the PKCE verifier and
	// exchanges the pasted code. Same D4 origin hardening as every other write.
	// The DELETE undoes exactly that: it removes swarmery's OWN store file for
	// the account and never the CLI's credential sources.
	mux.HandleFunc("POST /api/usage/accounts/{account}/login/start", requireLocalOrigin(h.usageLoginStart))
	mux.HandleFunc("POST /api/usage/accounts/{account}/login/complete", requireLocalOrigin(h.usageLoginComplete))
	mux.HandleFunc("DELETE /api/usage/accounts/{account}/login", requireLocalOrigin(h.usageLoginDisconnect))

	// multi-account: the accounts THEMSELVES (accounts.go), as opposed to the
	// swarmery-side quota credential the three routes above manage. Provisioning
	// creates a config dir and nothing else — the login is delegated to the
	// `claude` CLI via the loginCommand the POST returns, because the phase-1
	// spike measured that the CLI owns that credential and deletes any file a
	// second writer leaves behind. Same D4 origin hardening on the two
	// state-changing routes; the list is read-only and unfenced like /api/usage.
	// Per-project binding lives under the projects tree, next to .../plugins.
	mux.HandleFunc("GET /api/accounts", h.listAccounts)
	mux.HandleFunc("POST /api/accounts", requireLocalOrigin(h.createAccount))
	mux.HandleFunc("DELETE /api/accounts/{account}", requireLocalOrigin(h.deleteAccount))
	// The explicit operator re-check: the ONLY route that runs the readiness
	// probe (accounts.go — the list endpoint serves the stored verdict and
	// never spawns a process). Single-flight per account; same D4 origin
	// hardening because it spends a CLI invocation and writes the verdict row.
	mux.HandleFunc("POST /api/accounts/{account}/probe", requireLocalOrigin(h.probeAccountHandler))
	mux.HandleFunc("GET /api/projects/{id}/account", h.projectAccount)
	mux.HandleFunc("PUT /api/projects/{id}/account", requireLocalOrigin(h.putProjectAccount))

	// fusion phase 17: agent hub — agent-centric READ-ONLY aggregation over the
	// registry + retro scorecards + analytics cost + sessions (agent_hub.go).
	// Two GETs, no new tables, no new write paths: the roster and the per-agent
	// profile bundle. Definition editing stays on the existing versioned System
	// write surface (/api/system/agents/{id} + rollback) — the Hub UI calls it.
	mux.HandleFunc("GET /api/agents/hub", h.agentsHub)
	mux.HandleFunc("GET /api/agents/{id}/hub", h.agentHub)

	// fusion phase 15: embedded terminal — a PTY bridged over a WebSocket
	// (internal/term). NOT part of the frozen event bus (/api/ws): a separate
	// PTY socket, no new bus message types. The origin gate is STRICTER than
	// requireLocalOrigin (an absent Origin is rejected — browser-only), and the
	// ?cwd allow-list (registered project path OR live worktree_path) lives in
	// term.go; both run before the upgrade.
	mux.HandleFunc("GET /api/term/ws", h.term)

	// fusion phase 18: system hub — the catalog-wide extension of the agent-hub
	// pattern grouped by ROLE (system_hub.go). Aggregation over the existing
	// registry + events telemetry + config-lint; NO new tables. The per-type
	// {id}/hub profile routes are more specific than the plain GET
	// /api/system/{skills|hooks|commands}/{id} rows above, so the mux prefers
	// them. The ONE new write is template copy-to-project: requireLocalOrigin,
	// O_EXCL → 409, path-traversal fenced in the handler.
	mux.HandleFunc("GET /api/system/hub/summary", h.systemHubSummary)
	mux.HandleFunc("GET /api/system/skills/{id}/hub", h.skillHub)
	mux.HandleFunc("GET /api/system/hooks/{id}/hub", h.hookHub)
	mux.HandleFunc("GET /api/system/commands/{id}/hub", h.commandHub)
	mux.HandleFunc("GET /api/system/templates", h.listSystemTemplates)
	mux.HandleFunc("GET /api/system/templates/{name}", h.getSystemTemplate)
	mux.HandleFunc("POST /api/system/templates/{name}/copy", requireLocalOrigin(h.copySystemTemplate))

	// connectors (mcp servers): see + add/remove the MCP servers Claude Code
	// has configured, read through internal/mcpcfg (which shells to `claude mcp
	// …` with an argv-slice, test-isolated runner). GET is the read feed; the
	// add/remove writes carry the same D4 origin hardening as every other
	// mutating endpoint. {name} is decoded by the mux so space/colon names
	// round-trip.
	mux.HandleFunc("GET /api/connectors", h.connectors)
	mux.HandleFunc("POST /api/connectors", requireLocalOrigin(h.addConnector))
	mux.HandleFunc("DELETE /api/connectors/{name}", requireLocalOrigin(h.removeConnector))
}
