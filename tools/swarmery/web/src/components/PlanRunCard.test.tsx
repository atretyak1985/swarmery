// @vitest-environment jsdom
//
// Interaction tests for the collapsed plan-run card (Sessions redesign): it
// must open COLLAPSED, and "open plan →" must navigate without toggling it.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/components/PlanRunCard.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// (none of them are committed dependencies). The file still type-checks under
// `tsc --noEmit` in the normal build.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
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

function renderCard(expanded: boolean, onToggle: () => void): void {
  render(
    <MemoryRouter>
      <PlanRunCard run={makeRun()} expanded={expanded} onToggle={onToggle} slug={undefined} />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('PlanRunCard', () => {
  it('renders collapsed: the header only, no fan-out rows', () => {
    renderCard(false, () => undefined);

    const header = screen.getByRole('button', { name: /plan run Sessions page redesign/i });
    expect(header.getAttribute('aria-expanded')).toBe('false');
    // Header identity: the plan title, the PLAN RUN pill, tallies, the link.
    expect(screen.getByText('Sessions page redesign')).toBeTruthy();
    expect(screen.getByText('plan run')).toBeTruthy();
    expect(screen.getByText(/2 sessions · done 2/)).toBeTruthy();
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
    const row = screen.getAllByText('day-first timeline')[0]?.closest('[role="link"]');
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
});
