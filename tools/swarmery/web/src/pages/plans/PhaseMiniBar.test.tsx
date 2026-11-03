// @vitest-environment jsdom
//
// The list row's phase bar. The claims: one segment per phase, in phase order, each
// carrying the same state the plan header's bar would; nothing for a plan with no
// phases; and the bar is decorative (aria-hidden), since the summary carries the count.

import { afterEach, describe, expect, it } from 'vitest';
import type { EpicPhase } from '../../api/types';
import { cleanup, render } from '../../test/render';
import { PhaseMiniBar } from './PhaseMiniBar';
import { computeResolvedSeqs } from './planModel';

const base = {
  dependsOn: [],
  checkboxesDone: 0,
  checkboxesTotal: 3,
  manualOpen: 0,
  landOpen: 0,
  runBranch: '',
  runState: 'idle',
  runOutcome: 'idle',
  runSessionUuid: null,
  runEvents: [],
  completionState: 'incomplete',
  decisionHold: null,
  landing: null,
  forecasts: [],
  reopens: [],
} as unknown as EpicPhase;

const phases: EpicPhase[] = [
  { ...base, id: 1, seq: 1, name: 'a', completionState: 'complete', checkboxesDone: 3 } as EpicPhase,
  { ...base, id: 2, seq: 2, name: 'b', runState: 'running', runOutcome: 'running' } as EpicPhase,
  { ...base, id: 3, seq: 3, name: 'c' } as EpicPhase,
];

afterEach(cleanup);

describe('PhaseMiniBar', () => {
  it('draws one segment per phase, in order, by state', () => {
    const { container } = render(<PhaseMiniBar phases={phases} resolvedSeqs={computeResolvedSeqs(phases)} />);
    const bar = container.querySelector('[data-phase-bar]');
    expect(bar?.getAttribute('aria-hidden')).toBe('true');
    const states = [...(bar?.querySelectorAll('[data-state]') ?? [])].map((s) => s.getAttribute('data-state'));
    expect(states).toEqual(['landed', 'running', 'pending']);
    expect(bar?.querySelector('a, button')).toBeNull();
  });

  it('renders nothing for a plan with no phases', () => {
    const { container } = render(<PhaseMiniBar phases={[]} resolvedSeqs={new Set()} />);
    expect(container.querySelector('[data-phase-bar]')).toBeNull();
  });
});
