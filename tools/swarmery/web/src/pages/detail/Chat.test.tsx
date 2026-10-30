// @vitest-environment jsdom
//
// The transcript's two columns (handoff §5) and what the old bubbles did: the
// optimistic row's pending and failed states, the refusal's reason, and a retry
// that re-sends the same entry. The scroll follow is SessionPane's.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionDetail, Turn } from '../../api/types';
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { Chat, type PendingSend } from './Chat';

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
    tokensIn: null,
    tokensOut: null,
    tokensCacheRead: null,
    tokensCacheWrite: null,
    costUsd: null,
    text: null,
    stopReason: 'end_turn',
    ...over,
  };
}

function detail(over: Partial<SessionDetail> = {}): SessionDetail {
  return {
    id: 7,
    status: 'completed',
    turns: [
      turn({ id: 1, seq: 0, role: 'user', text: 'find the flake', stopReason: null }),
      turn({ id: 2, seq: 1, text: 'found it' }),
    ],
    events: [],
    fileChanges: [],
    recovered: 0,
    ...over,
  } as SessionDetail;
}

const sending: PendingSend = { key: 'a', text: 'also this', state: 'pending', sentAt: 0 };
const refused: PendingSend = { key: 'b', text: 'and that', state: 'failed', sentAt: 0, reason: 'a process is live' };

function renderChat(props: Partial<Parameters<typeof Chat>[0]> = {}): void {
  render(
    <MemoryRouter>
      <Chat detail={detail()} {...props} />
    </MemoryRouter>,
  );
}

describe('Chat', () => {
  it('sets each turn in a who column beside its text', () => {
    renderChat();
    expect(screen.getByText('you').nextElementSibling?.textContent).toBe('find the flake');
    expect(screen.getByText('claude').nextElementSibling?.textContent).toBe('found it');
  });

  it('shows a message on its way as sending', () => {
    renderChat({ pending: [sending] });
    expect(screen.getByText('also this')).toBeTruthy();
    expect(screen.getByText('sending…')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /retry/ })).toBeNull();
  });

  it('keeps a refused message with the reason, and retries the same entry', () => {
    const onRetry = vi.fn();
    renderChat({ pending: [refused], onRetry });
    expect(screen.getByText('and that')).toBeTruthy();
    expect(screen.getByText('a process is live')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /failed — retry/ }));
    expect(onRetry).toHaveBeenCalledWith('b');
  });

  it('points at the approvals screen only when no gate card answers it', () => {
    const waiting = detail({ status: 'waiting_approval' });
    renderChat({ detail: waiting });
    expect(screen.getByRole('link', { name: /awaiting approval/ })).toBeTruthy();
    cleanup();
    renderChat({ detail: waiting, gated: true });
    expect(screen.queryByRole('link', { name: /awaiting approval/ })).toBeNull();
  });
});
