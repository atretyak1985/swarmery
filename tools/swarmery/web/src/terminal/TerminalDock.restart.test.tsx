// A tab whose terminal closed shows why and offers Restart, which remounts the
// tab's XTerm (a fresh PTY) without closing the tab.

import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TermStatus } from './XTerm';
import type { TermCloseKind } from './closeReason';

type OnStatus = (s: TermStatus, closed?: TermCloseKind) => void;
const mounts: OnStatus[] = [];

vi.mock('./XTerm', async () => {
  const { forwardRef, useEffect } = await import('react');
  return {
    XTerm: forwardRef<unknown, { onStatus?: OnStatus }>(function FakeXTerm({ onStatus }) {
      useEffect(() => {
        if (onStatus) mounts.push(onStatus);
        // eslint-disable-next-line react-hooks/exhaustive-deps
      }, []);
      return <div data-testid="xterm" />;
    }),
  };
});

import { TerminalDock, type DockState } from './TerminalDock';

function renderDock(run?: string): void {
  const state: DockState = {
    open: true,
    tabs: [{ id: 't1', label: run === undefined ? 'shell 1' : 'Claude Code', cwd: '/tmp/project', run }],
    activeId: 't1',
  };
  render(<TerminalDock projectSlug="proj" projectPath="/tmp/project" state={state} onChange={() => undefined} />);
}

describe('terminal dock restart', () => {
  afterEach(cleanup);

  it('shows why a tab closed and restarts it with a fresh terminal', async () => {
    mounts.length = 0;
    renderDock();
    await screen.findByTestId('xterm');
    expect(mounts).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Restart' })).toBeNull();

    act(() => mounts[0]?.('closed', 'daemon-stopping'));
    expect(screen.getByRole('status').textContent).toContain('The swarmery daemon stopped or restarted');

    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await screen.findByTestId('xterm');
    expect(mounts).toHaveLength(2);
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('names Claude Code on the restart button of a Claude Code tab', async () => {
    mounts.length = 0;
    renderDock('claude');
    await screen.findByTestId('xterm');
    act(() => mounts[0]?.('closed', 'shell-exited'));
    expect(screen.getByRole('button', { name: 'Restart Claude Code' })).toBeTruthy();
  });
});
