// The session rail's derivations (Swarmery v4). The claims:
//
//   1. swarmRows: the main model first, its note by state; subagents from their
//      start and stop events, the latest few only, the total still counting them all.
//   2. linkedRows: the plan and the tracker task, and nothing invented when neither.
//   3. touchedAreas: paths lose the cwd, group on two segments, cap at four areas.
//   4. accountWindow: the session's own subscription, the same window rule as the
//      footer, and nothing rather than another account's numbers.
//   5. isHeavy and isHotHandoff draw their lines where the design does.
//
// Runs with the rest of the web suite: `npm test`.

import { describe, expect, it } from 'vitest';
import type { Event, FileChange, SessionDetail, UsageAccount, UsageProvider } from '../api/types';
import {
  accountWindow,
  isHeavy,
  isHotHandoff,
  linkedRows,
  mapLimited,
  swarmRows,
  touchedAreas,
} from './sessionRail';

const NOW = Date.parse('2026-10-31T12:00:00Z');

function detail(over: Partial<SessionDetail> = {}): SessionDetail {
  return {
    id: 1,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: 'uuid-1',
    model: 'claude-opus-4-8',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'main',
    cwd: '/work/repo',
    status: 'active',
    startedAt: '2026-10-31T11:00:00Z',
    endedAt: '2026-10-31T11:48:00Z',
    title: 'one',
    source: 'jsonl',
    turns: [],
    events: [],
    fileChanges: [],
    recovered: 0,
    ...over,
  } as SessionDetail;
}

function event(id: number, over: Partial<Event>): Event {
  return {
    id,
    turnId: 1,
    ts: '2026-10-31T11:10:00Z',
    type: 'tool_call',
    toolName: null,
    parentEventId: null,
    status: 'ok',
    durationMs: null,
    payload: {},
    ...over,
  } as Event;
}

const start = (id: number, name: string, description?: string): Event =>
  event(id, { type: 'subagent_start', payload: { agentType: name, description } });
const stop = (id: number, parent: number, status: string, durationMs: number | null = null): Event =>
  event(id, { type: 'subagent_stop', parentEventId: parent, status, durationMs } as Partial<Event>);

const change = (filePath: string): FileChange => ({ filePath }) as FileChange;

describe('swarmRows', () => {
  it('leads with the last model and says what it is doing', () => {
    const needs = swarmRows(detail(), 'needs', NOW);
    expect(needs.rows).toEqual([
      { key: 'main', name: 'claude-opus-5-5', state: 'needs', note: 'paused on your decision' },
    ]);
    expect(needs.total).toBe(1);
    const working = swarmRows(detail({ events: [event(1, {}), event(2, {})] }), 'working', NOW);
    expect(working.rows[0]?.note).toBe('main · step 2');
    expect(swarmRows(detail(), 'done', NOW).rows[0]?.note).toBe('12m');
  });

  it('reads subagents from their start and stop events', () => {
    const events = [
      start(1, 'Explore', 'read 7 files'),
      stop(2, 1, 'ok', 14_000),
      start(3, 'test-runner', '2 failed'),
      stop(4, 3, 'error'),
      start(5, 'reviewer'),
    ];
    const { rows } = swarmRows(detail({ events }), 'working', NOW);
    expect(rows.slice(1).map((r) => [r.name, r.state])).toEqual([
      ['Explore', 'done'],
      ['test-runner', 'failed'],
      ['reviewer', 'working'],
    ]);
    expect(rows[1]?.note).toContain('read 7 files · ');
    expect(rows[2]?.note).toBe('2 failed');
  });

  it('does not call a subagent working once its session has ended', () => {
    const { rows } = swarmRows(detail({ events: [start(1, 'Explore')] }), 'done', NOW);
    expect(rows[1]?.state).toBe('stopped');
  });

  it('shows the latest five and keeps counting the rest', () => {
    const events = Array.from({ length: 8 }, (_, i) => start(i + 1, `agent-${String(i + 1)}`));
    const swarm = swarmRows(detail({ events }), 'working', NOW);
    expect(swarm.rows.map((r) => r.name)).toEqual([
      'claude-opus-5-5',
      'agent-4',
      'agent-5',
      'agent-6',
      'agent-7',
      'agent-8',
    ]);
    expect(swarm.total).toBe(9);
    expect(swarm.hidden).toBe(3);
  });
});

describe('linkedRows', () => {
  const planGroup = { taskId: 7, title: 'Rate limiter', role: 'phase', phaseId: 30, phaseSeq: 3, phaseName: 'Wire it' } as const;

  it('links a phase session to its phase of the plan', () => {
    expect(linkedRows(detail({ planGroup }), 'orders-api')).toEqual([
      { key: 'plan', text: 'Rate limiter', sub: 'plan · phase 3', to: '/p/orders-api/plans?task=7&phase=3' },
    ]);
  });

  it('links a controller to the plan alone', () => {
    const [row] = linkedRows(detail({ planGroup: { ...planGroup, role: 'controller', phaseSeq: null } }), 'orders-api');
    expect(row?.sub).toBe('plan run');
    expect(row?.to).toBe('/p/orders-api/plans?task=7');
  });

  it('adds the tracker task, unlinked', () => {
    expect(linkedRows(detail({ taskExternalId: 'SHOP-12' }), 'orders-api')).toEqual([
      { key: 'task', text: 'SHOP-12', sub: 'tracker task' },
    ]);
  });

  it('has nothing to say about an ad-hoc session', () => {
    expect(linkedRows(detail(), 'orders-api')).toEqual([]);
  });
});

describe('touchedAreas', () => {
  it('cuts the cwd and groups on the first two segments', () => {
    const touched = touchedAreas(
      [change('/work/repo/web/src/lib/ws.ts'), change('/work/repo/web/src/App.tsx'), change('services/orders/a.go')],
      '/work/repo',
    );
    expect(touched).toEqual({
      areas: [
        { area: 'web/src', files: 2 },
        { area: 'services/orders', files: 1 },
      ],
      total: 3,
    });
  });

  it('counts a file edited twice once', () => {
    expect(touchedAreas([change('a/b/c.ts'), change('a/b/c.ts')], '/w').total).toBe(1);
  });

  it('keeps a file at the root and a path outside the cwd readable', () => {
    const touched = touchedAreas([change('/work/repo/README.md'), change('/tmp/out/x.log')], '/work/repo/');
    expect(touched.areas.map((a) => a.area)).toEqual(['README.md', 'tmp/out']);
  });

  it('keeps every area, leaving the cap to the display', () => {
    const changes = ['a', 'b', 'c', 'd', 'e', 'f'].map((dir) => change(`${dir}/x/y.ts`));
    const touched = touchedAreas(changes, null);
    expect(touched.areas).toHaveLength(6);
    expect(touched.total).toBe(6);
  });

  it('puts the busiest areas first, and the first touched first on a tie', () => {
    const changes = ['a/x/1', 'b/x/1', 'c/x/1', 'c/x/2', 'c/x/3', 'd/x/1', 'd/x/2'].map(change);
    expect(touchedAreas(changes, null).areas.map((a) => a.area)).toEqual(['c/x', 'd/x', 'a/x', 'b/x']);
  });

  it('is empty before anything was touched', () => {
    expect(touchedAreas([], '/w')).toEqual({ areas: [], total: 0 });
  });
});

describe('mapLimited', () => {
  it('keeps the order and never runs more than the limit at once', async () => {
    let running = 0;
    let peak = 0;
    const results = await mapLimited([1, 2, 3, 4, 5, 6], 2, async (n) => {
      running += 1;
      peak = Math.max(peak, running);
      await new Promise((resolve) => setTimeout(resolve, (7 - n) % 3));
      running -= 1;
      return n * 10;
    });
    expect(results).toEqual([10, 20, 30, 40, 50, 60]);
    expect(peak).toBe(2);
  });

  it('rejects when one does', async () => {
    await expect(mapLimited([1, 2], 2, async (n) => (n === 2 ? Promise.reject(new Error('no')) : n))).rejects.toThrow('no');
  });
});

describe('accountWindow', () => {
  const session = { key: 'session_5h', percentUsed: 41 };
  const weekly = { key: 'weekly', percentUsed: 12 };
  const provider = (over: Partial<UsageProvider>): UsageProvider =>
    ({ account: 'default', name: 'Claude', status: 'ok', source: 'oauth', windows: [weekly, session], ...over }) as UsageProvider;
  const accounts: UsageAccount[] = [
    { account: 'default', providers: [provider({})] },
    { account: 'work', providers: [provider({ account: 'work', status: 'no-auth', windows: [] })] },
  ];

  it('takes the session window of the account the session ran under', () => {
    expect(accountWindow(accounts, [], 'default')?.percentUsed).toBe(41);
  });

  it('falls back to the first window', () => {
    const only = [{ account: 'default', providers: [provider({ windows: [weekly] })] }] as UsageAccount[];
    expect(accountWindow(only, [], 'default')?.key).toBe('weekly');
  });

  it('shows no numbers for an account with no healthy card, never another account’s', () => {
    expect(accountWindow(accounts, [provider({})], 'work')).toBeNull();
    expect(accountWindow(accounts, [provider({})], 'unknown')).toBeNull();
  });

  it('reads the default alias when the payload has no accounts row', () => {
    expect(accountWindow([], [provider({})], 'default')?.percentUsed).toBe(41);
  });
});

describe('isHeavy', () => {
  it('starts past $20', () => {
    expect(isHeavy(20)).toBe(false);
    expect(isHeavy(20.01)).toBe(true);
    expect(isHeavy(null)).toBe(false);
  });
});

describe('isHotHandoff', () => {
  const handoff = { path: '/h.md', createdAt: '2026-10-31T11:00:00Z', contextTokens: 100_000 };

  it('turns on the context percentage or the brief’s own token count', () => {
    expect(isHotHandoff(detail({ handoff }))).toBe(false);
    expect(isHotHandoff(detail({ handoff: { ...handoff, contextTokens: 300_000 } }))).toBe(true);
    expect(isHotHandoff(detail({ handoff, link: { contextPct: 55 } as SessionDetail['link'] }))).toBe(true);
  });
});
