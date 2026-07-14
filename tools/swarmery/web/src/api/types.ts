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
export interface Project {
  id: number;
  path: string;
  slug: string;
  name: string | null;
  firstSeen: string;
  lastActivity: string | null;
  archived: boolean;
  sessions: number;
}

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
  /**
   * One-line intent summarised from the first user turn's prose (additive
   * optional): absent until the session has a user turn with text.
   */
  why?: string | null;
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

/** GET /api/sessions?project=<slug|id>&status=<status> */
export type SessionsResponse = Session[];

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
export type AnalyticsMetric = 'cost' | 'tokens' | 'runs';
export type AnalyticsDimension = 'project' | 'model' | 'agent' | 'skill';

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
  runs: number | null;
  sessions: number;
  last_used: string | null;
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
