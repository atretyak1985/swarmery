// @vitest-environment jsdom
//
// Render tests for the board card's readout (board redesign v2 phase 1): the
// source line, the single attention signal, the non-default-only badge row, and
// the stale card's dimming.
//
// The oldest fence here is about totality. The card used to pick its provenance
// badge out of a Record indexed by origin and DESTRUCTURE the looked-up entry,
// so an origin the map did not know was not a missing badge — it was `undefined`
// being destructured, i.e. a TypeError that unmounted the whole board. The
// daemon had minted 'verify-fix' cards since internal/verify shipped. That map
// is gone (the source line replaced it), and the fence moved with it: every
// origin must still render.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/TaskCard.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// (none of them are committed dependencies). web/tsconfig.json EXCLUDES
// *.test.tsx, so `npm run build` does NOT type-check this file — check it
// explicitly with `npx tsc --noEmit --project tsconfig.json <file>` if in doubt.

import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BoardTask, TaskOrigin } from '../api/types';
import { TaskCard } from './TaskCard';

function makeTask(over: Partial<BoardTask> = {}): BoardTask {
  return {
    id: 1,
    externalId: 'T-a1b2c3',
    projectId: 1,
    projectSlug: 'swarmery',
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    status: 'queued',
    boardColumn: 'todo',
    paused: false,
    userPaused: false,
    dependencies: [],
    model: null,
    playbook: null,
    fileScope: [],
    labels: [],
    branch: null,
    worktreePath: null,
    startPoint: null,
    dispatchError: null,
    retryCount: 0,
    verifyRetryCount: 0,
    verifyVerdict: null,
    verifyDetail: null,
    agent: null,
    origin: 'manual',
    originSessionId: null,
    source: null,
    staleAfter: null,
    dispatchedPrompt: null,
    planExternalId: null,
    resultNote: null,
    columnMovedAt: null,
    createdAt: '2026-08-01T00:00:00Z',
    ...over,
  };
}

/** The card inside a router — the source line links with <Link>, which needs
 * one. No route matches, so `useSessionHref` builds the fleet-mode href, the
 * same shape it builds outside a project. */
function renderCard(over: Partial<BoardTask> = {}, props: Partial<Parameters<typeof TaskCard>[0]> = {}): void {
  render(
    <MemoryRouter>
      <TaskCard task={makeTask(over)} onOpen={vi.fn()} onMove={vi.fn()} {...props} />
    </MemoryRouter>,
  );
}

/** The card's own root element (the action buttons share role=button). */
function cardRoot(externalId = 'T-a1b2c3', title = 'a task'): HTMLElement {
  return screen.getByRole('button', { name: `task ${externalId}: ${title}` });
}

afterEach(cleanup);

describe('TaskCard source line', () => {
  it('links a captured card to its session and hovers the captured quote', () => {
    renderCard({
      origin: 'session',
      originSessionId: 1867,
      source: { sessionId: 1867, turnUuid: 'u1', quote: 'add waypoint editing', files: [] },
    });
    const link = screen.getByText('from session #1867');
    expect(link.getAttribute('href')).toBe('/sessions/1867');
    expect(link.getAttribute('data-tip')).toBe('add waypoint editing');
  });

  it('links a plan card to its project plan list', () => {
    renderCard({ planExternalId: '2026-07-18-plan-doc-lifecycle', projectSlug: 'swarmery' });
    const link = screen.getByText('plan 2026-07-18-plan-doc-lifecycle');
    expect(link.getAttribute('href')).toBe('/p/swarmery/plans');
  });

  it('renders a verify-fix card without throwing, and names what it repairs', () => {
    expect(() => renderCard({ origin: 'verify-fix', externalId: 'T-12', title: 'fix: broken endpoint' })).not.toThrow();
    expect(screen.getByText(/fix: broken endpoint/)).toBeDefined();
    expect(screen.getByText('fix for T-12')).toBeDefined();
  });

  it('says "added by hand" for a manual card, with nothing to open', () => {
    renderCard({ origin: 'manual' });
    expect(screen.getByText('added by hand').getAttribute('href')).toBeNull();
  });

  // Enumerated explicitly: adding a member to TaskOrigin must not be able to
  // crash the renderer, which is exactly what the Record this replaced did.
  it('renders every origin in the union without throwing', () => {
    const origins: TaskOrigin[] = ['manual', 'session', 'llm', 'verify-fix'];
    for (const origin of origins) {
      expect(() => renderCard({ origin, originSessionId: origin === 'manual' ? null : 5 })).not.toThrow();
      cleanup();
    }
  });

  it('shows the card age next to its source', () => {
    renderCard({ createdAt: new Date(Date.now() - 12 * 86_400_000).toISOString() });
    expect(screen.getByText('· 12d')).toBeDefined();
  });
});

describe('TaskCard attention signal', () => {
  it('states a failed verdict with its detail', () => {
    renderCard({ verifyVerdict: 'fail', verifyDetail: 'typecheck: 3 errors' });
    expect(screen.getByText('verdict FAIL: typecheck: 3 errors')).toBeDefined();
  });

  it('shows exactly one signal — the FAIL, not the review it is also waiting for', () => {
    renderCard({ boardColumn: 'in_review', verifyVerdict: 'fail', verifyDetail: 'build broke' });
    expect(screen.getByText('verdict FAIL: build broke')).toBeDefined();
    expect(screen.queryByText('waiting for review')).toBeNull();
  });

  it('does not repeat a failed verdict as a chip; a passing one stays a chip', () => {
    renderCard({ verifyVerdict: 'fail', verifyDetail: 'nope' });
    expect(screen.queryByText('fail')).toBeNull();
    cleanup();
    renderCard({ verifyVerdict: 'pass' });
    expect(screen.getByText('pass')).toBeDefined();
  });

  it('reads a dependency block as waiting, and a real error as broken', () => {
    renderCard({ dispatchError: 'blocked by dependency T-14: still in_progress' });
    expect(screen.getByText('blocked by T-14: still in_progress')).toBeDefined();
    cleanup();
    renderCard({ dispatchError: 'worktree missing' });
    expect(screen.getByText('dispatch error: worktree missing')).toBeDefined();
  });

  it('says nothing on a card that wants nothing', () => {
    renderCard({ boardColumn: 'todo' });
    expect(screen.queryByText(/waiting for review|dispatch error|verdict FAIL/)).toBeNull();
  });
});

describe('TaskCard badges — non-default values only', () => {
  it('badges nothing for normal priority, a standard playbook and no agent', () => {
    renderCard({ priority: 'normal', playbook: 'standard', agent: null });
    expect(document.querySelector('[data-tip="normal priority"]')).toBeNull();
    expect(screen.queryByText(/▤/)).toBeNull();
    expect(screen.queryByText(/^@/)).toBeNull();
  });

  it('badges nothing for a null playbook either — null IS standard', () => {
    renderCard({ playbook: null });
    expect(screen.queryByText(/▤/)).toBeNull();
  });

  it('marks urgent and high priority, and leaves low unmarked', () => {
    renderCard({ priority: 'urgent' });
    expect(document.querySelector('[data-tip="urgent priority"]')).not.toBeNull();
    cleanup();
    renderCard({ priority: 'high' });
    expect(document.querySelector('[data-tip="high priority"]')).not.toBeNull();
    cleanup();
    renderCard({ priority: 'low' });
    expect(document.querySelector('[data-tip="low priority"]')).toBeNull();
  });

  it('shows a non-standard playbook and a selected agent', () => {
    renderCard({ playbook: 'plan-first', agent: 'implementation-agent' });
    expect(screen.getByText(/plan-first/)).toBeDefined();
    expect(screen.getByText('@implementation-agent')).toBeDefined();
  });
});

describe('TaskCard stale', () => {
  const inDays = (n: number): string => new Date(Date.now() + n * 86_400_000).toISOString();

  it('dims the card and captions its archive date', () => {
    renderCard({ origin: 'session', staleAfter: inDays(2) });
    expect(screen.getByText('archived in 2d')).toBeDefined();
    expect(cardRoot().className).toContain('opacity-60');
  });

  it('says the next sweep takes a card whose date has passed', () => {
    renderCard({ origin: 'session', staleAfter: inDays(-2) });
    expect(screen.getByText('archived at the next sweep')).toBeDefined();
  });

  // The regression the plan review caught: staleAfter is null for most cards on
  // a live board (manual, running, in review), and null must read as "never
  // expires". A naive `new Date(task.staleAfter) < now` dims half the board.
  it('leaves an undated card undimmed and uncaptioned', () => {
    renderCard({ staleAfter: null });
    expect(screen.queryByText(/^archived/)).toBeNull();
    expect(cardRoot().className).not.toContain('opacity-60');
  });

  it('leaves a card dated far out undimmed', () => {
    renderCard({ origin: 'session', staleAfter: inDays(11) });
    expect(screen.queryByText(/^archived/)).toBeNull();
    expect(cardRoot().className).not.toContain('opacity-60');
  });
});

// The lane action blocks are explicitly out of scope for this phase; this is the
// fence that says the readout above them did not disturb them.
describe('TaskCard lane actions (unchanged by phase 1)', () => {
  it('still offers the three Inbox verbs on a triage card', () => {
    renderCard({ boardColumn: 'triage' }, { onPlan: vi.fn() });
    expect(screen.getByText(/Run/)).toBeDefined();
    expect(screen.getByText(/Plan/)).toBeDefined();
    expect(screen.getByText('Dismiss')).toBeDefined();
  });

  it('still offers the Review verbs on a card in review', () => {
    renderCard({ boardColumn: 'in_review' });
    expect(screen.getByText(/Review…/)).toBeDefined();
    expect(screen.getByText(/Mark done/)).toBeDefined();
  });
});
