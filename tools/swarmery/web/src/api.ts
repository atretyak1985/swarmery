// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type {
  AnalyticsDimension,
  AnalyticsMetric,
  BreakdownResp,
  DocDetail,
  DocMeta,
  HealthResponse,
  MatrixResp,
  OnboardResponse,
  PermissionRequest,
  PermissionRequestStatus,
  ProjectsResponse,
  SessionDetailResponse,
  SessionsResponse,
  StatsOverview,
  StatsToday,
  TaskDetail,
  TasksResponse,
  TimeseriesResp,
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

export function fetchProjects(): Promise<ProjectsResponse> {
  if (MOCK) return mockApi.projects();
  return get('/api/projects');
}

/**
 * POST /api/projects/onboard — bootstrap a new consumer project (.claude/
 * settings.json + project.json + workspace namespace). The endpoint is fenced
 * to an allow-list and returns 403 when disabled; non-2xx throws the server's
 * error text so the form can surface it inline.
 */
export async function onboardProject(
  slug: string,
  path: string,
  packs: string[],
): Promise<OnboardResponse> {
  const res = await fetch('/api/projects/onboard', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug, path, packs }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `onboard failed: ${String(res.status)}`);
  }
  return (await res.json()) as OnboardResponse;
}

export function fetchSessions(filters: SessionFilters = {}): Promise<SessionsResponse> {
  if (MOCK) return mockApi.sessions(filters);
  const qs = new URLSearchParams();
  if (filters.project !== undefined) qs.set('project', filters.project);
  if (filters.status !== undefined) qs.set('status', filters.status);
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
export function fetchStatsOverview(day: string): Promise<StatsOverview> {
  if (MOCK) return mockApi.statsOverview(day);
  return get(`/api/stats/overview?day=${encodeURIComponent(day)}`);
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
}

function rangeQuery(range: AnalyticsRange, extra: Record<string, string>): string {
  const qs = new URLSearchParams(extra);
  if (range.from !== undefined) qs.set('from', range.from);
  if (range.to !== undefined) qs.set('to', range.to);
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
