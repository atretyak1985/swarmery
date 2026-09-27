// @vitest-environment jsdom
//
// The Inbox model (Canvas v3 phase 3): every source normalises, urgent
// approvals sort first by expiry, the rest oldest-first, classifier questions
// group per session, tabs filter and count.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/inbox/inboxModel.test.ts
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { describe, expect, it } from 'vitest';
import type { QueueItem } from '../../api/decisions';
import type { Lesson, RetirementProposal } from '../../api/lessons';
import type { AgentChangeProposal, PermissionRequest, Recommendation } from '../../api/types';
import {
  ageLabel,
  expiresInLabel,
  filterTab,
  groupClassifier,
  sortItems,
  tabCounts,
  toItems,
  waitingLine,
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
