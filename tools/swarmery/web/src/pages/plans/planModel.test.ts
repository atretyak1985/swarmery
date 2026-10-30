// What a plan reads as. The claims: a phase needs the operator only when a decision
// holds it or all that is left is manual; an unknown spend stays unknown rather than
// $0; the glyph ranks what blocks over what runs; the goal is one sentence of prose
// from the README, or nothing.

import { describe, expect, it } from 'vitest';
import type { Epic, EpicPhase, LinkedSession } from '../../api/types';
import {
  computeResolvedSeqs,
  fmtSpent,
  needYouCount,
  phaseNeedsYou,
  phasesLanded,
  planBranch,
  planGlyph,
  planGoal,
  planSpent,
  planSummary,
  planUpdatedAt,
} from './planModel';

function phase(over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    seq: 1,
    dependsOn: [],
    checkboxesDone: 0,
    checkboxesTotal: 3,
    manualOpen: 0,
    landOpen: 0,
    runBranch: '',
    runState: 'idle',
    runStartedAt: null,
    docUpdatedAt: null,
    docStatus: null,
    boardColumn: null,
    completionState: 'incomplete',
    decisionHold: null,
    ...over,
  } as EpicPhase;
}

function epic(over: Partial<Epic> = {}): Epic {
  return {
    status: 'active',
    startedAt: null,
    phases: [],
    linkedSessions: [],
    planRun: null,
    rollup: { done: 0, total: 0, pct: 0 },
    ...over,
  } as Epic;
}

const session = (costUsd: number | null): LinkedSession =>
  ({ sessionUuid: 'u', linkSource: 'explicit', confidence: 1, costUsd, startedAt: '', endedAt: null }) as LinkedSession;

describe('phaseNeedsYou', () => {
  it('is true when a decision holds the phase', () => {
    expect(phaseNeedsYou(phase({ decisionHold: { decisionId: 'D-1' } as EpicPhase['decisionHold'] }))).toBe(true);
  });
  it('is true when every open criterion is manual, landing aside', () => {
    expect(phaseNeedsYou(phase({ checkboxesDone: 2, checkboxesTotal: 3, manualOpen: 1 }))).toBe(true);
    expect(phaseNeedsYou(phase({ checkboxesDone: 1, checkboxesTotal: 3, manualOpen: 1, landOpen: 1 }))).toBe(true);
  });
  it('is false while an agent still has criteria to close', () => {
    expect(phaseNeedsYou(phase({ checkboxesDone: 0, checkboxesTotal: 3, manualOpen: 1 }))).toBe(false);
    expect(phaseNeedsYou(phase())).toBe(false);
  });
});

describe('planSpent', () => {
  it('sums the priced sessions and skips the unpriced ones', () => {
    expect(planSpent(epic({ linkedSessions: [session(1.5), session(null), session(2)] }))).toBe(3.5);
  });
  it('is null, not 0, when nothing is priced', () => {
    expect(planSpent(epic({ linkedSessions: [session(null)] }))).toBeNull();
    expect(planSpent(epic())).toBeNull();
  });
});

describe('fmtSpent', () => {
  it('keeps cents only where they matter', () => {
    expect(fmtSpent(0.4)).toBe('$0.40');
    expect(fmtSpent(85.6)).toBe('$86');
  });
});

describe('planUpdatedAt / planBranch', () => {
  it('takes the latest sign of life across the phases', () => {
    const e = epic({
      startedAt: '2026-10-01T00:00:00Z',
      phases: [
        phase({ docUpdatedAt: '2026-10-02T00:00:00Z' }),
        phase({ runStartedAt: '2026-10-03T00:00:00Z' }),
        phase({ docUpdatedAt: '2026-09-01T00:00:00Z' }),
      ],
    });
    expect(planUpdatedAt(e)).toBe('2026-10-03T00:00:00Z');
    expect(planUpdatedAt(epic())).toBeNull();
  });
  it('names the branch of the latest run, and none for a plan that never ran', () => {
    const e = epic({
      phases: [
        phase({ runBranch: 'plan/old', runStartedAt: '2026-10-01T00:00:00Z' }),
        phase({ runBranch: 'plan/new', runStartedAt: '2026-10-02T00:00:00Z' }),
        phase(),
      ],
    });
    expect(planBranch(e)).toBe('plan/new');
    expect(planBranch(epic({ phases: [phase()] }))).toBeNull();
  });
});

describe('planGlyph', () => {
  const needs = phase({ checkboxesDone: 2, checkboxesTotal: 3, manualOpen: 1 });
  const running = phase({ runState: 'running' });
  const done = phase({ completionState: 'complete' });
  const glyph = (e: Epic): string => planGlyph(e, computeResolvedSeqs(e.phases));

  it('ranks what blocks over what runs', () => {
    expect(glyph(epic({ phases: [needs, running] }))).toBe('needs');
    expect(glyph(epic({ phases: [running] }))).toBe('working');
  });
  it('reads a finished plan as done and a parked one as stopped', () => {
    expect(glyph(epic({ phases: [done] }))).toBe('done');
    expect(glyph(epic({ phases: [phase()] }))).toBe('pending');
    expect(glyph(epic({ status: 'paused', phases: [phase()] }))).toBe('stopped');
  });
  it('an archived plan blocks nothing, whatever its phases hold', () => {
    expect(glyph(epic({ status: 'archived', phases: [needs] }))).toBe('stopped');
    expect(glyph(epic({ status: 'archived', phases: [done] }))).toBe('done');
  });
});

describe('planSummary', () => {
  it('says what landed, what waits and what it cost', () => {
    const e = epic({
      phases: [phase({ completionState: 'complete' }), phase({ checkboxesDone: 2, checkboxesTotal: 3, manualOpen: 1 })],
      linkedSessions: [session(86)],
    });
    const resolved = computeResolvedSeqs(e.phases);
    expect(phasesLanded(e, resolved)).toBe(1);
    expect(needYouCount(e)).toBe(1);
    expect(planSummary(e, resolved)).toBe('1 of 2 phases landed · 1 needs you · $86');
  });
  it('says a plan nobody touched has not started', () => {
    const e = epic({ phases: [phase()] });
    expect(planSummary(e, computeResolvedSeqs(e.phases))).toBe('0 of 1 phases landed · not started');
  });
  it('leaves the cost out while it is unknown', () => {
    const e = epic({ phases: [phase({ completionState: 'complete' })] });
    expect(planSummary(e, computeResolvedSeqs(e.phases))).toBe('1 of 1 phases landed · all landed');
  });
});

describe('planGoal', () => {
  it('takes the first sentence, plain words first', () => {
    const md = '# T\n\n## Objective\n\nThe aim.\n\n## In plain words\n\nProtect the **API**. Then more.\n\n## Risks\n';
    expect(planGoal(md)).toBe('Protect the API.');
  });
  it('falls back to Objective, and joins a wrapped paragraph', () => {
    expect(planGoal('## Objective\n\nShip the app\nto testers; then roll out.\n')).toBe(
      'Ship the app to testers; then roll out.',
    );
  });
  it('does not stop at an abbreviation', () => {
    expect(planGoal('## Goal\n\nCover sites, e.g. the router. Next thing.')).toBe('Cover sites, e.g. the router.');
  });
  it('skips a section that opens with a table or a list, and gives up without one', () => {
    expect(planGoal('## In plain words\n\n| a | b |\n\n## Goal\n\nReal goal.')).toBe('Real goal.');
    expect(planGoal('## Risks\n\nNone.')).toBeNull();
    expect(planGoal('failed to load README.md: 404')).toBeNull();
  });
});
