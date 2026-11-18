// @vitest-environment jsdom
//
// The condensed plan bar (D-14), shown in place of the full PlanHeader while a
// phase is open. The claims: the plan tag, the title (which leads back to the
// phase list, not to the plan's details), the mini phase bar, what was spent
// only when known, and the caller's action on the right.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase } from '../../api/types';
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { computeResolvedSeqs } from './planModel';
import { PlanBar } from './PlanBar';

vi.mock('../../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({ project: { name: 'orders-api' }, slug: 'orders-api' }),
}));

afterEach(cleanup);

const idle = {
  seq: 1,
  name: 'Bucket',
  checkboxesDone: 0,
  checkboxesTotal: 3,
  manualOpen: 0,
  landOpen: 0,
  dependsOn: [],
  runBranch: '',
  runState: 'idle',
  runStartedAt: null,
  docUpdatedAt: null,
  docStatus: null,
  boardColumn: null,
  completionState: 'incomplete',
  decisionHold: null,
} as unknown as EpicPhase;

function epic(over: Partial<Epic> = {}): Epic {
  return {
    taskId: 7,
    externalId: '2026-10-30-rate-limiter',
    projectSlug: 'orders-api',
    title: 'Rate limiter for public API',
    status: 'active',
    phases: [idle],
    ...over,
  } as unknown as Epic;
}

function mount(e: Epic, spent: number | null = null): { onBack: ReturnType<typeof vi.fn> } {
  const onBack = vi.fn();
  render(
    <MemoryRouter>
      <PlanBar
        epic={e}
        resolvedSeqs={computeResolvedSeqs(e.phases)}
        spent={spent}
        onBack={onBack}
        action={<button type="button">▶ run</button>}
      />
    </MemoryRouter>,
  );
  return { onBack };
}

describe('PlanBar', () => {
  it('names the plan, and its title leads back to the phase list', () => {
    const { onBack } = mount(epic());
    expect(screen.getByText('plan')).toBeTruthy();
    const title = screen.getByRole('button', { name: 'Rate limiter for public API' });
    fireEvent.click(title);
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it('draws the mini phase bar, one segment per phase', () => {
    mount(epic({ phases: [idle, { ...idle, seq: 2, name: 'Router' } as EpicPhase] }));
    const bar = screen.getByRole('img', { name: 'phase progress' });
    expect(bar.querySelectorAll('a')).toHaveLength(2);
  });

  it('shows what was spent only when known', () => {
    mount(epic(), 86);
    expect(screen.getByText('$86').parentElement?.textContent).toBe('$86 spent');
    cleanup();
    mount(epic(), null);
    expect(screen.queryByText('spent')).toBeNull();
  });

  it('renders the caller-owned action on the right', () => {
    mount(epic());
    expect(screen.getByRole('button', { name: '▶ run' })).toBeTruthy();
  });
});
