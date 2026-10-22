// @vitest-environment jsdom
//
// The stalled-phase badge (verify-not-trust phase 11): «stalled: <stage> —
// <reason> <N min>» inline on the phase card, naming what the stage left out when
// its session ended, in English and Ukrainian; nothing when the phase is fine.

import { afterEach, describe, expect, it } from 'vitest';
import type { EpicPhase, PhaseLanding, PhaseStall } from '../../api/types';
import { activate } from '../../i18n';
import { act, cleanup, render, screen } from '../../test/render';
import { PhaseCard } from './PhaseCard';
import { StalledBadge } from './StalledBadge';

afterEach(async () => {
  cleanup();
  await act(() => activate('en'));
});

const stall = (over: Partial<PhaseStall> = {}): PhaseStall => ({
  stage: 'executor',
  reason: 'no-turns',
  since: '2026-10-22T11:35:00.000Z',
  sinceMinutes: 25,
  sessionUuid: 'exec-1',
  requestId: null,
  ...over,
});

const badge = (): HTMLElement | null => screen.queryByTestId('phase-stalled-badge');

describe('StalledBadge', () => {
  it.each([
    [stall(), 'stalled: executor — silent 25 min'],
    [stall({ stage: 'reviewer', reason: 'ended', sinceMinutes: 30 }), 'stalled: reviewer — ended without verdict 30 min'],
    [stall({ stage: 'executor', reason: 'ended', sinceMinutes: 5 }), 'stalled: executor — ended without report 5 min'],
    [stall({ stage: 'verifier', reason: 'ended', sinceMinutes: 7 }), 'stalled: verifier — ended without record 7 min'],
    [
      stall({ stage: 'verifier', reason: 'permission-pending', sinceMinutes: 25, requestId: 101 }),
      'stalled: verifier — permission pending 25 min',
    ],
  ])('names stage, reason and minutes (%#)', (s, text) => {
    render(<StalledBadge stall={s} />);
    expect(badge()?.textContent).toBe(`◷${text}`);
    expect(badge()?.getAttribute('role')).toBe('status');
  });

  it('renders nothing for a phase that is not stalled', () => {
    render(<StalledBadge stall={null} />);
    expect(badge()).toBeNull();
    cleanup();
    render(<StalledBadge stall={undefined} />);
    expect(badge()).toBeNull();
  });

  it('shows an unknown reason from a newer daemon verbatim', () => {
    render(<StalledBadge stall={stall({ reason: 'quota-wait' as PhaseStall['reason'] })} />);
    expect(badge()?.textContent).toBe('◷stalled: executor — quota-wait 25 min');
  });

  it('speaks Ukrainian', async () => {
    await act(() => activate('uk'));
    render(<StalledBadge stall={stall({ stage: 'reviewer', reason: 'ended', sinceMinutes: 30 })} />);
    expect(badge()?.textContent).toBe('◷застрягла: рев’юер — завершився без вердикту 30 хв');
    cleanup();
    render(<StalledBadge stall={stall()} />);
    expect(badge()?.textContent).toBe('◷застрягла: виконавець — мовчить 25 хв');
  });
});

const landing: PhaseLanding = {
  state: 'none',
  prUrl: null,
  prNumber: null,
  prProvider: null,
  prStatus: null,
  landedAt: null,
  error: null,
};

function phase(over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    id: 12,
    seq: 2,
    name: 'Line-item CRUD',
    checkboxesDone: 1,
    checkboxesTotal: 4,
    runCheckboxesBefore: 0,
    runOutcome: 'running',
    runState: 'running',
    runError: null,
    runModel: null,
    docModel: null,
    runStartedAt: null,
    runEndedAt: null,
    docUpdatedAt: null,
    completionReport: null,
    forecasts: [],
    forecastLints: [],
    surprise: null,
    landing,
    ...over,
  } as unknown as EpicPhase;
}

describe('PhaseCard stalled badge', () => {
  it('sits inline in the card header when the phase is stalled', () => {
    render(<PhaseCard phase={phase({ stalled: stall() })} />);
    const card = screen.getByTestId('phase-card');
    expect(card.contains(badge())).toBe(true);
    expect(badge()?.textContent).toBe('◷stalled: executor — silent 25 min');
  });

  it('is absent when the phase is not stalled, or the daemon predates the detector', () => {
    render(<PhaseCard phase={phase({ stalled: null })} />);
    expect(badge()).toBeNull();
    cleanup();
    render(<PhaseCard phase={phase()} />);
    expect(badge()).toBeNull();
  });
});
