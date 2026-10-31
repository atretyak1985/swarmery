// @vitest-environment jsdom
//
// The phase ledger (handoff §6). The claims: the columns are the design's, with no
// forecast and no tick (plan D-5); a line opens in place; the opened line holds the
// runs, each a link to its session, and every action the phase offers; the name stays
// the way to the full details; a done phase offers no doc edit.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, Session } from '../../api/types';
import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { PhaseLedger } from './PhaseLedger';
import { computeResolvedSeqs } from './planModel';
import type { Reverify } from './useReverify';

vi.mock('../../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({ project: { name: 'orders-api' }, slug: 'orders-api' }),
}));

const base = {
  dependsOn: [],
  checkboxesDone: 0,
  checkboxesTotal: 3,
  manualOpen: 0,
  landOpen: 0,
  runBranch: '',
  runState: 'idle',
  runOutcome: 'idle',
  runStartedAt: null,
  runEndedAt: null,
  runSessionUuid: null,
  runEvents: [],
  runModels: [],
  runModel: null,
  docModel: null,
  docUpdatedAt: null,
  docStatus: null,
  boardColumn: null,
  boardTaskExternalId: null,
  activatedAt: null,
  completionState: 'incomplete',
  decisionHold: null,
  landing: null,
  surprise: null,
  forecasts: [],
  reopens: [],
} as unknown as EpicPhase;

const idle = { ...base, id: 11, seq: 1, name: 'Design bucket' } as EpicPhase;
const done = { ...base, id: 12, seq: 2, name: 'Limiter core', completionState: 'complete', checkboxesDone: 3 } as EpicPhase;
const live = {
  ...base,
  id: 13,
  seq: 3,
  name: 'Router',
  runState: 'running',
  runOutcome: 'running',
  runSessionUuid: 'uuid-live',
  runStartedAt: new Date(Date.now() - 60_000).toISOString(),
} as EpicPhase;

const run = (id: number, phaseId: number, title: string, costUsd: number | null): Session =>
  ({
    id,
    sessionUuid: `uuid-${String(id)}`,
    title,
    why: null,
    status: 'completed',
    model: 'claude-opus-5-5',
    costUsd,
    startedAt: '2026-10-30T10:00:00Z',
    endedAt: '2026-10-30T10:41:00Z',
    planGroup: { taskId: 7, role: 'phase', phaseId, phaseSeq: 1, phaseName: 'x', title: 'x' },
  }) as unknown as Session;

function mount(phases: EpicPhase[], sessions: Session[] = [], status: Epic['status'] = 'active'): { onOpenPhase: ReturnType<typeof vi.fn>; onRun: ReturnType<typeof vi.fn>; onCancelRun: ReturnType<typeof vi.fn> } {
  const epic = { taskId: 7, status, phases } as unknown as Epic;
  const onOpenPhase = vi.fn();
  const onRun = vi.fn();
  const onCancelRun = vi.fn();
  render(
    <MemoryRouter>
      <PhaseLedger
        epic={epic}
        resolvedSeqs={computeResolvedSeqs(phases)}
        sessions={sessions}
        terminalSessions={[]}
        terms={null}
        now={Date.now()}
        runBusy={null}
        planRunning={false}
        phaseRunModel="default"
        onPhaseRunModel={() => undefined}
        phaseRunEffort="default"
        onPhaseRunEffort={() => undefined}
        onOpenPhase={onOpenPhase}
        phaseHref={(seq, tab) => `/plan/phase/${String(seq)}/${tab}`}
        onRun={onRun}
        onCancelRun={onCancelRun}
        reverify={{ busyPhaseId: null, start: () => undefined } as unknown as Reverify}
        onReverify={() => undefined}
        onOpenOutcome={() => undefined}
      />
    </MemoryRouter>,
  );
  return { onOpenPhase, onRun, onCancelRun };
}

const row = (name: RegExp | string): HTMLElement => screen.getByRole('button', { name });

afterEach(cleanup);

describe('PhaseLedger', () => {
  it('heads the ledger with the phase count, what landed, and the hint', () => {
    mount([
      { ...base, id: 1, seq: 1, name: 'One', completionState: 'complete' } as EpicPhase,
      { ...base, id: 2, seq: 2, name: 'Two' } as EpicPhase,
    ]);
    expect(screen.getByText('Phases')).toBeTruthy();
    expect(screen.getByText('2 · 1 landed')).toBeTruthy();
    expect(screen.getByText('click a name to open · ▸ to expand runs')).toBeTruthy();
  });

  it('has the design’s columns, without a forecast', () => {
    mount([done]);
    for (const head of ['#', 'phase', 'stage', 'cost', 'runs']) expect(screen.getByText(head)).toBeTruthy();
    expect(screen.queryByText(/forecast/i)).toBeNull();
  });

  it('shows the running phase open, and a click closes it', () => {
    mount([done, live]);
    expect(row('Phase 3 — Router').getAttribute('aria-expanded')).toBe('true');
    expect(row('Phase 2 — Limiter core').getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(row('Phase 3 — Router'));
    expect(row('Phase 3 — Router').getAttribute('aria-expanded')).toBe('false');
  });

  it('opens one line at a time, from the keyboard too', () => {
    mount([done, live]);
    fireEvent.keyDown(row('Phase 2 — Limiter core'), { key: 'Enter' });
    expect(row('Phase 2 — Limiter core').getAttribute('aria-expanded')).toBe('true');
    expect(row('Phase 3 — Router').getAttribute('aria-expanded')).toBe('false');
  });

  it('keeps the name a link to the details, and clicking it does not toggle the line', () => {
    mount([done]);
    const name = screen.getByRole('link', { name: 'Limiter core' });
    expect(name.getAttribute('href')).toBe('/plan/phase/2/story');
    fireEvent.click(name);
    expect(row('Phase 2 — Limiter core').getAttribute('aria-expanded')).toBe('false');
  });

  it('sums a phase’s runs, counts them, and links each to its session', () => {
    mount([done], [run(1, 12, 'spec + ADR', 6), run(2, 12, 'pass', 2)]);
    expect(within(row('Phase 2 — Limiter core')).getByText('$8')).toBeTruthy();
    expect(within(row('Phase 2 — Limiter core')).getByText(/^2 /)).toBeTruthy();
    fireEvent.click(row('Phase 2 — Limiter core'));
    const links = within(screen.getByRole('list', { name: 'runs of phase 2' })).getAllByRole('link');
    expect(links).toHaveLength(2);
    expect(links[0]?.textContent).toContain('spec + ADR');
    expect(links[0]?.textContent).toContain('41m');
  });

  it('offers Run on the next runnable phase, without a click', () => {
    const { onRun } = mount([done, idle]);
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));
    expect(onRun).toHaveBeenCalledWith(11);
  });

  it('keeps Cancel, the session and the live chip on a running phase', () => {
    const { onCancelRun } = mount([live]);
    expect(screen.getByRole('link', { name: 'session' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancelRun).toHaveBeenCalledWith(13);
  });

  it('keeps the summary on a done phase and offers no doc edit', () => {
    const { onOpenPhase } = mount([done]);
    fireEvent.click(row('Phase 2 — Limiter core'));
    fireEvent.click(screen.getByRole('button', { name: '✓ summary' }));
    expect(onOpenPhase).toHaveBeenCalledWith(2, 'report');
    expect(screen.queryByRole('link', { name: 'edit doc' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Run phase' })).toBeNull();
  });

  it('links every detail tab from an open line, and criteria to where they are ticked', () => {
    mount([idle]);
    const href = (name: string | RegExp): string | null => screen.getByRole('link', { name }).getAttribute('href');
    expect(href('story')).toBe('/plan/phase/1/story');
    expect(href('runs')).toBe('/plan/phase/1/runs');
    expect(href('report')).toBe('/plan/phase/1/report');
    expect(href('edit doc')).toBe('/plan/phase/1/edit');
    expect(href(/^criteria · 0\/3/)).toBe('/plan/phase/1/criteria');
  });

  it('puts the model and effort pickers beside the Run they govern', () => {
    mount([idle]);
    expect(screen.getByRole('button', { name: 'Run phase' })).toBeTruthy();
    expect(screen.getAllByRole('combobox')).toHaveLength(2);
  });

  it('leaves the pickers out while the phase waits on the operator, and keeps Run', () => {
    mount([{ ...idle, checkboxesDone: 2, manualOpen: 1 } as EpicPhase]);
    fireEvent.click(row('Phase 1 — Design bucket'));
    expect(screen.getByRole('button', { name: 'Run phase' })).toBeTruthy();
    expect(screen.queryAllByRole('combobox')).toHaveLength(0);
  });

  it('disables Run on a plan that is not active', () => {
    mount([live, idle], [], 'paused');
    fireEvent.click(row('Phase 1 — Design bucket'));
    expect((screen.getByRole('button', { name: 'Run phase' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
