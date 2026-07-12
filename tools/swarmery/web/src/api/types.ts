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

/** sessions.source */
export type SessionSource = 'jsonl' | 'hook' | 'both';

/** turns.role */
export type TurnRole = 'user' | 'assistant';

/** events.status */
export type EventStatus = 'ok' | 'error' | 'denied' | 'timeout';

/** file_changes.change_type */
export type FileChangeType = 'create' | 'edit' | 'delete' | 'rename';

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
}

// --- Parity wave (design parity pass — frozen contract) ----------------------

/** GET /api/health */
export interface HealthResponse {
  status: 'ok';
  version: string;
  db_size_bytes: number;
  watching: boolean;
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
}

/** WS event names — frozen; implemented by Agent A on /api/ws. */
export type WSMessageType = 'session_started' | 'session_updated' | 'event_appended';

/** Messages pushed over /api/ws — see docs/ws-protocol.md. */
export type WSMessage =
  | { type: 'session_started'; payload: Session }
  | { type: 'session_updated'; payload: Session }
  | { type: 'event_appended'; payload: { sessionId: number; event: Event } };
