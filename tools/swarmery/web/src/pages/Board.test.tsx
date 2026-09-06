// @vitest-environment jsdom
//
// The board shell's URL state and its three filter chips (board redesign v2
// phase 5). Three claims, all of them about the address bar:
//
//   1. Landing on `?f=stale&view=graph` restores BOTH — which is what "a reload
//      restores the state" means when the state has no other home. MemoryRouter
//      with an initial entry IS the reload: the component mounts against a URL
//      it did not write, exactly as it does after F5.
//   2. `?f=` and `?label=` compose. They answer different questions (what state
//      is this card in / what is it about) and neither may clobber the other.
//   3. An empty lane says what the lane is for, and says something DIFFERENT
//      when a filter is what emptied it.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run src/pages/Board.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does NOT type-check
// this file — the runner surfaces type errors as failures instead.

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BoardTask } from '../api/types';
import { Board } from './Board';

vi.mock('../api', () => ({
  bulkArchiveBoardTasks: vi.fn(async () => ({ matched: 0 })),
  fetchBoardTasks: vi.fn(async () => []),
}));

// The graph is stubbed down to the one thing this suite asks of it: which cards
// it was handed. Its own rendering is not what `?view=graph` is a claim about.
vi.mock('../workspace/TaskGraph', () => ({
  TaskGraph: ({ tasks }: { tasks: readonly BoardTask[] }) => (
    <div data-testid="graph">{tasks.map((t) => t.externalId).join(',')}</div>
  ),
}));

let mockTasks: BoardTask[] = [];

vi.mock('../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({
    slug: 'swarmery',
    project: { id: 3, slug: 'swarmery', name: 'swarmery' },
    projectId: 3,
    loading: false,
  }),
}));

vi.mock('../workspace/ProjectWorkspaceLayout', () => ({
  useWorkspaceBoard: () => ({
    tasks: mockTasks,
    loading: false,
    error: null,
    actionError: null,
    clearActionError: vi.fn(),
    setActionError: vi.fn(),
    reload: vi.fn(),
    moveTask: vi.fn(),
    patchTask: vi.fn(async () => mockTasks[0]),
    addTask: vi.fn(),
    deleteTask: vi.fn(async () => undefined),
  }),
  useWorkspaceTerminal: () => null,
}));

function makeTask(over: Partial<BoardTask> = {}): BoardTask {
  return {
    id: 1,
    externalId: 'T-000000',
    projectId: 3,
    projectSlug: 'swarmery',
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    status: 'queued',
    boardColumn: 'triage',
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
    resultNote: null,
    agent: null,
    origin: 'manual',
    originSessionId: null,
    source: null,
    staleAfter: null,
    dispatchedPrompt: null,
    pendingApprovalCount: 0,
    planExternalId: null,
    columnMovedAt: null,
    createdAt: new Date(Date.now() - 60_000).toISOString(),
    ...over,
  };
}

/** Reports the live query string so a click's effect on the URL is assertable. */
function Where(): JSX.Element {
  return <div data-testid="where">{useLocation().search}</div>;
}

function renderBoard(url: string, tasks: BoardTask[]): void {
  mockTasks = tasks;
  render(
    <MemoryRouter initialEntries={[url]}>
      <Board />
      <Where />
    </MemoryRouter>,
  );
}

const search = (): string => screen.getByTestId('where').textContent ?? '';
// Scoped to the chip group: cards are role=button too, and a card whose id says
// "running" would otherwise be indistinguishable from the chip that hides it.
const chip = (name: string): HTMLElement =>
  within(screen.getByLabelText('filter by state')).getByRole('button', { name: new RegExp(name) });

// Four cards, one per filter plus one that matches none.
const reviewCard = makeTask({ id: 1, externalId: 'T-review', boardColumn: 'in_review' });
const runningCard = makeTask({ id: 2, externalId: 'T-running', boardColumn: 'in_progress' });
const staleCard = makeTask({
  id: 3,
  externalId: 'T-stale',
  boardColumn: 'triage',
  // Inside the 3-day warn window, whenever this suite runs.
  staleAfter: new Date(Date.now() + 24 * 3_600_000).toISOString(),
});
const plainCard = makeTask({ id: 4, externalId: 'T-plain', boardColumn: 'todo' });
const everything = (): BoardTask[] => [reviewCard, runningCard, staleCard, plainCard];

afterEach(cleanup);

describe('board URL state', () => {
  it('restores the filter AND the view from the URL it mounts on', () => {
    renderBoard('/p/swarmery?f=stale&view=graph', everything());

    // The graph, not the lanes.
    expect(screen.getByTestId('graph')).toBeTruthy();
    expect(screen.queryByLabelText('Inbox lane')).toBeNull();
    // …carrying only the stale card, so the filter survived the view too.
    expect(screen.getByTestId('graph').textContent).toBe('T-stale');
    // …and the chip renders as the pressed one.
    expect(chip('stale').getAttribute('aria-pressed')).toBe('true');
    expect(chip('running').getAttribute('aria-pressed')).toBe('false');
  });

  it('writes ?view=graph on the toggle and drops the key going back to the board', () => {
    renderBoard('/p/swarmery', everything());
    expect(screen.queryByTestId('graph')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /Graph/ }));
    expect(search()).toContain('view=graph');
    expect(screen.getByTestId('graph')).toBeTruthy();

    // The default view is the ABSENCE of the key, not ?view=board.
    fireEvent.click(screen.getByRole('button', { name: /Board/ }));
    expect(search()).not.toContain('view=');
    expect(screen.getByLabelText('Inbox lane')).toBeTruthy();
  });

  it('toggles a chip off when it is already the active one', () => {
    renderBoard('/p/swarmery?f=running', everything());
    expect(chip('running').getAttribute('aria-pressed')).toBe('true');

    fireEvent.click(chip('running'));
    expect(search()).not.toContain('f=');
    expect(chip('running').getAttribute('aria-pressed')).toBe('false');
  });

  it('replaces one chip with another rather than accumulating them', () => {
    renderBoard('/p/swarmery?f=running', everything());
    fireEvent.click(chip('stale'));
    expect(search()).toContain('f=stale');
    expect(search()).not.toContain('running');
  });

  it('reads an unknown ?f= as no filter and shows the whole board', () => {
    // A stale bookmark must not look like an empty board. No chip is pressed and
    // every card is on screen.
    renderBoard('/p/swarmery?f=whatever', everything());
    for (const c of BOARD_CHIPS) expect(chip(c).getAttribute('aria-pressed')).toBe('false');
    expect(screen.getAllByLabelText(/^task T-/)).toHaveLength(everything().length);
  });
});

const BOARD_CHIPS = ['needs me', 'running', 'stale'];

describe('the state filter and the label filter compose', () => {
  const tagged = makeTask({
    id: 5,
    externalId: 'T-tagged',
    boardColumn: 'in_review',
    title: 'tagged review card',
    labels: ['ui'],
  });
  const untagged = makeTask({
    id: 6,
    externalId: 'T-untagged',
    boardColumn: 'in_review',
    title: 'untagged review card',
  });

  it('applies both when both are in the URL', () => {
    renderBoard('/p/swarmery?f=needsMe&label=ui', [tagged, untagged, runningCard]);
    expect(screen.getByText('tagged review card')).toBeTruthy();
    expect(screen.queryByText('untagged review card')).toBeNull();
  });

  it('keeps ?label= when a chip is clicked', () => {
    renderBoard('/p/swarmery?label=ui', [tagged, untagged, runningCard]);
    fireEvent.click(chip('needs me'));
    expect(search()).toContain('label=ui');
    expect(search()).toContain('f=needsMe');
  });

  it('keeps ?f= when the label filter changes', () => {
    renderBoard('/p/swarmery?f=needsMe', [tagged, untagged, runningCard]);
    fireEvent.change(screen.getByLabelText('filter by label'), { target: { value: 'ui' } });
    expect(search()).toContain('f=needsMe');
    expect(search()).toContain('label=ui');
  });

  it('keeps ?f= and ?view= when the label filter is cleared', () => {
    renderBoard('/p/swarmery?f=needsMe&label=ui&view=graph', [tagged, untagged]);
    fireEvent.click(screen.getByLabelText('clear label filter'));
    expect(search()).not.toContain('label=');
    expect(search()).toContain('f=needsMe');
    expect(search()).toContain('view=graph');
  });
});

describe('empty lanes explain themselves', () => {
  it('says what each lane is for when the board is empty', () => {
    renderBoard('/p/swarmery', []);
    expect(screen.getByText(/Tasks captured from sessions and routines land here/)).toBeTruthy();
    expect(screen.getByText(/The dispatcher picks cards up from here/)).toBeTruthy();
    expect(screen.getByText(/Cards that have run and carry a verdict wait here/)).toBeTruthy();
    // The old bare marker is gone from the lanes (the history strip keeps it).
    expect(screen.queryByText('empty', { selector: 'div' })).toBeNull();
  });

  it('says the FILTER emptied the lane, not that the lane is idle', () => {
    // One review card, filtered out by `running`: Review is empty because of the
    // chip, and saying "cards wait here for a verdict" would be a non-sequitur.
    renderBoard('/p/swarmery?f=running', [reviewCard, runningCard]);
    expect(screen.queryByText(/Cards that have run and carry a verdict wait here/)).toBeNull();
    expect(screen.getByText(/Nothing in Review matches/)).toBeTruthy();
    // Working has the running card, so it shows no note at all.
    expect(screen.queryByText(/Nothing in Working matches/)).toBeNull();
  });
});

describe('chip counts', () => {
  it('counts what a click would leave, under the label filter already applied', () => {
    const taggedReview = makeTask({ id: 7, externalId: 'T-t1', boardColumn: 'in_review', labels: ['ui'] });
    const plainReview = makeTask({ id: 8, externalId: 'T-t2', boardColumn: 'in_review' });
    renderBoard('/p/swarmery?label=ui', [taggedReview, plainReview, runningCard]);
    // Two review cards on the board, but only one carries the label — the chip
    // must promise what the click actually produces.
    expect(chip('needs me').textContent).toContain('1');
    expect(chip('running').textContent).toContain('0');
  });

  it('ignores the history columns, which no filter can reveal', () => {
    const doneWithError = makeTask({
      id: 9,
      externalId: 'T-done',
      boardColumn: 'done',
      dispatchError: 'session exited 1',
    });
    renderBoard('/p/swarmery', [doneWithError, plainCard]);
    expect(chip('needs me').textContent).toContain('0');
  });
});
