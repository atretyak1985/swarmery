// The feed's claims: a blocker opens the thing that answers it, nothing is told
// twice, what the queue already holds wins over the session and the phase, and only
// today and yesterday show.

import { describe, expect, it } from 'vitest';
import type { Epic, EpicPhase, NeedsYouItem, Session } from '../../api/types';
import type { InboxItem } from '../inbox/inboxModel';
import { blockerHref, buildFeed, feedLinks, filterFeed, groupFeed, type FeedEvent, type FeedInputs } from './feedModel';

const NOW = new Date('2026-10-30T12:00:00').getTime();
const iso = (hoursAgo: number): string => new Date(NOW - hoursAgo * 3600_000).toISOString();

const links = feedLinks(null, (id) => `/sessions/${String(id)}`);

function session(id: number, over: Partial<Session>): Session {
  return {
    id,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: `uuid-${String(id)}`,
    model: null,
    modelLast: null,
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'feat/x',
    cwd: null,
    status: 'completed',
    startedAt: iso(3),
    endedAt: iso(1),
    title: `Session ${String(id)}`,
    source: 'local',
    ...over,
  } as Session;
}

function blocker(over: Partial<NeedsYouItem>): NeedsYouItem {
  return {
    kind: 'approval',
    sessionId: 1,
    sessionUuid: 'uuid-1',
    sessionName: 'Session 1',
    projectSlug: 'orders-api',
    requestId: 9,
    toolName: 'Bash',
    preview: 'npm ci',
    question: '',
    asksQuestion: false,
    blockingSince: iso(0.2),
    blockingSeconds: 720,
    termFocusUrl: null,
    suggestion: null,
    ...over,
  };
}

function phase(id: number, over: Partial<EpicPhase>): EpicPhase {
  return {
    id,
    seq: id,
    name: `Phase name ${String(id)}`,
    runState: 'idle',
    runOutcome: 'idle',
    runStartedAt: null,
    runEndedAt: null,
    runSessionUuid: null,
    ...over,
  } as EpicPhase;
}

function epic(phases: EpicPhase[]): Epic {
  return { externalId: 'plan-1', projectSlug: 'orders-api', title: 'Rate limiter', phases } as Epic;
}

const inputs = (over: Partial<FeedInputs>): FeedInputs => ({
  sessions: [],
  needs: [],
  epics: [],
  inbox: [],
  links,
  nowMs: NOW,
  ...over,
});

const event = (key: string, at: string): FeedEvent => ({
  key,
  at,
  state: 'done',
  title: key,
  summary: '',
  to: '/',
});

describe('buildFeed', () => {
  it('tells a session the queue holds once, as the blocker, and opens its gate', () => {
    const events = buildFeed(
      inputs({ sessions: [session(1, { status: 'active', endedAt: iso(0.1) })], needs: [blocker({})] }),
    );
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      title: 'Session 1 asked for a permission',
      state: 'needs',
      summary: 'orders-api · npm ci',
      to: '/sessions/1',
    });
  });

  it('marks a production deploy destructive and a guard stop a failure on its phase', () => {
    const stop = blocker({
      kind: 'phase_decision',
      sessionId: 0,
      requestId: null,
      preview: 'stopped',
      phase: { taskId: 2, planExternalId: 'plan-1', planTitle: 'Rate limiter', phaseId: 5, seq: 3, name: 'x', manualOpen: 0 },
      decision: { decisionId: 'D-9', trigger: 'criterion-impossible' } as NeedsYouItem['decision'],
    });
    const events = buildFeed(
      inputs({
        needs: [blocker({ kind: 'prod_deploy_local' }), stop],
        epics: [epic([phase(5, { runState: 'blocked', runOutcome: 'failed', runEndedAt: iso(1) })])],
      }),
    );
    expect(events.map((e) => e.title)).toEqual([
      'Session 1 asked for a destructive permission',
      'Guard stopped phase 3 · D-9',
    ]);
    expect(events[1]).toMatchObject({
      state: 'failed',
      summary: 'orders-api · criterion-impossible',
      to: '/p/orders-api/plans/plan-1/phase/3/review',
    });
  });

  it('tells a phase run once, as the phase, and opens its runs', () => {
    const events = buildFeed(
      inputs({
        sessions: [session(7, { sessionUuid: 'run-1', status: 'active', endedAt: iso(0.1) })],
        epics: [
          epic([
            phase(3, { runState: 'running', runOutcome: 'running', runStartedAt: iso(1), runSessionUuid: 'run-1' }),
            phase(4, { runState: 'done', runOutcome: 'completed', runEndedAt: iso(2) }),
            phase(6, { runState: 'done', runOutcome: 'noop', runEndedAt: iso(3) }),
            phase(8, {}),
          ]),
        ],
      }),
    );
    expect(events.map((e) => [e.title, e.state])).toEqual([
      ['Phase 3 of Rate limiter started', 'working'],
      ['Phase 4 of Rate limiter finished', 'done'],
      ['Phase 6 of Rate limiter stopped with nothing done', 'stopped'],
    ]);
    expect(events[0]?.to).toBe('/p/orders-api/plans/plan-1/phase/3/runs');
  });

  it('names what became of a session and sorts newest first', () => {
    const events = buildFeed(
      inputs({
        sessions: [
          session(1, { endedAt: iso(5) }),
          session(2, { status: 'killed', endedAt: iso(1) }),
          session(3, { outcome: 'abandoned', endedAt: iso(2) } as Partial<Session>),
          session(4, { title: null, status: 'active', startedAt: iso(0.5), endedAt: iso(0.01) }),
        ],
      }),
    );
    expect(events.map((e) => e.title)).toEqual([
      'Untitled session session started',
      'Session 2 failed',
      'Session 3 was stopped',
      'Session 1 finished',
    ]);
    expect(events[1]?.summary).toBe('orders-api · feat/x');
  });

  it('turns proposals into events on their Inbox tab and leaves approvals and the classifier out', () => {
    const item = (kind: InboxItem['kind']): InboxItem =>
      ({ key: `${kind}:1`, kind, title: 'Dry-run migrations', context: 'from 3 off-plan runs', ageIso: iso(3) }) as InboxItem;
    const events = buildFeed(inputs({ inbox: [item('lesson'), item('approval'), item('classifier')] }));
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      title: 'Lesson proposed: Dry-run migrations',
      summary: 'from 3 off-plan runs',
      to: '/inbox?tab=lessons',
    });
  });
});

describe('blockerHref', () => {
  it('stays in the project mount for a session blocker and falls back to the queue', () => {
    const scoped = feedLinks('orders-api', (id) => `/p/orders-api/sessions/${String(id)}`);
    expect(blockerHref(blocker({}), scoped)).toBe('/p/orders-api/sessions/1');
    expect(blockerHref(blocker({ kind: 'worktree_cleanup', sessionId: 0 }), scoped)).toBe('/p/orders-api/needs-you');
  });
});

describe('groupFeed', () => {
  it('keeps today and yesterday and leaves anything older out', () => {
    const groups = groupFeed([event('a', iso(1)), event('b', iso(20)), event('c', iso(60))], NOW);
    expect(groups.today.map((e) => e.key)).toEqual(['a']);
    expect(groups.yesterday.map((e) => e.key)).toEqual(['b']);
  });
});

describe('filterFeed', () => {
  it('matches the title or the summary, ignoring case', () => {
    const events = [event('Rate limiter', iso(1)), { ...event('Other', iso(2)), summary: 'orders-api' }];
    expect(filterFeed(events, 'RATE').map((e) => e.key)).toEqual(['Rate limiter']);
    expect(filterFeed(events, 'orders').map((e) => e.key)).toEqual(['Other']);
    expect(filterFeed(events, '  ')).toHaveLength(2);
  });
});
