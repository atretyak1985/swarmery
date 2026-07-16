// Typed client for the phase-4 /api/system/* endpoints: the step-05 read
// surface plus the Stage 2 write surface (steps 09–12, contracts in
// ./types.ts). Lives in its own module so the System UI wave touches no
// shared files: MOCK dispatch mirrors ../api.ts, fixtures come from
// ../mock/system.ts.
//
// Write discipline (step-12 contract): a 409 conflict is resolved ONLY by an
// explicit refetch that yields a NEW base_hash — no code path in this module
// (or its callers) retries a write after 409 with the same base_hash, and no
// force-overwrite parameter exists in the API, intentionally.

import type {
  AgentHistory,
  SystemCommand,
  SystemConflict,
  SystemCreateAgentRequest,
  SystemCreateResponse,
  SystemDiff,
  SystemHook,
  SystemInsights,
  SystemItem,
  SystemItemDetail,
  SystemOverlays,
  SystemRestoreResponse,
  SystemRollbackRequest,
  SystemSummary,
  SystemWriteRequest,
  SystemWriteResponse,
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

/** Per-agent run history & statistics (folded by name, across all projects). */
export function fetchAgentHistory(id: number, days = 90): Promise<AgentHistory> {
  if (MOCK) return mockSystemApi.agentHistory(id, days);
  return get(`/api/system/agents/${String(id)}/history?days=${String(days)}`);
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

/** Promotion & drift detector — read-only insight lists (display-only UI). */
export function fetchSystemInsights(): Promise<SystemInsights> {
  if (MOCK) return mockSystemApi.insights();
  return get('/api/system/insights');
}

// --- Stage 2 write surface (steps 09–12) --------------------------------------

/**
 * Typed failure of a system write. Carries the parsed JSON error body so the
 * UI can branch on the step-09/10/11 contract: 409 with {disk_hash, base_hash,
 * diff} → conflict banner, 409 with {restore_id} → duplicate-of-soft-deleted
 * hint, 403 → plugin-managed vs readonly, 422 → parse/name error.
 */
export class SystemWriteError extends Error {
  readonly status: number;
  /** Parsed 409 conflict body (disk drift) — null for every other failure. */
  readonly conflict: SystemConflict | null;
  /** Soft-deleted duplicate hint of POST /api/system/agents (409). */
  readonly restoreId: number | null;

  constructor(status: number, body: Record<string, unknown>, context: string) {
    const msg = typeof body['error'] === 'string' ? body['error'] : `${context}: ${String(status)}`;
    super(msg);
    this.name = 'SystemWriteError';
    this.status = status;
    this.conflict =
      status === 409 && typeof body['disk_hash'] === 'string' && typeof body['diff'] === 'string'
        ? {
            error: msg,
            disk_hash: body['disk_hash'],
            base_hash: typeof body['base_hash'] === 'string' ? body['base_hash'] : '',
            diff: body['diff'],
          }
        : null;
    this.restoreId = typeof body['restore_id'] === 'number' ? body['restore_id'] : null;
  }

  /** Which 403 tier this is: plugin/installer-managed vs global readonly. */
  get forbidden(): 'readonly' | 'managed' | null {
    if (this.status !== 403) return null;
    return /readonly/i.test(this.message) ? 'readonly' : 'managed';
  }
}

/**
 * One guarded write call. Throws SystemWriteError on any non-2xx — there is
 * deliberately NO retry logic here: a 409 must bubble to the UI, which
 * resolves it only by refetching and editing on top of the new base_hash.
 */
async function writeCall<T>(path: string, method: 'PUT' | 'POST' | 'DELETE', body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  if (res.ok) return (await res.json()) as T;
  let payload: Record<string, unknown> = {};
  try {
    payload = (await res.json()) as Record<string, unknown>;
  } catch {
    // non-JSON error body — the status-based message fallback applies
  }
  throw new SystemWriteError(res.status, payload, `${method} ${path}`);
}

/** PUT /api/system/{agents|skills}/{id} → 200 {version_id, lint[]}. */
export function putSystemItem(
  kind: SystemItemsKind,
  id: number,
  req: SystemWriteRequest,
): Promise<SystemWriteResponse> {
  if (MOCK) return mockSystemApi.putItem(kind, id, req);
  return writeCall(`/api/system/${kind}/${String(id)}`, 'PUT', req);
}

/** POST .../{id}/rollback → 200 {version_id, lint[]} (append-style write). */
export function rollbackSystemItem(
  kind: SystemItemsKind,
  id: number,
  req: SystemRollbackRequest,
): Promise<SystemWriteResponse> {
  if (MOCK) return mockSystemApi.rollbackItem(kind, id, req);
  return writeCall(`/api/system/${kind}/${String(id)}/rollback`, 'POST', req);
}

/** POST /api/system/agents → 201 {id, version_id, lint[]}. */
export function createSystemAgent(req: SystemCreateAgentRequest): Promise<SystemCreateResponse> {
  if (MOCK) return mockSystemApi.createAgent(req);
  return writeCall('/api/system/agents', 'POST', req);
}

/** DELETE /api/system/agents/{id} — soft delete (file moves into backups). */
export function deleteSystemAgent(id: number): Promise<{ deleted: boolean }> {
  if (MOCK) return mockSystemApi.deleteAgent(id);
  return writeCall(`/api/system/agents/${String(id)}`, 'DELETE');
}

/** POST /api/system/agents/{id}/restore → 200 {id, version_id}. */
export function restoreSystemAgent(id: number): Promise<SystemRestoreResponse> {
  if (MOCK) return mockSystemApi.restoreAgent(id);
  return writeCall(`/api/system/agents/${String(id)}/restore`, 'POST');
}

/** POST /api/system/hooks/{id}/toggle {enabled, base_hash} → {status: ok}. */
export function toggleSystemHook(
  id: number,
  enabled: boolean,
  baseHash: string,
): Promise<{ status: string }> {
  if (MOCK) return mockSystemApi.toggleHook(id, enabled, baseHash);
  return writeCall(`/api/system/hooks/${String(id)}/toggle`, 'POST', {
    enabled,
    base_hash: baseHash,
  });
}

/** PUT /api/system/hooks/{id} {command, timeout?, base_hash} → {status: ok}. */
export function updateSystemHook(
  id: number,
  command: string,
  timeout: number | null,
  baseHash: string,
): Promise<{ status: string }> {
  if (MOCK) return mockSystemApi.updateHook(id, command, timeout, baseHash);
  const body: { command: string; timeout?: number; base_hash: string } = {
    command,
    base_hash: baseHash,
  };
  if (timeout !== null) body.timeout = timeout;
  return writeCall(`/api/system/hooks/${String(id)}`, 'PUT', body);
}

/**
 * base_hash of an agents/skills detail: the contentHash of the CURRENT
 * version, held in editor state from the moment Edit opens — that snapshot
 * (not a later refetch) is what makes the 409 protection real.
 */
export function currentContentHash(detail: SystemItemDetail): string | null {
  if (detail.currentVersionId === null) return null;
  return detail.versions.find((v) => v.id === detail.currentVersionId)?.contentHash ?? null;
}
