// ============================================================================
// FROZEN API CONTRACT — swarmery control plane
// ============================================================================
// Single source of truth for all API response shapes, generated from the Go
// DTO structs in internal/api/handlers.go (field names match the JSON tags
// exactly). Frozen at the step-05 quality gate before the parallel wave;
// updated ONCE at step-10 integration with the accepted contract requests
// (Turn.model, event_appended {sessionId, event}) — see web/CONTRACT-REQUESTS.md.
//
// DO NOT EDIT on branch agents' worktrees. Contract change requests go to
// web/CONTRACT-REQUESTS.md and are resolved at integration.
// ============================================================================

// --- Enum-like unions (documented value sets from the DB schema) ------------

/** sessions.status — MVP emits active|idle|completed; waiting_approval|killed reserved for hooks. */
export type SessionStatus = 'active' | 'waiting_approval' | 'idle' | 'completed' | 'killed';

/** sessions.proc_state — null when PID is unknown (remote machine / no hook). */
export type ProcState = 'running' | 'orphaned' | 'dead' | 'unknown';

/** sessions.source */
export type SessionSource = 'jsonl' | 'hook' | 'both';

/** turns.role */
export type TurnRole = 'user' | 'assistant';

/** events.status */
export type EventStatus = 'ok' | 'error' | 'denied' | 'timeout';

/** file_changes.change_type */
export type FileChangeType = 'create' | 'edit' | 'delete' | 'rename';

/** task_sessions.link_source — phase 3.5 workspaces. */
export type TaskLinkSource = 'explicit' | 'heuristic';

/** events.type */
export type EventType =
  | 'tool_call'
  | 'subagent_start'
  | 'subagent_stop'
  | 'skill_use'
  | 'file_change'
  | 'permission_request'
  | 'permission_resolved'
  | 'error'
  | 'test_run'
  | 'commit'
  | 'user_prompt'
  | 'session_end'
  | 'unknown';

// --- Core entities (mirror the Go DTOs, JSON-tag field names) ---------------

/** Go: projectDTO */
/** Swarmery-plugin view of a project, read from its .claude/settings.json. */
export interface PluginState {
  /** enabledPlugins["core@swarmery"] === true. */
  managed: boolean;
  /** Other "<pack>@swarmery" entries enabled alongside core (suffix stripped). */
  packs: string[];
  /** extraKnownMarketplaces.swarmery.source.repo, "" when absent. */
  marketplace: string;
  /** Whether the project path is under a daemon onboarding root (detach-eligible). */
  underOnboardRoot: boolean;
}

export interface Project {
  id: number;
  path: string;
  slug: string;
  name: string | null;
  firstSeen: string;
  lastActivity: string | null;
  archived: boolean;
  sessions: number;
  /** Lifetime token/cost totals across the project's sessions; null when unpriced. */
  tokens: number | null;
  costUsd: number | null;
  /** Dashboard meta (migration 0015): pinned floats the project in lists. */
  pinned: boolean;
  /** Decoded projects.tags JSON array — [] when untagged, never null. */
  tags: string[];
  /** Null for telemetry-only projects (no readable .claude/settings.json). */
  plugin: PluginState | null;
}

/** One project-local registry entry (agent, skill, command or hook). */
export interface ProjectComponent {
  name: string;
  /** "local" today; plugin-provided components ("core@swarmery", …) land later. */
  source: string;
}

export interface ProjectComponentCounts {
  agents: number;
  skills: number;
  commands: number;
  hooks: number;
}

export interface ProjectComponents {
  agents: ProjectComponent[];
  skills: ProjectComponent[];
  commands: ProjectComponent[];
  hooks: ProjectComponent[];
  counts: ProjectComponentCounts;
}

/** Thin session projection shown on the project detail page. */
export interface ProjectRecentSession {
  id: number;
  sessionUuid: string;
  title: string | null;
  status: string;
  startedAt: string;
  model: string | null;
  tokens: number | null;
  costUsd: number | null;
}

export interface ProjectStats {
  sessions: number;
  tokens: number | null;
  costUsd: number | null;
  firstSeen: string;
  lastActivity: string | null;
  recentSessions: ProjectRecentSession[];
}

/** GET /api/projects/{id} — enriched row + local components + headline stats. */
export interface ProjectDetail {
  project: Project;
  components: ProjectComponents;
  stats: ProjectStats;
}

/** POST /api/projects/{id}/detach — the plan (dry run) or the applied result. */
export interface DetachResponse {
  detached: boolean;
  dryRun: boolean;
  /** One human-readable line per removed entry (or a "nothing to detach" note). */
  steps: string[];
  /** Relative backup path, present only on a real write that changed something. */
  backup?: string;
}

/** POST /api/projects/{id}/attach — the plan (dry run) or the applied result. */
export interface AttachResponse {
  attached: boolean;
  dryRun: boolean;
  /** One human-readable line per restored entry (or a "nothing to attach" note). */
  steps: string[];
  /** Relative backup path, present only when a real run rewrote settings.json. */
  backup?: string;
}

/** sessions.outcome (migration 0014) — manual verdict; null = not judged. */
export type SessionOutcome = 'success' | 'fail' | 'abandoned';

/** Go: sessionDTO */
export interface Session {
  id: number;
  projectId: number;
  projectSlug: string;
  /** Clean project display name (projects.name, base of the path); additive — null until healed. */
  projectName?: string | null;
  sessionUuid: string;
  model: string | null;
  gitBranch: string | null;
  cwd: string | null;
  status: SessionStatus;
  startedAt: string;
  endedAt: string | null;
  title: string | null;
  source: SessionSource;
  /** Aggregate SUM(turns.tokens_in + tokens_out) — parity wave; optional until backend lands. */
  tokens?: number | null;
  /** Aggregate SUM(turns.cost_usd) — parity wave; optional until backend lands. */
  costUsd?: number | null;
  /** phase 3.5 workspaces (additive): best task link — explicit beats heuristic. */
  taskId?: number | null;
  /** Card task id (yyyy-mm-dd-slug) of the best-linked workspace task. */
  taskExternalId?: string | null;
  taskLinkSource?: TaskLinkSource | null;
  /** Overlap fraction 0..1 for heuristic links; null for explicit. */
  taskConfidence?: number | null;
  /** Process state from procwatch; null when PID is not tracked. */
  procState?: ProcState | null;
  /** OS PID of the claude process; null when not tracked or remote session. */
  procPid?: number | null;
  /** Manual verdict set from the dashboard (additive optional). */
  outcome?: SessionOutcome | null;
  /**
   * One-line intent summarised from the first user turn's prose (additive
   * optional): absent until the session has a user turn with text.
   */
  why?: string | null;
  /**
   * True while a dashboard-initiated headless resume (`claude -r -p`) is
   * running for this session — the chat composer shows Stop (cancel) instead
   * of Send. In-memory server state, recomputed on each read/WS push.
   */
  resumeInFlight?: boolean;
  /** RFC3339 start time of that resume run — drives a live "Working (Ns)" timer. */
  resumeStartedAt?: string | null;
}

/** Go: turnDTO */
export interface Turn {
  id: number;
  seq: number;
  role: TurnRole;
  messageId: string | null;
  /** Per-message API model; null for user turns (and pre-0002 rows). */
  model: string | null;
  startedAt: string;
  endedAt: string | null;
  tokensIn: number | null;
  tokensOut: number | null;
  tokensCacheRead: number | null;
  tokensCacheWrite: number | null;
  costUsd: number | null;
  /**
   * Turn prose (Chat tab, migration 0005): the user prompt, or the joined
   * assistant `text` content blocks (thinking/tool_use excluded). Never
   * truncated; null for pre-0005 rows until `swarmery backfill --rebuild-text`.
   */
  text: string | null;
}

/** Go: eventDTO — payload is raw JSON (json.RawMessage), decoded client-side. */
export interface Event {
  id: number;
  turnId: number | null;
  ts: string;
  type: EventType;
  toolName: string | null;
  parentEventId: number | null;
  status: EventStatus | null;
  durationMs: number | null;
  payload: unknown;
}

/** Go: fileChangeDTO */
export interface FileChange {
  id: number;
  eventId: number;
  filePath: string;
  changeType: FileChangeType;
  additions: number | null;
  deletions: number | null;
  diff: string | null;
  outOfScope: boolean;
}

/** Go: sessionDetailDTO (embeds sessionDTO) */
export interface SessionDetail extends Session {
  turns: Turn[];
  events: Event[];
  fileChanges: FileChange[];
  /**
   * Count of tool errors this session later cleared with a same-tool success
   * (the "auto-recovered" header stat). Always present; 0 when none.
   */
  recovered: number;
}

// --- Endpoint response shapes ------------------------------------------------

/** GET /api/projects */
export type ProjectsResponse = Project[];

/**
 * GET /api/sessions?project=&status=&limit=&cursor= — keyset-paginated
 * envelope (ops-hygiene wave; additive contract change resolved at
 * integration: the bare array became this envelope, all consumers updated).
 */
export interface SessionsPage {
  sessions: Session[];
  /** Opaque keyset cursor for the next page; null on the last page. */
  nextCursor: string | null;
}
export type SessionsResponse = SessionsPage;

/** GET /api/sessions/{id} — id is the numeric row id or the session UUID. */
export type SessionDetailResponse = SessionDetail;

// --- Future contracts (parallel wave — frozen NOW, implemented later) --------

/** GET /api/stats/today — implemented by Agent C (metrics branch). */
export interface StatsToday {
  sessions: number;
  active: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number | null;
  errors: number;
  /**
   * Test-run aggregates over the window (additive optional): absent when the
   * window has NO test_run events, so the Quality tile degrades instead of
   * showing a misleading zero.
   */
  tests_passed?: number | null;
  tests_failed?: number | null;
  tests_skipped?: number | null;
}

// --- Parity wave (design parity pass — frozen contract) ----------------------

/** GET /api/health */
export interface HealthResponse {
  status: 'ok';
  version: string;
  db_size_bytes: number;
  watching: boolean;
  /**
   * ISO timestamp of the most recent hook call received on POST /api/hooks/*
   * (heartbeat, phase 2 — gate 2.2). Additive optional: absent/null until the
   * hooks backend lands and the first hook checks in.
   */
  hooks_last_seen?: string | null;
}

/** GET /api/docs — list item. */
export interface DocMeta {
  slug: string;
  title: string;
  file: string;
}

/** GET /api/docs/{slug} */
export interface DocDetail extends DocMeta {
  markdown: string;
}

/** One point of the trailing series in StatsOverview (14 days, ascending). */
export interface StatsSeriesPoint {
  day: string;
  sessions: number;
  tokens: number;
  cost_usd: number | null;
  errors: number;
}

/** GET /api/stats/overview?day=YYYY-MM-DD */
export interface StatsOverview {
  day: string;
  sessions: number;
  active: number;
  waiting_approval: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number | null;
  errors: number;
  /** Trailing 14 days ending at `day`, ascending. */
  series: StatsSeriesPoint[];
  errors_by_project: { slug: string; name: string | null; errors: number }[];
  cost_by_model: { model: string; cost_usd: number }[];
  projects: { slug: string; name: string | null; sessions: number }[];
  /** Test-run aggregates over the day (additive optional); absent with no test signal. */
  tests_passed?: number | null;
  tests_failed?: number | null;
  tests_skipped?: number | null;
}

// --- Analytics (GET /api/stats/{timeseries,breakdown,matrix}) ----------------

/** $/tokens come from turns (project|model); runs come from events (agent|skill). */
export type AnalyticsMetric = 'cost' | 'tokens' | 'runs' | 'cache';
export type AnalyticsDimension = 'project' | 'model' | 'agent' | 'skill';

/** Range-total cache economics for metric=cache (analytics uplift). */
export interface CacheSummary {
  /** SUM(cache_read) / (SUM(cache_read) + SUM(tokens_in)) over the range, 0..1. */
  hit_rate: number;
  cache_read_tokens: number;
  input_tokens: number;
  /**
   * Estimated $ saved from REAL per-model cache_read pricing
   * (config/pricing.json); null when no cached model is in the pricing table.
   */
  saved_usd: number | null;
}

/** One toggleable series of the main chart; `values` aligns to `buckets`. */
export interface TimeseriesSeries {
  key: string;
  name: string;
  total: number;
  values: number[];
}

/** GET /api/stats/timeseries?from&to&metric&group */
export interface TimeseriesResp {
  from: string;
  to: string;
  metric: AnalyticsMetric;
  group: AnalyticsDimension;
  /** Daily buckets, ascending, zero-filled across the range. */
  buckets: string[];
  series: TimeseriesSeries[];
  /** Always false in Phase 1 (no per-agent $); reserved for the Phase 2 badge. */
  approx: boolean;
  /** Present only for metric=cache. Series values are 0..1 fractions — do not stack. */
  cache?: CacheSummary;
}

/**
 * One ranked row of GET /api/stats/breakdown?by=…. project|model rows carry
 * cost/tokens; agent|skill rows carry runs/last_used (cost/tokens null in
 * Phase 1). Every consumer must treat the null fields as "not available".
 */
export interface BreakdownRow {
  key: string;
  name: string;
  cost_usd: number | null;
  tokens_in: number | null;
  tokens_out: number | null;
  /** Cache columns (analytics uplift): set on project|model rows, null on agent|skill. */
  tokens_cache_read: number | null;
  cache_hit_rate: number | null;
  runs: number | null;
  sessions: number;
  last_used: string | null;
  /** Agent pivot only: success/(success+fail) over judged sessions; else null. */
  success_rate?: number | null;
}
export type BreakdownResp = BreakdownRow[];

/**
 * GET /api/stats/matrix?rows=agent|skill&cols=project&metric=runs|cost — a
 * cross-tab. metric=runs counts events; metric=cost (rows=agent only, phase 2)
 * sums turn cost. Cells carry `runs` for the runs metric and `cost` for the
 * cost metric.
 */
export interface MatrixResp {
  metric: 'runs' | 'cost';
  rows: { key: string; name: string }[];
  cols: { key: string; name: string }[];
  cells: { row: string; col: string; runs: number; cost?: number | null }[];
}

// --- Analytics uplift (GET /api/stats/{tools,durations,errors}) --------------

/** Per-agent share of one tool's calls; agent keys are normAgentType-folded, "main" = orchestrator. */
export interface ToolAgentSplit {
  agent: string;
  calls: number;
  errors: number;
}

/** One row of GET /api/stats/tools. avg/p95 are null when no call carried a duration. */
export interface ToolStatRow {
  tool: string;
  calls: number;
  errors: number;
  denied: number;
  avg_ms: number | null;
  p95_ms: number | null;
  agents: ToolAgentSplit[];
}

export interface ToolsResp {
  from: string;
  to: string;
  tools: ToolStatRow[];
  /** All attributed agents in range (not narrowed by the agent filter) — the dropdown option set. */
  agents: string[];
  /** True when the range overlaps pruned (rolled-up) days — counts undercount there. */
  approx: boolean;
}

/** One row of GET /api/stats/skills — mirror of ToolStatRow keyed by skill name. */
export interface SkillStatRow {
  skill: string;
  calls: number;
  errors: number;
  denied: number;
  avg_ms: number | null;
  p95_ms: number | null;
  agents: ToolAgentSplit[];
}

export interface SkillsResp {
  from: string;
  to: string;
  skills: SkillStatRow[];
  /** All attributed agents in range (not narrowed by the agent filter) — the dropdown option set. */
  agents: string[];
  /** True when the range overlaps pruned (rolled-up) days — counts undercount there. */
  approx: boolean;
}

/** GET /api/stats/durations — session-length + approval-wait aggregates. */
export interface DurationsResp {
  from: string;
  to: string;
  session_count: number;
  avg_session_sec: number | null;
  median_session_sec: number | null;
  approvals_resolved: number;
  avg_resolve_sec: number | null;
  wait_total_min: number;
}

/** One sample session inside an error group (title mirrors sessions.title, nullable). */
export interface ErrorSample {
  session_id: number;
  title: string | null;
}

/** GET /api/stats/errors — one normalized-message error group. */
export interface ErrorGroup {
  key: string;
  example: string;
  count: number;
  last_ts: string;
  samples: ErrorSample[];
}

export interface ErrorsResp {
  from: string;
  to: string;
  groups: ErrorGroup[];
  /** True when the range overlaps pruned (rolled-up) days — groups undercount there. */
  approx: boolean;
}

// --- Retro loop (GET /api/retro/{agents,friction}) ---------------------------

/** Same aggregates over the preceding window of equal length. */
export interface RetroPrev {
  runs: number;
  errors: number;
  error_rate: number;
  cost_usd: number;
}

/** Latest imported eval run for a registry agent (swarmery evals-import). */
export interface RetroAgentEval {
  passed: number;
  failed: number;
  finished_at: string;
}

/** One per-agent scorecard row of GET /api/retro/agents. */
export interface RetroAgentRow {
  agent: string;
  runs: number;
  sessions: number;
  cost_usd: number;
  tokens_out: number;
  /** Raw error-event count (a single run can carry many). */
  errors: number;
  /**
   * Failed-run share: runs with ≥1 error / runs (clamped to ≤1 — a run
   * spanning the window start can contribute a failed run without
   * contributing to the run count); 0 when no counted run.
   */
  error_rate: number;
  /** avg/p95 over subagent run durations; null when no run carried one. */
  avg_ms: number | null;
  p95_ms: number | null;
  /** success/(success+fail) over judged sessions; null when none judged. */
  success_rate: number | null;
  /** redispatch ledger rows / total ledger rows in range; null without rows. */
  re_dispatch_rate: number | null;
  /** latest eval run for the agent; null when none imported. */
  eval: RetroAgentEval | null;
  prev: RetroPrev;
}

/** The orchestrator ("main" fold key) — excluded from agents[]. */
export interface RetroMain {
  cost_usd: number;
  tokens_out: number;
  errors: number;
}

export interface RetroAgentsResp {
  from: string;
  to: string;
  /** True when the range overlaps pruned (rolled-up) days — counts undercount there. */
  approx: boolean;
  main: RetroMain;
  agents: RetroAgentRow[];
}

/** One denied-tool row of the friction board. */
export interface RetroDeniedTool {
  tool: string;
  denied: number;
  calls: number;
  /** An enabled approval rule (`Tool` or `Tool(argGlob)`) already covers this tool. */
  has_rule: boolean;
}

/** One error group of the friction board; sessions are sample session uuids. */
export interface RetroErrorGroup {
  key: string;
  example: string;
  count: number;
  last_ts: string;
  sessions: string[];
}

export interface RetroApprovals {
  resolved: number;
  avg_resolve_sec: number | null;
  wait_total_min: number;
  pending: number;
}

export interface RetroFrictionResp {
  denied_tools: RetroDeniedTool[];
  error_groups: RetroErrorGroup[];
  approvals: RetroApprovals;
  /** True when the range overlaps pruned (rolled-up) days — the board undercounts there. */
  approx: boolean;
}

/** One lessons-learned entry of GET /api/retro/lessons (09-retrospective.md). */
export interface RetroLesson {
  task_external_id: string;
  task_title: string;
  /** Task card calendar day, YYYY-MM-DD. */
  date: string;
  /** 1-based lesson order within the task's retro doc — stable render key with task_external_id. */
  seq: number;
  title: string;
  action: string | null;
  body: string | null;
}

export interface RetroLessonsResp {
  lessons: RetroLesson[];
}

/** Ledger verdict split of one task (redispatch = re-dispatch/redo/fail/reject + Ukrainian повтор/відхил/провал/фейл). */
export interface RetroTaskVerdicts {
  ok: number;
  redispatch: number;
}

/** One estimation-accuracy row of GET /api/retro/tasks. */
export interface RetroTaskRow {
  external_id: string;
  title: string;
  estimated_hours: number | null;
  actual_hours: number | null;
  variance_pct: number | null;
  loops: number;
  delegations: number;
  verdicts: RetroTaskVerdicts;
}

export interface RetroTasksResp {
  tasks: RetroTaskRow[];
}

// --- Retro phase 3 — advisor recommendations ---------------------------------

/** recommendations.status lifecycle: proposed → accepted|dismissed → adopted → verified. */
export type RecommendationStatus =
  | 'proposed'
  | 'accepted'
  | 'dismissed'
  | 'adopted'
  | 'verified';

/** recommendations.target_kind — what the recommendation is about. */
export type RecommendationTargetKind = 'tool' | 'agent' | 'error_group' | 'process' | 'config';

/** The advisor's metric snapshot written when a recommendation is accepted
 * (internal/advisor baseline JSON) — the verification comparison anchor. */
export interface RecommendationBaseline {
  metric: string;
  value: number;
  per_day: boolean;
  window_days: number;
  window: { from: string; to: string };
  accepted_at?: string;
  /** Stamped when adoption is auto-detected (agent/tool/process kinds). */
  adopted_at?: string;
}

/** One advisor recommendation (deterministic rule engine, R1..R6). */
export interface Recommendation {
  id: number;
  /** 'R1'..'R6'. */
  rule: string;
  target_kind: RecommendationTargetKind;
  target: string;
  title: string;
  /** Human-readable rationale with the numbers baked in. */
  detail: string;
  /** Raw evidence JSON passthrough: {window:{from,to}, counts, session_ids[], …}.
   * After a verify pass it may also carry note ("no measurable improvement
   * yet" / "insufficient post-adoption traffic") and post_adoption {value}. */
  evidence: unknown;
  /** Metric snapshot written on accept; null before that. */
  baseline: RecommendationBaseline | null;
  status: RecommendationStatus;
  created_at: string;
  updated_at: string;
}

export interface RecommendationsResp {
  recommendations: Recommendation[];
}

/** POST /api/retro/advise outcome tally. */
export interface AdviseStats {
  proposed: number;
  updated: number;
  adopted: number;
  verified: number;
}

// --- Phase 2 — approvals + hooks (frozen at gate 2.2) ------------------------

/** permission_requests.status */
export type PermissionRequestStatus =
  | 'pending'
  | 'approved'
  | 'denied'
  | 'expired'
  | 'resolved_elsewhere';

/**
 * One `permission_requests` row — the approval identity minted by the daemon
 * (the PermissionRequest hook stdin carries no tool_use_id; see
 * docs/hooks-protocol.md and docs/hooks-format.md E1/E11).
 */
export interface PermissionRequest {
  id: number;
  sessionId: number;
  toolName: string;
  /** The PermissionRequest hook stdin, verbatim (permission_requests.request_json TEXT). */
  requestJson: string;
  status: PermissionRequestStatus;
  requestedAt: string;
  resolvedAt: string | null;
  /** dashboard | terminal | mobile — free-form string in the frozen contract. */
  resolvedVia: string | null;
  /** Human-entered deny/approve reason; delivered to Claude verbatim on deny. */
  reason: string | null;
  expiresAt: string;
}

/**
 * One auto-approve rule (control-plane v2 — approval_rules row). A matching
 * enabled rule resolves incoming permission requests as approved with
 * resolvedVia 'rule'; the request row stays in History as the audit trail.
 */
export interface ApprovalRule {
  id: number;
  /** null = applies to every project. */
  projectId: number | null;
  projectSlug: string | null;
  /**
   * `Tool` (exact tool, any input) or `Tool(argGlob)` — `*` matches any run
   * of characters in the tool's argument (Bash → command PREFIX, Read/Write/
   * Edit → file_path, WebFetch → url, Glob/Grep → pattern).
   */
  toolPattern: string;
  action: 'approve';
  enabled: boolean;
  note: string | null;
  createdAt: string;
}

/** WS event names — frozen; MVP trio implemented by Agent A, permission_* added at gate 2.2 (phase 2). */
export type WSMessageType =
  | 'session_started'
  | 'session_updated'
  | 'event_appended'
  | 'permission_requested'
  | 'permission_resolved'
  | 'system_item_updated';

/** Messages pushed over /api/ws — see docs/ws-protocol.md. */
export type WSMessage =
  | { type: 'session_started'; payload: Session }
  | { type: 'session_updated'; payload: Session }
  | { type: 'event_appended'; payload: { sessionId: number; event: Event } }
  | { type: 'permission_requested'; payload: PermissionRequest }
  | { type: 'permission_resolved'; payload: PermissionRequest }
  | { type: 'system_item_updated'; payload: SystemItemUpdate };

// --- Phase 4: system registry (Stage 1) — additive contracts ------------------

/**
 * system_item_updated payload kind — which registry table itemId points into
 * (agents / skills / hooks / commands). Mirrors the Kind constants in
 * internal/sysscan.
 */
export type SystemItemKind = 'agent' | 'skill' | 'hook' | 'command';

/**
 * Payload of `system_item_updated` (phase 4 — system registry, frozen at
 * step-03): a cache-invalidation hint that one config item was created,
 * changed content (new version), or was soft-deleted. Carries ids only —
 * clients refetch the item via the /api/system endpoints (step-05). The
 * WS-side emission lands with those endpoints; the bus contract
 * (ingest.NoteSystemItemUpdated + Kind/ItemID) is frozen now.
 */
export interface SystemItemUpdate {
  kind: SystemItemKind;
  itemId: number;
}

// --- Phase 3.5: workspaces (E-lite) — additive task contracts -----------------

/** Derived task state: still working / card says done / moved to archive/. */
export type TaskOutcome = 'active' | 'done' | 'archived';

/** Go: taskSummaryDTO — one row of GET /api/tasks (workspace-ingested cards). */
export interface TaskSummary {
  id: number;
  /** Card task id: yyyy-mm-dd-slug. */
  externalId: string;
  workspaceSlug: string;
  projectSlug: string;
  projectName: string | null;
  title: string;
  status: string;
  outcome: TaskOutcome;
  startedAt: string | null;
  archivedAt: string | null;
  /** COUNT of linked sessions (task_sessions). */
  sessions: number;
  /** Σ cost of linked sessions; null while none is priced. */
  costUsd: number | null;
}

/** Go: taskSessionDTO — one linked session inside GET /api/tasks/{id}. */
export interface TaskSessionLink {
  sessionId: number;
  sessionUuid: string;
  title: string | null;
  startedAt: string;
  endedAt: string | null;
  linkSource: TaskLinkSource;
  confidence: number | null;
  costUsd: number | null;
}

/** Go: taskDetailDTO — GET /api/tasks/{id} (id = row id or externalId). */
export interface TaskDetail extends TaskSummary {
  /** Card **Ціль** line, when the README has one. */
  goal: string | null;
  sessionLinks: TaskSessionLink[];
}

/** GET /api/tasks?days=<n> — recently active workspace tasks (default 14 days). */
export type TasksResponse = TaskSummary[];

// --- Phase 4: system — read-only registry surface (step-05, contract for the
// --- System UI in step-06). Go DTOs live in internal/api/system.go. All
// --- served content (hook commands, frontmatter, bodies, version contents)
// --- is redacted at the response layer: secret-shaped values become "•••".

/** config_lint_findings.severity */
export type LintSeverity = 'info' | 'warn' | 'error';

/** Go: systemSummaryDTO — GET /api/system/summary (deleted=0 counters). */
export interface SystemSummary {
  agents: number;
  skills: number;
  hooks: number;
  commands: number;
  overlays: number;
  /** Active findings (resolved_at IS NULL) split by severity. */
  lint: { error: number; warn: number; info: number };
  /** Go: systemInsightCountsDTO — promotion/drift badge counters. */
  insights: { promotions: number; staleOverrides: number };
}

/** Go: systemItemDTO — one row of GET /api/system/{agents|skills}?scope=&project=. */
export interface SystemItem {
  id: number;
  name: string;
  scope: 'global' | 'project';
  projectSlug: string | null;
  origin: 'local' | 'plugin';
  pluginName: string | null;
  /** Agents only; always null for skills. */
  model: string | null;
  description: string | null;
  /** agents.file_path / skills.dir_path. */
  path: string;
  /** Worst ACTIVE lint finding severity; null = clean. */
  lintMax: LintSeverity | null;
  /** Active agent_dead finding (advisory — sparse events attribution). */
  dead: boolean;
  /** MAX(events.ts) by agent_id/skill_id; null while never referenced. */
  lastUsed: string | null;
  /** COUNT(DISTINCT session_id) over the last 30 days. */
  tasks30d: number;
}

/** Go: systemVersionDTO — one history row inside a detail response. */
export interface SystemVersion {
  id: number;
  createdAt: string;
  changeNote: string | null;
  contentHash: string;
}

/** Go: systemItemDetailDTO — GET /api/system/{agents|skills}/{id} (numeric row id). */
export interface SystemItemDetail extends SystemItem {
  deleted: boolean;
  currentVersionId: number | null;
  /** Raw YAML frontmatter block of the current version (redacted). */
  frontmatter: string;
  /** Markdown body of the current version (redacted). */
  body: string;
  /** Version history, newest first. */
  versions: SystemVersion[];
}

/** Go: systemVersionContentDTO — GET .../{id}/versions/{v}: one full snapshot (redacted). */
export interface SystemVersionContent extends SystemVersion {
  content: string;
}

/** Go: systemDiffDTO — GET .../{id}/diff?from=&to=: backend unified diff ("" = identical). */
export interface SystemDiff {
  from: number;
  to: number;
  diff: string;
}

// --- Phase 4+: insights — promotion & drift detector (GET /api/system/insights).
// --- Go DTOs live in internal/api/system_insights.go. Diffs are computed over
// --- redacted contents server-side; everything here is display-only.

/** Go: insightCopyDTO — one concrete copy of a component inside an insight. */
export interface SystemInsightCopy {
  itemId: number;
  projectSlug: string | null;
  scope: 'global' | 'project';
  path: string;
  /** null when no version is stored yet. */
  contentHash: string | null;
}

/** Go: insightDiffStatDTO — line churn between two copies. */
export interface SystemInsightDiffStat {
  added: number;
  removed: number;
}

/** Go: promotionCandidateDTO — one name, scope=project origin=local, ≥2 projects. */
export interface SystemPromotionCandidate {
  kind: 'agent' | 'skill' | 'command';
  name: string;
  copies: SystemInsightCopy[];
  similarity: 'identical' | 'diverged';
  /** Diverged only; null when contents are unavailable (unreadable command file). */
  diffStat: SystemInsightDiffStat | null;
  /** Redacted unified diff of the two most-diverged copies ("" when identical). */
  diff: string;
  /** Copyable next-step recipe (docs/EXTENDING.md graduation rule). */
  hint: string;
}

/** Go: staleOverrideDTO — a local name colliding with a plugin-shipped item. */
export interface SystemStaleOverride {
  kind: 'agent' | 'skill' | 'command';
  /** Base name — the plugin row is stored as "plugin:name". */
  name: string;
  pluginName: string;
  local: SystemInsightCopy;
  plugin: SystemInsightCopy;
  /** true → pointless override, safe to delete the local copy. */
  identical: boolean;
  diffStat: SystemInsightDiffStat | null;
  diff: string;
  hint: string;
}

/** Go: deadComponentDTO — an active agent_dead lint finding, insight framing. */
export interface SystemDeadComponent {
  /** Always 'agent' today — agent_dead is the only telemetry-dead rule. */
  kind: 'agent';
  id: number;
  name: string;
  scope: 'global' | 'project';
  projectSlug: string | null;
  message: string;
  hint: string;
}

/** Go: systemInsightsDTO — GET /api/system/insights. */
export interface SystemInsights {
  promotionCandidates: SystemPromotionCandidate[];
  staleOverrides: SystemStaleOverride[];
  dead: SystemDeadComponent[];
}

/**
 * Go: agentHistoryDTO — GET /api/system/agents/{id}/history?days=N.
 * Runs are folded across every notation of the agent name (core:x + x) and
 * across all projects; built-in agent types with no registry row are excluded.
 */
export interface AgentHistory {
  agentName: string;
  windowDays: number;
  totals: {
    runs: number;
    sessions: number;
    projects: number;
    okRuns: number;
    errorRuns: number;
    /** 0..1 over runs with a known status. */
    errorRate: number;
  };
  duration: { avgMs: number; p50Ms: number; p95Ms: number; totalMs: number };
  byProject: AgentHistoryProject[];
  /** Ascending by day; one entry per day that had ≥1 run. */
  byDay: { day: string; runs: number }[];
  /** Newest first, capped at 50. */
  recentRuns: AgentHistoryRun[];
}

export interface AgentHistoryProject {
  slug: string;
  name: string;
  runs: number;
  avgMs: number;
  errorRate: number;
  lastUsed: string;
}

export interface AgentHistoryRun {
  ts: string;
  projectSlug: string;
  sessionUuid: string;
  sessionTitle: string;
  description: string;
  status: string;
  durationMs: number;
}

/** Go: systemHookDTO — one row of GET /api/system/hooks?scope=&project=. */
export interface SystemHook {
  id: number;
  scope: 'global' | 'project';
  projectSlug: string | null;
  event: string;
  matcher: string | null;
  /** Redacted: secret-shaped values are masked with "•••". */
  command: string;
  /** Seconds; null when absent in settings JSON. */
  timeout: number | null;
  statusMessage: string | null;
  sourceFile: string;
  seq: number;
  enabled: boolean;
  /** 'swarmery' for installer-owned entries, else null. */
  managed: string | null;
  /**
   * hooks.content_hash — the base_hash every hook write (toggle/edit) must
   * carry (step-10 contract; the hash covers the UNredacted command, so the
   * client cannot compute it). Additive optional: the GET /api/system/hooks
   * handler does not serve it yet (see web/CONTRACT-REQUESTS.md) — while
   * absent, the UI disables hook write actions.
   */
  contentHash?: string;
}

/** Go: systemCommandDTO — one row of GET /api/system/commands?scope=&project=. */
export interface SystemCommand {
  id: number;
  name: string;
  scope: 'global' | 'project';
  projectSlug: string | null;
  origin: 'local' | 'plugin';
  pluginName: string | null;
  description: string | null;
  path: string;
}

// --- Phase 4: system — Stage 2 write surface (steps 09–12). Request/response
// --- shapes of the sysedit-backed write endpoints; Go DTOs live in
// --- internal/api/system_write.go, system_create.go, system_hooks.go. Field
// --- names match the Go JSON tags exactly (snake_case on this surface).

/** Go: sysscan.ContentFinding — ride-along lint of a write (never blocks). */
export interface SystemLintFinding {
  rule: string;
  /** info | warn — never error (that is the 422 parse_error tier). */
  severity: 'info' | 'warn';
  message: string;
}

/** Go: systemWriteRequest — PUT /api/system/{agents|skills}/{id} body. */
export interface SystemWriteRequest {
  /** Raw markdown, frontmatter included. */
  content: string;
  /** sha256 of the content the edit is based on (current version contentHash). */
  base_hash: string;
  change_note?: string;
}

/** Go: systemRollbackRequest — POST .../{id}/rollback body. */
export interface SystemRollbackRequest {
  version_id: number;
  base_hash: string;
}

/** Go: systemWriteResponse — 200 body of both PUT and rollback. */
export interface SystemWriteResponse {
  version_id: number;
  lint: SystemLintFinding[];
}

/**
 * Go: systemConflictDTO — the 409 body of every content write: enough for the
 * UI to show both versions and resolve by an EXPLICIT refetch (no force mode
 * exists in the API, intentionally).
 */
export interface SystemConflict {
  error: string;
  disk_hash: string;
  base_hash: string;
  /** base→disk unified diff, redacted. */
  diff: string;
}

/** Go: systemCreateAgentRequest — POST /api/system/agents body (step-11). */
export interface SystemCreateAgentRequest {
  /** kebab-case: lowercase letters, digits, single dashes. */
  name: string;
  scope: 'global' | 'project';
  /** Required when scope=project; must be absent for global. */
  project_id?: number;
  description: string;
  model?: string;
  tools?: string[];
  boundaries?: string;
}

/** Go: systemCreateResponse — 201 body of POST /api/system/agents. */
export interface SystemCreateResponse {
  id: number;
  version_id: number;
  lint: SystemLintFinding[];
}

/** POST /api/system/agents/{id}/restore — 200 body. */
export interface SystemRestoreResponse {
  id: number;
  version_id: number;
}

/** Go: hookToggleRequest — POST /api/system/hooks/{id}/toggle body. */
export interface SystemHookToggleRequest {
  enabled: boolean;
  /** The hook row contentHash loaded from GET /api/system/hooks. */
  base_hash: string;
}

/** Go: hookUpdateRequest — PUT /api/system/hooks/{id} body. */
export interface SystemHookUpdateRequest {
  command: string;
  /** Seconds; omit to remove the key (full-replace semantics). */
  timeout?: number;
  base_hash: string;
}

/** Go: systemOverlayDTO — one overlays/<dir>/ entry (safe project.json fields only). */
export interface SystemOverlay {
  dir: string;
  path: string;
  /** true when project.json exists but is not valid JSON; parsed fields stay null. */
  parseError: boolean;
  name: string | null;
  displayName: string | null;
  codePath: string | null;
  mainApp: string | null;
  repos: string[];
  enabledPacks: string[];
}

/** Go: systemOverlaysDTO — GET /api/system/overlays (read live from disk). */
export interface SystemOverlays {
  /** overlays/_schema/project.schema.json presence check. */
  schemaPresent: boolean;
  overlays: SystemOverlay[];
}

/** POST /api/projects/onboard body — bootstrap a new consumer project. */
export interface OnboardRequest {
  slug: string;
  path: string;
  packs: string[];
  /** Optional AGENT_WORKSPACE_ROOT override; empty → server default. */
  workspaceRoot?: string;
}

/** Go: onboardResponse — 201 body, one human-readable line per step done. */
export interface OnboardResponse {
  slug: string;
  path: string;
  /** The workspace root actually used (default or override). */
  workspaceRoot: string;
  steps: string[];
}

/** Go: onboardConfigResponse — GET /api/projects/onboard/config (modal defaults). */
export interface OnboardConfig {
  /** false → the endpoint is disabled (no SWARMERY_ONBOARD_ROOTS allow-list). */
  enabled: boolean;
  /** Default AGENT_WORKSPACE_ROOT shown as the workspace-root placeholder. */
  workspaceRoot: string;
  /** Allowed parent directories a project may be onboarded under. */
  roots: string[];
}

// --- global search (GET /api/search) + reverse file lookup ------------------

/** Go: searchSessionDTO — a session matched by title or git branch. */
export interface SearchSession {
  id: number;
  title: string | null;
  gitBranch: string | null;
  status: SessionStatus;
  startedAt: string;
  projectSlug: string;
  projectName: string | null;
}

/** Go: searchTurnDTO — snippet carries ⟦…⟧ highlight markers (never HTML). */
export interface SearchTurn {
  turnId: number;
  sessionId: number;
  sessionTitle: string | null;
  projectSlug: string;
  projectName: string | null;
  startedAt: string;
  role: TurnRole;
  /** Subagent that produced the turn; null = orchestrator. */
  agentName: string | null;
  snippet: string;
}

/** Go: searchFileDTO — a file path matched by substring, with session reach. */
export interface SearchFile {
  path: string;
  sessions: number;
  lastTouched: string;
}

/** Go: searchProjectDTO */
export interface SearchProject {
  id: number;
  slug: string;
  name: string | null;
}

/** Go: searchResponseDTO — GET /api/search grouped results. */
export interface SearchResponse {
  query: string;
  sessions: SearchSession[];
  turns: SearchTurn[];
  files: SearchFile[];
  projects: SearchProject[];
}

/** Go: fileSessionDTO — one session that touched a matching file. */
export interface FileSession {
  sessionId: number;
  title: string | null;
  projectSlug: string;
  status: SessionStatus;
  startedAt: string;
  changes: number;
  lastTouched: string;
}

/** Go: fileSessionsResponseDTO — GET /api/files/sessions. */
export interface FileSessionsResponse {
  path: string;
  sessions: FileSession[];
}

// --- Multi-project UX: global scope + health + pin/tags ----------------------

/** Go: projectHealthDTO — one row of GET /api/projects/health (camelCase). */
export interface ProjectHealth {
  id: number;
  slug: string;
  name: string | null;
  pinned: boolean;
  tags: string[];
  /** Σ turn cost over the rolling last 7 days; null when no priced turn. */
  costWeekUsd: number | null;
  /** Σ turn cost over days 8–14 back; null when no priced turn. */
  costPrevWeekUsd: number | null;
  /** error tool_calls / total tool_calls over 7d; null with no tool calls. */
  errorRate: number | null;
  /** Mean duration (ms) of sessions started in the last 7d that ended; null with none. */
  avgSessionMs: number | null;
  lastActivity: string | null;
}

/** GET /api/projects/health */
export type ProjectsHealthResponse = ProjectHealth[];

/** PATCH /api/projects/{id} body — both optional, at least one required. */
export interface ProjectMetaPatch {
  pinned?: boolean;
  tags?: string[];
}

/** Go: projectMetaDTO — PATCH /api/projects/{id} 200 body (the stored state). */
export interface ProjectMeta {
  pinned: boolean;
  tags: string[];
}

// --- Project plugin toggles ---------------------------------------------------

/** One row of GET /api/projects/{id}/plugins (marketplace catalog × project state). */
export interface ProjectPluginRow {
  name: string;
  description: string;
  enabled: boolean;
  /** core: toggled via attach/detach, never through the plugins endpoint. */
  locked: boolean;
}

export interface ProjectPluginsResponse {
  marketplaceVersion: string;
  /** Mirrors the PUT fence: SWARMERY_ONBOARD_ROOTS set + path inside the allow-list. */
  canWrite: boolean;
  plugins: ProjectPluginRow[];
}

/** PUT /api/projects/{id}/plugins/{name} result. */
export interface ProjectPluginToggleResponse {
  name: string;
  enabled: boolean;
  changed: boolean;
  backup?: string;
}

// --- Tool dashboards (GET /api/tools) -----------------------------------------

/** GET /api/tools — sidebar feed for daemon-managed tool dashboards. */
export interface ToolsSerenaProject {
  id: number;
  slug: string;
  name: string | null;
  state: 'stopped' | 'starting' | 'running' | 'failed';
  dashboardPath: string;
  /**
   * Raw serena dashboard origin (e.g. "http://127.0.0.1:24282/dashboard/index.html").
   * "" unless state === 'running'. The iframe uses this, not dashboardPath:
   * serena's dashboard.js makes root-absolute ajax calls that escape the proxy.
   */
  dashboardUrl: string;
  startedAt: string | null;
  logTail: string[];
  error: string;
}

export interface ToolsGraphifyProject {
  id: number;
  slug: string;
  name: string | null;
  hasViz: boolean;
  hasGraph: boolean;
  builtAt: string | null;
  vizPath: string;
}

export interface ArchitectureProject {
  id: number;
  slug: string;
  name: string | null;
  hasMap: boolean;
  builtAt: string | null;
  mapPath: string;
  /** Commit sha from architecture-map.json at build time; null when absent or unparseable. */
  analyzedAtCommit: string | null;
  /** Current HEAD commit of the project repo resolved without exec; null when unresolvable. */
  headCommit: string | null;
}

export interface ToolsResponse {
  serena: { available: boolean; projects: ToolsSerenaProject[] };
  graphify: { projects: ToolsGraphifyProject[] };
  architecture: { projects: ArchitectureProject[] };
}
