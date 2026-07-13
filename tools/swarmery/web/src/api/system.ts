// Typed client for the phase-4 /api/system/* read endpoints (step-05 frozen
// contract in ./types.ts). Lives in its own module so the System UI wave
// touches no shared files: MOCK dispatch mirrors ../api.ts, fixtures come
// from ../mock/system.ts.

import type {
  SystemCommand,
  SystemDiff,
  SystemHook,
  SystemItem,
  SystemItemDetail,
  SystemOverlays,
  SystemSummary,
} from './types';
import { MOCK } from '../api';
import { mockSystemApi } from '../mock/system';

/** Which /api/system list a request targets (agents/skills share DTOs). */
export type SystemItemsKind = 'agents' | 'skills';

export interface SystemListFilters {
  scope?: 'global' | 'project';
  project?: string;
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${String(res.status)}`);
  }
  return (await res.json()) as T;
}

function query(filters: SystemListFilters): string {
  const qs = new URLSearchParams();
  if (filters.scope !== undefined) qs.set('scope', filters.scope);
  if (filters.project !== undefined) qs.set('project', filters.project);
  const s = qs.toString();
  return s === '' ? '' : `?${s}`;
}

export function fetchSystemSummary(): Promise<SystemSummary> {
  if (MOCK) return mockSystemApi.summary();
  return get('/api/system/summary');
}

export function fetchSystemItems(
  kind: SystemItemsKind,
  filters: SystemListFilters = {},
): Promise<SystemItem[]> {
  if (MOCK) return mockSystemApi.items(kind, filters);
  return get(`/api/system/${kind}${query(filters)}`);
}

export function fetchSystemItemDetail(
  kind: SystemItemsKind,
  id: number,
): Promise<SystemItemDetail> {
  if (MOCK) return mockSystemApi.itemDetail(kind, id);
  return get(`/api/system/${kind}/${String(id)}`);
}

/** Backend unified diff between two version row ids ("" = identical). */
export function fetchSystemDiff(
  kind: SystemItemsKind,
  id: number,
  from: number,
  to: number,
): Promise<SystemDiff> {
  if (MOCK) return mockSystemApi.diff(kind, id, from, to);
  return get(`/api/system/${kind}/${String(id)}/diff?from=${String(from)}&to=${String(to)}`);
}

export function fetchSystemHooks(filters: SystemListFilters = {}): Promise<SystemHook[]> {
  if (MOCK) return mockSystemApi.hooks(filters);
  return get(`/api/system/hooks${query(filters)}`);
}

export function fetchSystemCommands(filters: SystemListFilters = {}): Promise<SystemCommand[]> {
  if (MOCK) return mockSystemApi.commands(filters);
  return get(`/api/system/commands${query(filters)}`);
}

export function fetchSystemOverlays(): Promise<SystemOverlays> {
  if (MOCK) return mockSystemApi.overlays();
  return get('/api/system/overlays');
}
