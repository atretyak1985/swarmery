// @vitest-environment jsdom
//
// The run-log panel and, more importantly, the predicate that decides whether it
// exists at all (board redesign v2 phase 2).
//
// `hasRunLog` is the fix for the defect that opened this phase: the modal used
// to render the dispatcher block unconditionally, so a captured card sitting in
// the Inbox showed "branch —, worktree —" about a run that had never happened.
// The peel-one-condition-at-a-time test below is what keeps a future edit from
// quietly widening it back to "always".
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/task/RunLog.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BoardTask } from '../../api/types';
import { hasRunLog, RunLog } from './RunLog';

vi.mock('../../api', () => ({
  getBoardTaskDiff: vi.fn(async () => {
    throw new Error('the diff is lazy — nothing should fetch it here');
  }),
}));

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
    planExternalId: null,
    resultNote: null,
    columnMovedAt: null,
    createdAt: '2026-08-01T00:00:00Z',
    ...over,
  };
}

afterEach(cleanup);

describe('hasRunLog', () => {
  it('is false for a card that has never been near the dispatcher', () => {
    expect(hasRunLog(makeTask())).toBe(false);
  });

  it('is true for each single piece of run history, one at a time', () => {
    const triggers: Array<Partial<BoardTask>> = [
      { branch: 'swarm/T-a1b2c3' },
      { worktreePath: '/tmp/wt' },
      { startPoint: '9f2c1ab' },
      { retryCount: 1 },
      { verifyRetryCount: 1 },
      { verifyVerdict: 'pass' },
      { dispatchError: 'runner exited 1' },
      { dispatchedPrompt: 'do the thing' },
    ];
    for (const t of triggers) {
      expect(hasRunLog(makeTask(t))).toBe(true);
    }
  });

  it('stays false for a card whose only non-default fields are user-owned', () => {
    // Priority, model, playbook, agent, labels and a file scope are the card's
    // CONFIGURATION. None of them means anything has run.
    expect(
      hasRunLog(
        makeTask({
          priority: 'urgent',
          model: 'opus',
          playbook: 'plan-first',
          agent: 'debugger',
          labels: ['ui'],
          fileScope: ['web/src/'],
          dependencies: ['T-000001'],
        }),
      ),
    ).toBe(false);
  });
});

describe('RunLog', () => {
  it('states the board state, never the raw status value', () => {
    render(<RunLog task={makeTask({ boardColumn: 'in_progress', status: 'running' })} />);
    const text = document.body.textContent ?? '';
    expect(text).toContain('In Progress');
    expect(text).not.toContain('running');
  });

  it('prints no em-dash placeholders for the facts it does not have', () => {
    render(<RunLog task={makeTask({ dispatchedPrompt: 'do the thing' })} />);
    expect(document.body.textContent ?? '').not.toContain('—');
  });

  it('shows a failed verdict with its detail, the error, and the retry budgets', () => {
    render(
      <RunLog
        task={makeTask({
          boardColumn: 'in_review',
          branch: 'swarm/T-a1b2c3',
          worktreePath: '/tmp/wt',
          startPoint: '9f2c1ab',
          verifyVerdict: 'fail',
          verifyDetail: 'bundle grew 40KB',
          verifyRetryCount: 1,
          dispatchError: 'runner exited 1',
        })}
      />,
    );
    const text = document.body.textContent ?? '';
    expect(text).toContain('swarm/T-a1b2c3');
    expect(text).toContain('/tmp/wt');
    expect(text).toContain('9f2c1ab');
    expect(text).toContain('fail');
    expect(text).toContain('bundle grew 40KB');
    expect(text).toContain('runner exited 1');
    expect(text).toContain('verify retries');
    // The dispatch budget is a different column and must not be invented.
    expect(text).not.toContain('dispatch retries');
  });

  it('keeps the dispatched prompt collapsed until it is asked for', () => {
    const prompt = 'the exact first-stage prompt the runner received';
    render(<RunLog task={makeTask({ dispatchedPrompt: prompt })} />);
    expect(document.body.textContent ?? '').not.toContain(prompt);
    fireEvent.click(screen.getByRole('button', { name: 'dispatched prompt' }));
    expect(document.body.textContent ?? '').toContain(prompt);
  });

  it('offers the worktree terminal only when the caller gives it one', () => {
    const open = vi.fn();
    const { unmount } = render(<RunLog task={makeTask({ worktreePath: '/tmp/wt' })} />);
    expect(screen.queryByText(/Open terminal in worktree/)).toBeNull();
    unmount();
    render(<RunLog task={makeTask({ worktreePath: '/tmp/wt' })} onOpenTerminal={open} />);
    fireEvent.click(screen.getByText(/Open terminal in worktree/));
    expect(open).toHaveBeenCalledTimes(1);
  });
});
