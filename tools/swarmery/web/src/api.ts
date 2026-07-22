// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type {
  AdviseStats,
  AnalyticsDimension,
  AnalyticsMetric,
  ApprovalRule,
  AttachResponse,
  BreakdownResp,
  DetachResponse,
  DocDetail,
  DocMeta,
  DurationsResp,
  ErrorsResp,
  FileSessionsResponse,
  HealthResponse,
  MatrixResp,
  OnboardConfig,
  OnboardRequest,
  OnboardResponse,
  PermissionRequest,
  PermissionRequestStatus,
  ProjectDetail,
  ProjectMeta,
  ProjectMetaPatch,
  ProjectPluginsResponse,
  ProjectPluginToggleResponse,
  ProjectsHealthResponse,
  ProjectsResponse,
  Recommendation,
  RecommendationsResp,
  RecommendationStatus,
  RetroAgentsResp,
  RetroFrictionResp,
  RetroLessonsResp,
  RetroTasksResp,
  SearchResponse,
  SessionDetailResponse,
  SessionOutcome,
  SessionsResponse,
  StatsOverview,
  StatsToday,
  TaskDetail,
  SkillsResp,
  TasksResponse,
  TimeseriesResp,
  ToolsResp,
  ToolsResponse,
} from './api/types';
import { mockApi } from './mock/data';

/** Offline mock mode — fixture data + fake WS (VITE_MOCK=1). */
export const MOCK: boolean = import.meta.env.VITE_MOCK === '1';

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${res.status}`);
  }
  return (await res.json()) as T;
}

export interface SessionFilters {
  /** Project slug or id (server matches either). */
  project?: string;
  status?: string;
}

export function fetchProjects(includeArchived = false): Promise<ProjectsResponse> {
  if (MOCK) return mockApi.projects();
  return get(`/api/projects${includeArchived ? '?include=archived' : ''}`);
}

/** GET /api/projects/{id} — enriched project + local components + stats. */
export function fetchProject(id: number | string): Promise<ProjectDetail> {
  if (MOCK) return mockApi.project(id);
  return get(`/api/projects/${encodeURIComponent(id)}`);
}

/** DELETE /api/projects/{id} — soft-archive (remove from the default list). */
export async function archiveProject(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `archive failed: ${String(res.status)}`);
  }
}

/** POST /api/projects/{id}/restore — un-archive. */
export async function restoreProject(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/restore`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `restore failed: ${String(res.status)}`);
  }
}

/** GET /api/projects/health — per-project week-over-week health rows. */
export function fetchProjectsHealth(): Promise<ProjectsHealthResponse> {
  if (MOCK) return mockApi.projectsHealth();
  return get('/api/projects/health');
}

/** PATCH /api/projects/{id} — update pinned / tags (dashboard-only meta). */
export async function patchProject(id: number, patch: ProjectMetaPatch): Promise<ProjectMeta> {
  if (MOCK) return { pinned: patch.pinned ?? false, tags: patch.tags ?? [] };
  const res = await fetch(`/api/projects/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `update failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectMeta;
}

/**
 * POST /api/projects/{id}/detach — remove the swarmery-owned entries from the
 * project's .claude/settings.json. dryRun=true returns the plan without writing
 * (rendered as a preview before the real call); full=true also removes the
 * other onboarding artifacts (project.json, statusline scripts). 403 when the
 * endpoint is disabled (no SWARMERY_ONBOARD_ROOTS) or the path is outside the
 * allow-list.
 */
export async function detachProject(
  id: number,
  dryRun: boolean,
  full: boolean,
): Promise<DetachResponse> {
  if (MOCK) {
    return {
      detached: true,
      dryRun,
      steps: ['- enabledPlugins.core@swarmery', '- extraKnownMarketplaces.swarmery'],
      ...(dryRun ? {} : { backup: '.claude/settings.json.bak' }),
    };
  }
  const params = new URLSearchParams();
  if (dryRun) params.set('dryRun', '1');
  if (full) params.set('full', '1');
  const qs = params.size > 0 ? `?${params.toString()}` : '';
  const res = await fetch(`/api/projects/${String(id)}/detach${qs}`, {
    method: 'POST',
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `detach failed: ${String(res.status)}`);
  }
  return (await res.json()) as DetachResponse;
}

/**
 * POST /api/projects/{id}/attach — re-enable swarmery for a detached project:
 * merge the swarmery entries back into settings.json, restore project.json
 * from its .bak, reinstall hooks. dryRun=true returns the plan without writing.
 * 403 when the endpoint is disabled (no SWARMERY_ONBOARD_ROOTS / workspace
 * root) or the path is outside the allow-list.
 */
export async function attachProject(id: number, dryRun: boolean): Promise<AttachResponse> {
  if (MOCK) {
    return {
      attached: true,
      dryRun,
      steps: ['+ enabledPlugins.core@swarmery', '+ .claude/project.json restored from project.json.bak'],
      ...(dryRun ? {} : { backup: '.claude/settings.json.bak' }),
    };
  }
  const qs = dryRun ? '?dryRun=1' : '';
  const res = await fetch(`/api/projects/${String(id)}/attach${qs}`, {
    method: 'POST',
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `attach failed: ${String(res.status)}`);
  }
  return (await res.json()) as AttachResponse;
}

/** GET /api/projects/{id}/plugins — marketplace catalog × enabledPlugins. */
export function fetchProjectPlugins(id: number | string): Promise<ProjectPluginsResponse> {
  if (MOCK) return mockApi.projectPlugins();
  return get(`/api/projects/${encodeURIComponent(id)}/plugins`);
}

/**
 * PUT /api/projects/{id}/plugins/{name} — flip a pack in the project's
 * .claude/settings.json (merge-only, .bak backup on the daemon side). Takes
 * effect in the NEXT Claude Code session. 403 when the daemon write fence is
 * closed; 409 when settings.json is missing/malformed — surfaced inline.
 */
export async function toggleProjectPlugin(
  id: number,
  name: string,
  enabled: boolean,
): Promise<ProjectPluginToggleResponse> {
  if (MOCK) return { name, enabled, changed: true, backup: '.claude/settings.json.bak' };
  const res = await fetch(`/api/projects/${String(id)}/plugins/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `toggle failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectPluginToggleResponse;
}

/** GET /api/projects/onboard/config — defaults + enabled state for the modal. */
export function fetchOnboardConfig(): Promise<OnboardConfig> {
  return get('/api/projects/onboard/config');
}

/**
 * POST /api/projects/onboard — bootstrap a new consumer project (.claude/
 * settings.json + project.json + workspace namespace). The endpoint is fenced
 * to an allow-list and returns 403 when disabled; non-2xx throws the server's
 * error text so the form can surface it inline. An empty workspaceRoot falls
 * back to the server default.
 */
export async function onboardProject(
  slug: string,
  path: string,
  packs: string[],
  workspaceRoot?: string,
): Promise<OnboardResponse> {
  const body: OnboardRequest = { slug, path, packs };
  if (workspaceRoot !== undefined && workspaceRoot !== '') body.workspaceRoot = workspaceRoot;
  const res = await fetch('/api/projects/onboard', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `onboard failed: ${String(res.status)}`);
  }
  return (await res.json()) as OnboardResponse;
}

export interface SessionPageOpts {
  /** Server default 100, max 500. */
  limit?: number;
  /** nextCursor of the previous page. */
  cursor?: string;
}

export function fetchSessions(
  filters: SessionFilters = {},
  page: SessionPageOpts = {},
): Promise<SessionsResponse> {
  if (MOCK) return mockApi.sessions(filters);
  const qs = new URLSearchParams();
  if (filters.project !== undefined) qs.set('project', filters.project);
  if (filters.status !== undefined) qs.set('status', filters.status);
  if (page.limit !== undefined) qs.set('limit', String(page.limit));
  if (page.cursor !== undefined) qs.set('cursor', page.cursor);
  const query = qs.toString();
  return get(`/api/sessions${query === '' ? '' : `?${query}`}`);
}

export function fetchSession(id: number | string): Promise<SessionDetailResponse> {
  if (MOCK) return mockApi.session(id);
  return get(`/api/sessions/${encodeURIComponent(id)}`);
}

export function fetchStatsToday(): Promise<StatsToday> {
  if (MOCK) return mockApi.statsToday();
  return get('/api/stats/today');
}

/** Day-scoped overview stats + trailing series (parity contract). */
export function fetchStatsOverview(day: string, project?: string): Promise<StatsOverview> {
  if (MOCK) return mockApi.statsOverview(day);
  const qs = new URLSearchParams({ day });
  if (project !== undefined) qs.set('project', project);
  return get(`/api/stats/overview?${qs.toString()}`);
}

export function fetchHealth(): Promise<HealthResponse> {
  if (MOCK) return mockApi.health();
  return get('/api/health');
}

// --- analytics ----------------------------------------------------------------

/** Optional local-day range; the server defaults to the last 14 days. */
export interface AnalyticsRange {
  from?: string;
  to?: string;
  /** Global project scope — slug or id (server matches either). */
  project?: string;
}

function rangeQuery(range: AnalyticsRange, extra: Record<string, string>): string {
  const qs = new URLSearchParams(extra);
  if (range.from !== undefined) qs.set('from', range.from);
  if (range.to !== undefined) qs.set('to', range.to);
  if (range.project !== undefined) qs.set('project', range.project);
  return qs.toString();
}

/** Daily series for the main chart (one series per group member). */
export function fetchTimeseries(
  metric: AnalyticsMetric,
  group: AnalyticsDimension,
  range: AnalyticsRange = {},
): Promise<TimeseriesResp> {
  if (MOCK) return mockApi.timeseries(metric, group, range);
  return get(`/api/stats/timeseries?${rangeQuery(range, { metric, group })}`);
}

/** Ranked totals for the current pivot dimension. */
export function fetchBreakdown(
  by: AnalyticsDimension,
  range: AnalyticsRange = {},
): Promise<BreakdownResp> {
  if (MOCK) return mockApi.breakdown(by, range);
  return get(`/api/stats/breakdown?${rangeQuery(range, { by })}`);
}

/** Agents|skills × projects cross-tab (metric=runs, or cost for agents). */
export function fetchMatrix(
  rows: 'agent' | 'skill',
  metric: 'runs' | 'cost' = 'runs',
  range: AnalyticsRange = {},
): Promise<MatrixResp> {
  if (MOCK) return mockApi.matrix(rows, metric, range);
  return get(`/api/stats/matrix?${rangeQuery(range, { rows, cols: 'project', metric })}`);
}

/** Per-tool call/error/denied counts + duration stats (analytics uplift).
 * `agent` optionally narrows every row + column to one attributed agent. */
export function fetchToolStats(range: AnalyticsRange = {}, agent?: string): Promise<ToolsResp> {
  if (MOCK) return mockApi.toolStats(range, agent);
  return get(`/api/stats/tools?${rangeQuery(range, agent ? { agent } : {})}`);
}

/** Per-skill invocation/error/denied counts + duration stats (analytics uplift).
 * `agent` optionally narrows every row + column to one attributed agent. */
export function fetchSkillStats(range: AnalyticsRange = {}, agent?: string): Promise<SkillsResp> {
  if (MOCK) return mockApi.skillStats(range, agent);
  return get(`/api/stats/skills?${rangeQuery(range, agent ? { agent } : {})}`);
}

/** Session-duration + approval-wait aggregates (analytics uplift). */
export function fetchDurations(range: AnalyticsRange = {}): Promise<DurationsResp> {
  if (MOCK) return mockApi.durations(range);
  return get(`/api/stats/durations?${rangeQuery(range, {})}`);
}

/** Error events grouped by normalized message key (analytics uplift). */
export function fetchErrorGroups(range: AnalyticsRange = {}): Promise<ErrorsResp> {
  if (MOCK) return mockApi.errorGroups(range);
  return get(`/api/stats/errors?${rangeQuery(range, {})}`);
}

// --- retro loop (per-agent scorecards + friction board) -----------------------

/** Per-agent health scorecards + previous-window comparison (retro loop). */
export function fetchRetroAgents(range: AnalyticsRange = {}): Promise<RetroAgentsResp> {
  if (MOCK) return mockApi.retroAgents();
  return get(`/api/retro/agents?${rangeQuery(range, {})}`);
}

/** Friction board: denied tools, top error groups, approval waits (retro loop). */
export function fetchRetroFriction(range: AnalyticsRange = {}): Promise<RetroFrictionResp> {
  if (MOCK) return mockApi.retroFriction();
  return get(`/api/retro/friction?${rangeQuery(range, {})}`);
}

/** Lessons-learned feed parsed from 09-retrospective.md docs (retro phase 2). */
export function fetchRetroLessons(range: AnalyticsRange = {}): Promise<RetroLessonsResp> {
  if (MOCK) return mockApi.retroLessons();
  return get(`/api/retro/lessons?${rangeQuery(range, {})}`);
}

/** Estimation accuracy + loop/delegation counts per task (retro phase 2). */
export function fetchRetroTasks(range: AnalyticsRange = {}): Promise<RetroTasksResp> {
  if (MOCK) return mockApi.retroTasks();
  return get(`/api/retro/tasks?${rangeQuery(range, {})}`);
}

/**
 * Advisor recommendations (retro phase 3). `status` is a CSV filter or 'all';
 * the server defaults to the actionable set (proposed,accepted,adopted).
 */
export function fetchRecommendations(status?: string): Promise<RecommendationsResp> {
  if (MOCK) return mockApi.retroRecommendations();
  const qs = status !== undefined ? `?status=${encodeURIComponent(status)}` : '';
  return get(`/api/retro/recommendations${qs}`);
}

/**
 * PATCH /api/retro/recommendations/{id} — accept or dismiss one proposal.
 * Illegal transitions come back 422 with an {error} body.
 */
export async function patchRecommendation(
  id: number,
  status: Extract<RecommendationStatus, 'accepted' | 'dismissed'>,
): Promise<Recommendation> {
  if (MOCK) return mockApi.patchRecommendation(id, status);
  const res = await fetch(`/api/retro/recommendations/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `patch recommendation failed: ${String(res.status)}`);
  }
  return (await res.json()) as Recommendation;
}

/** POST /api/retro/advise — run the advisor rule engine now ("Analyze now"). */
export async function runAdvise(): Promise<AdviseStats> {
  if (MOCK) return mockApi.advise();
  const res = await fetch('/api/retro/advise', { method: 'POST' });
  if (!res.ok) throw new Error(`advise failed: ${String(res.status)}`);
  return (await res.json()) as AdviseStats;
}

export function fetchDocs(): Promise<DocMeta[]> {
  if (MOCK) return mockApi.docs();
  return get('/api/docs');
}

export function fetchDoc(slug: string): Promise<DocDetail> {
  if (MOCK) return mockApi.doc(slug);
  return get(`/api/docs/${encodeURIComponent(slug)}`);
}

// --- phase 3.5: workspaces ----------------------------------------------------

/** Recently active workspace tasks (default window: 14 days). */
export function fetchTasks(days = 14): Promise<TasksResponse> {
  if (MOCK) return mockApi.tasks();
  return get(`/api/tasks?days=${String(days)}`);
}

/** One workspace task: card metadata + linked sessions + Σ cost. */
export function fetchTask(id: number | string): Promise<TaskDetail> {
  if (MOCK) return mockApi.task(id);
  return get(`/api/tasks/${encodeURIComponent(id)}`);
}

// --- phase 2 — approvals (docs/hooks-protocol.md; DTO frozen in api/types.ts) ---

/**
 * `resolved` is a meta-filter covering every terminal status — assumed
 * server-side (see web/CONTRACT-REQUESTS.md); the UI only ever asks for
 * `pending` and `resolved`.
 */
export type ApprovalStatusFilter = PermissionRequestStatus | 'resolved';

/**
 * `answer` resolves an AskUserQuestion with per-question answers; `terminal`
 * is the no-decision handoff to the native terminal selector (E12d/E12e —
 * a plain approve would resolve the questions unanswered).
 */
export type ApprovalAction = 'approve' | 'deny' | 'answer' | 'terminal';

/** {action:"answer"} answers: string, or an array of labels for multiSelect. */
export type ApprovalAnswers = Record<string, string | string[]>;

export function fetchApprovals(status?: ApprovalStatusFilter): Promise<PermissionRequest[]> {
  if (MOCK) return mockApi.approvals(status);
  const qs = status !== undefined ? `?status=${encodeURIComponent(status)}` : '';
  return get(`/api/approvals${qs}`);
}

/**
 * POST /api/approvals/{id} → 200 with the updated PermissionRequest.
 * Non-2xx (e.g. 409 when the row raced to a terminal state via the terminal
 * dialog or expiry) throws — callers silently refetch; the WS
 * permission_resolved is the authoritative reconciliation either way.
 */
export async function resolveApproval(
  id: number,
  action: ApprovalAction,
  reason?: string,
  answers?: ApprovalAnswers,
): Promise<PermissionRequest> {
  if (MOCK) return mockApi.resolveApproval(id, action, reason, answers);
  const body: { action: ApprovalAction; reason?: string; answers?: ApprovalAnswers } = { action };
  if (reason !== undefined && reason !== '') body.reason = reason;
  if (answers !== undefined) body.answers = answers;
  const res = await fetch(`/api/approvals/${String(id)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`POST /api/approvals/${String(id)}: ${String(res.status)}`);
  }
  return (await res.json()) as PermissionRequest;
}

// --- control-plane v2 — auto-approve rules ------------------------------------

export interface ApprovalRuleInput {
  projectId: number | null;
  toolPattern: string;
  note?: string;
}

export function fetchApprovalRules(): Promise<ApprovalRule[]> {
  if (MOCK) return Promise.resolve([]);
  return get('/api/approval-rules');
}

export async function createApprovalRule(input: ApprovalRuleInput): Promise<ApprovalRule> {
  const res = await fetch('/api/approval-rules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `create rule failed: ${String(res.status)}`);
  }
  return (await res.json()) as ApprovalRule;
}

export async function toggleApprovalRule(id: number, enabled: boolean): Promise<ApprovalRule> {
  const res = await fetch(`/api/approval-rules/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw new Error(`toggle rule failed: ${String(res.status)}`);
  return (await res.json()) as ApprovalRule;
}

export async function deleteApprovalRule(id: number): Promise<void> {
  const res = await fetch(`/api/approval-rules/${String(id)}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`delete rule failed: ${String(res.status)}`);
}

/** POST /api/sessions/{id}/kill — send SIGTERM (force=false) or SIGKILL (force=true). */
export async function killSession(id: number, force = false): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/kill`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ force }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `kill failed: ${String(res.status)}`);
  }
}

/** POST /api/sessions/{id}/stop — graceful SIGTERM; the session is recorded
 * as 'completed' (not 'killed'). Works even when no PID is known. */
export async function stopSession(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/stop`, { method: 'POST' });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `stop failed: ${String(res.status)}`);
  }
}

/**
 * POST /api/sessions/{id}/message — resume an idle/completed conversation
 * headlessly (`claude -r <uuid> -p <text>`). Returns 202 immediately; the
 * resulting user + assistant turns arrive on the open detail via the WS bus
 * once the ingest watcher tails the transcript. Non-2xx (409 for a live
 * session, 503 when the claude binary is missing) throws with the server's
 * error text so the composer can surface it inline.
 */
export async function sendSessionMessage(id: number, text: string): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/message`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `send failed: ${String(res.status)}`);
  }
}

/**
 * POST /api/sessions/{id}/message/cancel — abort the in-flight headless resume
 * run (kills the child claude process). 409 when nothing is in flight.
 */
export async function cancelSessionMessage(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/message/cancel`, { method: 'POST' });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `cancel failed: ${String(res.status)}`);
  }
}

/**
 * PATCH /api/sessions/{id} — set or clear (null) the manual session outcome.
 */
export async function patchSessionOutcome(
  id: number,
  outcome: SessionOutcome | null,
): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ outcome }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `outcome failed: ${String(res.status)}`);
  }
}

/**
 * PATCH /api/sessions/{id} — rename a session. A blank/null title clears the
 * override and reverts to the ingested ai-title.
 */
export async function renameSession(id: number, title: string | null): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `rename failed: ${String(res.status)}`);
  }
}

// --- tool dashboards (serena LSP dashboard + graphify viz) ----------------------

/** GET /api/tools — sidebar feed for daemon-managed tool dashboards. */
export function fetchTools(): Promise<ToolsResponse> {
  if (MOCK) return mockApi.tools();
  return get('/api/tools');
}

/**
 * POST /api/projects/{id}/serena/start — launch the project's serena dashboard
 * process. Non-2xx (403 fence closed, 404 no lsp-pack, 409 already running,
 * 503 binary missing) throws the server's {error} text for inline display.
 */
export async function serenaStart(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/serena/start`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `serena start failed: ${String(res.status)}`);
  }
}

/** POST /api/projects/{id}/serena/stop — stop the dashboard process. */
export async function serenaStop(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/serena/stop`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `serena stop failed: ${String(res.status)}`);
  }
}

// --- global search (Cmd+K palette) ---------------------------------------------

/**
 * GET /api/search — grouped global search (sessions / turns / files /
 * projects). Mock mode returns empty groups so the palette still renders its
 * static Navigation section offline.
 */
export function fetchSearch(q: string, project?: string, limit = 20): Promise<SearchResponse> {
  if (MOCK) {
    return Promise.resolve({ query: q, sessions: [], turns: [], files: [], projects: [] });
  }
  const qs = new URLSearchParams({ q, limit: String(limit) });
  if (project !== undefined && project !== '') qs.set('project', project);
  return get(`/api/search?${qs.toString()}`);
}

/** GET /api/files/sessions — sessions that touched files matching `path`. */
export function fetchFileSessions(path: string, project?: string): Promise<FileSessionsResponse> {
  if (MOCK) return Promise.resolve({ path, sessions: [] });
  const qs = new URLSearchParams({ path });
  if (project !== undefined && project !== '') qs.set('project', project);
  return get(`/api/files/sessions?${qs.toString()}`);
}
