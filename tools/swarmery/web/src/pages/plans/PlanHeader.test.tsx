// @vitest-environment jsdom
//
// The plan header (handoff §6). The claims: one main button per state; what the
// design leaves out of the row stays reachable behind ⋯; the amber block appears
// only when a phase waits and leads to Needs you; a forecast never appears, because
// no dollar forecast exists (plan D-5); the goal comes from the README.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PlanRun } from '../../api/types';
import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { computeResolvedSeqs } from './planModel';
import { PlanHeader, type PlanHeaderProps } from './PlanHeader';

const fetchPlanDoc = vi.hoisted(() => vi.fn());
vi.mock('../../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api')>()),
  fetchPlanDoc,
}));
vi.mock('../../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({ project: { name: 'orders-api' }, slug: 'orders-api' }),
}));

const manual = {
  seq: 1,
  checkboxesDone: 2,
  checkboxesTotal: 3,
  manualOpen: 1,
  landOpen: 0,
  dependsOn: [],
  runBranch: 'plan/rate-limiter',
  runState: 'idle',
  runStartedAt: '2026-10-30T10:00:00Z',
  docUpdatedAt: null,
  docStatus: null,
  boardColumn: null,
  completionState: 'incomplete',
  decisionHold: null,
} as unknown as EpicPhase;
const idle = { ...manual, seq: 2, checkboxesDone: 0, manualOpen: 0, runBranch: '', runStartedAt: null } as EpicPhase;

const running: PlanRun = {
  agent: 'tech-lead',
  mode: 'auto',
  runState: 'running',
  runSessionUuid: 'uuid-1',
  runStartedAt: '2026-10-30T10:00:00Z',
  runError: null,
  runEvents: [],
} as unknown as PlanRun;

function epic(over: Partial<Epic> = {}): Epic {
  return {
    taskId: 7,
    externalId: '2026-10-30-rate-limiter',
    projectSlug: 'orders-api',
    title: 'Rate limiter for public API',
    status: 'active',
    startedAt: null,
    hasSpec: true,
    spec: { covered: 20, total: 20, criteria: [], unknownRefs: [] },
    phases: [idle],
    rollup: { done: 2, total: 6, pct: 33 },
    planRun: null,
    cardExternalId: null,
    linkedSessions: [],
    ...over,
  } as unknown as Epic;
}

function mount(e: Epic, over: Partial<PlanHeaderProps> = {}): {
  onLifecycle: ReturnType<typeof vi.fn>;
  onOpenPlan: ReturnType<typeof vi.fn>;
  onRevise: ReturnType<typeof vi.fn>;
  onRunPlan: ReturnType<typeof vi.fn>;
} {
  const onLifecycle = vi.fn();
  const onOpenPlan = vi.fn();
  const onRevise = vi.fn();
  const onRunPlan = vi.fn();
  render(
    <MemoryRouter>
      <PlanHeader
        epic={e}
        resolvedSeqs={computeResolvedSeqs(e.phases)}
        complete={false}
        now={Date.now()}
        terminal={[]}
        busyLifecycle={false}
        onLifecycle={onLifecycle}
        planRunBusy={false}
        phaseRunActive={false}
        onRunPlan={onRunPlan}
        onCancelPlanRun={() => undefined}
        onOpenPlan={onOpenPlan}
        stagedCount={0}
        reviseBusy={false}
        onRevise={onRevise}
        {...over}
      />
    </MemoryRouter>,
  );
  return { onLifecycle, onOpenPlan, onRevise, onRunPlan };
}

function openMenu(): HTMLElement {
  fireEvent.click(screen.getByRole('button', { name: 'plan actions' }));
  return screen.getByRole('menu');
}

beforeEach(() => {
  fetchPlanDoc.mockReset();
  fetchPlanDoc.mockResolvedValue({ path: 'README.md', content: '## Objective\n\nProtect the API. Then more.\n' });
});
afterEach(cleanup);

describe('PlanHeader', () => {
  it('shows the goal from the README, the numbers, and no forecast', async () => {
    mount(epic({ linkedSessions: [{ costUsd: 86 } as Epic['linkedSessions'][number]] }));
    await screen.findByText('Protect the API.');
    expect(screen.getByText('phases landed')).toBeTruthy();
    expect(screen.getByText('$86')).toBeTruthy();
    expect(screen.getByText('2/6')).toBeTruthy();
    expect(screen.queryByText(/forecast/i)).toBeNull();
  });

  it('leaves `spent` out while no session is priced', () => {
    mount(epic());
    expect(screen.queryByText('spent')).toBeNull();
  });

  it('points the amber block at Needs you when a phase waits, and hides it otherwise', () => {
    mount(epic({ phases: [manual, idle] }));
    const block = screen.getByRole('link', { name: /1 phase waits on a decision from you/ });
    expect(block.getAttribute('href')).toBe('/p/orders-api/needs-you');
    cleanup();
    mount(epic());
    expect(screen.queryByRole('link', { name: /wait/ })).toBeNull();
  });

  it('offers run on an idle plan and opens the agent and mode panel', () => {
    const { onRunPlan } = mount(epic());
    fireEvent.click(screen.getByRole('button', { name: '▶ run plan' }));
    fireEvent.click(screen.getByRole('button', { name: '▶ Run' }));
    expect(onRunPlan).toHaveBeenCalledWith('', 'auto');
  });

  it('offers pause while a run is live, and keeps its Cancel', () => {
    const { onLifecycle } = mount(epic({ planRun: running }));
    expect(screen.queryByRole('button', { name: '▶ run plan' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '❚❚ pause plan' }));
    expect(onLifecycle).toHaveBeenCalledWith(expect.anything(), 'pause');
  });

  it('offers resume on a paused plan and says why it is paused', () => {
    const { onLifecycle } = mount(epic({ status: 'paused' }));
    screen.getByText('paused · nothing new starts until you resume');
    fireEvent.click(screen.getByRole('button', { name: '▶ resume plan' }));
    expect(onLifecycle).toHaveBeenCalledWith(expect.anything(), 'resume');
  });

  it('does not claim a pause stopped the run in flight', () => {
    mount(epic({ status: 'paused', planRun: running }));
    screen.getByText('paused · the run in flight keeps going; nothing new starts after it');
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy();
  });

  it('offers retry after a failed run', () => {
    mount(epic({ planRun: { ...running, runState: 'failed', runError: 'boom' } }));
    expect(screen.getByRole('button', { name: '▶ retry plan' })).toBeTruthy();
    screen.getByText('Run failed');
  });

  it('keeps pause, revise, edit and archive behind ⋯ on an idle plan', () => {
    const { onLifecycle, onOpenPlan, onRevise } = mount(epic());
    const menu = within(openMenu());
    expect(menu.getAllByRole('menuitem').map((b) => b.getAttribute('aria-label'))).toEqual([
      'Pause plan',
      'Revise plan',
      'Edit phases',
      'Archive plan',
    ]);
    fireEvent.click(menu.getByRole('menuitem', { name: 'Revise plan' }));
    expect(onRevise).toHaveBeenCalled();

    fireEvent.click(within(openMenu()).getByRole('menuitem', { name: 'Edit phases' }));
    expect(onOpenPlan).toHaveBeenCalledWith('edit');

    fireEvent.click(within(openMenu()).getByRole('menuitem', { name: 'Archive plan' }));
    expect(onLifecycle).toHaveBeenCalledWith(expect.anything(), 'archive');
  });

  it('refuses revise while a run owns the docs', () => {
    mount(epic(), { phaseRunActive: true });
    const item = within(openMenu()).getByRole('menuitem', { name: 'Revise plan' });
    expect(item.hasAttribute('disabled')).toBe(true);
    expect(item.textContent).toContain('a run owns the plan docs');
  });

  it('unarchives an archived plan and has nothing to run', () => {
    const { onLifecycle } = mount(epic({ status: 'archived' }));
    expect((screen.getByRole('button', { name: 'archived' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(within(openMenu()).getByRole('menuitem', { name: 'Unarchive' }));
    expect(onLifecycle).toHaveBeenCalledWith(expect.anything(), 'restore');
  });

  it('closes the menu on Esc without leaking the key to the page', () => {
    mount(epic());
    openMenu();
    const page = vi.fn();
    window.addEventListener('keydown', page);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    window.removeEventListener('keydown', page);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(page).not.toHaveBeenCalled();
  });

  it('opens README, spec and the summary of a finished plan', () => {
    const { onOpenPlan } = mount(epic(), { complete: true });
    expect(screen.queryByRole('button', { name: '▶ run plan' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '❐ README' }));
    expect(onOpenPlan).toHaveBeenLastCalledWith('plan');
    fireEvent.click(screen.getByRole('button', { name: 'spec 20/20' }));
    expect(onOpenPlan).toHaveBeenLastCalledWith('spec');
    fireEvent.click(screen.getByRole('button', { name: '✓ summary' }));
    expect(onOpenPlan).toHaveBeenLastCalledWith('summary');
  });

  it('links a staged revision from the state line', () => {
    const { onOpenPlan } = mount(epic(), { stagedCount: 2 });
    fireEvent.click(screen.getByRole('button', { name: '2 revisions staged · review →' }));
    expect(onOpenPlan).toHaveBeenCalledWith('revisions');
  });
});
