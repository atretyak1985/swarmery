// Typed client for GET /api/worktrees — the worktree janitor's live inventory
// plus its sweep journal (Go DTOs in internal/api/worktrees.go).
//
// Read-only by design: the janitor decides on its own schedule against proof it
// gathers itself, so there is deliberately no "clean now" write path here.

import type { WorktreesResponse } from './types';
import { MOCK } from '../api';
import { apiFetch } from '../lib/dashboardSession';

/** GET /api/worktrees?project= — inventory + recent decisions. */
export async function fetchWorktrees(project?: string): Promise<WorktreesResponse> {
  if (MOCK) return { live: [], sweeps: [], enabled: true };
  const qs =
    project !== undefined && project !== '' ? `?project=${encodeURIComponent(project)}` : '';
  const res = await apiFetch(`/api/worktrees${qs}`);
  if (!res.ok) {
    throw new Error(`GET /api/worktrees: ${String(res.status)}`);
  }
  return (await res.json()) as WorktreesResponse;
}
