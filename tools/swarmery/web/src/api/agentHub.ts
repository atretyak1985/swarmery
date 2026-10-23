// Typed client for the fusion-phase-17 /api/agents/* endpoints: the READ-ONLY
// Agent Hub aggregation (roster + per-agent profile bundle). Contracts in
// ./types.ts, Go DTOs in internal/api/agent_hub.go. Its own module so the Agent
// Hub UI wave touches no shared client file: MOCK dispatch mirrors ../api.ts,
// fixtures come from ../mock/agentHub.ts.
//
// There is deliberately NO write call here — Definition editing reuses the
// existing System write surface (../api/system.ts).

import type { AgentProfile, AgentRosterResp } from './types';
import { MOCK } from '../api';
import { mockAgentHubApi } from '../mock/agentHub';
import { apiFetch } from '../lib/dashboardSession';

async function get<T>(path: string): Promise<T> {
  const res = await apiFetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${String(res.status)}`);
  }
  return (await res.json()) as T;
}

/** GET /api/agents/hub?projectId= — the roster of every registered agent with
 * 30-day rollups. An optional projectId (slug or id) narrows the rollup window. */
export function fetchAgentRoster(projectId?: string): Promise<AgentRosterResp> {
  if (MOCK) return mockAgentHubApi.roster();
  const qs = projectId !== undefined && projectId !== '' ? `?projectId=${encodeURIComponent(projectId)}` : '';
  return get(`/api/agents/hub${qs}`);
}

/** GET /api/agents/{id}/hub?projectId= — the full profile bundle for one agent. */
export function fetchAgentProfile(id: number, projectId?: string): Promise<AgentProfile> {
  if (MOCK) return mockAgentHubApi.profile(id);
  const qs = projectId !== undefined && projectId !== '' ? `?projectId=${encodeURIComponent(projectId)}` : '';
  return get(`/api/agents/${String(id)}/hub${qs}`);
}
