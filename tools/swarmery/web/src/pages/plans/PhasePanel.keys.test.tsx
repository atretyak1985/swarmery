// @vitest-environment jsdom
//
// PhasePanel's ↑/↓ listener must exist as soon as the panel's DOM does
// (flaky-tests plan, W1). Testing Library's findBy* can resolve right after a
// commit and before React flushes passive effects, so a keydown fired at that
// moment must still reach the panel.
//
// The race is reproduced without timers: a sibling rendered after the panel
// dispatches the key from its own layout effect. Layout effects run during the
// commit, in tree order, before any passive effect, so the key lands exactly in
// the window a loaded test runner can hit.

import { useLayoutEffect } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase } from '../../api/types';
import { cleanup, render } from '../../test/render';
import { PhasePanel } from './PhasePanel';

afterEach(cleanup);

function phase(seq: number): EpicPhase {
  return {
    id: 10 + seq,
    seq,
    name: `Phase ${String(seq)} name`,
    checkboxesDone: 0,
    checkboxesTotal: 3,
    runState: 'idle',
    runOutcome: 'idle',
    runModel: null,
    docModel: null,
  } as unknown as EpicPhase;
}

const PHASES = [phase(1), phase(2), phase(3)];
const EPIC = { taskId: 7, phases: PHASES } as unknown as Epic;

/** Presses ArrowDown on the document during the commit's layout phase. */
function Prober(): null {
  useLayoutEffect(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  }, []);
  return null;
}

describe('PhasePanel keys', () => {
  it('handles an ArrowDown pressed in the commit that mounts the panel', () => {
    const onNext = vi.fn();
    render(
      <MemoryRouter>
        <PhasePanel
          epic={EPIC}
          phase={PHASES[1] as EpicPhase}
          tab="story"
          onTab={vi.fn()}
          onPrev={vi.fn()}
          onNext={onNext}
        >
          <p>body</p>
        </PhasePanel>
        <Prober />
      </MemoryRouter>,
    );
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});
