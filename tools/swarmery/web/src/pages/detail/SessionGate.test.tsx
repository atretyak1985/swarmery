// @vitest-environment jsdom
//
// The session's gate for a question with no suggested replies. Both its buttons lead
// to a written reply, so with a live terminal process and no link mod they have
// nowhere to go. The claims:
//
//   1. With a way in, both buttons open it, and so do `e` and `x`.
//   2. Without one, both are disabled, the keys do nothing, and the card says why.
//   3. A suggested reply still goes out as it did.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import type { NeedsYouItem, SessionDetail } from '../../api/types';
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { SessionGate } from './SessionGate';
import type { PaneActions } from './SessionPane';

vi.mock('../../api', () => ({
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => undefined),
  sendSessionMessage: vi.fn(async () => undefined),
}));

const BLOCKED = 'running in a terminal · answer there, or stop it to reply here';

const ITEM = {
  kind: 'awaiting_reply',
  requestId: null,
  toolName: '',
  question: 'Run the store submit?',
  preview: '',
  blockingSince: '2026-10-31T11:00:00Z',
} as unknown as NeedsYouItem;

const SESSION = { id: 7, projectSlug: 'cars', projectName: 'cars', events: [] } as unknown as SessionDetail;

function renderGate(canCompose: boolean, item: NeedsYouItem = ITEM): { open: () => void; actions: { current: PaneActions } } {
  const open = vi.fn();
  const actions = { current: {} as PaneActions };
  render(
    <MemoryRouter>
      <SessionGate
        item={item}
        session={SESSION}
        onChanged={() => {}}
        onOpenComposer={open}
        canCompose={canCompose}
        onSent={() => {}}
        actions={actions}
      />
    </MemoryRouter>,
  );
  return { open, actions };
}

const button = (name: RegExp): HTMLButtonElement => screen.getByRole('button', { name }) as HTMLButtonElement;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('SessionGate, a question with no suggestions', () => {
  it('opens the way in from both buttons and both keys', () => {
    const { open, actions } = renderGate(true);
    fireEvent.click(button(/^reply/));
    fireEvent.click(button(/^write my own/));
    actions.current.primary?.();
    actions.current.secondary?.();
    expect(open).toHaveBeenCalledTimes(4);
    expect(screen.queryByText(BLOCKED)).toBeNull();
  });

  it('disables both and says why when nothing can reach the session', () => {
    const { open, actions } = renderGate(false);
    expect(button(/^reply/).disabled).toBe(true);
    expect(button(/^write my own/).disabled).toBe(true);
    actions.current.primary?.();
    actions.current.secondary?.();
    expect(open).not.toHaveBeenCalled();
    expect(screen.getByText(BLOCKED)).toBeTruthy();
  });
});

describe('SessionGate, suggested replies', () => {
  it('still sends the pick with no composer to open', () => {
    const item = { ...ITEM, suggestion: { options: ['yes', 'no'] } } as unknown as NeedsYouItem;
    renderGate(false, item);
    fireEvent.click(button(/^reply/));
    expect(api.sendSessionMessage).toHaveBeenCalledWith(7, 'yes');
  });
});
