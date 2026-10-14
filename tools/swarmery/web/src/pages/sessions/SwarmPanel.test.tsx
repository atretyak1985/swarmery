// @vitest-environment jsdom
//
// Swarm messaging + file claims (mods-swarmery-link phase 6). The claims:
//
//   1. The feed renders send / receive / dashboard rows with their delivery
//      state; an undelivered dashboard message shows the reason.
//   2. The send form is disabled without a (controllable) link, and posts
//      {to, text} to the SENDER's session through sendSwarmMessage.
//   3. A failed link_command ack for the message shows "undeliverable".
//   4. A card whose session is in a conflict wears the badge; others do not.
//   5. swarm_message / swarm_claim frames refetch the feed / the claims.
//
// `npx vitest run src/pages/sessions/SwarmPanel.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  LinkCommandNote,
  Session,
  SessionLink,
  SwarmClaims,
  SwarmMessage,
  WSMessage,
} from '../../api/types';
import { ConflictBadge, SwarmConflictProvider } from '../../lib/swarm';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { SwarmPanel } from './SwarmPanel';

const api = vi.hoisted(() => ({
  fetchSwarmMessages: vi.fn(),
  fetchSwarmClaims: vi.fn(),
  fetchSessions: vi.fn(),
  sendSwarmMessage: vi.fn(),
}));
vi.mock('../../api', () => api);

const ws = vi.hoisted(() => ({ handlers: new Set<(m: WSMessage) => void>() }));
vi.mock('../../lib/ws', async () => {
  const react = await import('react');
  return {
    useLiveUpdates: (onMessage: (m: WSMessage) => void): void => {
      react.useEffect(() => {
        ws.handlers.add(onMessage);
        return () => {
          ws.handlers.delete(onMessage);
        };
      }, [onMessage]);
    },
  };
});

function emit(msg: WSMessage): void {
  act(() => {
    for (const h of ws.handlers) h(msg);
  });
}

const LINK: SessionLink = {
  status: 'working',
  seenAt: null,
  live: true,
  contextPct: null,
  rateLimits: [],
  costUsd: null,
  controllable: true,
};

function session(id: number, title: string, link: SessionLink | null): Session {
  return { id, title, sessionUuid: `uuid-${String(id)}-aaaaaaaa`, link } as unknown as Session;
}

const A = session(1, 'Alpha', LINK);
const B = session(2, 'Beta', LINK);
const C = session(3, 'Gamma (no link)', null);

const ref = (s: Session): { id: number; uuid: string; title: string | null } => ({
  id: s.id,
  uuid: s.sessionUuid,
  title: s.title,
});

function msg(over: Partial<SwarmMessage> & Pick<SwarmMessage, 'id' | 'kind'>): SwarmMessage {
  return {
    fromSession: null,
    toSession: null,
    toAddress: null,
    body: 'hello',
    truncated: false,
    origin: null,
    at: new Date().toISOString(),
    delivered: null,
    reason: null,
    ...over,
  };
}

const NO_CLAIMS: SwarmClaims = { claims: [], conflicts: [] };

function renderPanel(sender?: Pick<Session, 'id' | 'link'>): void {
  render(
    <MemoryRouter>
      <SwarmPanel projectId={7} projectSlug="proj" sender={sender} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  api.fetchSwarmMessages.mockReset().mockResolvedValue([]);
  api.fetchSwarmClaims.mockReset().mockResolvedValue(NO_CLAIMS);
  api.fetchSessions.mockReset().mockResolvedValue({ sessions: [A, B, C], nextCursor: null });
  api.sendSwarmMessage.mockReset();
  ws.handlers.clear();
});
afterEach(cleanup);

describe('feed', () => {
  it('renders send, receive and dashboard rows with their delivery state', async () => {
    api.fetchSwarmMessages.mockResolvedValue([
      msg({ id: 3, kind: 'dashboard', fromSession: ref(A), toAddress: 'ghost', body: 'from the dashboard', delivered: false, reason: 'undeliverable: no such session' }),
      msg({ id: 2, kind: 'receive', fromSession: ref(A), toSession: ref(B), body: 'got it' }),
      msg({ id: 1, kind: 'send', fromSession: ref(A), toSession: ref(B), body: 'sent ok', delivered: true }),
      msg({ id: 0, kind: 'send', fromSession: ref(B), toSession: ref(A), body: 'long one', truncated: true }),
    ]);
    renderPanel();
    const rows = await screen.findAllByTestId('swarm-message');
    expect(rows.map((r) => r.getAttribute('data-kind'))).toEqual(['dashboard', 'receive', 'send', 'send']);

    // The undelivered dashboard message states the reason.
    expect(within(rows[0]!).getByTestId('delivery').textContent).toContain('not delivered: undeliverable: no such session');
    // A receive row carries no delivery state of its own.
    expect(within(rows[1]!).queryByTestId('delivery')).toBeNull();
    expect(within(rows[2]!).getByTestId('delivery').textContent).toContain('delivered');
    expect(within(rows[3]!).getByTestId('delivery').textContent).toBe('pending');
    expect(within(rows[3]!).getByTestId('truncated')).toBeTruthy();
    // Sessions are links into the session detail.
    expect(within(rows[2]!).getByRole('link', { name: 'Alpha' }).getAttribute('href')).toBe('/sessions/1');
  });

  it('shows the empty and the error state', async () => {
    renderPanel();
    expect(await screen.findByText(/No messages between/)).toBeTruthy();
    cleanup();
    api.fetchSwarmMessages.mockRejectedValue(new Error('boom'));
    renderPanel();
    expect((await screen.findAllByRole('alert')).some((a) => a.textContent?.includes('boom'))).toBe(true);
  });
});

describe('claims', () => {
  it('highlights a conflicted path and names its holders', async () => {
    api.fetchSwarmClaims.mockResolvedValue({
      claims: [
        { sessionId: 1, sessionUuid: A.sessionUuid, path: '/r/a.go', claimedAt: 'x' },
        { sessionId: 2, sessionUuid: B.sessionUuid, path: '/r/a.go', claimedAt: 'x' },
        { sessionId: 1, sessionUuid: A.sessionUuid, path: '/r/b.go', claimedAt: 'x' },
      ],
      conflicts: [{ path: '/r/a.go', sessions: [{ sessionId: 1, sessionUuid: A.sessionUuid }, { sessionId: 2, sessionUuid: B.sessionUuid }] }],
    } satisfies SwarmClaims);
    renderPanel();
    const rows = await screen.findAllByTestId('swarm-claim');
    expect(rows.map((r) => r.getAttribute('data-conflict'))).toEqual(['true', 'false']);
    expect(within(rows[0]!).getByText('Beta')).toBeTruthy();
  });
});

describe('send form', () => {
  it('is disabled, with the reason, when the session has no link', async () => {
    renderPanel({ id: 1, link: null });
    const send = await screen.findByRole('button', { name: 'Send' });
    expect((send as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/No swarmery link mod reports on this session/)).toBeTruthy();
    expect((screen.getByLabelText('Message') as HTMLTextAreaElement).disabled).toBe(true);
  });

  it('is disabled while the link is not controllable', async () => {
    renderPanel({ id: 1, link: { ...LINK, controllable: false } });
    expect(((await screen.findByRole('button', { name: 'Send' })) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/not polling for commands/)).toBeTruthy();
  });

  it('offers only other sessions that have a link, and posts {to, text} for the sender', async () => {
    api.sendSwarmMessage.mockResolvedValue({ commandId: 11, messageId: 5 });
    renderPanel({ id: 1, link: LINK });
    const select = (await screen.findByLabelText('Recipient')) as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBe(2)); // placeholder + Beta
    expect([...select.options].map((o) => o.textContent)).toEqual(['Choose a session…', 'Beta']);

    const send = screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement;
    expect(send.disabled).toBe(true); // nothing chosen yet
    fireEvent.change(select, { target: { value: '2' } });
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'rebase please' } });
    expect(screen.getByTestId('swarm-count').textContent).toBe('13 / 4096');
    expect(send.disabled).toBe(false);
    fireEvent.click(send);

    await waitFor(() => expect(api.sendSwarmMessage).toHaveBeenCalledWith(1, 2, 'rebase please'));
    expect((await screen.findByTestId('swarm-command-state')).textContent).toContain('queued');
  });

  it('refuses text over the 4096-character limit', async () => {
    renderPanel({ id: 1, link: LINK });
    const select = (await screen.findByLabelText('Recipient')) as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBe(2));
    fireEvent.change(select, { target: { value: '2' } });
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'x'.repeat(4097) } });
    expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId('swarm-count').textContent).toBe('4097 / 4096');
  });

  it('shows the undeliverable reason when the command fails, and ignores other sessions', async () => {
    api.sendSwarmMessage.mockResolvedValue({ commandId: 11, messageId: 5 });
    renderPanel({ id: 1, link: LINK });
    const select = (await screen.findByLabelText('Recipient')) as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBe(2));
    fireEvent.change(select, { target: { value: '2' } });
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hi' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    await screen.findByTestId('swarm-command-state');

    const note = (over: Partial<LinkCommandNote>): WSMessage => ({
      type: 'link_command',
      payload: { sessionId: 1, commandId: 11, kind: 'message', state: 'failed', ...over },
    });
    emit({ type: 'link_command', payload: { sessionId: 9, commandId: 11, kind: 'message', state: 'done' } });
    expect(screen.getByTestId('swarm-command-state').textContent).toContain('queued');
    emit(note({ message: 'undeliverable: no such session' }));
    const state = screen.getByTestId('swarm-command-state');
    expect(state.textContent).toContain('failed: undeliverable: no such session');
  });

  it('shows a refused POST (409) inline', async () => {
    api.sendSwarmMessage.mockRejectedValue(new Error('session has no link'));
    renderPanel({ id: 1, link: LINK });
    const select = (await screen.findByLabelText('Recipient')) as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBe(2));
    fireEvent.change(select, { target: { value: '2' } });
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hi' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    expect((await screen.findByTestId('swarm-command-state')).textContent).toContain('not sent: session has no link');
  });
});

describe('conflict badge', () => {
  it('appears on a session in a conflict and not on the others', async () => {
    api.fetchSwarmClaims.mockResolvedValue({
      claims: [],
      conflicts: [{ path: '/r/a.go', sessions: [{ sessionId: 1, sessionUuid: 'u1' }, { sessionId: 2, sessionUuid: 'u2' }] }],
    } satisfies SwarmClaims);
    render(
      <SwarmConflictProvider>
        <div data-testid="s1"><ConflictBadge sessionId={1} /></div>
        <div data-testid="s3"><ConflictBadge sessionId={3} /></div>
      </SwarmConflictProvider>,
    );
    const badge = await within(await screen.findByTestId('s1')).findByTestId('swarm-conflict');
    expect(badge.getAttribute('data-tip')).toContain('/r/a.go');
    expect(badge.getAttribute('aria-label')).toContain('/r/a.go');
    expect(within(screen.getByTestId('s3')).queryByTestId('swarm-conflict')).toBeNull();
    // One fetch for the whole list, scoped to every project.
    expect(api.fetchSwarmClaims).toHaveBeenCalledTimes(1);
    expect(api.fetchSwarmClaims).toHaveBeenCalledWith(undefined);
  });

  it('renders nothing outside a provider', () => {
    render(<ConflictBadge sessionId={1} />);
    expect(screen.queryByTestId('swarm-conflict')).toBeNull();
  });
});

describe('live frames', () => {
  it('refetch the feed on swarm_message for this project only', async () => {
    renderPanel();
    await screen.findByText(/No messages between/);
    expect(api.fetchSwarmMessages).toHaveBeenCalledTimes(1);
    emit({ type: 'swarm_message', payload: { sessionId: 1, projectId: 99 } });
    expect(api.fetchSwarmMessages).toHaveBeenCalledTimes(1);
    emit({ type: 'swarm_message', payload: { sessionId: 1, projectId: 7 } });
    await waitFor(() => expect(api.fetchSwarmMessages).toHaveBeenCalledTimes(2));
  });

  it('refetch the claims on swarm_claim for this project only', async () => {
    renderPanel();
    await screen.findByText(/No files are claimed/);
    expect(api.fetchSwarmClaims).toHaveBeenCalledTimes(1);
    emit({ type: 'swarm_claim', payload: { projectId: 99 } });
    expect(api.fetchSwarmClaims).toHaveBeenCalledTimes(1);
    emit({ type: 'swarm_claim', payload: { projectId: 7 } });
    await waitFor(() => expect(api.fetchSwarmClaims).toHaveBeenCalledTimes(2));
  });
});
