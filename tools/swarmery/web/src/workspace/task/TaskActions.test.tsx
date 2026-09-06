// @vitest-environment jsdom
//
// The card's verbs (board redesign v2 phase 2), and the risk this phase's own
// plan named as its high-impact one: "splitting the modal breaks the review
// loop (Land / Re-run / Discard)".
//
// So the four review tests below assert the API CALL, not the button — that
// Land still reaches `landBoardTask(id)`, Re-run still reaches
// `rerunBoardTask(id, feedback)`, Discard still reaches `discardBoardTask(id)`
// and Re-verify still reaches `verifyBoardTask(id)`, each behind the same gate
// it had before the split (a confirm for the two irreversible ones, non-empty
// feedback for the re-run). A refactor that renamed a handler, dropped an
// argument or lost a confirm would pass a render test and fail these.
//
// The lane tests fence the other half of the phase: exactly ONE primary verb per
// lane, and every other verb still reachable behind "…".
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/task/TaskActions.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { BoardColumn, BoardTask } from '../../api/types';
import {
  discardBoardTask,
  landBoardTask,
  rerunBoardTask,
  verifyBoardTask,
} from '../../api';
import { TaskActions } from './TaskActions';

vi.mock('../../api', () => ({
  verifyBoardTask: vi.fn(async () => undefined),
  rerunBoardTask: vi.fn(async () => ({})),
  discardBoardTask: vi.fn(async () => ({ deleted: true, branch: 'swarm/T-a1b2c3' })),
  landBoardTask: vi.fn(async () => ({ prUrl: 'https://example.test/pr/1' })),
}));

function makeTask(over: Partial<BoardTask> = {}): BoardTask {
  return {
    id: 42,
    externalId: 'T-a1b2c3',
    projectId: 1,
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

/** A card in review with everything the four decisions need. */
function reviewTask(over: Partial<BoardTask> = {}): BoardTask {
  return makeTask({
    boardColumn: 'in_review',
    status: 'in_review',
    branch: 'swarm/T-a1b2c3',
    worktreePath: '/tmp/wt',
    verifyVerdict: 'fail',
    verifyDetail: 'bundle grew 40KB',
    ...over,
  });
}

function renderActions(task: BoardTask, onPatch = vi.fn(async () => task)): void {
  render(
    <TaskActions task={task} onPatch={onPatch} onDelete={vi.fn(async () => undefined)} onClose={vi.fn()} />,
  );
}

/** The primary verb is the only brand-toned button in the row. */
function primaryLabel(): string {
  const btn = document.querySelector('button.border-brand\\/50');
  return (btn?.textContent ?? '').trim();
}

/** Opens the review panel of a card in review (its primary IS the toggle). */
function openReview(): void {
  fireEvent.click(screen.getByRole('button', { name: '◇ Review…' }));
}

beforeEach(() => {
  vi.mocked(verifyBoardTask).mockClear();
  vi.mocked(rerunBoardTask).mockClear();
  vi.mocked(discardBoardTask).mockClear();
  vi.mocked(landBoardTask).mockClear();
});
afterEach(cleanup);

describe('one primary verb per lane', () => {
  const expected: Array<[BoardColumn, string]> = [
    ['triage', '▶ Run'],
    ['todo', '❙❙ Pause'],
    ['in_progress', '❙❙ Pause'],
    ['in_review', '◇ Review…'],
    ['done', 'Archive'],
    ['archived', '↩ Restore'],
  ];

  for (const [column, label] of expected) {
    it(`offers "${label}" in ${column}`, () => {
      renderActions(makeTask({ boardColumn: column }));
      expect(primaryLabel()).toBe(label);
      // Exactly one: the review panel's own Land button is brand-toned too, and
      // it must not be on screen until the reader asks for it.
      expect(document.querySelectorAll('button.border-brand\\/50')).toHaveLength(1);
    });
  }

  it('accepts an Inbox card into the queue', () => {
    const onPatch = vi.fn(async () => makeTask());
    renderActions(makeTask({ boardColumn: 'triage' }), onPatch);
    fireEvent.click(screen.getByRole('button', { name: '▶ Run' }));
    expect(onPatch).toHaveBeenCalledWith({ boardColumn: 'todo' });
  });

  it('flips the user pause, and reads Resume once it is paused', () => {
    const onPatch = vi.fn(async () => makeTask());
    renderActions(makeTask({ boardColumn: 'todo', userPaused: true }), onPatch);
    expect(primaryLabel()).toBe('▶ Resume');
    fireEvent.click(screen.getByRole('button', { name: '▶ Resume' }));
    expect(onPatch).toHaveBeenCalledWith({ userPaused: false });
  });

  it('has no Save button — the modal autosaves', () => {
    renderActions(makeTask({ boardColumn: 'todo' }));
    expect(screen.queryByText('Save')).toBeNull();
  });

  it('keeps Move to / Archive / Pause / Delete behind the overflow menu', () => {
    renderActions(makeTask({ boardColumn: 'triage' }));
    expect(screen.queryByLabelText('move task to column')).toBeNull();
    expect(screen.queryByText('Archive')).toBeNull();
    expect(screen.queryByText('Delete')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'more actions' }));
    expect(screen.getByLabelText('move task to column')).not.toBeNull();
    expect(screen.getByText('Archive')).not.toBeNull();
    expect(screen.getByText('Delete')).not.toBeNull();
  });

  it('moves the card through the overflow column menu', () => {
    const onPatch = vi.fn(async () => makeTask());
    renderActions(makeTask({ boardColumn: 'triage' }), onPatch);
    fireEvent.click(screen.getByRole('button', { name: 'more actions' }));
    fireEvent.change(screen.getByLabelText('move task to column'), {
      target: { value: 'in_review' },
    });
    expect(onPatch).toHaveBeenCalledWith({ boardColumn: 'in_review' });
  });

  it('leaves a done card decidable without making Review the lane primary', () => {
    renderActions(makeTask({ boardColumn: 'done' }));
    expect(primaryLabel()).toBe('Archive');
    expect(screen.getByRole('button', { name: '◇ Review…' })).not.toBeNull();
  });
});

describe('the review loop calls the same API functions as before the split', () => {
  it('Re-verify → verifyBoardTask(id)', () => {
    renderActions(reviewTask());
    openReview();
    fireEvent.click(screen.getByRole('button', { name: 'Re-verify' }));
    expect(verifyBoardTask).toHaveBeenCalledWith(42);
  });

  it('Re-verify is refused, with the reason, once the worktree is reclaimed', () => {
    renderActions(reviewTask({ worktreePath: null }));
    openReview();
    const btn = screen.getByRole('button', { name: 'Re-verify' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(document.body.textContent ?? '').toContain('worktree reclaimed');
    fireEvent.click(btn);
    expect(verifyBoardTask).not.toHaveBeenCalled();
  });

  it('Re-run with feedback → rerunBoardTask(id, feedback)', () => {
    renderActions(reviewTask());
    openReview();
    fireEvent.change(screen.getByLabelText('reviewer feedback'), {
      target: { value: 'split the chunk instead' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Re-run with feedback' }));
    expect(rerunBoardTask).toHaveBeenCalledWith(42, 'split the chunk instead');
  });

  it('Re-run stays disabled with no notes — it would repeat the same work', () => {
    renderActions(reviewTask());
    openReview();
    const btn = screen.getByRole('button', { name: 'Re-run with feedback' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(btn);
    expect(rerunBoardTask).not.toHaveBeenCalled();
  });

  it('Land → landBoardTask(id), and only after the confirm', () => {
    renderActions(reviewTask());
    openReview();
    fireEvent.click(screen.getByRole('button', { name: 'Land' }));
    expect(landBoardTask).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'land' }));
    expect(landBoardTask).toHaveBeenCalledWith(42);
  });

  it('Land is refused for a card with no run branch', () => {
    renderActions(reviewTask({ branch: null }));
    openReview();
    expect((screen.getByRole('button', { name: 'Land' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('Discard → discardBoardTask(id), and only after the confirm', () => {
    renderActions(reviewTask());
    openReview();
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(discardBoardTask).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'discard' }));
    expect(discardBoardTask).toHaveBeenCalledWith(42);
  });

  it('offers none of the four to a card that has not been through a run', () => {
    renderActions(makeTask({ boardColumn: 'todo' }));
    expect(screen.queryByRole('button', { name: 'Land' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Re-verify' })).toBeNull();
    expect(screen.queryByLabelText('reviewer feedback')).toBeNull();
  });
});
