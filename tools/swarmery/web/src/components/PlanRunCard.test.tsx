// @vitest-environment jsdom
//
// Interaction tests for the collapsed plan-run card (Sessions redesign): it
// must open COLLAPSED, and "open plan →" must navigate without toggling it.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/PlanRunCard.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import type { Session, SessionPlanGroup } from '../api/types';
import type { PlanRun } from '../lib/sessionsView';
import { PlanRunCard } from './PlanRunCard';

const BOILERPLATE =
  'You are the controller for an ENTIRE approved implementation plan. Read the plan…';
/** The daemon derives `why` from the first user turn — for a phase run that IS
 * the boilerplate dispatch prompt, so the sub-line must never fall back to it. */
const PHASE_BOILERPLATE =
  'You are executing ONE phase of an approved implementation plan, headlessly, in an isolated git worktree of the project repo (y…';

let nextId = 1;

function makeSession(planGroup: SessionPlanGroup, over: Partial<Session> = {}): Session {
  return {
    id: nextId++,
    projectId: 1,
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    sessionUuid: `uuid-${String(nextId)}`,
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status: 'completed',
    startedAt: '2026-08-02T10:00:00Z',
    endedAt: '2026-08-02T11:00:00Z',
    title: BOILERPLATE,
    source: 'jsonl',
    planGroup,
    ...over,
  };
}

function makeRun(): PlanRun {
  const rows = [
    makeSession({
      taskId: 7,
      title: 'Sessions page redesign',
      role: 'controller',
      phaseId: null,
      phaseSeq: null,
      phaseName: null,
    }),
    makeSession(
      {
        taskId: 7,
        title: 'Sessions page redesign',
        role: 'phase',
        phaseId: 1,
        phaseSeq: 5,
        phaseName: 'day-first timeline',
      },
      { title: PHASE_BOILERPLATE, why: PHASE_BOILERPLATE },
    ),
  ];
  return {
    taskId: 7,
    title: 'Sessions page redesign',
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    newestAt: '2026-08-02T10:00:00Z',
    rows,
  };
}

function renderCard(expanded: boolean, onToggle: () => void, hideProject = false): void {
  render(
    <MemoryRouter>
      <PlanRunCard
        run={makeRun()}
        expanded={expanded}
        onToggle={onToggle}
        slug={undefined}
        hideProject={hideProject}
      />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('PlanRunCard', () => {
  it('renders collapsed: the header only, no fan-out rows', () => {
    renderCard(false, () => undefined);

    const header = screen.getByRole('button', { name: /plan run Sessions page redesign/i });
    expect(header.getAttribute('aria-expanded')).toBe('false');
    // Header identity: the plan title, the plan-run label, the sentence, the link.
    expect(screen.getByText('Sessions page redesign')).toBeTruthy();
    expect(screen.getByText('plan run')).toBeTruthy();
    expect(screen.getByText('phase 5 · controller + 1 session · swarmery')).toBeTruthy();
    // The status tallies moved into the header's tooltip.
    expect(header.getAttribute('data-tip')).toBe('done 2');
    // Fan-out rows are NOT in the DOM until expanded.
    expect(screen.queryByText('plan controller')).toBeNull();
    expect(screen.queryByText('day-first timeline')).toBeNull();
  });

  it('never renders the boilerplate prompt, collapsed or expanded', () => {
    renderCard(true, () => undefined);
    expect(screen.queryByText(/You are the controller/)).toBeNull();
    // …not as a TITLE and not as a SUB-LINE: `why` is derived from the same
    // dispatch prompt, so the fan-out row must render no sub-line rather than
    // fall back to it.
    expect(screen.queryByText(/You are executing ONE phase/)).toBeNull();
    expect(document.body.textContent).not.toContain('isolated git worktree');
    // Expanded, the rows are identified by role instead.
    expect(screen.getAllByText('plan controller').length).toBeGreaterThan(0);
    expect(screen.getAllByText('day-first timeline').length).toBeGreaterThan(0);
    expect(screen.getAllByText('ctl').length).toBeGreaterThan(0);
    expect(screen.getAllByText('#5').length).toBeGreaterThan(0);
  });

  it('gives a non-retry phase row no sub-line at all (not a truncated prompt)', () => {
    renderCard(true, () => undefined);
    // The controller keeps its explainer …
    expect(screen.getAllByText(/dispatches phases in dependency order/).length).toBeGreaterThan(0);
    // … while the phase row's only text is its tag + name (+ the mono meta
    // line, which is model/branch/time — never prompt text).
    const row = screen.getAllByText('day-first timeline')[0]?.closest('a');
    expect(row).toBeTruthy();
    expect(row?.textContent ?? '').not.toContain('You are executing');
  });

  it('toggles when the header is clicked', () => {
    const onToggle = vi.fn();
    renderCard(false, onToggle);
    fireEvent.click(screen.getByRole('button', { name: /plan run/i }));
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it('toggles on Enter and Space from the keyboard', () => {
    const onToggle = vi.fn();
    renderCard(false, onToggle);
    const header = screen.getByRole('button', { name: /plan run/i });
    fireEvent.keyDown(header, { key: 'Enter' });
    fireEvent.keyDown(header, { key: ' ' });
    expect(onToggle).toHaveBeenCalledTimes(2);
  });

  it('does NOT toggle when "open plan →" is clicked', () => {
    const onToggle = vi.fn();
    renderCard(false, onToggle);
    const link = screen.getByRole('link', { name: /open plan/i });
    expect(link.getAttribute('href')).toBe('/p/swarmery/plans?task=7');
    fireEvent.click(link);
    expect(onToggle).not.toHaveBeenCalled();
  });

  // Artboard 2a on v4 (plan D-7): the header stands in the list like a row.
  it("stands on the session row's grid and padding, inside its frame", () => {
    renderCard(false, () => undefined);
    const header = screen.getByRole('button', { name: /plan run Sessions page redesign/i });
    expect(header.className).toContain('grid-cols-[18px_minmax(0,1fr)_auto]');
    expect(header.className).toContain('py-[9px]');
    expect(header.className).toContain('pl-3');
    expect(header.parentElement?.className).toContain('border-line');
  });

  it('says plan run in brand text, never in a pill', () => {
    renderCard(false, () => undefined);
    const label = screen.getByTestId('plan-run-label');
    expect(label.className).toContain('text-brand');
    expect(label.className).not.toContain('rounded');
    expect(label.className).not.toContain('border');
    expect(label.className).not.toContain('bg-');
  });

  it('drops the project from the sentence in a project list', () => {
    renderCard(false, () => undefined, true);
    expect(screen.getByText('phase 5 · controller + 1 session')).toBeTruthy();
  });
});
