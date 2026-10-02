// Offline fixtures for the fusion-phase-17 /api/agents/* endpoints (VITE_MOCK=1)
// — mirror the DTOs frozen in ../api/types.ts so the Agent Hub develops without
// the daemon. Three agents, one deliberately unhealthy (high failedShare) per
// the phase spec. Same plain-fixture + per-call-delay pattern as ./system.ts.

import type {
  AgentActivity,
  AgentProfile,
  AgentRosterResp,
  AgentRosterRow,
  AgentRun,
  AgentTask,
} from '../api/types';

const delay = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

function daysAgoISO(n: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return d.toISOString();
}

function isoDay(n: number): string {
  return daysAgoISO(n).slice(0, 10);
}

const ROSTER: AgentRosterRow[] = [
  {
    id: 1,
    name: 'core:tech-lead',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: 'opus',
    path: '/plugins/core/agents/tech-lead.md',
    description: 'Primary 9-phase orchestrator',
    role: 'orchestrate',
    enabledInProject: true,
    improvable: true,
    runs30d: 128,
    successRate: 0.91,
    failedShare: 0.12,
    cost30d: 42.18,
    lastActiveAt: daysAgoISO(0),
  },
  {
    id: 2,
    name: 'core:implementation-agent',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: 'sonnet',
    path: '/plugins/core/agents/implementation-agent.md',
    description: 'Primary code executor',
    role: 'implement',
    enabledInProject: true,
    improvable: true,
    // The unhealthy one — failedShare above the red threshold.
    runs30d: 96,
    successRate: 0.58,
    failedShare: 0.71,
    cost30d: 61.4,
    lastActiveAt: daysAgoISO(1),
  },
  {
    id: 3,
    name: 'debugger',
    scope: 'global',
    projectSlug: null,
    origin: 'local',
    pluginName: null,
    model: 'sonnet',
    path: '/agents/debugger.md',
    description: 'Root-cause analysis',
    role: 'domain',
    enabledInProject: true,
    improvable: false,
    runs30d: 14,
    successRate: null,
    failedShare: 0.21,
    cost30d: 3.9,
    lastActiveAt: daysAgoISO(4),
  },
];

function runsByDay(total: number): { day: string; runs: number }[] {
  const out: { day: string; runs: number }[] = [];
  for (let i = 29; i >= 0; i--) {
    // A gentle deterministic wave so the sparkline is legible in demos.
    out.push({ day: isoDay(i), runs: Math.max(0, Math.round((total / 30) * (1 + Math.sin(i / 3)))) });
  }
  return out;
}

const RUNS: AgentRun[] = [
  {
    ts: daysAgoISO(0),
    projectSlug: 'alpha',
    sessionUuid: 'sess-a1',
    sessionTitle: 'Agent hub aggregation',
    description: 'plan the roster endpoint',
    status: 'ok',
    durationMs: 184_000,
  },
  {
    ts: daysAgoISO(1),
    projectSlug: 'alpha',
    sessionUuid: 'sess-a2',
    sessionTitle: 'Retro helper extraction',
    description: 'share the scorecard SQL',
    status: 'error',
    durationMs: 92_000,
  },
  {
    ts: daysAgoISO(2),
    projectSlug: 'beta',
    sessionUuid: 'sess-b1',
    sessionTitle: 'System editor embed',
    description: 'wire the Definition tab',
    status: 'ok',
    durationMs: 240_500,
  },
];

const ACTIVITY: AgentActivity[] = [
  { ts: daysAgoISO(0), type: 'subagent_start', toolName: null, status: 'ok', sessionUuid: 'sess-a1', projectSlug: 'alpha' },
  { ts: daysAgoISO(0), type: 'tool_call', toolName: 'Edit', status: 'ok', sessionUuid: 'sess-a1', projectSlug: 'alpha' },
  { ts: daysAgoISO(1), type: 'subagent_stop', toolName: 'Agent', status: 'error', sessionUuid: 'sess-a2', projectSlug: 'alpha' },
  { ts: daysAgoISO(2), type: 'skill_use', toolName: null, status: 'ok', sessionUuid: 'sess-b1', projectSlug: 'beta' },
];

const TASKS: AgentTask[] = [
  { externalId: '2026-07-24-agent-hub', title: 'Ship the Agent Hub', status: 'done', source: 'delegation', phase: '4', verdict: 'OK', startedAt: daysAgoISO(0) },
  { externalId: '2026-07-24-agent-hub', title: 'Ship the Agent Hub', status: 'done', source: 'delegation', phase: '5', verdict: 'RE-DISPATCH', startedAt: daysAgoISO(0) },
];

// ROSTER is a non-empty literal, so index 0 is always present; the explicit
// narrowing keeps `base` non-optional under noUncheckedIndexedAccess.
const FIRST: AgentRosterRow = ROSTER[0] as AgentRosterRow;

function profileFor(id: number): AgentProfile {
  const base: AgentRosterRow = ROSTER.find((a) => a.id === id) ?? FIRST;
  return {
    ...base,
    overview: {
      runs30d: base.runs30d,
      successRate: base.successRate,
      failedShare: base.failedShare,
      cost30d: base.cost30d,
      tokensOut30d: base.runs30d * 1800,
      lastActiveAt: base.lastActiveAt,
      avgMs: 172_000,
      p95Ms: 410_000,
      runsByDay: runsByDay(base.runs30d),
      errors: Math.round(base.runs30d * base.failedShare),
      errorsByClass: { behavior_fixable: Math.round(base.runs30d * base.failedShare), infra_noise: 3 },
    },
    runs: RUNS,
    activity: ACTIVITY,
    tasks: TASKS,
    insights: {
      recommendations:
        base.failedShare > 0.6
          ? [
              {
                id: 1,
                rule: 'R2',
                target_kind: 'agent',
                target: base.name,
                title: 'High behavior-failed-run share',
                detail: 'This agent’s failed-run share crossed the R2 threshold over the last 30 days.',
                evidence: { session_ids: ['sess-a2'] },
                baseline: null,
                status: 'proposed',
                created_at: daysAgoISO(1),
                updated_at: daysAgoISO(1),
              },
            ]
          : [],
      proposals: [],
      lessons: [
        {
          task_external_id: '2026-07-24-agent-hub',
          task_title: 'Ship the Agent Hub',
          date: isoDay(0),
          seq: 1,
          title: 'Read before edit',
          action: 'always Read a file before Edit',
          body: null,
        },
      ],
    },
  };
}

export const mockAgentHubApi = {
  async roster(): Promise<AgentRosterResp> {
    await delay(110);
    return { agents: ROSTER };
  },
  async profile(id: number): Promise<AgentProfile> {
    await delay(140);
    return profileFor(id);
  },
};
