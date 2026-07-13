// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type {
  DocDetail,
  DocMeta,
  HealthResponse,
  PermissionRequest,
  PermissionRequestStatus,
  ProjectsResponse,
  SessionDetailResponse,
  SessionsResponse,
  StatsOverview,
  StatsToday,
  TaskDetail,
  TasksResponse,
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

export type ApprovalAction = 'approve' | 'deny';

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
): Promise<PermissionRequest> {
  if (MOCK) return mockApi.resolveApproval(id, action, reason);
  const body: { action: ApprovalAction; reason?: string } = { action };
  if (reason !== undefined && reason !== '') body.reason = reason;
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
