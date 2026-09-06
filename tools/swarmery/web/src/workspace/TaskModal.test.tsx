// @vitest-environment jsdom
//
// The task modal end to end (board redesign v2 phase 2) — the three claims the
// phase is judged on, asserted against the assembled modal rather than a panel:
//
//   1. A card that never ran opens with no run-log tab, no em-dash placeholders
//      and no raw `status` value. That card — captured, twelve days in the
//      Inbox, never dispatched — is what the phase was written for.
//   2. A card that DID run gets the tab, and the tab holds the verdict, the
//      dispatched prompt and the diff.
//   3. Saving is automatic and guarded: blur and ⌘S save, a field the user did
//      not touch follows the server, and a field they DID touch is never
//      written over a server change that landed underneath it.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/TaskModal.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BoardTask } from '../api/types';
import { TaskModal } from './TaskModal';

vi.mock('../api', () => ({
  fetchPlaybooks: vi.fn(async () => []),
  getBoardTaskDiff: vi.fn(async () => {
    throw new Error('the diff is lazy — nothing should fetch it here');
  }),
  verifyBoardTask: vi.fn(async () => undefined),
  rerunBoardTask: vi.fn(async () => ({})),
  discardBoardTask: vi.fn(async () => ({ deleted: true, branch: 'b' })),
  landBoardTask: vi.fn(async () => ({ prUrl: 'https://example.test/pr/1' })),
}));
vi.mock('../api/agentHub', () => ({ fetchAgentRoster: vi.fn(async () => ({ agents: [] })) }));

function makeTask(over: Partial<BoardTask> = {}): BoardTask {
  return {
    id: 42,
    externalId: 'T-a1b2c3',
    projectId: 1,
    projectSlug: 'swarmery',
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    // The dispatcher's own word for a row nothing has dispatched. The modal used
    // to print it verbatim next to two em-dashes.
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

function renderModal(
  task: BoardTask,
  onPatch: (patch: unknown) => Promise<BoardTask> = vi.fn(async () => task),
): { rerender: (next: BoardTask) => void } {
  const view = render(
    <MemoryRouter>
      <TaskModal
        task={task}
        onClose={vi.fn()}
        onPatch={onPatch as never}
        onDelete={vi.fn(async () => undefined)}
      />
    </MemoryRouter>,
  );
  return {
    rerender: (next) =>
      view.rerender(
        <MemoryRouter>
          <TaskModal
            task={next}
            onClose={vi.fn()}
            onPatch={onPatch as never}
            onDelete={vi.fn(async () => undefined)}
          />
        </MemoryRouter>,
      ),
  };
}

/** A card with a run behind it, and a FAIL to show for it. */
function ranTask(over: Partial<BoardTask> = {}): BoardTask {
  return makeTask({
    boardColumn: 'in_review',
    status: 'in_review',
    branch: 'swarm/T-a1b2c3',
    worktreePath: '/tmp/wt',
    startPoint: '9f2c1ab',
    verifyVerdict: 'fail',
    verifyDetail: 'bundle grew 40KB',
    dispatchedPrompt: 'the exact prompt the runner received',
    ...over,
  });
}

afterEach(cleanup);

describe('a card that has never run', () => {
  it('has no run-log tab', () => {
    renderModal(makeTask());
    expect(screen.queryByRole('button', { name: 'run log' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'brief' })).toBeNull();
  });

  it('shows no em-dash placeholders anywhere', () => {
    // "branch —, worktree —" was the defect: three rows about a run that never
    // happened, two of them a punctuation mark standing in for a fact.
    renderModal(makeTask());
    expect(document.body.textContent ?? '').not.toContain('—');
  });

  it('states the board state, not the dispatcher status value', () => {
    renderModal(makeTask({ boardColumn: 'todo' }));
    const text = document.body.textContent ?? '';
    expect(text).toContain('Queued');
    expect(text).not.toContain('queued');
  });

  it('opens with the brief and the collapsed run-config line, and no Save button', () => {
    renderModal(makeTask());
    expect(screen.getByLabelText('title')).not.toBeNull();
    expect(screen.getByLabelText('prompt')).not.toBeNull();
    expect(screen.getByRole('button', { name: 'run config' }).getAttribute('aria-expanded')).toBe(
      'false',
    );
    expect(screen.queryByText('Save')).toBeNull();
  });
});

describe('a card that has run', () => {
  it('offers the run-log tab and keeps the brief selected until it is clicked', () => {
    renderModal(ranTask());
    const log = screen.getByRole('button', { name: 'run log' });
    expect(log.getAttribute('aria-selected')).toBe('false');
    expect(screen.getByRole('button', { name: 'brief' }).getAttribute('aria-selected')).toBe('true');
    expect(document.body.textContent ?? '').not.toContain('bundle grew 40KB');
  });

  it('shows the verdict, its detail, the branch and the dispatched prompt in the tab', () => {
    renderModal(ranTask());
    fireEvent.click(screen.getByRole('button', { name: 'run log' }));
    const text = document.body.textContent ?? '';
    expect(text).toContain('fail');
    expect(text).toContain('bundle grew 40KB');
    expect(text).toContain('swarm/T-a1b2c3');
    expect(screen.getByRole('button', { name: 'dispatched prompt' })).not.toBeNull();
    // The evidence panel of the review loop travelled with the run history.
    expect(text.toLowerCase()).toContain('diff');
  });

  it('swaps the brief out for the log rather than stacking them', () => {
    renderModal(ranTask());
    fireEvent.click(screen.getByRole('button', { name: 'run log' }));
    expect(screen.queryByLabelText('title')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'brief' }));
    expect(screen.getByLabelText('title')).not.toBeNull();
  });
});

describe('autosave and its conflict guard', () => {
  it('saves an edited title on blur, sending only the field that changed', async () => {
    const onPatch = vi.fn(async () => makeTask({ title: 'a better title' }));
    renderModal(makeTask(), onPatch);
    const title = screen.getByLabelText('title');
    fireEvent.change(title, { target: { value: 'a better title' } });
    fireEvent.blur(title);
    expect(onPatch).toHaveBeenCalledWith({ title: 'a better title' });
  });

  it('saves nothing on a blur that changed nothing', () => {
    const onPatch = vi.fn(async () => makeTask());
    renderModal(makeTask(), onPatch);
    fireEvent.blur(screen.getByLabelText('title'));
    expect(onPatch).not.toHaveBeenCalled();
  });

  it('saves on ⌘S without waiting for a blur', () => {
    const onPatch = vi.fn(async () => makeTask({ title: 'typed but not blurred' }));
    renderModal(makeTask(), onPatch);
    fireEvent.change(screen.getByLabelText('title'), {
      target: { value: 'typed but not blurred' },
    });
    fireEvent.keyDown(document, { key: 's', metaKey: true });
    expect(onPatch).toHaveBeenCalledWith({ title: 'typed but not blurred' });
  });

  it('refuses an empty title rather than sending it', () => {
    const onPatch = vi.fn(async () => makeTask());
    renderModal(makeTask(), onPatch);
    const title = screen.getByLabelText('title');
    fireEvent.change(title, { target: { value: '   ' } });
    fireEvent.blur(title);
    expect(onPatch).not.toHaveBeenCalled();
    expect(document.body.textContent ?? '').toContain('title and prompt cannot be empty');
  });

  it('follows the server on a field the reader is not editing', () => {
    // A re-run appends the reviewer's feedback to the prompt; a modal left open
    // must show what the card now says.
    const { rerender } = renderModal(makeTask());
    rerender(makeTask({ prompt: 'a task\n\nreviewer: split the chunk' }));
    expect((screen.getByLabelText('prompt') as HTMLTextAreaElement).value).toContain(
      'reviewer: split the chunk',
    );
  });

  it('refuses to write an edit over a server change to the SAME field', () => {
    const onPatch = vi.fn(async () => makeTask());
    const { rerender } = renderModal(makeTask(), onPatch);
    const title = screen.getByLabelText('title');
    fireEvent.change(title, { target: { value: 'my rename' } });
    // Someone else renamed the card while this draft was open.
    rerender(makeTask({ title: 'their rename' }));
    fireEvent.blur(screen.getByLabelText('title'));
    expect(onPatch).not.toHaveBeenCalled();
    const text = document.body.textContent ?? '';
    expect(text).toContain('title changed on the server while you were editing');
    // The reader's own words are still in the box — the refusal loses nothing.
    expect((screen.getByLabelText('title') as HTMLTextAreaElement).value).toBe('my rename');
  });

  it('still saves an untouched-by-the-server field after an unrelated frame', () => {
    const onPatch = vi.fn(async () => makeTask());
    const { rerender } = renderModal(makeTask(), onPatch);
    fireEvent.change(screen.getByLabelText('title'), { target: { value: 'my rename' } });
    // The dispatcher stamped the playbook it picked: a different field.
    rerender(makeTask({ playbook: 'plan-first' }));
    fireEvent.blur(screen.getByLabelText('title'));
    expect(onPatch).toHaveBeenCalledWith({ title: 'my rename' });
  });
});
