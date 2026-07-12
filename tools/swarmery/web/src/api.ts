// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type {
  ProjectsResponse,
  SessionDetailResponse,
  SessionsResponse,
  StatsToday,
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
