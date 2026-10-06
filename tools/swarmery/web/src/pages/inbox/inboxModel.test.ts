// @vitest-environment jsdom
//
// The Inbox model (Canvas v3 phase 3): every source normalises, urgent
// approvals sort first by expiry, the rest oldest-first, classifier questions
// group per session, tabs filter and count.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/inbox/inboxModel.test.ts`.

import { describe, expect, it } from 'vitest';
import type { Alert } from '../../api/alerts';
import type { QueueItem } from '../../api/decisions';
import type { Lesson, RetirementProposal } from '../../api/lessons';
import type { AgentChangeProposal, PermissionRequest, Recommendation } from '../../api/types';
import type { TriageVerdict } from '../../api/triage';
import {
  AGENT_KINDS,
  AGENT_WAIT_MS,
  INBOX_TABS,
  ageLabel,
  agentOffer,
  attachSuggestions,
  expiresInLabel,
  filterTab,
  groupClassifier,
  sortItems,
  suggestionBreakdown,
  tabCounts,
  toItems,
  waitingLine,
  type InboxItem,
} from './inboxModel';

function approval(id: number, requestedAt: string, expiresAt: string, input: unknown = { command: 'ls' }): PermissionRequest {
  return {
    id,
    sessionId: 10 + id,
    toolName: 'Bash',
    requestJson: JSON.stringify({ tool_name: 'Bash', tool_input: input }),
    status: 'pending',
    requestedAt,
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt,
  };
}

function lesson(id: number, createdAt: string): Lesson {
  return {
    id,
    phaseId: 1,
    phaseName: 'Phase 3 — templates v2',
    planId: 'p',
    sourcePhaseRun: 'r',
    title: 'Run the fixture generator first',
    normTitle: 'run-the-fixture-generator-first',
    guidance: 'Run it.',
    areaGlobs: ['services/orders/**'],
    evidence: [],
    cause: 'fixtures were stale',
    sourceParagraph: '',
    surpriseIndex: 0.62,
    status: 'candidate',
    linkedNormTitle: '',
    mergedIntoId: null,
    recurrences: 3,
    recurrenceRuns: [],
    model: 'm',
    createdAt,
    updatedAt: createdAt,
    activatedAt: null,
    retiredAt: null,
    retireReason: null,
    promotedBranch: '',
    matches: [],
  };
}

function rec(id: number, created: string): Recommendation {
  return {
    id,
    rule: 'R2',
    target_kind: 'agent',
    target: 'implementation-agent',
    title: 'implementation-agent fails 3× the fleet',
    detail: 'Most failures are one thing.',
    evidence: {},
    baseline: null,
    status: 'proposed',
    created_at: created,
    updated_at: created,
  } as Recommendation;
}

function proposal(id: number, created: string): AgentChangeProposal {
  return {
    id,
    recommendation_id: null,
    agent: 'implementation-agent',
    agent_path: 'agents/implementation-agent.md',
    target_kind: 'agent',
    target_path: '',
    base_sha256: '',
    diff: '+ a line',
    rationale: 'tool hygiene',
    status: 'proposed',
    error: null,
    pr_url: null,
    created_at: created,
    decided_at: null,
  } as AgentChangeProposal;
}

function queue(id: number, session: string, createdAt: string): QueueItem {
  return {
    id,
    questionId: `d2.q${String(id)}`,
    subject: session,
    sessionUuid: session,
    sessionTitle: `session ${session}`,
    answer: 'feature',
    confidence: 1,
    createdAt,
    options: ['feature', 'bugfix'],
  };
}

function retirement(id: number, proposedAt: string): RetirementProposal {
  return {
    id,
    lessonId: 2,
    title: 'Index every FK child column',
    guidance: 'Index them.',
    areaGlobs: [],
    reason: 'ineffective',
    detail: 'no measured drop',
    evidence: {},
    state: 'proposed',
    proposedAt,
    decidedAt: null,
    autoRetireAt: null,
    effectiveness: null,
  };
}

describe('toItems', () => {
  it('normalises all six sources into one shape', () => {
    const items = toItems({
      approvals: [approval(1, '2026-09-27T10:00:00Z', '2026-09-27T10:05:00Z')],
      lessons: [lesson(2, '2026-09-26T10:00:00Z')],
      recommendations: [rec(3, '2026-09-25T10:00:00Z')],
      proposals: [proposal(4, '2026-09-24T10:00:00Z')],
      classifier: [queue(5, 'a', '2026-09-23T10:00:00Z')],
      retirements: [retirement(6, '2026-09-22T10:00:00Z')],
    });
    expect(items.map((i) => i.kind)).toEqual(['approval', 'lesson', 'advisor', 'proposal', 'classifier', 'retire']);
    expect(items.map((i) => i.key)).toEqual([
      'approval:1',
      'lesson:2',
      'advisor:3',
      'proposal:4',
      'classifier:a',
      'retire:6',
    ]);
    expect(items[0]?.urgent).toBe(true);
    expect(items.slice(1).every((i) => !i.urgent)).toBe(true);
    // Code → UI dictionary: the retirement reason is plain language.
    expect(items[5]?.context).toBe('not helping');
  });

  it('titles an AskUserQuestion approval by its question count', () => {
    const r = { ...approval(1, '2026-09-27T10:00:00Z', '2026-09-27T10:05:00Z'), toolName: 'AskUserQuestion' };
    r.requestJson = JSON.stringify({
      tool_name: 'AskUserQuestion',
      tool_input: {
        questions: [
          { question: 'Which?', header: 'Pick', options: [{ label: 'a', description: '' }, { label: 'b', description: '' }], multiSelect: false },
          { question: 'And?', header: 'More', options: [{ label: 'c', description: '' }, { label: 'd', description: '' }], multiSelect: false },
        ],
      },
    });
    expect(toItems({ approvals: [r] })[0]?.title).toBe('AskUserQuestion · 2 questions');
  });

  it('treats a missing source as empty', () => {
    expect(toItems({ lessons: undefined })).toEqual([]);
  });
});

describe('alerts', () => {
  const breaker = (id: number, kind: 'auth' | 'quota', resetsAt?: string): Alert => ({
    id,
    rule: 'account_breaker_open',
    target: 'account:work',
    severity: 'error',
    message: 'Claude refused this account. Runs on it are paused until a probe succeeds.',
    detectedAt: '2026-09-27T09:00:00Z',
    account: 'work',
    kind,
    reason: "Claude refused this account's access",
    openedAt: '2026-09-27T09:00:00Z',
    ...(resetsAt === undefined ? {} : { resetsAt }),
  });

  it('normalises a paused account into an urgent alert item', () => {
    const [auth, quota] = toItems({ alerts: [breaker(1, 'auth'), breaker(2, 'quota', '2026-09-27T14:00:00Z')] });
    expect(auth?.kind).toBe('alert');
    expect(auth?.key).toBe('alert:1');
    expect(auth?.title).toBe('Account work is paused');
    expect(auth?.context).toBe('sign-in or access');
    expect(auth?.urgent).toBe(true);
    // An auth pause does not end on its own; a quota pause does, at its reset.
    expect(auth?.expiresIso).toBeUndefined();
    expect(quota?.context).toBe('usage limit');
    expect(quota?.expiresIso).toBe('2026-09-27T14:00:00Z');
  });

  it('sorts a paused account ahead of the calm queue and counts it under its tab', () => {
    const items = toItems({
      lessons: [lesson(3, '2026-09-20T10:00:00Z')],
      alerts: [breaker(1, 'auth')],
    });
    expect(sortItems(items).map((i) => i.key)).toEqual(['alert:1', 'lesson:3']);
    expect(tabCounts(items).alerts).toBe(1);
    expect(filterTab(items, 'alerts').map((i) => i.key)).toEqual(['alert:1']);
  });

  it('keeps an alert that is not an account breaker, with its own message', () => {
    const other: Alert = {
      id: 9,
      rule: 'some_future_rule',
      target: 'thing:1',
      severity: 'error',
      message: 'Something needs you.',
      detectedAt: '2026-09-27T09:00:00Z',
    };
    const [item] = toItems({ alerts: [other] });
    expect(item?.title).toBe('Something needs you.');
    expect(item?.context).toBe('thing:1');
    expect(item?.ageIso).toBe('2026-09-27T09:00:00Z');
  });

  it('gives an auto mode outage a short title, keeps it urgent and never expiring', () => {
    const outage: Alert = {
      id: 5,
      rule: 'auto_mode_no_verdict',
      target: 'auto-mode-classifier',
      severity: 'warn',
      message:
        "9 permission checks got no verdict in the last 10 minutes across 2 sessions — Claude Code's server-side classifier is failing; affected sessions pause until it recovers.",
      detectedAt: '2026-09-28T06:39:00Z',
    };
    const [item] = toItems({ alerts: [outage] });
    expect(item?.key).toBe('alert:5');
    expect(item?.title).toBe('Permission checks are getting no verdict');
    expect(item?.context).toBe('Claude Code auto mode');
    // A warn alert sorts with the other alerts: work is stopped while it is open.
    expect(item?.urgent).toBe(true);
    expect(item?.expiresIso).toBeUndefined();
    expect(item?.ageIso).toBe('2026-09-28T06:39:00Z');
  });
});

describe('sortItems', () => {
  it('puts urgent approvals first by expiry, the rest oldest-first', () => {
    const items = toItems({
      approvals: [
        approval(1, '2026-09-27T10:00:00Z', '2026-09-27T10:09:00Z'),
        approval(2, '2026-09-27T10:01:00Z', '2026-09-27T10:02:00Z'),
      ],
      lessons: [lesson(3, '2026-09-26T10:00:00Z')],
      recommendations: [rec(4, '2026-09-20T10:00:00Z')],
    });
    expect(sortItems(items).map((i) => i.key)).toEqual(['approval:2', 'approval:1', 'advisor:4', 'lesson:3']);
  });
});

describe('groupClassifier', () => {
  it('makes one item per session, dated by its oldest question', () => {
    const groups = groupClassifier([
      queue(1, 'a', '2026-09-24T10:00:00Z'),
      queue(2, 'b', '2026-09-24T11:00:00Z'),
      queue(3, 'a', '2026-09-23T10:00:00Z'),
    ]);
    expect(groups).toHaveLength(2);
    expect(groups[0]?.key).toBe('classifier:a');
    expect(groups[0]?.context).toBe('2 questions · grouped');
    expect(groups[0]?.ageIso).toBe('2026-09-23T10:00:00Z');
    expect(groups[0]?.kind === 'classifier' && groups[0].raw.map((q) => q.id)).toEqual([1, 3]);
  });
});

describe('tabs', () => {
  it('counts and filters by the tab URL word', () => {
    const items = toItems({
      approvals: [approval(1, '2026-09-27T10:00:00Z', '2026-09-27T10:05:00Z')],
      lessons: [lesson(2, '2026-09-26T10:00:00Z'), lesson(3, '2026-09-26T11:00:00Z')],
    });
    const counts = tabCounts(items);
    expect(counts.all).toBe(3);
    expect(counts.lessons).toBe(2);
    expect(counts.approvals).toBe(1);
    expect(counts.retire).toBe(0);
    expect(filterTab(items, 'lessons').map((i) => i.key)).toEqual(['lesson:2', 'lesson:3']);
    expect(filterTab(items, 'all')).toHaveLength(3);
  });
});

function verdict(over: Partial<TriageVerdict> & Pick<TriageVerdict, 'id' | 'kind' | 'ref'>): TriageVerdict {
  return {
    runId: 1,
    class: '',
    itemKey: '',
    title: '',
    value: 'accept',
    reason: 'because',
    payload: null,
    prior: null,
    state: 'suggested',
    createdAt: '2026-10-05T09:00:00Z',
    decidedAt: null,
    projectId: null,
    ...over,
  };
}

describe('attachSuggestions', () => {
  const items = toItems({
    lessons: [lesson(2, '2026-09-26T10:00:00Z'), lesson(3, '2026-09-26T11:00:00Z')],
    recommendations: [rec(11, '2026-09-26T10:00:00Z')],
    retirements: [retirement(6, '2026-09-22T10:00:00Z')],
    classifier: [queue(20, 's1', '2026-09-26T10:00:00Z'), queue(21, 's1', '2026-09-26T10:00:00Z')],
    proposals: [proposal(4, '2026-09-26T10:00:00Z')],
  });
  const byKey = (out: InboxItem[], key: string): InboxItem => out.find((i) => i.key === key) as InboxItem;

  it('matches a lesson by id and leaves the others unchanged', () => {
    const out = attachSuggestions(items, [verdict({ id: 1, kind: 'lesson', ref: '3', value: 'dismiss' })]);
    expect(byKey(out, 'lesson:3').suggestion).toEqual({
      verdictIds: [1],
      value: 'dismiss',
      reason: 'because',
      sample: false,
    });
    expect(byKey(out, 'lesson:2').suggestion).toBeUndefined();
    expect(byKey(out, 'lesson:2')).toBe(items.find((i) => i.key === 'lesson:2'));
  });

  it('matches an advisor recommendation and carries the fix-card', () => {
    const out = attachSuggestions(items, [
      verdict({ id: 2, kind: 'advisor', ref: '11', value: 'fix-card', payload: { title: 'T', prompt: 'P' } }),
    ]);
    expect(byKey(out, 'advisor:11').suggestion?.card).toEqual({ title: 'T', prompt: 'P' });
  });

  it('matches a retirement proposal by id', () => {
    const out = attachSuggestions(items, [verdict({ id: 3, kind: 'retire', ref: '6', value: 'keep' })]);
    expect(byKey(out, 'retire:6').suggestion?.value).toBe('keep');
  });

  it('does not cross kinds: a lesson verdict never lands on an advisor with the same id', () => {
    const out = attachSuggestions(items, [verdict({ id: 4, kind: 'lesson', ref: '11' })]);
    expect(byKey(out, 'advisor:11').suggestion).toBeUndefined();
  });

  it('puts one suggestion on a classifier group, answers per decision, from one matching verdict', () => {
    const out = attachSuggestions(items, [
      verdict({ id: 5, kind: 'classifier', ref: '21', value: 'bugfix', state: 'sample' }),
    ]);
    const s = byKey(out, 'classifier:s1').suggestion;
    expect(s).toMatchObject({ verdictIds: [5], value: 'bugfix', sample: true, answers: { 21: 'bugfix' } });
    expect(s?.answers).not.toHaveProperty('20');
  });

  it('fills answers for both decisions of a group', () => {
    const out = attachSuggestions(items, [
      verdict({ id: 5, kind: 'classifier', ref: '20', value: 'feature' }),
      verdict({ id: 6, kind: 'classifier', ref: '21', value: 'bugfix' }),
    ]);
    const s = byKey(out, 'classifier:s1').suggestion;
    expect(s?.verdictIds).toEqual([5, 6]);
    expect(s?.answers).toEqual({ 20: 'feature', 21: 'bugfix' });
    expect(s?.sample).toBe(false);
  });

  it('attaches nothing for verdicts that are no longer open', () => {
    const out = attachSuggestions(items, [
      verdict({ id: 7, kind: 'lesson', ref: '2', state: 'stale' }),
      verdict({ id: 8, kind: 'lesson', ref: '3', state: 'applied' }),
    ]);
    expect(out.every((i) => i.suggestion === undefined)).toBe(true);
  });
});

describe('agentOffer', () => {
  const now = Date.parse('2026-10-05T12:00:00Z');
  const isoAgo = (ms: number): string => new Date(now - ms).toISOString();

  it('counts agent-shaped items without a suggestion under agent, everything else under you', () => {
    const base = toItems({
      approvals: [approval(1, isoAgo(1000), isoAgo(-1000))],
      lessons: [lesson(2, isoAgo(1000)), lesson(3, isoAgo(1000))],
      proposals: [proposal(4, isoAgo(1000))],
    });
    const items = attachSuggestions(base, [verdict({ id: 1, kind: 'lesson', ref: '3' })]);
    expect(agentOffer(items, now)).toEqual({ agent: 1, you: 3, show: false });
  });

  it('show: false at exactly 24 h, true one second later', () => {
    const at = toItems({ lessons: [lesson(2, isoAgo(AGENT_WAIT_MS))] });
    expect(agentOffer(at, now).show).toBe(false);
    const past = toItems({ lessons: [lesson(2, isoAgo(AGENT_WAIT_MS + 1000))] });
    expect(agentOffer(past, now).show).toBe(true);
  });

  it('an old item that already carries a suggestion does not trigger the offer', () => {
    const old = toItems({ lessons: [lesson(2, isoAgo(AGENT_WAIT_MS * 3))] });
    const items = attachSuggestions(old, [verdict({ id: 1, kind: 'lesson', ref: '2' })]);
    expect(agentOffer(items, now)).toEqual({ agent: 0, you: 1, show: false });
  });

  it('knows which kinds the agent handles', () => {
    expect([...AGENT_KINDS].sort()).toEqual(['advisor', 'classifier', 'lesson', 'retire']);
  });
});

describe('handled tab', () => {
  it('exists, takes its count from the second parameter and filters to nothing', () => {
    expect(INBOX_TABS.at(-1)).toEqual({ id: 'handled', label: 'handled by agent' });
    const items = toItems({ lessons: [lesson(2, '2026-09-26T10:00:00Z')] });
    expect(tabCounts(items).handled).toBe(0);
    expect(tabCounts(items, 5).handled).toBe(5);
    expect(tabCounts(items, 5).all).toBe(1);
    expect(filterTab(items, 'handled')).toEqual([]);
  });
});

describe('labels', () => {
  const now = Date.parse('2026-09-27T12:00:00Z');
  it('formats ages, countdowns and the waiting line', () => {
    expect(ageLabel('2026-09-27T11:59:18Z', now)).toBe('42 s');
    expect(ageLabel('2026-09-27T10:00:00Z', now)).toBe('2 h');
    expect(ageLabel('2026-09-26T11:00:00Z', now)).toBe('1 d');
    expect(expiresInLabel('2026-09-27T12:01:18Z', now)).toBe('1:18');
    expect(expiresInLabel('2026-09-27T11:00:00Z', now)).toBe('expired');
    const items = toItems({ lessons: [lesson(1, '2026-09-26T11:00:00Z'), lesson(2, '2026-09-27T11:00:00Z')] });
    expect(waitingLine(items, now)).toBe('2 waiting · oldest 1 d');
    expect(waitingLine([], now)).toBe('nothing waiting');
  });
});

describe('suggestionBreakdown', () => {
  it('counts per action, keeps fix tasks and audit samples out of the acceptable set', () => {
    const b = suggestionBreakdown([
      verdict({ id: 1, kind: 'lesson', ref: '1', value: 'accept', state: 'suggested' }),
      verdict({ id: 2, kind: 'lesson', ref: '2', value: 'accept', state: 'suggested' }),
      verdict({ id: 3, kind: 'advisor', ref: '3', value: 'dismiss', state: 'suggested' }),
      verdict({ id: 4, kind: 'advisor', ref: '4', value: 'fix-card', state: 'suggested' }),
      verdict({ id: 5, kind: 'classifier', ref: '5', value: 'bugfix', state: 'sample' }),
    ]);
    expect(b.acceptable.map((v) => v.id)).toEqual([1, 2, 3]);
    expect(b.parts).toEqual(['2 accept', '1 dismiss']);
    expect(b.fixTasks).toBe(1);
  });
});
