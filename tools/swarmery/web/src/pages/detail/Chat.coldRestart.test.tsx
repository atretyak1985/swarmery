// @vitest-environment jsdom
//
// Cold-restart marks in the Chat turn list (migration 0122): a turn the daemon
// marked coldRestart gets an inline mark right where the pause ended — before
// that turn's prose — and an unmarked session shows none.
//
// `npx vitest run src/pages/detail/Chat.coldRestart.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import type { SessionDetail, Turn } from '../../api/types';
import { cleanup, render, screen } from '../../test/render';
import { Chat } from './Chat';

afterEach(cleanup);

function turn(over: Partial<Turn>): Turn {
  return {
    id: 1,
    seq: 0,
    role: 'assistant',
    messageId: null,
    model: 'claude-opus-5-5',
    startedAt: '2026-10-22T10:00:00Z',
    endedAt: '2026-10-22T10:00:05Z',
    tokensIn: 10,
    tokensOut: 100,
    tokensCacheRead: 0,
    tokensCacheWrite: 0,
    costUsd: 0.1,
    text: null,
    stopReason: 'end_turn',
    ...over,
  };
}

function detail(turns: Turn[]): SessionDetail {
  return {
    id: 7,
    projectId: 1,
    projectSlug: 'demo',
    projectName: 'demo',
    sessionUuid: 'uuid-7',
    model: 'claude-opus-5-5',
    gitBranch: 'main',
    cwd: '/work/demo',
    status: 'completed',
    startedAt: '2026-10-22T10:00:00Z',
    endedAt: '2026-10-22T12:05:00Z',
    title: 't',
    source: 'jsonl',
    turns,
    events: [],
    fileChanges: [],
    recovered: 0,
    coldRestarts: turns.filter((t) => t.coldRestart === true).length,
  } as SessionDetail;
}

const turns = [
  turn({ id: 1, seq: 0, text: 'first answer', tokensCacheWrite: 200_000 }),
  turn({ id: 2, seq: 1, role: 'user', text: 'carry on', startedAt: '2026-10-22T12:00:00Z' }),
  turn({
    id: 3,
    seq: 2,
    text: 'answer after the pause',
    startedAt: '2026-10-22T12:00:05Z',
    tokensCacheRead: 25_000,
    tokensCacheWrite: 178_000,
    costUsd: 1.42,
    coldRestart: true,
  }),
];

describe('Chat cold-restart marks', () => {
  it('marks the turn that paid for a cold cache, before its prose', () => {
    render(
      <MemoryRouter>
        <Chat detail={detail(turns)} />
      </MemoryRouter>,
    );
    const mark = screen.getByText(/cold restart/);
    expect(mark.textContent).toContain('re-wrote 178k tokens of context');
    const prose = screen.getByText('answer after the pause');
    // The mark precedes the prose of the turn it belongs to.
    expect(mark.compareDocumentPosition(prose) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('shows no mark on a warm session', () => {
    render(
      <MemoryRouter>
        <Chat detail={detail(turns.map((t) => ({ ...t, coldRestart: false })))} />
      </MemoryRouter>,
    );
    expect(screen.queryByText(/cold restart/)).toBeNull();
  });
});
