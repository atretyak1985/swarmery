// @vitest-environment jsdom
//
// The phase ledger (handoff §6). The claims: the columns are the design's, with no
// forecast and no tick (plan D-5); a line opens the phase's page, from anywhere on it,
// and opens nothing in place; the name stays a real link. The action column carries
// the one thing a line needs now — run, stop, tick, verify again, write the report, or
// the green report link — and using it does not open the phase. A phase with every
// criterion ticked never offers a run, whatever its last run's outcome.

import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, Session } from '../../api/types';
import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { PhaseLedger, criterionMarks } from './PhaseLedger';
import { computeResolvedSeqs } from './planModel';

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

const onRun = vi.fn();
const onCancelRun = vi.fn();
const onReverify = vi.fn();

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

function Location(): JSX.Element {
  const { pathname } = useLocation();
  return <output aria-label="location">{pathname}</output>;
}

function mount(phases: EpicPhase[], sessions: Session[] = []): void {
  render(
    <MemoryRouter initialEntries={['/plan']}>
      <PhaseLedger
        epic={{ taskId: 7, status: 'active', phases } as unknown as Epic}
        runBusy={null}
        planRunning={false}
        onRun={onRun}
        onCancelRun={onCancelRun}
        reverify={{ busy: new Set(), start: vi.fn() }}
        onReverify={onReverify}
        resolvedSeqs={computeResolvedSeqs(phases)}
        sessions={sessions}
        terminalSessions={[]}
        terms={null}
        now={Date.now()}
        phaseHref={(seq, tab) => `/plan/phase/${String(seq)}/${tab}`}
      />
      <Location />
    </MemoryRouter>,
  );
}

const line = (name: string): HTMLElement => screen.getByRole('link', { name }).closest('li') as HTMLElement;
const at = (): string | null => screen.getByRole('status', { name: 'location' }).textContent;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('PhaseLedger', () => {
  it('heads the ledger with the phase count and what landed', () => {
    mount([done, idle]);
    expect(screen.getByText('Phases')).toBeTruthy();
    expect(screen.getByText('2 · 1 landed')).toBeTruthy();
  });

  it('has the design’s columns, without a forecast', () => {
    mount([done]);
    expect(screen.getByText('cost')).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/forecast/i);
  });

  it('keeps every desk column a fixed track, so each line stays under its header', () => {
    // Each line is its own grid: an `auto` track sizes per line, and one wide pill
    // shifted that line's stage, cost and criteria off the header.
    mount([done, idle]);
    const grids = [...document.querySelectorAll('div.grid')];
    expect(grids.length).toBeGreaterThan(1);
    for (const g of grids) {
      const desk = g.className.match(/desk:grid-cols-\[([^\]]+)\]/)?.[1] ?? '';
      expect(desk).not.toMatch(/auto/);
    }
  });

  it('counts ticked criteria and links them to the criteria tab without opening the story', () => {
    const manual = { ...base, id: 14, seq: 4, name: 'Store accounts', checkboxesDone: 2, checkboxesTotal: 4, manualOpen: 1 } as EpicPhase;
    const none = { ...base, id: 15, seq: 5, name: 'Empty', checkboxesTotal: 0 } as EpicPhase;
    mount([done, manual, none]);
    expect(screen.getByText('criteria')).toBeTruthy();
    const full = within(line('Limiter core')).getByText('3/3');
    expect(full.className).toContain('text-green');
    expect(within(line('Store accounts')).getByText('2/4').className).toContain('text-brand');
    const open = within(line('Store accounts')).getByRole('link', {
      name: '2 of 4 criteria ticked · 1 [MANUAL] wait on you',
    });
    expect(open.getAttribute('data-tip')).toBe('2 of 4 criteria ticked · 1 [MANUAL] wait on you');
    expect(within(line('Empty')).queryByText(/\d+\/\d+/)).toBeNull();
    expect(within(line('Empty')).queryByTestId('criteria-bar')).toBeNull();
    fireEvent.click(open);
    expect(at()).toBe('/plan/phase/4/criteria');
  });

  it('draws a dash per criterion: ticked, then [MANUAL] waiting, then still open', () => {
    const manual = { ...base, id: 14, seq: 4, name: 'Store accounts', checkboxesDone: 2, checkboxesTotal: 4, manualOpen: 1 } as EpicPhase;
    mount([manual]);
    const dashes = [...within(line('Store accounts')).getByTestId('criteria-bar').children];
    expect(dashes.map((d) => d.getAttribute('data-mark'))).toEqual(['ticked', 'ticked', 'manual', 'open']);
    expect(dashes.map((d) => d.className.match(/bg-(green|brand|line)/)?.[1])).toEqual(['green', 'green', 'brand', 'line']);
  });

  it('folds a long criteria list into three proportional runs', () => {
    expect(criterionMarks(13, 13, 0).every((m) => m === 'ticked')).toBe(true);
    expect(criterionMarks(5, 3, 2)).toEqual(['ticked', 'ticked', 'ticked']);
    const long = { ...base, id: 16, seq: 6, name: 'Long', checkboxesDone: 10, checkboxesTotal: 20, manualOpen: 4 } as EpicPhase;
    mount([long]);
    const runs = [...within(line('Long')).getByTestId('criteria-bar').children] as HTMLElement[];
    expect(runs.map((r) => [r.getAttribute('data-mark'), r.style.flexGrow])).toEqual([
      ['ticked', '10'],
      ['manual', '4'],
      ['open', '6'],
    ]);
  });

  it('opens the phase from anywhere on its line', () => {
    mount([done, idle]);
    fireEvent.click(within(line('Design bucket')).getByText('1'));
    expect(at()).toBe('/plan/phase/1/story');
  });

  it('keeps the name a real link, for the keyboard and a new tab', () => {
    mount([done]);
    expect(screen.getByRole('link', { name: 'Limiter core' }).getAttribute('href')).toBe('/plan/phase/2/story');
  });

  it('opens nothing in place: no expansion, no caret', () => {
    mount([live, idle]);
    fireEvent.click(within(line('Design bucket')).getByText('1'));
    expect(document.querySelector('[aria-expanded]')).toBeNull();
    expect(document.body.textContent).not.toMatch(/[▸▾]/);
  });

  it('sums a phase’s runs, without a run-count column', () => {
    mount([done], [run(1, 12, 'spec + ADR', 6), run(2, 12, 'pass', 2)]);
    expect(within(line('Limiter core')).getByText('$8')).toBeTruthy();
    expect(within(line('Limiter core')).getAllByText('2')).toHaveLength(1); // the seq alone
    expect(screen.getByText('action')).toBeTruthy();
    expect(screen.queryByText('runs')).toBeNull();
  });

  it('runs a ready phase from its line without opening it', () => {
    mount([idle]);
    fireEvent.click(within(line('Design bucket')).getByRole('button', { name: '▶ run' }));
    expect(onRun).toHaveBeenCalledWith(11);
    expect(at()).toBe('/plan');
  });

  it('stops a live phase from its line', () => {
    mount([live]);
    fireEvent.click(within(line('Router')).getByRole('button', { name: 'stop' }));
    expect(onCancelRun).toHaveBeenCalledWith(13);
  });

  it('links a landed phase to its report, in green', () => {
    mount([done]);
    const report = within(line('Limiter core')).getByRole('link', { name: '✓ report' });
    expect(report.getAttribute('href')).toBe('/plan/phase/2/report');
    expect(report.className).toContain('text-green');
  });

  it('names what a phase depends on, a landed dependency in green', () => {
    const third = { ...base, id: 16, seq: 3, name: 'Wiring', dependsOn: [1, 2] } as EpicPhase;
    mount([idle, done, third]);
    const deps = within(line('Wiring'));
    expect(deps.getByText('depends on')).toBeTruthy();
    expect(deps.getByText('#2').className).toContain('text-green');
    expect(deps.getByText('#1').className).not.toContain('text-green');
    expect(within(line('Design bucket')).queryByText('depends on')).toBeNull();
  });

  it('offers nothing to a phase still waiting on another', () => {
    const waiting = { ...base, id: 14, seq: 4, name: 'Budgets', dependsOn: [1] } as EpicPhase;
    mount([idle, waiting]);
    expect(within(line('Budgets')).queryByRole('button')).toBeNull();
  });

  it('sends a phase only you can close to its criteria', () => {
    const manual = { ...base, id: 15, seq: 5, name: 'Store review', manualOpen: 1, checkboxesDone: 2 } as EpicPhase;
    mount([manual]);
    expect(within(line('Store review')).getByRole('link', { name: 'tick criteria' }).getAttribute('href')).toBe(
      '/plan/phase/5/criteria',
    );
  });

  // All ticked, the verifier timed out, and the last run (a rebase) ticked nothing:
  // the line offered "↻ retry", which would re-run a finished phase.
  const unverified = {
    ...base,
    id: 16,
    seq: 6,
    name: 'Lesson discipline',
    checkboxesDone: 3,
    completionState: 'unverified',
    runState: 'done',
    runOutcome: 'noop',
    verifyVerdict: 'inconclusive',
    canReverify: true,
  } as EpicPhase;

  it('verifies an unconfirmed phase again instead of re-running it', () => {
    mount([unverified]);
    const row = line('Lesson discipline');
    expect(within(row).queryByText('↻ retry')).toBeNull();
    expect(within(row).getByText('verification could not conclude')).toBeTruthy();
    fireEvent.click(within(row).getByRole('button', { name: 'Verify again' }));
    expect(onReverify).toHaveBeenCalledWith(16);
    expect(onRun).not.toHaveBeenCalled();
    expect(at()).toBe('/plan');
  });

  it('offers nothing to an unconfirmed phase the server will not re-grade', () => {
    mount([{ ...unverified, canReverify: false } as EpicPhase]);
    expect(within(line('Lesson discipline')).queryByRole('button')).toBeNull();
  });

  it('sends an unreported phase to its report, not to a run', () => {
    const unreported = { ...unverified, completionState: 'unreported', verifyVerdict: 'pass' } as EpicPhase;
    mount([unreported]);
    const row = line('Lesson discipline');
    expect(within(row).queryByRole('button')).toBeNull();
    expect(within(row).getByText('no completion report')).toBeTruthy();
    expect(within(row).getByRole('link', { name: 'write report' }).getAttribute('href')).toBe('/plan/phase/6/report');
  });
});
