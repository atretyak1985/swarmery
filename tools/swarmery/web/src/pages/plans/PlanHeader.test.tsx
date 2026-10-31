// @vitest-environment jsdom
//
// The plan header (handoff §6). The claims: one main button per state; what the
// design leaves out of the row stays reachable behind ⋯; the blocker banner stands
// over the band only when a phase waits, and leads to Needs you; the phase bar shows
// each phase in its state and opens it, with a legend of what is present; the
// metadata line invents nothing: a forecast never appears, because no dollar forecast
// exists (plan D-5, D-10); the goal comes from the README.

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
        phaseRunModel="default"
        phaseRunEffort="default"
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
  it('shows the goal from the README, what was spent, and no forecast', async () => {
    mount(epic({ linkedSessions: [{ costUsd: 86 } as Epic['linkedSessions'][number]] }));
    await screen.findByText('Protect the API.');
    expect(screen.getByText('$86').parentElement?.textContent).toBe('$86 spent');
    expect(screen.getByText('2/6 criteria')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/forecast/i);
  });

  it('keeps to what frames the phase when one is open under it', async () => {
    mount(epic({ linkedSessions: [{ costUsd: 86 } as Epic['linkedSessions'][number]] }), { nested: true });
    await new Promise((resolve) => setTimeout(resolve));
    expect(screen.queryByText('Protect the API.')).toBeNull();
    expect(screen.getByText('$86').parentElement?.className).toContain('whitespace-nowrap');
    expect(screen.getByText('2/6 criteria')).toBeTruthy();
  });

  it('leaves `spent` out while no session is priced', () => {
    mount(epic());
    expect(document.body.textContent).not.toContain('spent');
  });

  it('stands the blocker over the band, leading to Needs you, and hides it otherwise', () => {
    mount(epic({ phases: [manual, idle] }));
    const banner = screen.getByRole('link', { name: /1 phase waits on a decision from you/ });
    expect(banner.getAttribute('href')).toBe('/p/orders-api/needs-you');
    expect(banner.textContent).toContain('1 manual check');
    expect(banner.compareDocumentPosition(screen.getByRole('heading')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    cleanup();
    mount(epic());
    expect(screen.queryByRole('link', { name: /wait/ })).toBeNull();
  });

  it('draws a segment per phase, in its state, opening that phase', () => {
    const landed = { ...idle, seq: 1, name: 'Bucket', completionState: 'complete' } as EpicPhase;
    const live = { ...idle, seq: 2, name: 'Router', runState: 'running' } as EpicPhase;
    const stopped = { ...idle, seq: 3, name: 'Migrate', guardDecision: { decisionId: 'D-1' } } as unknown as EpicPhase;
    const waiting = { ...manual, seq: 4, name: 'Check' } as EpicPhase;
    const next = { ...idle, seq: 5, name: 'Docs' } as EpicPhase;
    mount(epic({ phases: [landed, live, stopped, waiting, next] }));
    const segments = screen.getAllByRole('link', { name: /^phase \d/ });
    expect(segments.map((s) => s.getAttribute('data-state'))).toEqual(['landed', 'running', 'guard', 'manual', 'pending']);
    expect(segments.map((s) => s.className.match(/bg-[a-z-]+/)?.[0])).toEqual([
      'bg-green',
      'bg-blue',
      'bg-red',
      'bg-amber',
      'bg-line-strong',
    ]);
    expect(segments[2]?.getAttribute('aria-label')).toBe('phase 3 · Migrate · guard stop');
    expect(segments[1]?.getAttribute('href')).toBe('/p/orders-api/plans/2026-10-30-rate-limiter/phase/2');
  });

  it('counts only the states present in the legend', () => {
    mount(epic({ phases: [manual, idle] }));
    expect(screen.getAllByText('■').map((mark) => mark.parentElement?.textContent)).toEqual([
      '■ 1 manual check',
      '■ 1 not started',
    ]);
  });

  it('shows a failed phase in red, named as failed, not as not started', () => {
    const failed = { ...idle, seq: 1, name: 'Bucket', runState: 'failed' } as EpicPhase;
    mount(epic({ phases: [failed] }));
    const segment = screen.getByRole('link', { name: 'phase 1 · Bucket · failed' });
    expect(segment.className).toContain('bg-red');
    expect(screen.getByText('■').parentElement?.textContent).toBe('■ 1 failed');
  });

  it('prints what a run started now uses, from the page state', () => {
    mount(epic(), { phaseRunModel: 'fable', phaseRunEffort: 'xhigh' });
    expect(screen.getByText(/worktree per phase$/).textContent).toBe('fable · xhigh · worktree per phase');
    cleanup();
    mount(epic());
    expect(screen.getByText(/worktree per phase$/).textContent).toContain('per phase doc, else');
  });

  it('says paused and archived in a notice above the content', () => {
    mount(epic({ status: 'archived' }));
    const notice = screen.getByText('archived · shown under Archived in the list; nothing runs');
    expect(notice.compareDocumentPosition(screen.getByRole('heading')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('keeps the action row to the design: spec moved to the metadata line', () => {
    const { onOpenPlan } = mount(epic({ spec: { covered: 14, total: 20, criteria: [], unknownRefs: [] } as Epic['spec'] }));
    const spec = screen.getByRole('button', { name: 'spec 14/20' });
    expect(spec.className).toContain('text-amber');
    fireEvent.click(spec);
    expect(onOpenPlan).toHaveBeenCalledWith('spec');
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
