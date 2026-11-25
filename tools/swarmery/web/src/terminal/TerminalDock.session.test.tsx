// A Claude Code dock tab is bound to one Claude session: it mints the session
// UUID, types `claude --session-id <uuid>` until the daemon has ingested that
// session and `claude --resume <uuid>` after, and shows the daemon's session
// title (short form in the tab, full text in its title attribute).

import { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Session, SessionDetailResponse, WSMessage } from '../api/types';
import type { TermStatus } from './XTerm';
import type { TermCloseKind } from './closeReason';

type OnStatus = (s: TermStatus, closed?: TermCloseKind) => void;
interface Mount {
  onStatus: OnStatus;
  initialInput: string | undefined;
}
const mounts: Mount[] = [];
const sent: string[] = [];

vi.mock('./XTerm', async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import('react');
  return {
    XTerm: forwardRef<unknown, { onStatus?: OnStatus; initialInput?: string }>(function FakeXTerm(
      { onStatus, initialInput },
      ref,
    ) {
      useImperativeHandle(ref, () => ({
        clear: () => undefined,
        fit: () => undefined,
        send: (text: string) => sent.push(text),
      }));
      useEffect(() => {
        if (onStatus) mounts.push({ onStatus, initialInput });
        // eslint-disable-next-line react-hooks/exhaustive-deps
      }, []);
      return <div data-testid="xterm" />;
    }),
  };
});

const api = vi.hoisted(() => ({
  fetchSession: vi.fn<(id: number | string) => Promise<SessionDetailResponse>>(),
}));
vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  fetchSession: api.fetchSession,
}));

const ws = vi.hoisted(() => ({ handlers: new Set<(m: WSMessage) => void>() }));
vi.mock('../lib/ws', async () => {
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

import { TerminalDock, type DockState, type TermTab } from './TerminalDock';

const UUID = '3f2b9c1e-8d4a-4f6b-9a1c-2e7d5b8f0a11';
const SESSION_CMD = /^claude --session-id ([0-9a-f-]{36})$/;

function sessionFrame(uuid: string, title: string | null, type: 'session_started' | 'session_updated' = 'session_updated'): void {
  const payload = { id: 7, sessionUuid: uuid, title, startedAt: '2026-11-25T10:00:00Z' } as Session;
  act(() => {
    for (const h of ws.handlers) h({ type, payload });
  });
}

function notFound(): Promise<never> {
  return Promise.reject(new Error(`GET /api/sessions/${UUID}: 404`));
}

let latest: DockState | null = null;
function StatefulDock({ initial }: { initial: DockState }): JSX.Element {
  const [state, setState] = useState(initial);
  latest = state;
  return <TerminalDock projectSlug="proj" projectPath="/tmp/project" state={state} onChange={setState} />;
}

function oneTab(tab: Omit<TermTab, 'id' | 'cwd'>): DockState {
  return { open: true, tabs: [{ id: 't1', cwd: '/tmp/project', ...tab }], activeId: 't1' };
}

describe('Claude Code tab session binding', () => {
  beforeEach(() => {
    mounts.length = 0;
    sent.length = 0;
    latest = null;
    api.fetchSession.mockReset();
    api.fetchSession.mockImplementation(notFound);
  });
  afterEach(cleanup);

  it('mints a session id for a new Claude tab and types claude --session-id with it', async () => {
    const shell: DockState = { open: true, tabs: [{ id: 's1', label: 'shell 1', cwd: '/tmp/project' }], activeId: 's1' };
    render(<StatefulDock initial={shell} />);
    await screen.findByTestId('xterm');

    fireEvent.click(screen.getByRole('button', { name: 'open Claude Code in a new terminal' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));

    const input = mounts[1]?.initialInput ?? '';
    expect(input).toMatch(SESSION_CMD);
    const claudeTab = latest?.tabs[1];
    expect(claudeTab?.run).toBe('claude');
    expect(claudeTab?.sessionId).toBe(SESSION_CMD.exec(input)?.[1]);
    expect(api.fetchSession).toHaveBeenCalledTimes(1);
    expect(api.fetchSession).toHaveBeenCalledWith(claudeTab?.sessionId);
  });

  it('names the tab after its session and follows a rename', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByTestId('xterm');
    expect(screen.getByRole('button', { name: 'close Claude Code' })).toBeTruthy();

    sessionFrame(UUID, 'Add WebStorm style splits');
    const caption = screen.getByText('Add WebStorm style…');
    expect(caption.getAttribute('title')).toBe('Add WebStorm style splits');

    sessionFrame(UUID, 'Rename the dock tabs');
    expect(screen.queryByText('Add WebStorm style…')).toBeNull();
    expect(screen.getByText('Rename the dock…').getAttribute('title')).toBe('Rename the dock tabs');
    // The saved label stays the fallback; the title is never written into it.
    expect(latest?.tabs[0]?.label).toBe('Claude Code');
  });

  it('ignores frames for another session', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByTestId('xterm');
    sessionFrame('00000000-0000-4000-8000-000000000000', 'Somebody else', 'session_started');
    expect(screen.getByRole('button', { name: 'close Claude Code' })).toBeTruthy();
    expect(screen.queryByText('Somebody else')).toBeNull();
  });

  it('takes the title from the daemon on mount', async () => {
    api.fetchSession.mockResolvedValue({ id: 7, sessionUuid: UUID, title: '✳ Refactor dock' } as SessionDetailResponse);
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    expect(await screen.findByText('Refactor dock')).toBeTruthy();
    expect(api.fetchSession).toHaveBeenCalledTimes(1);
  });

  it('resumes a known session on Restart', async () => {
    api.fetchSession.mockResolvedValue({ id: 7, sessionUuid: UUID, title: 'Fix the flaky planrun test' } as SessionDetailResponse);
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByText('Fix the flaky…');

    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    fireEvent.click(screen.getByRole('button', { name: 'Restart Claude Code' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));
    expect(mounts[1]?.initialInput).toBe(`claude --resume ${UUID}`);
  });

  it('resumes a session a live frame announced', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByTestId('xterm');
    sessionFrame(UUID, null, 'session_started');

    act(() => mounts[0]?.onStatus('open'));
    fireEvent.click(screen.getByRole('button', { name: 'Start Claude Code' }));
    expect(sent).toEqual([`claude --resume ${UUID}\r`]);
  });

  it('creates an unknown session under its id on Start (daemon answers 404)', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByTestId('xterm');
    await vi.waitFor(() => expect(api.fetchSession).toHaveBeenCalledWith(UUID));

    act(() => mounts[0]?.onStatus('open'));
    fireEvent.click(screen.getByRole('button', { name: 'Start Claude Code' }));
    expect(sent).toEqual([`claude --session-id ${UUID}\r`]);
  });

  it('treats a daemon-minted pending session (202) as unknown', async () => {
    api.fetchSession.mockResolvedValue({ pending: true, sessionUuid: UUID, source: 'phase' } as SessionDetailResponse);
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude', sessionId: UUID })} />);
    await screen.findByTestId('xterm');
    await vi.waitFor(() => expect(api.fetchSession).toHaveBeenCalled());

    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    fireEvent.click(screen.getByRole('button', { name: 'Restart Claude Code' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));
    expect(mounts[1]?.initialInput).toBe(`claude --session-id ${UUID}`);
  });

  it('gives a legacy Claude tab a session id on Start', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude' })} />);
    await screen.findByTestId('xterm');
    expect(api.fetchSession).not.toHaveBeenCalled();

    act(() => mounts[0]?.onStatus('open'));
    fireEvent.click(screen.getByRole('button', { name: 'Start Claude Code' }));

    expect(sent).toHaveLength(1);
    const id = /^claude --session-id ([0-9a-f-]{36})\r$/.exec(sent[0] ?? '')?.[1];
    expect(id).toBeDefined();
    expect(latest?.tabs[0]?.sessionId).toBe(id);
    expect(latest?.tabs[0]?.run).toBe('claude');
  });

  it('gives a legacy Claude tab a session id on Restart', async () => {
    render(<StatefulDock initial={oneTab({ label: 'Claude Code', run: 'claude' })} />);
    await screen.findByTestId('xterm');

    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    fireEvent.click(screen.getByRole('button', { name: 'Restart Claude Code' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));

    const id = latest?.tabs[0]?.sessionId;
    expect(id).toMatch(/^[0-9a-f-]{36}$/);
    expect(mounts[1]?.initialInput).toBe(`claude --session-id ${id}`);
  });

  it('leaves a plain shell tab alone', async () => {
    render(<StatefulDock initial={oneTab({ label: 'shell 1' })} />);
    await screen.findByTestId('xterm');
    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));
    expect(mounts[1]?.initialInput).toBeUndefined();
    expect(latest?.tabs[0]?.sessionId).toBeUndefined();
    expect(api.fetchSession).not.toHaveBeenCalled();
  });
});
