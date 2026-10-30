// The list's groups, its one sentence per row, the detail's state sentence and
// the filter field (handoff §5). Pure logic, no DOM.

import { describe, expect, it } from 'vitest';
import type { NeedsYouItem, Session, SessionPlanGroup } from '../api/types';
import {
  buildGroups,
  compactSpan,
  gatesBySession,
  matchesFilter,
  parseListQuery,
  planRowLabel,
  rowState,
  rowSummary,
  stateSentence,
  stepDuration,
} from './sessionsView';

const BOILERPLATE = 'You are the controller for an ENTIRE approved implementation plan.';

let nextId = 1;

function makeSession(over: Partial<Session> = {}): Session {
  const id = nextId;
  nextId += 1;
  return {
    id,
    projectId: 1,
    projectSlug: 'p',
    projectName: 'p',
    sessionUuid: `uuid-${String(id)}`,
    model: 'opus',
    modelLast: 'opus',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'main',
    cwd: '/w',
    status: 'completed',
    startedAt: '2026-08-02T11:30:00Z',
    endedAt: '2026-08-02T11:59:50Z',
    title: 'a session',
    source: 'jsonl',
    ...over,
  };
}

describe('rowState', () => {
  const NOW = Date.parse('2026-08-02T12:00:00Z');

  it('reads a queued gate as needs even while the status says active', () => {
    const gate = { kind: 'approval' } as NeedsYouItem;
    expect(rowState(makeSession({ status: 'active' }), NOW, gate)).toBe('needs');
    expect(rowState(makeSession({ status: 'active' }), NOW)).toBe('working');
  });

  it.each([
    ['waiting_approval', 'needs'],
    ['awaiting_reply', 'needs'],
    ['completed', 'done'],
    ['killed', 'failed'],
  ] as const)('%s reads as %s', (status, state) => {
    expect(rowState(makeSession({ status }), NOW)).toBe(state);
  });

  it('lets the operator verdict decide a finished row', () => {
    expect(rowState(makeSession({ outcome: 'fail' }), NOW)).toBe('failed');
    expect(rowState(makeSession({ outcome: 'abandoned' }), NOW)).toBe('stopped');
  });
});

describe('buildGroups', () => {
  const NOW = new Date(2026, 7, 2, 12).getTime();
  const at = (day: number): string => new Date(2026, 7, day, 9).toISOString();

  it('splits finished rows into today and earlier, keeping the order', () => {
    const today = makeSession({ startedAt: at(2), endedAt: at(2) });
    const old = makeSession({ startedAt: at(1), endedAt: at(1) });
    const groups = buildGroups([today, old], NOW, new Map());
    expect(groups.done).toEqual([today]);
    expect(groups.earlier).toEqual([old]);
    expect(groups.needs).toEqual([]);
  });

  it('keeps a quiet session under Working: it has not ended', () => {
    const quiet = makeSession({ status: 'idle', procState: 'running', startedAt: at(1), endedAt: at(1) });
    expect(rowState(quiet, NOW)).toBe('pending');
    expect(buildGroups([quiet], NOW, new Map()).working).toEqual([quiet]);
  });
});

describe('gatesBySession', () => {
  const item = (kind: string, sessionId: number, requestId: number): NeedsYouItem =>
    ({ kind, sessionId, requestId }) as unknown as NeedsYouItem;

  it('keeps the oldest gate per session and skips the kinds a session cannot answer', () => {
    const gates = gatesBySession([
      item('approval', 1, 7),
      item('question', 1, 8),
      item('failed', 2, 0),
      item('manual_phase', 0, 0),
    ]);
    expect([...gates.keys()]).toEqual([1]);
    expect(gates.get(1)?.requestId).toBe(7);
  });
});

describe('compact spans', () => {
  it.each([
    [40_000, '40s'],
    [720_000, '12m'],
    [13_380_000, '3h 43m'],
    [7_200_000, '2h'],
    [259_200_000, '3d'],
  ])('%i ms reads %s', (ms, text) => {
    expect(compactSpan(ms)).toBe(text);
  });

  it.each([
    [null, ''],
    [6_000, '6s'],
    [130_000, '2m 10s'],
    [62_000, '1m 02s'],
    [180_000, '3m'],
  ])('a step of %s ms reads "%s"', (ms, text) => {
    expect(stepDuration(ms)).toBe(text);
  });
});

describe('row sentences', () => {
  const NOW = Date.parse('2026-08-02T12:00:00Z');
  const plan: SessionPlanGroup = {
    taskId: 7,
    title: 'Rate limiter',
    role: 'phase',
    phaseId: 3,
    phaseSeq: 3,
    phaseName: 'wire it',
  };

  it('names what a gate asks', () => {
    const gate = { kind: 'approval', toolName: 'Bash', preview: 'ls', question: '' } as NeedsYouItem;
    expect(rowSummary(makeSession(), 'needs', NOW, null, gate)).toBe('Allow Bash: ls?');
  });

  it('prefers the live action while working', () => {
    expect(rowSummary(makeSession({ why: 'the intent' }), 'working', NOW, 'Edit a.go')).toBe('Edit a.go');
    expect(rowSummary(makeSession({ why: 'the intent' }), 'working', NOW, null)).toBe('the intent');
  });

  it('never prints a plan row its boilerplate why', () => {
    const s = makeSession({ why: BOILERPLATE, planGroup: plan, gitBranch: 'p3' });
    expect(rowSummary(s, 'done', NOW, null)).toBe('opus · p3');
    expect(stateSentence(s, 'done', NOW, null, null).rest).not.toContain('controller');
    expect(planRowLabel(s)).toBe('Rate limiter · wire it');
  });
});

describe('stateSentence', () => {
  const NOW = Date.parse('2026-08-02T12:00:00Z');

  it('waits for a permission, then for an answer', () => {
    const blockingSince = '2026-08-02T11:48:00Z';
    const approval = { kind: 'approval', blockingSince } as NeedsYouItem;
    expect(stateSentence(makeSession(), 'needs', NOW, null, null, approval)).toEqual({
      lead: 'Waiting for your permission',
      rest: 'for 12m · the session does nothing until you decide.',
    });
    const question = { kind: 'awaiting_reply', blockingSince } as NeedsYouItem;
    expect(stateSentence(makeSession(), 'needs', NOW, null, null, question).lead).toBe('Waiting for your answer');
  });

  it('says how long ago a working session last acted', () => {
    const s = makeSession({ status: 'active', why: 'wiring the router' });
    expect(stateSentence(s, 'working', NOW, '2026-08-02T11:59:20Z', null)).toEqual({
      lead: 'Working',
      rest: '· last action 40s ago · wiring the router.',
    });
  });

  it('calls a finished one by its duration', () => {
    const s = makeSession({ startedAt: '2026-08-02T11:00:00Z', endedAt: '2026-08-02T11:34:00Z', why: 'tests green' });
    expect(stateSentence(s, 'done', NOW, null, null)).toEqual({ lead: 'Finished', rest: 'in 34m · tests green.' });
  });
});

describe('the filter field', () => {
  it('splits tokens from text', () => {
    expect(parseListQuery('  S:Needs o:fail flaky  n:limiter ')).toEqual({
      text: 'flaky',
      name: 'limiter',
      state: 'needs',
      outcome: 'fail',
    });
  });

  it('reads an unfinished token as text', () => {
    expect(parseListQuery('s:').text).toBe('s:');
  });

  it('matches a state by the row state, the status or its label', () => {
    const s = makeSession({ status: 'killed', title: 'x' });
    expect(matchesFilter(s, parseListQuery('s:failed'), 'failed')).toBe(true);
    expect(matchesFilter(s, parseListQuery('s:killed'), 'failed')).toBe(true);
    expect(matchesFilter(s, parseListQuery('s:needs'), 'failed')).toBe(false);
  });

  it('matches outcome and name', () => {
    const s = makeSession({ title: 'Rate limiter', outcome: 'success' });
    expect(matchesFilter(s, parseListQuery('o:succ n:rate'), 'done')).toBe(true);
    expect(matchesFilter(s, parseListQuery('o:fail'), 'done')).toBe(false);
  });
});
