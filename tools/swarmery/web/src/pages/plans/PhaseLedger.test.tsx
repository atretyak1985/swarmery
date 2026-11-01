// @vitest-environment jsdom
//
// The phase ledger (handoff §6). The claims: the columns are the design's, with no
// forecast and no tick (plan D-5); a line opens the phase's page, from anywhere on it,
// and opens nothing in place; the name stays a real link.

import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, Session } from '../../api/types';
import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { PhaseLedger } from './PhaseLedger';
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

afterEach(cleanup);

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

  it('opens the phase from anywhere on its line', () => {
    mount([done, idle]);
    fireEvent.click(within(line('Design bucket')).getByText('1'));
    expect(at()).toBe('/plan/phase/1/story');
  });

  it('keeps the name a real link, for the keyboard and a new tab', () => {
    mount([done]);
    expect(screen.getByRole('link', { name: 'Limiter core' }).getAttribute('href')).toBe('/plan/phase/2/story');
  });

  it('opens nothing in place: no expansion, no caret, no actions', () => {
    mount([live, idle]);
    fireEvent.click(within(line('Design bucket')).getByText('1'));
    expect(document.querySelector('[aria-expanded]')).toBeNull();
    expect(document.body.textContent).not.toMatch(/[▸▾]/);
    expect(screen.queryByRole('button', { name: /Run phase|Cancel/ })).toBeNull();
  });

  it('sums a phase’s runs and counts them on the line', () => {
    mount([done], [run(1, 12, 'spec + ADR', 6), run(2, 12, 'pass', 2)]);
    expect(within(line('Limiter core')).getByText('$8')).toBeTruthy();
    expect(within(line('Limiter core')).getAllByText('2')).toHaveLength(2); // the seq, then the count
  });
});
