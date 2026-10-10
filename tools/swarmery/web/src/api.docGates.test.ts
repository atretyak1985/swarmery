// The two doc-gate run refusals (phase-run outcomes plan, phase 3): 409
// `manual-only` and `not-yet-earliest` reach the run strip as their sentence
// plus the next step (`hint`), and a `done` run's "[LAND] left" detail reads as
// a note, not an error.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { markPlanCriterion, RUN_CONFLICT_HINTS, runEpicPhase, type PhaseRunBranchError } from './api';
import { isRunNote } from './components/RunOutcomeModal';

function respond(status: number, body: unknown): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })),
  );
}

async function refusal(): Promise<PhaseRunBranchError> {
  try {
    await runEpicPhase(1, 2);
  } catch (e) {
    return e as PhaseRunBranchError;
  }
  throw new Error('runEpicPhase resolved');
}

afterEach(() => vi.unstubAllGlobals());

describe('doc-gate run refusals', () => {
  it('manual-only carries the daemon hint after its sentence', async () => {
    respond(409, {
      error: 'every open criterion is [MANUAL]',
      code: 'manual-only',
      manualOpen: 2,
      hint: 'Do the checks by hand.',
    });
    const err = await refusal();
    expect(err.code).toBe('manual-only');
    expect(err.hint).toBe('Do the checks by hand.');
    expect(err.message).toBe('every open criterion is [MANUAL] — Do the checks by hand.');
  });

  it('not-yet-earliest falls back to the client hint when the body has none', async () => {
    respond(409, { error: 'not before 2026-11-01', code: 'not-yet-earliest', earliest: '2026-11-01' });
    const err = await refusal();
    expect(err.code).toBe('not-yet-earliest');
    expect(err.hint).toBe(RUN_CONFLICT_HINTS['not-yet-earliest']);
    expect(err.message.startsWith('not before 2026-11-01 — ')).toBe(true);
  });

  it('leaves every other refusal untouched', async () => {
    respond(409, { error: 'dependencies unmet', code: 'deps-unmet', hint: 'ignored' });
    const err = await refusal();
    expect(err.message).toBe('dependencies unmet');
    expect(err.hint).toBeUndefined();
  });
});

describe('markPlanCriterion', () => {
  it('PATCHes {line, class} and surfaces the daemon error', async () => {
    respond(422, { error: 'line is not an acceptance criterion' });
    await expect(markPlanCriterion(3, 'phase-1.md', 4, 'LAND')).rejects.toThrow('line is not an acceptance criterion');
    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe('/api/epics/3/docs?path=phase-1.md');
    expect(call?.[1]?.method).toBe('PATCH');
    expect(call?.[1]?.body).toBe(JSON.stringify({ line: 4, class: 'LAND' }));
  });
});

describe('isRunNote', () => {
  it('recognises the settle detail of a done run with classed criteria left', () => {
    expect(isRunNote('2 [LAND] criteria left for landing')).toBe(true);
    expect(isRunNote('1 [LAND] criteria left for landing; 1 [MANUAL] criteria left for the operator')).toBe(true);
    expect(isRunNote('1 [MANUAL] criteria left for the operator')).toBe(true);
    expect(isRunNote('exit status 1: boom')).toBe(false);
    expect(isRunNote('blocked: [LAND] criteria left for landing')).toBe(false);
  });
});
