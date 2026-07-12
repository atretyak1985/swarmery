// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type { SessionDetailResponse, SessionsResponse } from './api/types';

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${res.status}`);
  }
  return (await res.json()) as T;
}

export const fetchSessions = (): Promise<SessionsResponse> => get('/api/sessions');
export const fetchSession = (id: number): Promise<SessionDetailResponse> =>
  get(`/api/sessions/${id}`);
