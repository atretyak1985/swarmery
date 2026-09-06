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
  /** The deliberate System project (~/.swarmery, daemon telemetry runs) — demoted out of the main list. */
  isSystem: boolean;
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

/**
 * Go: sessionPlanDTO — the plan run that spawned a session, resolved server-side
 * from the run branch the daemon stamps (`swarm/plan-<taskId>` for the plan-run
 * controller, `swarm/phase-<phaseId>` for one phase of it). Null on ordinary
 * interactive sessions.
 */
export interface SessionPlanGroup {
  /** Workspace task holding the plan — the group key AND the Plans-page target. */
  taskId: number;
  /** Plan title, for the group header. */
  title: string;
  role: 'controller' | 'phase';
  /** epic_phases.id — set only when role === 'phase'. */
  phaseId: number | null;
  /** 1-based order within the plan; drives in-group ordering and the `#N` label. */
  phaseSeq: number | null;
  phaseName: string | null;
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
  /**
   * Claude Code subscription this session ran under (migration 0047), derived
   * at ingest from its transcript root's config dir: '' or 'default' for the
   * stock ~/.claude, otherwise the suffix ('nabu-org' for ~/.claude-nabu-org).
   * Additive optional — absent on responses from an older daemon. Only
   * NON-default values are surfaced (accountLabel), so a single-subscription
   * machine sees no account UI at all.
   */
  account?: string;
  /** Aggregate SUM(turns.tokens_in + tokens_out) — parity wave; optional until backend lands. */
  tokens?: number | null;
  /** Aggregate SUM(turns.cost_usd) — parity wave; optional until backend lands. */
  costUsd?: number | null;
  /**
   * Context occupancy: the last assistant turn's input footprint
   * (tokens_in + cache_read + cache_write) ≈ how full the model's context
   * window is. A large value on a long-lived session is the fat-session cost
   * driver. Additive optional; null until the session has a priced turn.
   */
  contextTokens?: number | null;
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
  /**
   * Latest daemon-generated handoff brief for this fat session (migration
   * 0039), or null when none exists. The card shows a "Handoff" chip; the
   * detail rail fetches the markdown body via GET /api/sessions/{id}/handoff.
   */
  handoff?: {
    path: string;
    createdAt: string;
    contextTokens: number;
  } | null;
  /**
   * The plan run that spawned this session (additive optional): the plan-run
   * controller itself or one of its phases. Null/absent for an ordinary
   * interactive session. Drives the Sessions page's group-by-plan view.
   */
  planGroup?: SessionPlanGroup | null;
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

/** GET /api/sessions/{id}/handoff — the latest daemon-generated brief's body. */
export interface SessionHandoffResponse {
  markdown: string;
  path: string;
  createdAt: string;
}

/** One tool's share of a session's context growth (context-hogs analyzer). */
export interface ContextHogTool {
  name: string;
  calls: number;
  /** Σ len(raw tool-result content JSON)/4 for this tool — an ESTIMATE. */
  estTokens: number;
}

/** One assistant API message's cache-write cost — the growth curve. */
export interface ContextTurnGrowth {
  seq: number;
  cacheWrite: number;
}

/**
 * GET /api/sessions/{id}/context-hogs — per-tool context attribution, parsed
 * on demand from the session transcript. Token figures are estimates
 * (~4 bytes/token from tool-result sizes), not real accounting.
 */
export interface ContextHogsReport {
  /** Sorted by estTokens DESC, top 20. */
  tools: ContextHogTool[];
  /** Growth curve in file/seq order. */
  turns: ContextTurnGrowth[];
  /** Σ all tool-result estimates. */
  totalEst: number;
  /** tool_results whose tool_use id had no matching name. */
  uninspected: number;
  /** lines that failed to parse and were skipped. */
  malformed: number;
}

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
  /**
   * Build identity of the running daemon — the release semver plus the commit
   * it was built from ("0.2.0-15-g41157a8-dirty"). Additive: absent when
   * talking to a daemon built before the build stamp landed, where `version`
   * (frozen bare semver) is the only identity available.
   */
  build?: string;
  db_size_bytes: number;
  watching: boolean;
  /**
   * ISO timestamp of the most recent hook call received on POST /api/hooks/*
   * (heartbeat, phase 2 — gate 2.2). Additive optional: absent/null until the
   * hooks backend lands and the first hook checks in.
   */
  hooks_last_seen?: string | null;
  /**
   * Unresolved plugin_* finding counts — enabled plugins Claude Code cannot
   * actually load. Additive optional: absent when talking to a daemon older
   * than the drift scanner.
   */
  pluginDrift?: { error: number; warn: number };
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

// --- Analytics uplift (fusion phase 14) --------------------------------------
// These mirror the daemon DTOs in internal/api/stats_uplift.go + usage.go, which
// serialize as camelCase — do not snake_case them.

/** GET /api/stats/autonomy — tool-calls per human intervention. */
export interface AutonomyResp {
  from: string;
  to: string;
  toolCalls: number;
  interventions: {
    approvals: number;
    userPrompts: number;
    total: number;
  };
  /** toolCalls / max(1, interventions); when fullyAutonomous, this IS toolCalls. */
  ratio: number;
  /** True when there were zero human interventions in the range. */
  fullyAutonomous: boolean;
}

/** One language bucket for the productivity chart (top 12 by LOC). */
export interface LanguageStat {
  ext: string;
  files: number;
  loc: number;
}

/** Completed-task duration aggregates (nearest-rank percentiles). Null when none. */
export interface TaskDurationsStat {
  completed: number;
  avgSec: number | null;
  medianSec: number | null;
  p90Sec: number | null;
  totalActiveMs: number;
}

/** Human-hours saved — ALWAYS an estimate; the UI must label it so. */
export interface HoursSaved {
  value: number;
  formula: string;
  estimate: boolean;
}

/** GET /api/stats/productivity — LOC / languages / durations / hours-saved. */
export interface ProductivityResp {
  from: string;
  to: string;
  commits: number;
  filesModified: number;
  loc: number;
  languages: LanguageStat[];
  taskDurations: TaskDurationsStat;
  humanHoursSaved: HoursSaved;
}

/** One board column in the SDLC funnel snapshot. */
export interface FunnelColumn {
  column: string;
  /** Current occupancy. */
  count: number;
  /** Reached-in-range for terminal columns; current count for intake columns. */
  entered: number;
}

/** GET /api/stats/funnel — board SDLC funnel. `snapshot` is always true (honesty). */
export interface FunnelResp {
  from: string;
  to: string;
  columns: FunnelColumn[];
  enteredInRange: number;
  doneInRange: number;
  completionRate: number;
  perDay: number;
  snapshot: boolean;
}

/** GET /api/stats/playbooks — per-playbook rollup (empty pre-Phase-13). */
export interface PlaybookRollup {
  playbook: string;
  tasksDone: number;
  inProgress: number;
  costUsd: number | null;
  tokens: number;
}

/**
 * Consumption vs. time elapsed inside a window, in PERCENTAGE POINTS
 * (percentUsed - percentElapsed, ±5-point dead band).
 *
 * Mind the vocabulary — it reads backwards at first pass and is kept verbatim
 * from the reference implementation: `ahead` means burning FASTER than a linear
 * burn of the window (render it as a warning), `behind` means under pace
 * (render it positively).
 */
export interface UsagePace {
  status: 'ahead' | 'on-track' | 'behind';
  percentElapsed: number;
  message: string;
}

/** One quota window: session, weekly, per-model weekly, or a telemetry estimate. */
export interface UsageWindow {
  /** Stable id across refreshes — safe for React keys and per-window prefs. */
  key: string;
  label: string;
  percentUsed: number;
  percentLeft: number;
  /** "resets in 3h 30m" — absent when the window has no known reset instant. */
  resetText?: string;
  resetMs?: number;
  /** RFC3339 instant of the reset. */
  resetAt?: string;
  windowDurationMs?: number;
  /** Absent when the window lacks the timing data to compute a pace. */
  pace?: UsagePace;
  source: 'oauth' | 'estimate';
  /** Estimate provider only — the telemetry card's raw token counts. */
  used?: number;
  limit?: number;
}

/**
 * Operator-facing setup guidance on a provider that is not connected YET — no
 * `claude` login, an expired or rejected token, a missing scope, or an explicit
 * opt-out. Present only on a `no-auth` provider; the card renders it instead of
 * the raw `error` line, because none of these are failures of the dashboard,
 * they are one local step the operator has not taken.
 */
export interface UsageHint {
  kind: 'login' | 'scope' | 'opted-out';
  /** Headline: "Claude login required". */
  title: string;
  /** What exactly is missing, one sentence. */
  detail: string;
  /** The exact command to run, offered with a copy button. */
  command?: string;
  /** Locations the daemon reads the credential from — paths and/or the keychain. */
  sources?: string[];
  /** What supplying it unlocks. */
  why: string;
  /** How the credential is handled once supplied. */
  handling: string;
}

/**
 * One card in the Usage modal. `status` carries every failure mode so a broken
 * provider degrades to one error card instead of breaking the endpoint; `hint`
 * separates "you have not connected this yet" from "this is broken".
 */
export interface UsageProvider {
  /**
   * The account key this card belongs to ("default", "nabu-org", …). Every
   * account contributes a card called "Claude", so card identity across the UI
   * — React keys, hidden-window prefs — is `${account}:${name}`, never `name`.
   */
  account: string;
  name: string;
  status: 'ok' | 'error' | 'no-auth';
  error?: string;
  /** "Max" | "Pro" | "Team" — oauth provider only, omitted when unknown. */
  plan?: string;
  source: 'oauth' | 'estimate';
  windows: UsageWindow[];
  /** Set only on a `no-auth` provider — see UsageHint. */
  hint?: UsageHint;
  /**
   * Present — as `'swarmery'` — when this card's credential came from swarmery's
   * OWN store, i.e. the account was connected from this panel rather than with
   * the `claude` CLI. Absent for a CLI credential and for an account with no
   * credential at all. Provenance only: never a token, never a path.
   *
   * Two decisions read it. Only a card marked here may offer `Disconnect` (the
   * CLI's credentials are not ours to delete), and only a card marked here turns
   * a credential failure into `Reconnect` — for a store swarmery owns, a `claude`
   * login writes to the wrong place and would change nothing.
   */
  connectedVia?: 'swarmery';
}

/**
 * One subscription's row in the usage payload. `error` is NOT how a failed quota
 * read surfaces — that is a per-provider error/hint card — it is the daemon
 * reporting that this account's lookup could not produce cards at all, so one
 * account can never blank the others.
 */
export interface UsageAccount {
  account: string;
  providers: UsageProvider[];
  error?: string;
}

/**
 * GET /api/usage — the operator's LIVE Claude subscription windows (read from
 * their own local credential via internal/usage) plus an optional
 * telemetry-estimate card, present only when SWARMERY_USAGE_LIMITS is set.
 * The estimate card's presence replaces the old top-level `configured` flag.
 *
 * `accounts` is the payload: one row per subscription the daemon can see (on a
 * stock config that is exactly one, and the UI is identical to single-account).
 * `providers` is a deliberate ALIAS of the DEFAULT account's row, so the header
 * chip — which speaks for one account — needs no scan on every render.
 */
export interface UsageResp {
  generatedAt: string;
  providers: UsageProvider[];
  accounts: UsageAccount[];
}

/**
 * POST /api/usage/accounts/{account}/login/start — begin connecting an account
 * whose credential the daemon cannot read on its own (the usual state of a
 * non-default account on macOS, where the CLI keeps its credential in the login
 * Keychain under an undocumented per-config-dir name).
 *
 * The response carries ONLY the URL to open. The PKCE verifier and the CSRF
 * state stay in the daemon; the browser never sees them, and the completing
 * request sends back just the code the callback page displays.
 */
export interface UsageLoginStart {
  authorizeUrl: string;
}

/**
 * Go: completeResponse (internal/api/usage_login.go) — what one Connect click
 * produced, step by step. Complete is a three-step transaction: authorize
 * (connected), credential handoff into the account's config dir (handoff), and
 * the authoritative CLI-readiness probe (runnable). A failed handoff or an
 * unready probe does NOT fail the request — the quota connection genuinely
 * succeeded; nextStep tells the UI to offer the interactive PTY login instead.
 */
export interface UsageLoginComplete {
  /** The quota half: the credential is in swarmery's store. */
  connected: boolean;
  handoff: 'written' | 'already-present' | 'skipped-default' | 'failed';
  /** The probe verdict — the authoritative "can the CLI run" answer. */
  runnable: 'ready' | 'no-login' | 'unknown';
  /** Short fixed phrase when runnable is not 'ready' — never raw CLI output. */
  reason?: string;
  /** Absent when ready; 'pty-login' when the UI should offer the terminal. */
  nextStep?: 'pty-login';
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
   * Behavior-failed-run share: runs with ≥1 behavior-fixable error / runs
   * (clamped to ≤1 — a run spanning the window start can contribute a failed
   * run without contributing to the run count); 0 when no counted run.
   * Infra noise and harness mechanics are excluded — the same grain the
   * advisor's R2 (behavior_failed_run_share) fires on.
   */
  error_rate: number;
  /** Raw error events per class: behavior_fixable / harness_recoverable / infra_noise. */
  errors_by_class?: Record<string, number>;
  /** avg/p95 over subagent run durations; null when no run carried one. */
  avg_ms: number | null;
  p95_ms: number | null;
  /** success/(success+fail) over judged sessions; null when none judged. */
  success_rate: number | null;
  /** redispatch ledger rows / total ledger rows in range; null without rows. */
  re_dispatch_rate: number | null;
  /** latest eval run for the agent; null when none imported. */
  eval: RetroAgentEval | null;
  /**
   * True when the agent resolves to a live registry row with an editable
   * definition file — the agents the rewriter can act on. Built-in agents
   * (Explore, general-purpose, debugger) are false, so the UI hides their
   * "Improve" button. Optional for forward-compat with older daemons.
   */
  improvable?: boolean;
  prev: RetroPrev;
}

/**
 * GET /api/retro/agents/{agent}/evidence — read-only preview of the evidence
 * bundle the rewriter would feed the model. A built-in agent answers
 * in_registry:false with no bundle.
 */
export interface AgentEvidence {
  agent: string;
  in_registry: boolean;
  agent_path?: string;
  base_sha256?: string;
  bundle?: string;
}

/** The orchestrator ("main" fold key) — excluded from agents[]. */
export interface RetroMain {
  cost_usd: number;
  tokens_out: number;
  errors: number;
  /** Raw error events per class: behavior_fixable / harness_recoverable / infra_noise. */
  errors_by_class?: Record<string, number>;
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

/** Ledger verdict split of one task (redispatch = re-dispatch/redo/fail/reject, matched in English or the legacy Ukrainian ledger vocabulary: повтор/відхил/провал/фейл). */
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

/** recommendations.status lifecycle: proposed → accepted|dismissed → adopted → verified,
 *  plus `resolved` — the advisor closed the row itself because its condition stopped
 *  reproducing (migration 0063). Terminal like `verified` and `dismissed`, and never in
 *  the actionable rail: a row nobody has to act on must not keep asking to be acted on. */
export type RecommendationStatus =
  | 'proposed'
  | 'accepted'
  | 'dismissed'
  | 'adopted'
  | 'verified'
  | 'resolved';

/** recommendations.target_kind — what the recommendation is about. */
export type RecommendationTargetKind = 'tool' | 'agent' | 'error_group' | 'process' | 'config' | 'project';

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

/** One advisor recommendation (deterministic rule engine, R1..R7). */
export interface Recommendation {
  id: number;
  /** 'R1'..'R7'. */
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

// --- Retro report + digest (GET /api/retro/report) ---------------------------

/**
 * The whole /retro window in one consistent snapshot. The five sections are
 * the SAME payloads the per-section endpoints serve — the report exists so the
 * improver loop reads one moment in time instead of five.
 */
export interface RetroReport {
  from: string;
  to: string;
  /** Project scope as requested; '' = whole fleet. */
  scope: string;
  approx: boolean;
  agents: RetroAgentsResp;
  friction: RetroFrictionResp;
  lessons: RetroLessonsResp;
  tasks: RetroTasksResp;
  recommendations: RecommendationsResp;
  /**
   * True when at least one section's query failed. Those sections come back
   * EMPTY, not zero — `partialSections` names them so the UI can say so.
   */
  partial: boolean;
  partialSections: string[];
}

/** Lifecycle of a saved system analysis (migration 0061). */
export type RetroAnalysisStatus =
  | 'running'
  | 'proposed'
  | 'accepted'
  | 'dismissed'
  | 'planned'
  | 'failed';

/**
 * One saved analysis of a retro window, written by the system-improver agent
 * and gated by the operator. Nothing downstream — no workspace write, no
 * planning session — happens before `accepted`.
 */
export interface RetroAnalysis {
  id: number;
  windowFrom: string;
  windowTo: string;
  /** Project slug; '' = the whole fleet. */
  scope: string;
  /** sha256 of the digest this analysis was written from. */
  digestSha256: string;
  markdown: string;
  /** Distinct [E:kind:id] citations the analysis carries. */
  citations: number;
  status: RetroAnalysisStatus;
  /** Verbatim failure reason; '' unless status is 'failed'. */
  error: string;
  /** Set when status is 'planned'. */
  planningSessionUuid: string;
  createdAt: string;
  decidedAt: string | null;
}

export interface RetroAnalysisResp {
  analysis: RetroAnalysis | null;
}

/** 202 body of POST /api/retro/analysis/{id}/plan. */
export interface RetroPlanStarted {
  sessionUuid: string;
  /** DB path slug, for the /p/<slug>/planning deep link. */
  projectSlug: string;
  analysis: RetroAnalysis | null;
}

/**
 * 409 body when the target project already has a planning run in flight. It
 * carries the ACTIVE session so the UI can link there instead of showing a
 * bare status code.
 */
export interface RetroPlanConflict {
  error: string;
  sessionUuid: string;
  projectSlug: string;
}

export interface RetroReportResp {
  report: RetroReport;
  /**
   * Deterministic markdown rendering of the report: the evidence text the
   * system-improver agent reads, every line ending in `[E:kind:id]` citation
   * markers. The same report always renders byte-identically.
   */
  digest: string;
  /** True when sections were dropped to fit the digest budget. */
  digestTruncated: boolean;
  /** sha256 of `digest` — what an analysis row is pinned to. */
  digestSha256: string;
}

/** POST /api/retro/advise outcome tally. */
export interface AdviseStats {
  proposed: number;
  updated: number;
  adopted: number;
  verified: number;
}

// --- fusion phase 12 — project memory surface --------------------------------

/** memory file kind: which of a project's three memory roots it comes from. */
export type MemoryKind = 'claude-md' | 'auto-memory' | 'serena';

/** One listed memory file (GET /api/projects/{id}/memory). */
export interface MemoryFile {
  kind: MemoryKind;
  /** Absolute on-disk path — the opaque ?path= handle for read/write. */
  path: string;
  /** Display basename. */
  name: string;
  sizeBytes: number;
  /** RFC3339 file mtime. */
  updatedAt: string;
  /** False in readonly mode — the editor shows a read-only badge. */
  writable: boolean;
}

export interface MemoryListResp {
  files: MemoryFile[];
}

/** GET /api/projects/{id}/memory/file?path= — one file's content + hash. */
export interface MemoryFileContent {
  path: string;
  kind: MemoryKind;
  content: string;
  /** sha256 of content — the base_hash handle for the next versioned PUT. */
  hash: string;
  writable: boolean;
}

/** 409 body of a PUT whose base_hash no longer matches disk. */
export interface MemoryConflict {
  error: string;
  disk_hash: string;
  base_hash: string;
}

// --- self-improvement phase 4 — agent change proposals -----------------------

/** agent_change_proposals.status lifecycle (migration 0021). */
export type ProposalStatus = 'proposed' | 'approved' | 'applied' | 'rejected' | 'failed';

/**
 * One agent-rewrite proposal (internal/improve): a unified diff against an
 * agent definition file, generated from advisor evidence and gated behind a
 * human Approve → apply/PR pipeline.
 */
export interface AgentChangeProposal {
  id: number;
  /** Links back to the accepted recommendation, or null for the ad-hoc trigger. */
  recommendation_id: number | null;
  /** Registry key (normalized). */
  agent: string;
  /** Absolute source path at generation time. */
  agent_path: string;
  /** sha256 of the agent content the diff was generated against. */
  base_sha256: string;
  /** Unified diff. */
  diff: string;
  /** Model's per-hunk rationale. */
  rationale: string;
  status: ProposalStatus;
  /** Populated when status is 'failed' or a recoverable apply error occurred. */
  error: string | null;
  /** Populated once the PR is opened (status 'applied'). */
  pr_url: string | null;
  created_at: string;
  decided_at: string | null;
}

export interface ProposalsResp {
  proposals: AgentChangeProposal[];
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
  /**
   * 'manual' (hand-written) or 'preset' (compiled from a project's permission
   * preset, fusion phase 11). Managed ('preset') rules are read-only in the
   * manual rules UI — the preset owns their lifecycle.
   */
  source: 'manual' | 'preset';
}

/* ----- permission presets (fusion phase 11 — DESIGN.md §2 item 11) ----- */

/** The three presets, over the low-level approval_rules. */
export type PermissionPreset = 'unrestricted' | 'approval-required' | 'locked-down';

/** Per-category override policy — two states only ("block" is out of scope). */
export type CategoryPolicy = 'allow' | 'ask';

/** One category's resolved policy under the current preset + overrides. */
export interface PermissionCategory {
  category: string;
  patterns: string[];
  policy: CategoryPolicy;
}

/** GET /api/projects/{id}/permission-preset — the effective policy view. */
export interface PermissionPresetView {
  projectId: number;
  preset: PermissionPreset;
  overrides: Record<string, CategoryPolicy>;
  /** true iff preset === 'locked-down' — the dispatcher refuses this project's tasks. */
  lockedDown: boolean;
  categories: PermissionCategory[];
}

/** PUT body for setting a project's permission preset. */
export interface PermissionPresetInput {
  preset: PermissionPreset;
  overrides?: Record<string, CategoryPolicy>;
  /** Required (→ true) when the change escalates privileges (R13); else 428. */
  confirm?: boolean;
}

/** The 428 payload when a privileged change needs explicit confirmation. */
export interface PermissionEscalation {
  error: string;
  reason: string;
  escalations: string[];
}

/** WS event names — frozen; MVP trio implemented by Agent A, permission_* added at gate 2.2 (phase 2). */
export type WSMessageType =
  | 'session_started'
  | 'session_updated'
  | 'event_appended'
  | 'permission_requested'
  | 'permission_resolved'
  | 'system_item_updated'
  | 'task_updated'
  | 'plan_updated'
  | 'task_deleted';

/** Messages pushed over /api/ws — see docs/ws-protocol.md. */
export type WSMessage =
  | { type: 'session_started'; payload: Session }
  | { type: 'session_updated'; payload: Session }
  | { type: 'event_appended'; payload: { sessionId: number; event: Event } }
  | { type: 'permission_requested'; payload: PermissionRequest }
  | { type: 'permission_resolved'; payload: PermissionRequest }
  | { type: 'system_item_updated'; payload: SystemItemUpdate }
  | { type: 'task_updated'; payload: BoardTask }
  | { type: 'plan_updated'; payload: { taskId: number; projectId: number } }
  /** A board row was permanently deleted — ids only, the row cannot be hydrated. */
  | { type: 'task_deleted'; payload: { taskId: number; projectId: number } };

// --- Fusion phase 1: task board — additive contracts --------------------------

/** Closed set of kanban columns (Fusion builtin:coding semantics). */
export type BoardColumn =
  | 'triage'
  | 'todo'
  | 'in_progress'
  | 'in_review'
  | 'done'
  | 'archived';

/** Accepted task priority tokens (mapped to the INTEGER priority column server-side). */
export type TaskPriority = 'urgent' | 'high' | 'normal' | 'low';

/**
 * Where a board card came from. 'manual' is a hand-written card (the default
 * every pre-capture row carries); 'session' and 'llm' are minted by capture and
 * are never creatable over HTTP — see insertCapturedTask in tasks_board.go.
 * 'verify-fix' is minted by the verifier when a graded card FAILS (createFixTask
 * in internal/verify/service.go). It was missing from this union, so consumers
 * were written against an incomplete set — and the one that indexes a Record by
 * origin crashed the card renderer on the first fix card that reached the board.
 */
export type TaskOrigin = 'manual' | 'session' | 'llm' | 'verify-fix';

/**
 * Where a captured card came from — Go: boardTaskSourceDTO (migration 0066).
 *
 * The whole object is null on `BoardTask.source` for a hand-written card AND for
 * a verifier fix task, so "was this captured?" is one null check instead of a
 * compare against `origin` — a fix card has a non-manual origin and no capture
 * provenance at all, which is precisely the case an origin test gets wrong.
 */
export interface BoardTaskSource {
  /**
   * The session the card was captured FROM. Not the session it runs in —
   * `tasks.session_id` is that one, and confusing the two is the documented trap
   * (plan README, board redesign v2).
   */
  sessionId: number | null;
  /** uuid of the transcript record the card was minted from; null on older rows. */
  turnUuid: string | null;
  /**
   * The session's opening prompt, clipped at capture (400 runes for new
   * captures; rows backfilled by 0066 keep the full original excerpt).
   */
  quote: string | null;
  /**
   * Files that session had touched by capture time (≤20). Never null inside a
   * non-null source — the DTO writes `[]`.
   */
  files: string[];
}

/**
 * A dispatchable board task — response of POST/PATCH /api/board/tasks, item of
 * GET /api/board/tasks, and the `task_updated` WS payload. Mirrors
 * boardTaskDTO in internal/api/tasks_board.go. Dispatcher-owned fields
 * (branch, worktreePath, startPoint, dispatchError, retryCount,
 * verifyRetryCount, verifyVerdict, verifyDetail) are read-only from the client.
 */
export interface BoardTask {
  id: number;
  externalId: string;
  projectId: number;
  projectSlug: string | null;
  title: string;
  prompt: string;
  priority: TaskPriority;
  status: string;
  boardColumn: BoardColumn;
  paused: boolean;
  userPaused: boolean;
  dependencies: string[];
  model: string | null;
  /** Selected execution recipe name (fusion phase 13); null = default 'standard'. */
  playbook: string | null;
  fileScope: string[];
  /** Free-form card marks (0049). Lowercase slugs; empty array, never null. */
  labels: string[];
  branch: string | null;
  worktreePath: string | null;
  /**
   * SHA the worktree was pinned to at admission (0051) — the base verification
   * diffs against. Null on a card that was never admitted, and on rows
   * dispatched before the column existed.
   */
  startPoint: string | null;
  dispatchError: string | null;
  /** Dispatcher's no-progress heal budget. Distinct from `verifyRetryCount`. */
  retryCount: number;
  /** Verification's fix-chain budget (0051). Distinct from `retryCount`. */
  verifyRetryCount: number;
  verifyVerdict: string | null;
  verifyDetail: string | null;
  /**
   * One-line outcome of the card: the sentinel line the dispatcher recorded on a
   * no-op exit, or the URL of the PR `land` opened (board redesign phase 3). It
   * was always on the row; the board only started showing it with the review loop.
   */
  resultNote: string | null;
  /** Registry agent name this card dispatches as; null = a plain run. */
  agent: string | null;
  /** Where the card came from: hand-written, captured from a session, or LLM-suggested. */
  origin: TaskOrigin;
  /** Session a captured card was minted from; null for manual cards. */
  originSessionId: number | null;
  /**
   * Capture provenance as one nullable object (0066) — the turn, the quote and
   * the files behind `origin`. Null for a manual card and for a verifier fix
   * task. `originSessionId` above is the same id, kept for the callers that
   * predate this field.
   */
  source: BoardTaskSource | null;
  /**
   * When the inbox sweeper will retire this card (RFC3339), or null when the
   * sweep can never touch it — taskcap.StaleAfter under the daemon's
   * SWARMERY_INBOX_TTL. Derived per request, never stored.
   *
   * NULL MEANS "NEVER EXPIRES", not "expired long ago": the sweeper only touches
   * non-manual cards sitting in triage with no worktree, so most cards on the
   * board carry null here. Read it with `isStale`/`staleLabel` in boardModel —
   * `new Date(task.staleAfter) < now` on a null yields 1970 and marks half the
   * board archivable.
   */
  staleAfter: string | null;
  /**
   * The exact first-stage prompt the dispatcher handed the runner — the audit of
   * what actually went to the agent. Null until the card has been dispatched.
   */
  dispatchedPrompt: string | null;
  /**
   * External id of the WORKSPACE task this card's micro-plan became — the Plans
   * page entry for the same unit of work. A dispatched card materializes a
   * single-phase plan so its outcome is evidence in a doc (ticked checkboxes, a
   * Completion Report) instead of the column someone dragged it to. Null when the
   * card has no micro-plan, or before the scanner has indexed the dir.
   */
  planExternalId: string | null;
  /**
   * Derived server-side on every request, never stored: whether a task that
   * claims to be running actually is, and the evidence behind that verdict.
   * OMITTED (not null) when there is no hint to give — absence is the signal —
   * which is why these two are optional rather than nullable.
   */
  staleness?: string;
  stalenessReason?: string;
  columnMovedAt: string | null;
  createdAt: string;
}

// --- board redesign phase 3: the review loop ---------------------------------

/** One commit on a card's run branch (item of `TaskDiff.commits`). */
export interface TaskDiffCommit {
  sha: string;
  subject: string;
}

/**
 * One changed path with its line deltas. A binary file reports 0/0 — git prints
 * no counts for one, and the path is what the file table is for.
 */
export interface TaskDiffFile {
  path: string;
  additions: number;
  deletions: number;
}

/**
 * GET /api/board/tasks/{id}/diff — what the agent committed on this card's run
 * branch, measured from the start point admission pinned it to. Mirrors
 * taskDiffDTO in internal/api/tasks_diff.go.
 */
export interface TaskDiff {
  /** The pinned start-point SHA the branch is measured against. */
  base: string;
  branch: string;
  commits: TaskDiffCommit[];
  files: TaskDiffFile[];
  /** Unified diff, capped at 200 KB — see `patchTruncated`. */
  patch: string;
  patchTruncated: boolean;
}

/** POST /api/board/tasks/{id}/discard — 200 once the branch is gone. */
export interface DiscardTaskResponse {
  /** False when the branch was already gone: discarding is idempotent. */
  deleted: boolean;
  branch: string;
  task: BoardTask;
}

/** POST /api/board/tasks/{id}/land — 200 once the PR exists and the card is done. */
export interface LandTaskResponse {
  prUrl: string;
  branch: string;
  task: BoardTask;
}

// --- fusion phase 13: playbooks (selectable workflows) ------------------------

/** One stage of a playbook: a name + its prompt-template body. */
export interface PlaybookStage {
  name: string;
  body: string;
}

/** Verify strictness knob a playbook hands to auto-verification (Phase 6). */
export type PlaybookVerify = 'strict' | 'normal' | 'off';

/** Where a resolved playbook came from (built-in vs project-local override). */
export type PlaybookSource = 'builtin' | 'project';

/**
 * The `--permission-mode` a playbook's runs spawn under. `''` (the neutral
 * default every built-in ships) inherits the daemon's global knob; `'default'`
 * means the flag is omitted entirely.
 */
export type PlaybookPermissionMode = '' | 'bypassPermissions' | 'acceptEdits' | 'default';

/**
 * A playbook — item of GET /api/playbooks. Mirrors playbookDTO in
 * internal/api/playbooks.go. Structure is read-only in the UI; a project makes
 * a built-in's prompts editable by duplicating it (POST …/duplicate).
 */
export interface Playbook {
  name: string;
  description: string;
  model: string;
  verify: PlaybookVerify;
  /** Spawn permission mode; '' = inherit the daemon's global knob. */
  permissionMode: PlaybookPermissionMode;
  source: PlaybookSource;
  stages: PlaybookStage[];
  /** On-disk path of a project playbook (""/absent for a built-in). */
  path: string;
}

/** Response of POST /api/projects/{id}/playbooks/{name}/duplicate. */
export interface DuplicatePlaybookResponse {
  name: string;
  path: string;
  hint: string;
}

/**
 * GET /api/dispatch — dispatcher runtime snapshot (fusion phase 3). Mirrors the
 * Go `dispatch.Status` struct (internal/dispatch/service.go). Read-only; the
 * status bar derives its pause chip + slot readout from it. `pausedScopes`
 * lists every currently-paused scope key ("global" or "project:<id>").
 */
export interface DispatchStatus {
  enabled: boolean;
  globalPaused: boolean;
  maxConcurrent: number;
  maxWorktrees: number;
  activeRuns: number;
  freeSlots: number;
  pausedScopes: string[];
}

// --- fusion phase 8: planning mode -------------------------------------------

/** Go `planning.PlanningOption` (internal/planning/protocol.go) — one
 * selectable answer of a wizard question. */
export interface PlanningOption {
  id: string;
  label: string;
  description?: string;
  pros?: string[];
  cons?: string[];
  isOther?: boolean;
}

/**
 * Go `planning.PlanningSummary` — the running plan rebuilt after every answer.
 * `suggestedSize` is a free string on the wire (the protocol asks for S|M|L but
 * the Go side does not enforce the set — mirror, don't narrow).
 */
export interface PlanningSummary {
  title: string;
  description: string;
  proposedChanges?: string[];
  acceptanceCriteria?: string[];
  suggestedSize?: string;
}

/** Go `planning.PlanningQuestion` — one structured interview question. */
export interface PlanningQuestion {
  id: string;
  type: 'single_select' | 'multi_select';
  question: string;
  description?: string;
  options: PlanningOption[];
  runningPlan?: PlanningSummary;
}

/** Go `planning.wizardAnswer` — the JSON stamped onto a history turn. */
export interface PlanningAnswer {
  kind: 'answer' | 'refine';
  selectedOptionIds?: string[];
  otherText?: string;
  instructions?: string;
}

/**
 * Go `planning.WizardTurn` — one history entry. `question` is null when the
 * stored JSON failed to re-parse (Go sends the pointer); `answer` is null until
 * the operator answered (a PROCEED dismisses a question without an answer).
 */
export interface PlanningTurn {
  seq: number;
  question: PlanningQuestion | null;
  answer: PlanningAnswer | null;
  reasoning: string;
}

/** The closed wizard status set persisted in planning_sessions.status. */
export type PlanningWizardStatus =
  | 'generating'
  | 'awaiting_answer'
  | 'proceeding'
  | 'done'
  | 'failed'
  | 'cancelled';

/**
 * Go `planning.WizardStatus` (internal/planning/state.go) — GET
 * /api/projects/{id}/planning. Field names are FROZEN (mirrored verbatim from
 * the Go json tags; see TestWizardGET_DTOFieldNames). `sessionUuid` is the
 * pre-generated planner session id (present while active, so the page links to
 * /sessions/{uuid} and matches the transcript before the numeric row is
 * minted); `sessionId` is that numeric row once ingest/the hook mints it (null
 * until then); `startedAt` is the RFC3339 run start.
 *
 * `status` is the wire shape as Go sends it: the EMPTY STRING when the project
 * has no wizard row yet (legacy idle) — NOT null. Kept as `'' |
 * PlanningWizardStatus` rather than the plan sketch's `| null` so the TS type
 * matches the bytes on the wire.
 */
export interface PlanningStatus {
  active: boolean;
  sessionUuid: string;
  sessionId: number | null;
  startedAt: string | null;
  status: PlanningWizardStatus | '';
  currentQuestion: PlanningQuestion | null;
  runningPlan: PlanningSummary | null;
  rawReply: string | null;
  history: PlanningTurn[];
  planDir: string | null;
  /** Wizard mode (plan-revision phase 2): `revise` interviews against an
   * EXISTING plan and stages a diff instead of writing docs. The empty string
   * mirrors the wire ("" when no wizard row exists), same as `status`. */
  mode: 'plan' | 'revise' | '';
  /** The workspace task whose plan a `revise` wizard targets; null otherwise. */
  reviseTaskId: number | null;
  /** Why the LAST action did not go through (failed resume spawn, planner
   * process gone). The wizard is back in `awaiting_answer` with the SAME
   * question in that case, which is indistinguishable from the planner asking
   * again — this is the only signal that the answer was never delivered. Null
   * as soon as the next action starts. */
  lastError: string | null;
}

/** POST /api/projects/{id}/planning → 202 body. */
export interface PlanningStart {
  sessionUuid: string;
}

// --- plan revisions (plan-revision phase 3/4) ---------------------------------
// Go DTOs: internal/api/revisions.go (revisionDTO / revisionDetailFileDTO) and
// the apply 409 body (internal/planrev.Conflict). Field names are FROZEN there.

/** planrev status column — `staged` is the only open state. */
export type RevisionStatus = 'staged' | 'applied' | 'rejected' | 'superseded' | 'failed';

/** Who asked for the revision: the operator, or a blocked-phase diagnosis. */
export type RevisionOrigin = 'operator_revise' | 'phase_diagnosis';

/** What a revision does to one plan doc. */
export type RevisionAction = 'create' | 'update' | 'delete' | 'rename';

/** One file of a revision. The list endpoint sends actions only; the detail
 * endpoint adds `stale` (live file drifted since staging) and `diff` (unified
 * diff against the LIVE bytes at request time). */
export interface RevisionFile {
  docPath: string;
  action: RevisionAction;
  renameFrom?: string;
  /** Detail endpoint only: the live file changed since staging. */
  stale?: boolean;
  /** Detail endpoint only: unified diff, live → proposed. */
  diff?: string;
}

/** Go `revisionDTO` — one revision of GET /api/epics/{taskId}/revisions
 * (newest first) and the `revision` half of GET /api/revisions/{id}. */
export interface PlanRevision {
  id: number;
  status: RevisionStatus;
  origin: RevisionOrigin;
  reason: string;
  /** epic_phases id of the diagnosing phase (origin=phase_diagnosis). */
  triggerPhaseId?: number;
  /** The revise wizard session that staged this revision. */
  sessionUuid?: string;
  /** Staging failure detail (status=failed). */
  error?: string;
  createdAt: string;
  decidedAt?: string;
  decidedBy?: string;
  files: RevisionFile[];
}

/** Go `planrev.Conflict` — one row of the apply 409 body's `conflicts`. */
export interface RevisionConflict {
  docPath: string;
  baseHash: string;
  diskHash: string;
  diff: string;
}

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
  /**
   * Active findings (resolved_at IS NULL) split by severity. Usage-guide
   * findings (docs_*) are NOT counted here — they saturate the corpus and would
   * make these click-to-filter badges disagree with the `lintMax` list they
   * filter. Guide coverage is `docs` below.
   */
  lint: { error: number; warn: number; info: number };
  /** Go: systemInsightCountsDTO — promotion/drift badge counters. */
  insights: { promotions: number; staleOverrides: number };
  /**
   * Go: systemDocsCoverageDTO — `# How to use` coverage over live agents +
   * skills + commands, derived from the linter's active docs findings.
   * `documented` ⊇ `reviewed`; both are clamped at 0.
   */
  docs: { total: number; documented: number; reviewed: number };
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
  /**
   * Worst ACTIVE lint finding severity; null = clean. Usage-guide findings
   * (docs_*) are deliberately excluded — see `documented`.
   */
  lintMax: LintSeverity | null;
  /** Active agent_dead finding (advisory — sparse events attribution). */
  dead: boolean;
  /**
   * false while a docs_missing or docs_incomplete finding is active — the item
   * has no usable `# How to use` guide. Carries the docs axis that `lintMax`
   * deliberately drops.
   */
  documented: boolean;
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

/**
 * Go: systemDocsDTO — the item's parsed `# How to use` usage guide, served on
 * every detail endpoint (contract: tools/swarmery/docs/system-docs-format.md).
 * Every string in it is redacted server-side, including the section titles.
 */
export interface SystemDocs {
  /** A `# How to use` block was found (§1). */
  present: boolean;
  /** Two blocks in one file — a violation; the parser kept the first (§1.4). */
  duplicate: boolean;
  /** The whole block as markdown, redacted; '' when absent. */
  markdown: string;
  /** H2 subsection titles found, in file order. Never null — [] when absent. */
  sections: string[];
  /** Required §2 subsections absent or under the rune floor, in contract order.
   * Never null, and always [] when `present` is false ("no guide at all" is a
   * distinct finding from "guide with gaps"). */
  missing: string[];
  /** frontmatter docs.status (§3): 'generated' | 'reviewed' | '' when absent.
   * An unrecognised value is kept verbatim so the UI can report it. */
  status: string;
  /** docs.source_sha is well-formed and no longer matches the body (§4) — the
   * item changed after its guide was written. Unknown staleness is not stale. */
  stale: boolean;
}

/** Go: systemItemDetailDTO — GET /api/system/{agents|skills}/{id} (numeric row id). */
export interface SystemItemDetail extends SystemItem {
  deleted: boolean;
  currentVersionId: number | null;
  /** Raw YAML frontmatter block of the current version (redacted). */
  frontmatter: string;
  /** Markdown body of the current version (redacted). */
  body: string;
  /** Parsed `# How to use` guide of the current version. */
  docs: SystemDocs;
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

/** Go: undocumentedItemDTO — one item the linter reports as having no usable
 * `# How to use` guide (docs/system-docs-format.md), in insight framing. */
export interface SystemUndocumentedItem {
  kind: 'agent' | 'skill' | 'command';
  id: number;
  name: string;
  scope: 'global' | 'project';
  projectSlug: string | null;
  pluginName: string | null;
  /** agents/commands: file path; skills: dir path. */
  path: string;
  /** Which guide rule fired. */
  rule: 'docs_missing' | 'docs_incomplete';
  /** Required §2 subsection names still absent. Never null — all of them when
   * the guide is missing entirely. */
  missing: string[];
  hint: string;
}

/** Go: systemInsightsDTO — GET /api/system/insights. */
export interface SystemInsights {
  promotionCandidates: SystemPromotionCandidate[];
  staleOverrides: SystemStaleOverride[];
  dead: SystemDeadComponent[];
  /** Items with an active docs_missing / docs_incomplete finding. */
  undocumented: SystemUndocumentedItem[];
  /**
   * Active plugin_* findings across every project — the only cross-project view
   * of plugin drift. Additive optional: absent on a daemon older than the
   * drift scanner.
   */
  pluginDrift?: SystemPluginDrift[];
}

/** One active plugin_* finding, resolved to the project it belongs to. */
export interface SystemPluginDrift {
  /** "<name>@<marketplace>", or "detector" for the machine-wide blindness row. */
  pluginId: string;
  rule: string;
  severity: string;
  message: string;
  /** null when the finding is machine-wide or the path matches no project row. */
  projectSlug: string | null;
  projectPath: string;
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

/** Go: commandHubDTO — GET /api/system/commands/{id}/hub. Read-only: commands
 * have no write surface, so this carries no versions and no base_hash. */
export interface SystemCommandHub extends SystemCommand {
  /** Raw YAML frontmatter block (redacted). */
  frontmatter: string;
  /** Markdown body (redacted). */
  content: string;
  /** Parsed `# How to use` guide. */
  docs: SystemDocs;
  usage: {
    windowDays: number;
    invocations: number;
    /** ALWAYS true — slash-command invocations are inferred from prompt text,
     * never an authoritative event. Render the caveat, never a bare number. */
    approximate: boolean;
  };
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
  /** Mirrors Project.isSystem — the health table drops the System row. */
  isSystem: boolean;
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
/**
 * Drift verdict from the daemon's plugin_* findings. 'unknown' means the plugin
 * is disabled here, so nothing was checked and nothing is claimed — it is not a
 * synonym for 'ok'.
 */
export type PluginDriftStatus = 'ok' | 'missing' | 'behind' | 'orphaned' | 'unknown';

/**
 * Verdict on the .claude/project.json key the pack declared for itself
 * (requirements.json). Distinct from PluginDriftStatus: drift is about whether
 * the pack is installed, this is about whether it can actually run here.
 *
 * The field is optional on the row, and its ABSENCE is not 'ok' — it means the
 * question was not asked, for one of four reasons: the row is disabled, drift
 * outranks it (missing/behind/orphaned — repair before config), the pack
 * declares no requirements, or its declaration is malformed.
 */
export type PluginConfigStatus = 'ok' | 'needs-config';

export interface ProjectPluginRow {
  name: string;
  description: string;
  enabled: boolean;
  /** core: toggled via attach/detach, never through the plugins endpoint. */
  locked: boolean;
  status: PluginDriftStatus;
  /** Human explanation for a non-ok status; absent when status is ok/unknown. */
  detail?: string;
  /** Absent when the config question was not asked — see PluginConfigStatus. */
  configStatus?: PluginConfigStatus;
  /** The project.json key the pack declared, e.g. 'jira'. */
  configKey?: string;
  /** Pack-supplied modal copy: what the key is, and why the pack needs it. */
  configTitle?: string;
  configWhy?: string;
  /** Pack-relative path to the doc explaining the key. */
  configDocs?: string;
  /** Dotted paths of unfilled required leaves, e.g. ['qaStatus', 'repro.test']. */
  configMissing?: string[];
  /**
   * The pack's own JSON Schema fragment for the key, passed through verbatim to
   * render a form from. Sent only alongside configStatus — a schema is ~1 KB,
   * and the daemon will not attach one to every row of a catalog that mostly
   * declares nothing.
   */
  configSchema?: unknown;
  /** The key's existing value, for prefill. Absent when the key is not set. */
  configCurrent?: unknown;
  /**
   * Present only when the pack declared a runnable probe — a `claude` session
   * the daemon can ask for REAL candidate values (the obvious guess for a
   * status name is usually not the one on the board). Deliberately not the
   * whole spec: the prompt is the daemon's business and would ride on every
   * poll of this endpoint.
   */
  configProbe?: PluginConfigProbe;
}

export interface PluginConfigProbe {
  /** Dotted paths that must be filled before the probe has anything to work
   * from — its inputs, not its outputs. Gates the button. */
  needs: string[];
  /** Dotted paths the probe may suggest values for. A whitelist: the daemon
   * discards anything the agent returns outside it. */
  fields: string[];
}

/**
 * POST /api/projects/{id}/config/{key}/probe.
 *
 * `suggestions` is always an object, including when the probe found nothing —
 * the failure shape and the success shape are the same, so there is one code
 * path. `reason` is set only when nothing came back, and is written to be read
 * by a human in a grey line under the form.
 */
export interface ProjectConfigProbeResponse {
  suggestions: Record<string, string[]>;
  reason?: string;
  notes?: string;
}

export interface ProjectPluginsResponse {
  marketplaceVersion: string;
  /** The marketplace these rows come from — the repair call needs name@marketplace. */
  marketplaceName: string;
  /** Mirrors the PUT fence: SWARMERY_ONBOARD_ROOTS set + path inside the allow-list. */
  canWrite: boolean;
  plugins: ProjectPluginRow[];
  /**
   * Settings sources beyond the repo's settings.json that contributed to the
   * enabled state: "settings.local.json" for the repo's own local file, plus
   * any declared launcher overlays. Absent when settings.json is the whole story.
   */
  overlaySources?: string[];
}

/** POST /api/projects/{id}/plugins/{name}/repair */
export interface PluginRepairResponse {
  id: string;
  action: 'install' | 'update';
  /**
   * Which scope the CLI was actually asked for. 'user' means the project's
   * .claude is a symlinked overlay, where a project-scope write is impossible —
   * the daemon installed at user scope and reverted the global enable.
   */
  scope: 'project' | 'user';
  output: string;
  status: PluginDriftStatus;
  /** Always true: a repaired plugin loads only in the next Claude Code session. */
  restart: boolean;
  /**
   * Set when the repair succeeded but left something the operator must act on
   * (currently: the global enable could not be reverted, so the pack is on for
   * every project until the key is removed by hand).
   */
  warning?: string;
}

/** PUT /api/projects/{id}/plugins/{name} result. */
export interface ProjectPluginToggleResponse {
  name: string;
  enabled: boolean;
  changed: boolean;
  backup?: string;
  /**
   * Set when settings.json was written but .claude/settings.local.json pins the
   * same key to the opposite value — sessions keep the local value until that
   * key is removed, so the toggle did not change the effective state.
   */
  warning?: string;
}

/** PUT /api/projects/{id}/config/{key} result — one top-level project.json key. */
export interface ProjectConfigWriteResponse {
  /** The key that was written — echoed back so a stale response is detectable. */
  key: string;
  written: boolean;
  /** Relative path of the pre-write copy (".claude/project.json.bak"). */
  backup?: string;
  /** False when the key already held this exact value; the file was still rewritten. */
  changed: boolean;
}

/**
 * 422 body from PUT /api/projects/{id}/config/{key}: the value did not satisfy
 * the schema the pack declared. `problems` carries one line per field, so the
 * form can put each next to its input instead of showing one long sentence.
 */
export interface ProjectConfigInvalid {
  error: string;
  problems: string[];
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

/**
 * Live auto-provision job state for a project's architecture map (phases 2–3):
 * emitted while enabling architecture-pack drives an install + /architecture-map
 * run. `null` when no job is (or has been) tracked for the project.
 */
export interface ProvisionState {
  state: 'pending' | 'installing' | 'generating' | 'installed' | 'done' | 'skipped' | 'failed';
  /** Last stdout line of the running job; "" when none yet. */
  lastLine: string;
  /** Failure reason on state='failed'; "" otherwise. */
  error: string;
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
  /** Auto-provision job state; null when no job is tracked. */
  provision: ProvisionState | null;
}

export interface ToolsResponse {
  serena: { available: boolean; projects: ToolsSerenaProject[] };
  graphify: { projects: ToolsGraphifyProject[] };
  architecture: { projects: ArchitectureProject[] };
}

// ── Connectors (MCP servers Claude Code has configured) ─────────────────────

export type ConnectorTransport = 'stdio' | 'http' | 'sse' | '';
export type ConnectorScope = 'local' | 'user' | 'project' | 'claudeai' | 'unknown';
export type ConnectorStatus =
  | 'connected'
  | 'failed'
  | 'needs_auth'
  | 'pending'
  | 'disabled'
  | 'unknown';

export interface Connector {
  name: string;
  transport: ConnectorTransport;
  scope: ConnectorScope;
  status: ConnectorStatus;
  /** command+args (stdio) or URL (http/sse) as printed by the CLI; display-only. */
  detail: string;
  source: string;
}

export interface ConnectorsResponse {
  connectors: Connector[];
}

// ── Accounts (multi-account, phase 7) ────────────────────────────────────────

/** Go: accountDTO (internal/api/accounts.go:82) */
export interface Account {
  key: string;
  configDir: string;
  isDefault: boolean;
  /** Tri-state, NOT boolean: true/false = the question was asked and answered,
   * null = it could not be asked at all (SWARMERY_USAGE_OAUTH=0). Rendering
   * null as false is a false statement about the operator's subscription. */
  connected: boolean | null;
  /** Raw rateLimitTier, "" when unresolved — display as-is, do not re-derive. */
  plan: string;
  /** Authoritative "the CLI can run under this account" verdict — a DIFFERENT
   * question from `connected` (quota readable), and the two legitimately
   * disagree. Tri-state like `connected`: true/false = probed and answered,
   * null = never probed or the probe could not determine an answer. Served
   * from the stored verdict; refresh via POST /api/accounts/{key}/probe. */
  runnable: boolean | null;
  /** Short fixed phrase explaining a false/undetermined verdict, absent otherwise. */
  runnableReason?: string;
  /** RFC3339 time the verdict was taken; absent when never probed. */
  runnableCheckedAt?: string;
  ingested: boolean;
  /** Project paths EXPLICITLY bound to this account (not "every unbound project"). */
  projects: string[];
}

export interface AccountsResponse {
  accounts: Account[];
}

/** Go: provisionResponse (internal/api/accounts.go:113) */
export interface ProvisionResponse {
  account: Account;
  /** "CLAUDE_CONFIG_DIR=<dir> claude" — the OPERATOR runs this, we never do. */
  loginCommand: string;
  hint?: string;
}

/** Go: removeAccountResponse (internal/api/accounts.go:126) */
export interface RemoveAccountResponse {
  ok: boolean;
  danglingBindings?: string[];
}

/** Go: accountProbeResponse — POST /api/accounts/{account}/probe. The same
 * three fields as on Account, so a client can patch its row in place. */
export interface AccountProbeResponse {
  runnable: boolean | null;
  runnableReason?: string;
  runnableCheckedAt?: string;
}

/** Go: accountBindingDTO (internal/api/accounts.go:137) */
export interface AccountBinding {
  /** Stored binding, "" when the project has none. */
  account: string;
  effective: string;
  configDir: string;
  source: 'binding' | 'default';
}

// --- Project overview (GET /api/projects/{id}/overview, Canvas v2 phase 1) ----

/** One "Right now" live-count tile. tone: green | amber | red | neutral. */
export interface OverviewTile {
  label: string;
  value: number;
  sub: string;
  tone: 'green' | 'amber' | 'red' | 'neutral';
}

/** One "This week" delta metric. Value/delta/deltaTone are null when no data. */
export interface WeekMetric {
  label: string;
  /** Null when the current window has no data (honesty rule). */
  value: string | null;
  /** Null when there is no previous-window data to compare against. */
  delta: string | null;
  /** green | red | neutral; null when delta is null. */
  deltaTone: 'green' | 'red' | 'neutral' | null;
  sub: string;
}

/** One "Needs attention" action row. tone: amber | red. */
export interface AttentionItem {
  text: string;
  action: string;
  href: string;
  tone: 'amber' | 'red';
}

/** GET /api/projects/{id}/overview — editorial aggregate for the Canvas v2 home. */
export interface ProjectOverviewResp {
  rightNow: OverviewTile[];
  thisWeek: WeekMetric[];
  attention: AttentionItem[];
}

/** POST body for adding a server. command/args apply to stdio; url to http/sse.
 * The page only offers 'stdio' | 'http' | 'sse' and scope 'local' | 'user'. */
export interface AddConnectorInput {
  name: string;
  transport: 'stdio' | 'http' | 'sse';
  command?: string;
  args?: string[];
  url?: string;
  scope: 'local' | 'user';
}

// ── Routines (fusion phase 7 — scheduled automation) ────────────────────────

/** routines step kind (mirrors internal/routines/step.go). */
export type RoutineStepType = 'command' | 'ai-prompt' | 'create-task';

/** One typed routine step. Only the fields relevant to `type` are set. */
export interface RoutineStep {
  type: RoutineStepType;
  name: string;
  // command
  command?: string;
  timeoutSec?: number;
  continueOnFailure?: boolean;
  // ai-prompt
  prompt?: string;
  model?: string;
  // create-task
  taskTitle?: string;
  taskPrompt?: string;
  boardColumn?: string;
}

/** routines.catch_up policy. */
export type RoutineCatchUp = 'skip' | 'run_one';

/** A routine (GET/POST/PATCH response, list item). webhookToken is present ONLY
 * on the create/rotate response; list/get expose hasWebhook instead. */
export interface Routine {
  id: string;
  projectId: number | null;
  name: string;
  cronExpr: string;
  enabled: boolean;
  catchUp: RoutineCatchUp;
  steps: RoutineStep[];
  hasWebhook: boolean;
  webhookToken?: string;
  timeoutSec: number;
  createdAt: string;
  updatedAt: string;
  lastRunAt: string | null;
  nextRunAt: string | null;
}

/** routine_runs row status. */
export type RoutineRunStatus = 'running' | 'ok' | 'failed' | 'timeout';

/** One run-history entry. detail is the per-step results JSON (string). */
export interface RoutineRun {
  id: number;
  trigger: 'cron' | 'manual' | 'webhook';
  status: RoutineRunStatus;
  detail: string | null;
  startedAt: string;
  finishedAt: string | null;
}

/** POST/PATCH /api/routines request body. */
export interface RoutineInput {
  projectId?: number | null;
  name: string;
  cronExpr?: string;
  enabled?: boolean;
  catchUp?: RoutineCatchUp;
  steps?: RoutineStep[];
  timeoutSec?: number;
  webhook?: boolean;
}

// --- fusion phase 10: epic rollups + plan-doc editor --------------------------

/** Direct phase-run lifecycle (interactive planning v2 phase 5). */
export type PhaseRunState = 'idle' | 'running' | 'done' | 'failed';

/** What a run ACHIEVED, derived server-side — mirrors internal/phasediag.OutcomeFromRow.
 *  Distinct from PhaseRunState, which only says how the process ended. */
export type PhaseRunOutcome = 'idle' | 'running' | 'completed' | 'partial' | 'noop' | 'failed';

/**
 * The `code` discriminator every run 409 carries — mirrors the constants in
 * internal/api/runconflict.go, which is the authoritative list.
 *
 * POST …/run and DELETE …/branch answer 409 for a dozen different reasons, and the
 * client used to tell them apart by sniffing which fields were present (`branch`
 * ⇒ "the branch holds commits"). That silently mis-classifies every future case
 * that happens to carry the same field, so the discriminator is the contract and
 * the fields are display data.
 */
export type RunConflictCode =
  // Run admission (both the phase and the whole-plan surface).
  | 'already-running'
  | 'deps-unmet'
  | 'doc-unreadable'
  | 'no-project-path'
  // Branch lifecycle — the phase surface's own gate, then the worktree sentinels.
  | 'no-run-branch'
  | 'branch-dirty'
  | 'branch-checked-out'
  | 'branch-is-head'
  | 'branch-refused'
  | 'branch-busy'
  | 'detached-head'
  /** orphan-cleanup route only: the named branch's id IS a live phase row, so it is
   *  some phase's run branch rather than stranded work. */
  | 'branch-live-phase'
  // Plan-run-only admission gates.
  | 'phase-running'
  | 'plan-not-active'
  | 'no-phases'
  | 'plan-complete';

/** The phase doc's verification opt-in (epic_phases.verify_mode). */
export type PhaseVerifyMode = 'off' | 'normal' | 'strict';

/** A read-only verification verdict (epic_phases.verify_verdict / tasks.verify_verdict). */
export type PhaseVerifyVerdict = 'pass' | 'fail' | 'inconclusive';

/** One reason a phase did not progress — mirrors phasediag.Blocker. */
export interface PhaseBlocker {
  kind:
    | 'dep-incomplete'
    | 'dep-unmerged'
    | 'branch-blocks-retry'
    | 'branch-dirty'
    /** the branch holds commits but its own run's worktree is still checked out on
     *  it — a retry continues that work, and a delete 409s, so this kind offers NO
     *  delete action (phasediag.KindOwnWorktree). */
    | 'own-worktree'
    /** the phase opted into post-run verification (`**Verify:** strict`) and the
     *  read-only verifier returned FAIL. An INPUT to the diagnosis, not a status:
     *  `runOutcome` stays checkbox-derived, and this blocker carries the verifier's
     *  own reasons in `detail` (phasediag.KindVerifyFailed). */
    | 'verify-failed'
    | 'no-criteria'
    /** a swarm/phase-<id> branch whose id matches no phase row: work stranded under
     *  a previous id generation. A legitimate delete target, but only through the
     *  orphan-cleanup route — this phase cannot derive its name (phasediag.KindOrphanBranch). */
    | 'orphan-branch';
  summary: string;
  detail: string;
  /** branch-dirty only: the branch a delete would destroy and how many commits go
   *  with it. Carried as data so the delete confirmation names both from the source
   *  that proved them, rather than parsing `summary` or rebuilding the branch name
   *  client-side. Omitted on every other kind (Go `omitempty`). */
  branch?: string;
  commitsAhead?: number;
}

/** The executor's own last word — mirrors phasediag.AgentMessage. */
export interface PhaseAgentMessage {
  sessionUuid: string;
  text: string;
  truncated: boolean;
}

/** On-demand phase-run diagnosis — mirrors phasediag.Diagnosis. */
export interface PhaseDiagnosis {
  phaseId: number;
  seq: number;
  name: string;
  runOutcome: PhaseRunOutcome;
  criteriaTotal: number;
  /** null when the run predates measurement — render "not measured", never 0. */
  criteriaBefore: number | null;
  criteriaAfter: number;
  runStartedAt: string | null;
  runEndedAt: string | null;
  runError: string | null;
  /** Last verification verdict for this phase, null when it was never graded (the
   *  default — verification is opt-in per doc). A `fail` also appears in `blockers`
   *  as `verify-failed`; the outcome above is unaffected either way. */
  verifyVerdict: PhaseVerifyVerdict | null;
  verifyDetail: string | null;
  blockers: PhaseBlocker[];
  agentMessage: PhaseAgentMessage | null;
}

/** One epic phase — mirrors epicPhaseDTO in internal/api/epics.go. */
export interface EpicPhase {
  id: number;
  seq: number;
  name: string;
  /** Absolute doc path on disk (informational). */
  docPath: string;
  /** Path relative to the plan/ dir — the value the docs endpoints accept. */
  docRelPath: string;
  dependsOn: number[];
  checkboxesDone: number;
  checkboxesTotal: number;
  /** The phase doc's own `Status:` header marker (normalized); null when the
   * doc carries none. Lets an executor flag "working on this now" before the
   * first checkbox tick. */
  docStatus: 'pending' | 'in_progress' | 'done' | null;
  /** RFC3339 mtime of the phase doc at scan time — liveness signal (executor
   * edits change the doc and re-trigger the scan). */
  docUpdatedAt: string | null;
  /** The doc's `## Completion Report` section (markdown) — what the executor
   * shipped. Null until written; shown in a summary modal on done phases. */
  completionReport: string | null;
  activatedAt: string | null;
  /** external_id of the board task this phase was activated into (null until). */
  boardTaskExternalId: string | null;
  boardTaskId: number | null;
  /** The current board column of that board task (null until activated). */
  boardColumn: BoardColumn | null;
  /** Direct phase-run state (no board task involved). */
  runState: PhaseRunState;
  runSessionUuid: string | null;
  runStartedAt: string | null;
  /** Failure detail (stderr tail / timeout / cancelled) when runState==='failed'. */
  runError: string | null;
  /** Derived: what the run ACHIEVED, as opposed to how the process ended. A
   * `runState: 'done'` run that ticked nothing is `noop`, not `completed` — the
   * green chip keys on THIS, never on runState. */
  runOutcome: PhaseRunOutcome;
  /** End of the last run (null while running / never run). */
  runEndedAt: string | null;
  /** Ticked-criteria count snapshotted at the run's start. NULL means UNMEASURED
   * (rows predating the snapshot), never zero — never render a 0 → N delta from it. */
  runCheckboxesBefore: number | null;
  /** The phase doc's opt-in to post-run verification (`**Verify:** strict`). `off` is
   *  the default, which is why verification is invisible on every plan that never
   *  asked for it. */
  verifyMode: PhaseVerifyMode;
  /** The verdict that opt-in produced, null until a run has been graded. Rendered as
   *  a chip BESIDE the outcome chip, never merged into it: the outcome answers "did
   *  work land?" and the verdict answers "was it confirmed?" — two questions. */
  verifyVerdict: PhaseVerifyVerdict | null;
  verifyDetail: string | null;
  /** THE completion gate's answer (server-side, internal/phasegate): may this phase
   *  be called DONE? Read this instead of comparing checkboxesDone to
   *  checkboxesTotal — the client used to re-derive completion and could therefore
   *  disagree with the daemon about the same row.
   *
   *  `unverified` is the state that did not exist before: every criterion ticked and
   *  the grade the doc asked for never arrived. It is deliberately distinct from
   *  both `complete` and a failing verdict — the work may be fine, and nobody knows. */
  completionState: PhaseCompletionState;
  /** Why the gate refused, in the operator's words; [] when complete. A list because
   *  one gate cites every reason it has. */
  completionBlockers: string[];
}

/** The completion gate's states (server-side internal/phasegate).
 *  `unverified` — the work is there and unconfirmed (the grade never landed).
 *  `unreported` — the work is there and unrecorded (no Completion Report, or no
 *  lesson). Two states rather than one because the remedies differ: re-run the
 *  verifier versus write down what happened. */
export type PhaseCompletionState = 'complete' | 'unverified' | 'unreported' | 'incomplete';

/** Checkbox rollup across an epic's phases. */
export interface EpicRollup {
  done: number;
  total: number;
  /** 0..100; 0 when total===0. */
  pct: number;
  /** How many phases the completion gate refuses. A plan reads `done` only at 0. */
  incompletePhases: number;
}

/** One SC-tagged acceptance criterion from plan/spec.md, with the phases that
 * declare they deliver it — mirrors specCriterionDTO in internal/api/epics.go. */
export interface SpecCriterion {
  /** Uppercase spec-criterion id ("SC-1"). */
  cid: string;
  text: string;
  done: boolean;
  /** Phase seqs whose `**Covers:**` line declares this cid; empty = uncovered
   * (never null). */
  coveredBy: number[];
}

/** A phase `**Covers:**` reference to an id the spec never declared — a
 * speculation signal. Mirrors specUnknownRefDTO. */
export interface SpecUnknownRef {
  seq: number;
  cid: string;
}

/** Per-epic spec coverage rollup (criteria × phase Covers) — mirrors
 * epicSpecDTO. */
export interface EpicSpec {
  criteria: SpecCriterion[];
  covered: number;
  total: number;
  unknownRefs: SpecUnknownRef[];
}

/** One epic (a workspace plan) — mirrors epicDTO in internal/api/epics.go.
 * `status` is the derived planStatus (zone > README > rollup precedence). */
export interface Epic {
  taskId: number;
  externalId: string;
  projectId: number;
  projectSlug: string;
  title: string;
  status: 'active' | 'paused' | 'done' | 'archived';
  startedAt: string | null;
  planDir: string;
  /** True when plan/SUMMARY.md exists — openable via the docs endpoint
   * (path=SUMMARY.md) in the plan-level summary modal. */
  hasSummary: boolean;
  /** True when plan/spec.md exists. Independent of `spec` below: the file may
   * exist before the scanner has parsed criteria rows out of it. */
  hasSpec: boolean;
  /** Per-epic spec coverage; null when the task has no spec_criteria rows. */
  spec: EpicSpec | null;
  phases: EpicPhase[];
  rollup: EpicRollup;
  /** Whole-plan run state (one agent handed the whole plan, driving core's
   * run-plan skill). Null until the plan has ever been run. Distinct from the
   * per-phase EpicPhase.runState — a plan run never stamps individual phases. */
  planRun: PlanRun | null;
  /** Set when this plan is a MICRO-PLAN: the external id of the board card whose
   * dispatch materialized it. The two are one unit of work seen from two pages. */
  cardExternalId: string | null;
  /** Every session task_sessions attaches to this plan: the daemon's own runs
   * (explicit) and the interactive sessions inferred from the plan files they
   * edited (heuristic). Never null — [] means nothing has worked on this plan,
   * which is a different claim from "we don't know". */
  linkedSessions: LinkedSession[];
}

/** One task_sessions row joined to its session — mirrors linkedSessionDTO.
 *
 * This is the THIRD execution path made visible: an operator's own session, or a
 * /run-plan session, is not daemon-spawned and carries no run branch, so the
 * ?planTask= grouping (which resolves sessions from the stamped branch / worktree
 * cwd) cannot see it. Its link is the only evidence it worked on the plan. */
export interface LinkedSession {
  sessionUuid: string;
  /** `explicit` = the daemon spawned this session for this task; `heuristic` =
   * inferred from evidence (edited plan files, cwd overlap). */
  linkSource: 'explicit' | 'heuristic';
  /** 0..1 for heuristic links; 1.0 for explicit ones. Null for links written
   * before the column was populated. */
  confidence: number | null;
  /** SUM of the session's priced turns; null while none is priced. */
  costUsd: number | null;
  startedAt: string;
  endedAt: string | null;
}

/** How a plan run executes its phases. `auto` leaves the run-plan skill's own
 * DAG triage authoritative; the other two force the one call it cannot derive
 * from the manifest. */
export type PlanRunMode = 'auto' | 'subagents' | 'inline';

/** The plan_runs row for one epic. */
export interface PlanRun {
  agent: string | null;
  mode: PlanRunMode;
  runState: 'idle' | 'running' | 'done' | 'failed';
  runSessionUuid: string | null;
  runStartedAt: string | null;
  runError: string | null;
}

/** GET/PUT/PATCH /api/epics/{taskId}/docs response body. */
export interface PlanDoc {
  path: string;
  content: string;
  /** On-disk backup path a write created (absent on GET). */
  backup?: string;
}

// --- fusion phase 17: agent hub ----------------------------------------------
// Agent-centric READ-ONLY aggregation. Go DTOs live in internal/api/agent_hub.go.
// Reuses Recommendation / AgentChangeProposal / RetroLesson for the Insights tab
// (same shapes the Retro page renders) — no forked contracts.

/** One roster card: a registry identity + its 30-day rollups (folded by
 * normalised agent name). Go: agentRosterRow. */
export interface AgentRosterRow {
  id: number;
  name: string;
  scope: 'global' | 'project';
  projectSlug: string | null;
  origin: 'local' | 'plugin';
  pluginName: string | null;
  model: string | null;
  path: string;
  description: string | null;
  /** The agent resolves to a live registry row the rewriter can act on. */
  improvable: boolean;
  runs30d: number;
  /** success/(success+fail) over judged sessions; null when none in range. */
  successRate: number | null;
  /** Behaviour-failed-run share (0..1) — the roster health dot thresholds it. */
  failedShare: number;
  cost30d: number;
  lastActiveAt: string | null;
}

export interface AgentRosterResp {
  agents: AgentRosterRow[];
}

/** One runs/day sparkline bucket (local day). Go: agentDayCount. */
export interface AgentDayCount {
  day: string;
  runs: number;
}

/** Profile Overview tab. Go: agentOverviewDTO. */
export interface AgentOverview {
  runs30d: number;
  successRate: number | null;
  failedShare: number;
  cost30d: number;
  tokensOut30d: number;
  lastActiveAt: string | null;
  avgMs: number | null;
  p95Ms: number | null;
  runsByDay: AgentDayCount[];
  errors: number;
  errorsByClass: Record<string, number>;
}

/** One Runs-tab row (a subagent run in a session). Go: agentRunRow. */
export interface AgentRun {
  ts: string;
  projectSlug: string;
  sessionUuid: string;
  sessionTitle: string;
  description: string;
  status: string;
  durationMs: number;
}

/** One Activity-tab event. Go: agentActivityRow. */
export interface AgentActivity {
  ts: string;
  type: string;
  toolName: string | null;
  status: string | null;
  sessionUuid: string;
  projectSlug: string;
}

/** One Tasks-tab row (a board task or delegation ledger row). Go: agentTaskRow. */
export interface AgentTask {
  externalId: string;
  title: string;
  status: string;
  source: 'session' | 'delegation';
  phase: string | null;
  verdict: string | null;
  startedAt: string | null;
}

/** Insights tab — the retro/improve rows filtered to the agent. */
export interface AgentInsights {
  recommendations: Recommendation[];
  proposals: AgentChangeProposal[];
  lessons: RetroLesson[];
}

/** GET /api/agents/{id}/hub — the full profile bundle (identity + all tabs). */
export interface AgentProfile extends AgentRosterRow {
  overview: AgentOverview;
  runs: AgentRun[];
  activity: AgentActivity[];
  tasks: AgentTask[];
  insights: AgentInsights;
}

// --- fusion phase 18: system hub ---------------------------------------------
// The catalog-wide extension of the Agent Hub pattern grouped by ROLE (Toolkit =
// Skills/Commands/Templates · Hooks · Insights). Go DTOs live in
// internal/api/system_hub.go. Reuses SystemItem / SystemHook / SystemCommand for
// definition meta — no forked contracts.

/** Go: systemHubSummaryDTO — GET /api/system/hub/summary: the nav count badges. */
export interface SystemHubSummary {
  agents: number;
  skills: number;
  hooks: number;
  commands: number;
  templates: number;
  /** Open insights-inbox count (promotion + stale-override). */
  insights: number;
  /** Active (unresolved) config-lint findings. */
  lintFindings: number;
}

/** One local-day bucket in a usage sparkline. Go: systemHubDayCount. */
export interface SystemHubDayCount {
  day: string;
  count: number;
}

/** Go: skillUsageDTO — the skill profile's 30-day rollup (skill_use grain). */
export interface SkillUsage {
  windowDays: number;
  invocations: number;
  sessions: number;
  projects: number;
  errors: number;
  lastUsed: string | null;
  /** true when the window overlaps pruned (rolled-up) days — counts undercount. */
  approximate: boolean;
  byDay: SystemHubDayCount[];
}

/** One recent invoking session in the skill profile. Go: skillSessionRow. */
export interface SkillSession {
  ts: string;
  sessionUuid: string;
  sessionTitle: string;
  projectSlug: string;
  status: string;
}

/** GET /api/system/skills/{id}/hub — definition meta + usage. Go: skillHubDTO. */
export interface SkillHub extends SystemItem {
  usage: SkillUsage;
  sessions: SkillSession[];
}

/** One config_lint_findings row on a hub profile. Go: systemLintFindingDTO. */
export interface SystemLintFindingRow {
  rule: string;
  severity: LintSeverity;
  message: string;
}

/** GET /api/system/hooks/{id}/hub — the settings entry + lint. Go: hookHubDTO. */
export interface HookHub extends SystemHook {
  lint: SystemLintFindingRow[];
  /** Always false in v1 — hook firings are not tracked (honest note in the UI). */
  firingTelemetry: boolean;
}

/** GET /api/system/commands/{id}/hub — frontmatter + content + approximate usage. */
export interface CommandHub extends SystemCommand {
  /** Redacted frontmatter block. */
  frontmatter: string;
  /** Redacted markdown body. */
  content: string;
  /** Parsed `# How to use` guide. Go: commandHubDTO, always populated
   * (internal/api/system_hub.go:136-140, :443-446 — out.Docs = emptyDocsDTO()
   * first, so the JSON key is never absent). */
  docs: SystemDocs;
  usage: {
    windowDays: number;
    invocations: number;
    /** ALWAYS true — slash-command usage is inferred from prompt text. */
    approximate: boolean;
  };
}

/** Go: systemTemplateDTO — one row of GET /api/system/templates. */
export interface SystemTemplate {
  /** Identity (file stem) — the {name} path handle. */
  name: string;
  fileName: string;
  path: string;
  /** Badge: "core" / "pack:<name>" / "project override". */
  resolution: string;
  /** plugin | project (only plugin built-ins can be copied into the project). */
  source: 'plugin' | 'project';
  pluginName: string;
  /** A built-in shadowed by a project-local copy (project list only). */
  overridden: boolean;
}

/** GET /api/system/templates/{name} — content (read-only). */
export interface SystemTemplateContent extends SystemTemplate {
  content: string;
}

/** 201 body of POST /api/system/templates/{name}/copy. */
export interface SystemTemplateCopyResponse {
  name: string;
  path: string;
  hint: string;
}

/* ---- worktree janitor (internal/api/worktrees.go) ---- */

/** One live worktree, annotated with the janitor's most recent decision. */
export interface WorktreeRow {
  projectSlug: string;
  path: string;
  branch: string | null;
  isMain: boolean;
  dirtyFiles: number;
  lastVerdict: string | null;
  lastReason: string | null;
  lastSweptAt: string | null;
}

/** One journal row: what the janitor decided, and whether it acted. */
export interface WorktreeSweep {
  ts: string;
  projectSlug: string | null;
  path: string;
  branch: string | null;
  verdict: string;
  reason: string;
  salvageBranch: string | null;
  files: number;
  removed: boolean;
  error: string | null;
}

export interface WorktreesResponse {
  live: WorktreeRow[];
  sweeps: WorktreeSweep[];
  /** False when SWARMERY_WTJANITOR disables the sweeper — the history below is
   * then historical, not current, and the panel says so. */
  enabled: boolean;
}
