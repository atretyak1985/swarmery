// Dock terminal reattach UI (terminal-dock-webstorm phase 5): a tab's hello
// frame is saved as its ptyId; closing a tab with × ends the PTY on the
// server (DELETE), but a plain unmount (reload, remount) never does; the
// "Background terminals" menu lists PTYs no open tab already owns; the
// too-many overlay shows the live session list and lets the operator close
// one from there.

import { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DockTermSession } from '../api/types';
import type { TermStatus } from './XTerm';
import type { TermCloseKind } from './closeReason';

type OnStatus = (s: TermStatus, closed?: TermCloseKind) => void;
interface Mount {
  id: string;
  ptyId: string | undefined;
  onStatus: OnStatus;
  onHello: (id: string) => void;
}
const mounts: Mount[] = [];

vi.mock('./XTerm', async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import('react');
  return {
    XTerm: forwardRef<
      unknown,
      { cwd?: string; ptyId?: string; onStatus?: OnStatus; onHello?: (id: string) => void }
    >(function FakeXTerm({ cwd, ptyId, onStatus, onHello }, ref) {
      useImperativeHandle(ref, () => ({ clear: () => undefined, fit: () => undefined, send: () => undefined }));
      useEffect(() => {
        if (onStatus && onHello) mounts.push({ id: cwd ?? '', ptyId, onStatus, onHello });
        // eslint-disable-next-line react-hooks/exhaustive-deps
      }, []);
      return <div data-testid="xterm" />;
    }),
  };
});

const apiMock = vi.hoisted(() => ({
  listDockTermSessions: vi.fn<(cwdPrefix: string) => Promise<DockTermSession[]>>(),
  deleteDockTermSession: vi.fn<(id: string) => Promise<void>>(),
}));
vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  listDockTermSessions: apiMock.listDockTermSessions,
  deleteDockTermSession: apiMock.deleteDockTermSession,
}));

import { TerminalDock, type DockState, type TermTab } from './TerminalDock';
import { migrateDock } from './layout';

const PROJECT = '/tmp/project';

function dockWith(tabs: Partial<TermTab>[]): DockState {
  return migrateDock({
    open: true,
    tabs: tabs.map((t, i) => ({ id: `t${i + 1}`, label: `shell ${i + 1}`, cwd: PROJECT, ...t })),
    activeId: tabs.length > 0 ? 't1' : null,
  });
}

function StatefulDock({ initial }: { initial: DockState }): JSX.Element {
  const [state, setState] = useState(initial);
  return (
    <>
      <TerminalDock projectSlug="proj" projectPath={PROJECT} state={state} onChange={setState} />
      <span data-testid="tab-count">{state.tabs.length}</span>
      <span data-testid="pty-ids">{state.tabs.map((t) => t.ptyId ?? '-').join(',')}</span>
    </>
  );
}

beforeEach(() => {
  mounts.length = 0;
  apiMock.listDockTermSessions.mockReset().mockResolvedValue([]);
  apiMock.deleteDockTermSession.mockReset().mockResolvedValue(undefined);
});
afterEach(cleanup);

describe('hello saves ptyId on the tab', () => {
  it('stores the id onto the dock state once the hello frame names it', async () => {
    render(<StatefulDock initial={dockWith([{}])} />);
    await screen.findByTestId('xterm');
    expect(screen.getByTestId('pty-ids').textContent).toBe('-');

    act(() => mounts[0]?.onHello('pty-1'));
    expect(screen.getByTestId('pty-ids').textContent).toBe('pty-1');
  });
});

describe('a restored tab reattaches rather than starting a new PTY', () => {
  it('passes the saved ptyId straight to XTerm on mount', async () => {
    render(<StatefulDock initial={dockWith([{ ptyId: 'pty-9' }])} />);
    await screen.findByTestId('xterm');
    expect(mounts[0]?.ptyId).toBe('pty-9');
  });
});

describe('closing a tab (×)', () => {
  it('deletes the PTY on the server, then removes the tab', async () => {
    render(<StatefulDock initial={dockWith([{ ptyId: 'pty-1' }])} />);
    await screen.findByTestId('xterm');

    fireEvent.click(screen.getByRole('button', { name: 'close shell 1' }));

    expect(apiMock.deleteDockTermSession).toHaveBeenCalledWith('pty-1');
    expect(screen.getByTestId('tab-count').textContent).toBe('0');
  });

  it('never deletes a tab that has not received a hello yet', async () => {
    render(<StatefulDock initial={dockWith([{}])} />);
    await screen.findByTestId('xterm');

    fireEvent.click(screen.getByRole('button', { name: 'close shell 1' }));
    expect(apiMock.deleteDockTermSession).not.toHaveBeenCalled();
  });

  it('a plain unmount (reload/navigation) never calls DELETE', async () => {
    const { unmount } = render(<StatefulDock initial={dockWith([{ ptyId: 'pty-1' }])} />);
    await screen.findByTestId('xterm');

    unmount();
    expect(apiMock.deleteDockTermSession).not.toHaveBeenCalled();
  });
});

describe('background terminals', () => {
  const SESSIONS: DockTermSession[] = [
    { id: 'pty-open', cwd: '/tmp/project', startedAt: '2026-01-01T00:00:00Z', attached: true },
    {
      id: 'pty-bg',
      cwd: '/tmp/project/sub',
      label: 'worker',
      foreground: 'claude',
      startedAt: '2026-01-01T00:00:00Z',
      attached: false,
      detachedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
    },
  ];

  it('lists only the PTYs no open tab already owns, with Attach and Close', async () => {
    apiMock.listDockTermSessions.mockResolvedValue(SESSIONS);
    render(<StatefulDock initial={dockWith([{ ptyId: 'pty-open' }])} />);
    await screen.findByTestId('xterm');
    await vi.waitFor(() => expect(apiMock.listDockTermSessions).toHaveBeenCalledWith(PROJECT));

    fireEvent.click(screen.getByRole('button', { name: 'background terminals' }));
    const list = await screen.findByRole('list');
    expect(list.textContent).toContain('worker');
    expect(list.textContent).not.toContain('pty-open');

    fireEvent.click(screen.getByRole('button', { name: 'attach to worker' }));
    expect(screen.getByTestId('tab-count').textContent).toBe('2');
    expect(screen.getByTestId('pty-ids').textContent).toContain('pty-bg');
  });

  it('closes a background terminal without touching any open tab', async () => {
    apiMock.listDockTermSessions.mockResolvedValue(SESSIONS);
    render(<StatefulDock initial={dockWith([{ ptyId: 'pty-open' }])} />);
    await screen.findByTestId('xterm');
    await vi.waitFor(() => expect(apiMock.listDockTermSessions).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('button', { name: 'background terminals' }));
    await screen.findByRole('list');
    fireEvent.click(screen.getByRole('button', { name: 'close worker' }));

    expect(apiMock.deleteDockTermSession).toHaveBeenCalledWith('pty-bg');
    expect(screen.getByTestId('tab-count').textContent).toBe('1');
  });
});

describe('too-many overlay', () => {
  it('renders the live session list and closes one from a Close button', async () => {
    const FULL: DockTermSession[] = Array.from({ length: 5 }, (_, i) => ({
      id: `pty-${i}`,
      cwd: `/tmp/project/${i}`,
      startedAt: '2026-01-01T00:00:00Z',
      attached: true,
    }));
    apiMock.listDockTermSessions.mockResolvedValue(FULL);
    render(<StatefulDock initial={dockWith([{}])} />);
    await screen.findByTestId('xterm');

    act(() => mounts[0]?.onStatus('closed', 'too-many'));
    expect(screen.getByRole('status').textContent).toContain('Too many terminals are open');

    const list = await screen.findByRole('list');
    expect(list.querySelectorAll('li')).toHaveLength(5);

    // None of these fixture sessions carries a label or foreground, so every
    // row shows the generic "shell" fallback — click the first one.
    fireEvent.click(screen.getAllByRole('button', { name: 'close shell' })[0]);
    expect(apiMock.deleteDockTermSession).toHaveBeenCalledWith('pty-0');

    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await screen.findByTestId('xterm');
    expect(mounts).toHaveLength(2);
  });
});

describe('attached-elsewhere takes over without a fresh PTY', () => {
  it('shows "Take over here" and remounts without re-arming the shell', async () => {
    render(<StatefulDock initial={dockWith([{ ptyId: 'pty-1', run: 'claude' }])} />);
    await screen.findByTestId('xterm');

    act(() => mounts[0]?.onStatus('closed', 'attached-elsewhere'));
    expect(screen.getByRole('status').textContent).toContain('reattached from another tab or window');

    fireEvent.click(screen.getByRole('button', { name: 'Take over here' }));
    expect(apiMock.deleteDockTermSession).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(mounts).toHaveLength(2));
    expect(mounts[1]?.ptyId).toBe('pty-1');
  });
});
