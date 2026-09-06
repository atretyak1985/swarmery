// @vitest-environment jsdom
//
// The brief panel (board redesign v2 phase 2): the two editable fields and the
// source block that answers "why do I have this card".
//
// The source block is the phase-0 provenance columns finally reaching a screen —
// quote, turn link and files — and the case worth fencing is the manual card,
// which has no provenance at all and must still say something rather than render
// a blank where a fact should be.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/task/TaskBrief.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BoardTask } from '../../api/types';
import { TaskBrief } from './TaskBrief';
import type { TaskDraft } from './useTaskDraft';

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
    pendingApprovalCount: 0,
    planExternalId: null,
    resultNote: null,
    columnMovedAt: null,
    createdAt: '2026-08-01T00:00:00Z',
    ...over,
  };
}

function makeDraft(over: Partial<TaskDraft> = {}): TaskDraft {
  return {
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    model: 'default',
    playbook: '',
    agent: '',
    fileScope: [],
    dependencies: [],
    ...over,
  };
}

function renderBrief(
  task: BoardTask,
  props: { setField?: ReturnType<typeof vi.fn>; commit?: ReturnType<typeof vi.fn> } = {},
): void {
  render(
    <MemoryRouter>
      <TaskBrief
        task={task}
        draft={makeDraft({ title: task.title, prompt: task.prompt })}
        setField={props.setField ?? vi.fn()}
        commit={props.commit ?? vi.fn()}
      />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('TaskBrief source block', () => {
  it('shows the captured quote, the link to the session and the touched files', () => {
    renderBrief(
      makeTask({
        origin: 'session',
        originSessionId: 1867,
        source: {
          sessionId: 1867,
          turnUuid: 'turn-uuid',
          quote: 'make the board readable in five seconds',
          files: ['web/src/pages/Board.tsx', 'internal/api/tasks_board.go'],
        },
      }),
    );
    const link = screen.getByRole('link', { name: 'from session #1867' });
    expect(link.getAttribute('href')).toBe('/sessions/1867');
    const text = document.body.textContent ?? '';
    expect(text).toContain('make the board readable in five seconds');
    expect(text).toContain('web/src/pages/Board.tsx');
    expect(text).toContain('internal/api/tasks_board.go');
  });

  it('says a hand-written card was added by hand rather than leaving a blank', () => {
    renderBrief(makeTask());
    expect(document.body.textContent ?? '').toContain('added by hand');
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('renders a captured card with no quote without an empty blockquote', () => {
    // Rows captured before 0066, and rows whose session opened with nothing.
    renderBrief(
      makeTask({
        origin: 'session',
        originSessionId: 12,
        source: { sessionId: 12, turnUuid: null, quote: null, files: [] },
      }),
    );
    expect(screen.getByRole('link', { name: 'from session #12' })).not.toBeNull();
    expect(document.querySelector('blockquote')).toBeNull();
  });

  it('renders the labels the card carries, read-only', () => {
    renderBrief(makeTask({ labels: ['jira-ticket', 'ui'] }));
    expect(document.body.textContent ?? '').toContain('jira-ticket');
    expect(document.body.textContent ?? '').toContain('ui');
  });
});

describe('TaskBrief editing', () => {
  it('reports a title edit and saves it on blur — there is no Save button', () => {
    const setField = vi.fn();
    const commit = vi.fn();
    renderBrief(makeTask(), { setField, commit });
    const title = screen.getByLabelText('title');
    fireEvent.change(title, { target: { value: 'a better title' } });
    expect(setField).toHaveBeenCalledWith('title', 'a better title');
    expect(commit).not.toHaveBeenCalled();
    fireEvent.blur(title);
    expect(commit).toHaveBeenCalledTimes(1);
    expect(screen.queryByText('Save')).toBeNull();
  });

  it('saves the prompt on blur too, under its plain-language label', () => {
    const commit = vi.fn();
    renderBrief(makeTask(), { commit });
    // "what needs doing" is the reader's question; "prompt" stays as the
    // accessible name so every existing query still finds the field.
    expect(document.body.textContent ?? '').toContain('what needs doing');
    fireEvent.blur(screen.getByLabelText('prompt'));
    expect(commit).toHaveBeenCalledTimes(1);
  });
});
