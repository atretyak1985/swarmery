// What one ledger line says. The claims: what waits on the operator outranks every
// other stage, even on a done phase; the note names the decision rather than the
// status; a cost is the sum of the phase's own runs and unknown stays unknown; the
// open phase is the running one, else the next that can run, and never a blocked one.

import { describe, expect, it } from 'vitest';
import type { EpicPhase, NeedsYouDecision, Session } from '../../api/types';
import { computeResolvedSeqs, phaseStatus } from './planModel';
import {
  criteriaLine,
  defaultOpenSeq,
  phaseCost,
  phaseCostBand,
  phaseGlyph,
  phaseNote,
  phaseRuns,
  phaseStage,
  sessionGlyph,
} from './phaseLedgerModel';

function phase(over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    id: 1,
    seq: 1,
    dependsOn: [],
    checkboxesDone: 0,
    checkboxesTotal: 3,
    manualOpen: 0,
    landOpen: 0,
    runBranch: '',
    runState: 'idle',
    runOutcome: 'idle',
    runStartedAt: null,
    docUpdatedAt: null,
    docStatus: null,
    boardColumn: null,
    completionState: 'incomplete',
    decisionHold: null,
    landing: null,
    surprise: null,
    reopens: [],
    ...over,
  } as unknown as EpicPhase;
}

const done = phase({ completionState: 'complete', checkboxesDone: 3 });
const manual = phase({ checkboxesDone: 2, manualOpen: 1 });
const guard = { decisionId: 'D-9', trigger: 'criterion-impossible' } as NeedsYouDecision;

const stage = (p: EpicPhase): string => phaseStage(p, phaseStatus(p, new Set()));
const glyph = (p: EpicPhase): string => phaseGlyph(p, phaseStatus(p, new Set()));
const note = (p: EpicPhase, deps: number[] = [], elapsed = '', change: string | null = 'PR'): string =>
  phaseNote(p, phaseStatus(p, new Set()), deps, elapsed, change);

describe('phaseStage', () => {
  it('walks the design stages', () => {
    expect(stage(done)).toBe('landed');
    expect(stage(phase({ runState: 'running' }))).toBe('exec');
    expect(stage(phase({ verifyRunning: true }))).toBe('review');
    expect(stage(phase({ landing: { state: 'pr_open' } as EpicPhase['landing'] }))).toBe('review');
    expect(stage(manual)).toBe('manual');
    expect(stage(phase())).toBe('none');
  });
  it('lets a decision outrank even a done phase', () => {
    expect(stage({ ...done, reviewDecision: guard })).toBe('decision');
    expect(stage(phase({ guardDecision: guard }))).toBe('decision');
  });
});

describe('phaseGlyph', () => {
  it('ranks what blocks over what runs', () => {
    expect(glyph(phase({ runState: 'running', guardDecision: guard }))).toBe('needs');
    expect(glyph(phase({ runState: 'running' }))).toBe('working');
    expect(glyph(done)).toBe('done');
    expect(glyph(phase({ runState: 'failed' }))).toBe('failed');
    expect(glyph(phase({ runState: 'blocked' }))).toBe('stopped');
    expect(glyph(phase())).toBe('pending');
  });
});

describe('phaseNote', () => {
  it('names the decision a phase waits on', () => {
    expect(note(phase({ guardDecision: guard }))).toBe('guard stopped the run · D-9 · criterion-impossible');
    expect(note(phase({ guardDecision: { decisionId: 'D-2' } as NeedsYouDecision }))).toBe('guard stopped the run · D-2');
  });
  it('says who can close a manual phase', () => {
    expect(note(manual)).toBe('1 criterion [MANUAL] — only you can close it');
    expect(note(phase({ checkboxesDone: 0, manualOpen: 3 }))).toBe('3 criteria [MANUAL] — only you can close them');
  });
  it('reads a landed phase with its change request, in the host’s word', () => {
    const merged = { ...done, landing: { state: 'merged', prNumber: 311 } as EpicPhase['landing'] };
    expect(note(merged)).toBe('landed · PR #311');
    expect(note(merged, [], '', null)).toBe('landed · #311');
    expect(note(done)).toBe('landed');
  });
  it('counts how often a phase came back', () => {
    const once = { ...done, reopens: [{}] } as unknown as EpicPhase;
    const twice = { ...done, reopens: [{}, {}] } as unknown as EpicPhase;
    expect(note(once)).toBe('landed · came back once');
    expect(note(twice)).toBe('landed · came back 2 times');
  });
  it('tolerates a phase with no reopens field', () => {
    expect(note({ ...done, reopens: undefined } as unknown as EpicPhase)).toBe('landed');
  });
  it('reads a pending phase by what it waits on', () => {
    expect(note(phase({ dependsOn: [3] }), [3])).toBe('starts when 3 lands');
    expect(note(phase())).toBe('not started');
    expect(note(phase({ runState: 'running' }), [], '18m')).toBe('running · 18m');
  });
});

describe('criteriaLine', () => {
  it('shows the count, then what an agent cannot close', () => {
    expect(criteriaLine(phase({ checkboxesDone: 6, checkboxesTotal: 8 }))).toBe('6/8');
    expect(criteriaLine(phase({ checkboxesDone: 6, checkboxesTotal: 8, manualOpen: 1, landOpen: 1 }))).toBe(
      '6/8 · 1 [MANUAL] · 1 [LAND] pending',
    );
  });
});

describe('phaseRuns / phaseCost', () => {
  const run = (id: number, phaseId: number | null, costUsd: number | null, startedAt: string): Session =>
    ({
      id,
      costUsd,
      startedAt,
      planGroup: { role: phaseId === null ? 'controller' : 'phase', phaseId },
    }) as unknown as Session;
  const all = [
    run(1, 7, 2, '2026-10-02T00:00:00Z'),
    run(2, 7, null, '2026-10-01T00:00:00Z'),
    run(3, 8, 5, '2026-10-01T00:00:00Z'),
    run(4, null, 9, '2026-10-01T00:00:00Z'),
  ];
  it('keeps the phase’s own runs, oldest first, and no controller', () => {
    expect(phaseRuns(all, 7).map((s) => s.id)).toEqual([2, 1]);
    expect(phaseRuns(null, 7)).toEqual([]);
  });
  it('sums what is priced and keeps unknown unknown', () => {
    expect(phaseCost(phaseRuns(all, 7))).toBe(2);
    expect(phaseCost([run(5, 7, null, '')])).toBeNull();
    expect(phaseCost([])).toBeNull();
  });
});

describe('phaseCostBand', () => {
  it('gives a verdict only where the daemon has one', () => {
    expect(phaseCostBand(phase())).toBeNull();
    expect(phaseCostBand(phase({ surprise: { index: 0 } as EpicPhase['surprise'] }))).toBe('on plan');
  });
});

describe('defaultOpenSeq', () => {
  const at = (seq: number, over: Partial<EpicPhase> = {}): EpicPhase => phase({ id: seq, seq, ...over });
  const open = (phases: EpicPhase[], active = true): number | null =>
    defaultOpenSeq(phases, computeResolvedSeqs(phases), active);

  it('opens the running phase first', () => {
    expect(open([at(1), at(2, { runState: 'running' })])).toBe(2);
    expect(open([at(1, { runState: 'running' })], false)).toBe(1);
  });
  it('else the next phase that can run', () => {
    const landed = at(1, { completionState: 'complete' });
    expect(open([landed, at(2, { dependsOn: [1] }), at(3)])).toBe(2);
  });
  it('skips what waits on a person or on a dependency', () => {
    expect(open([at(1, { guardDecision: guard }), at(2, { dependsOn: [1] }), at(3)])).toBe(3);
    expect(open([at(1, { guardDecision: guard })])).toBeNull();
  });
  it('opens nothing on a finished or inactive plan', () => {
    expect(open([at(1, { completionState: 'complete' })])).toBeNull();
    expect(open([at(1)], false)).toBeNull();
  });
});

describe('sessionGlyph', () => {
  it('reads a run the way the lists read a session', () => {
    expect(sessionGlyph('active')).toBe('working');
    expect(sessionGlyph('waiting_approval')).toBe('needs');
    expect(sessionGlyph('completed')).toBe('done');
    expect(sessionGlyph('killed')).toBe('stopped');
  });
});
