// @vitest-environment jsdom
//
// The loop map model (Canvas v3 phase 4). Dev-only suite:
//   npx vitest run src/pages/today
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { describe, expect, it } from 'vitest';
import type { Epic, EpicPhase } from '../../api/types';
import { buildLoop, epicCounts, fmtCount, type LoopInputs } from './loopModel';

const NOW = Date.parse('2026-09-27T12:00:00Z');
const daysAgo = (d: number): string => new Date(NOW - d * 86400_000).toISOString();

function phase(over: Partial<EpicPhase>): EpicPhase {
  return {
    forecasts: [],
    surprise: null,
    runOutcome: 'idle',
    runEndedAt: null,
    ...over,
  } as EpicPhase;
}

function epic(status: Epic['status'], phases: EpicPhase[]): Epic {
  return { status, phases } as Epic;
}

const ZERO: LoopInputs = {
  epics: [],
  liveSessions: 0,
  pendingApprovals: 0,
  stoppedPhases: 0,
  scoredRuns: 0,
  farOffPlan: 0,
  classifierCalls: 0,
  classifierChecked: 0,
  lessonCandidates: 0,
  advisorFindings: 0,
  retirements: 0,
  verifiedChanges: 0,
  gatheringProof: 0,
};

const forecast = [{ kind: 'prior' }] as EpicPhase['forecasts'];

describe('buildLoop', () => {
  it('returns five stages in order plan → change', () => {
    expect(buildLoop(ZERO, null).map((s) => s.id)).toEqual(['plan', 'run', 'measure', 'learn', 'change']);
    expect(buildLoop(ZERO, null).map((s) => s.step)).toEqual([1, 2, 3, 4, 5]);
  });

  it('teaches instead of printing zeros when every input is zero', () => {
    const stages = buildLoop(ZERO, null);
    for (const s of stages) {
      expect(s.sentence).not.toMatch(/n\/a/);
      expect(s.waiting).toBe(false);
      expect(s.alert).toBe('');
    }
    expect(stages[0]?.sentence).toMatch(/^Appears after the first plan/);
    expect(stages[2]?.sentence).toBe('Appears after the first finished phase run.');
    expect(stages[3]?.sentence).toMatch(/^Nothing to review/);
    expect(stages[4]?.sentence).toMatch(/^Appears after you accept a change/);
  });

  it('marks run waiting on pending approvals and learn waiting on candidates', () => {
    const stages = buildLoop({ ...ZERO, pendingApprovals: 2, lessonCandidates: 2 }, null);
    const run = stages.find((s) => s.id === 'run');
    const learn = stages.find((s) => s.id === 'learn');
    expect(run?.waiting).toBe(true);
    expect(run?.alert).toBe('2 approvals waiting.');
    expect(learn?.waiting).toBe(true);
    expect(learn?.n).toBe(2);
    expect(learn?.linkLabel).toBe('Review in Inbox →');
    expect(stages.filter((s) => s.waiting).map((s) => s.id)).toEqual(['run', 'learn']);
  });

  it('writes 1a-shaped sentences from real numbers', () => {
    const epics = [
      epic('active', [phase({ forecasts: forecast }), phase({ forecasts: forecast }), phase({})]),
      epic('done', [phase({ forecasts: forecast })]),
    ];
    const stages = buildLoop(
      {
        ...ZERO,
        epics,
        liveSessions: 2,
        stoppedPhases: 1,
        scoredRuns: 14,
        farOffPlan: 3,
        classifierCalls: 1434,
        lessonCandidates: 2,
        advisorFindings: 1,
        retirements: 1,
        verifiedChanges: 1,
        gatheringProof: 2,
      },
      'orders-api',
    );
    const [plan, run, measure, learn, change] = stages;
    expect(plan?.n).toBe(1);
    expect(plan?.unit).toBe('plan');
    expect(plan?.sentence).toBe('2 phases carry a forecast — what the planner expects before anything runs.');
    expect(run?.sentence).toBe('1 phase stopped with nothing done.');
    expect(measure?.sentence).toBe(
      `3 landed far from the plan. The classifier labelled ${fmtCount(1434)} sessions —`,
    );
    expect(measure?.alert).toBe('none checked by you yet.');
    expect(learn?.sentence).toBe(
      '2 lesson candidates, 1 Advisor finding, 1 lesson that may have stopped helping. Nothing changes until you say so.',
    );
    expect(change?.sentence).toBe('1 change proved it helped. 2 changes still gathering proof.');
  });

  it('builds hrefs per scope', () => {
    const project = buildLoop(ZERO, 'orders-api').map((s) => s.href);
    expect(project).toEqual([
      '/p/orders-api/plans',
      '/p/orders-api/sessions',
      '/p/orders-api/health',
      '/p/orders-api/inbox',
      '/p/orders-api/learning?tab=proof',
    ]);
    const fleet = buildLoop(ZERO, null).map((s) => s.href);
    expect(fleet).toEqual(['/projects', '/sessions', '/health', '/inbox', '/learning?tab=proof']);
    expect(buildLoop(ZERO, null, 'billing')[0]?.href).toBe('/p/billing/plans');
  });
});

describe('epicCounts', () => {
  it('counts scored, off-plan and stopped phases in the last 7 days', () => {
    const surprise = (index: number, at: string) => ({ index, computedAt: at }) as EpicPhase['surprise'];
    const epics = [
      epic('active', [
        phase({ surprise: surprise(0.1, daysAgo(1)) }),
        phase({ surprise: surprise(0.45, daysAgo(2)) }),
        phase({ surprise: surprise(0.8, daysAgo(3)) }),
        phase({ surprise: surprise(0.9, daysAgo(10)) }),
        phase({ runOutcome: 'noop', runEndedAt: daysAgo(1) }),
        phase({ runOutcome: 'noop', runEndedAt: daysAgo(9) }),
      ]),
    ];
    expect(epicCounts(epics, NOW)).toEqual({ scoredRuns: 3, farOffPlan: 2, stoppedPhases: 1 });
  });
});
