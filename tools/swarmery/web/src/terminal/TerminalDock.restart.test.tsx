// A tab whose terminal closed shows why and offers Restart, which remounts the
// tab's XTerm (a fresh PTY) without closing the tab. A tab restored from
// localStorage never re-types its command on its own: each replay used to start
// a new, empty Claude session.

import { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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

import { TerminalDock, type DockState } from './TerminalDock';

function renderDock(run?: string): void {
  const state: DockState = {
    open: true,
    tabs: [{ id: 't1', label: run === undefined ? 'shell 1' : 'Claude Code', cwd: '/tmp/project', run }],
    activeId: 't1',
  };
  render(<TerminalDock projectSlug="proj" projectPath="/tmp/project" state={state} onChange={() => undefined} />);
}

function StatefulDock({ initial }: { initial: DockState }): JSX.Element {
  const [state, setState] = useState(initial);
  return <TerminalDock projectSlug="proj" projectPath="/tmp/project" state={state} onChange={setState} />;
}

describe('terminal dock restart', () => {
  beforeEach(() => {
    mounts.length = 0;
    sent.length = 0;
  });
  afterEach(cleanup);

  it('shows why a tab closed and restarts it with a fresh terminal', async () => {
    renderDock();
    await screen.findByTestId('xterm');
    expect(mounts).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Restart' })).toBeNull();

    act(() => mounts[0]?.onStatus('closed', 'daemon-stopping'));
    expect(screen.getByRole('status').textContent).toContain('The swarmery daemon stopped or restarted');

    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await screen.findByTestId('xterm');
    expect(mounts).toHaveLength(2);
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('names Claude Code on the restart button of a Claude Code tab', async () => {
    renderDock('claude');
    await screen.findByTestId('xterm');
    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    expect(screen.getByRole('button', { name: 'Restart Claude Code' })).toBeTruthy();
  });
});

describe('restored Claude Code tab', () => {
  beforeEach(() => {
    mounts.length = 0;
    sent.length = 0;
  });
  afterEach(cleanup);

  it('opens a plain shell and starts claude only on Start', async () => {
    renderDock('claude');
    await screen.findByTestId('xterm');
    expect(mounts[0]?.initialInput).toBeUndefined();

    act(() => mounts[0]?.onStatus('open'));
    fireEvent.click(screen.getByRole('button', { name: 'Start Claude Code' }));
    expect(sent).toEqual(['claude\r']);
    expect(screen.queryByRole('button', { name: 'Start Claude Code' })).toBeNull();
  });

  it('hides the Start strip when dismissed, without typing anything', async () => {
    renderDock('claude');
    await screen.findByTestId('xterm');
    act(() => mounts[0]?.onStatus('open'));
    fireEvent.click(screen.getByRole('button', { name: 'dismiss' }));
    expect(screen.queryByRole('button', { name: 'Start Claude Code' })).toBeNull();
    expect(sent).toEqual([]);
  });

  it('runs claude again on an explicit Restart', async () => {
    renderDock('claude');
    await screen.findByTestId('xterm');
    act(() => mounts[0]?.onStatus('closed', 'shell-exited'));
    fireEvent.click(screen.getByRole('button', { name: 'Restart Claude Code' }));
    await screen.findByTestId('xterm');
    expect(mounts[1]?.initialInput).toBe('claude');
  });
});

describe('new Claude Code tab', () => {
  beforeEach(() => {
    mounts.length = 0;
    sent.length = 0;
  });
  afterEach(cleanup);

  it('types claude on its first connect and shows no Start strip', async () => {
    const shell: DockState = { open: true, tabs: [{ id: 's1', label: 'shell 1', cwd: '/tmp/project' }], activeId: 's1' };
    render(<StatefulDock initial={shell} />);
    await screen.findByTestId('xterm');

    fireEvent.click(screen.getByRole('button', { name: 'open Claude Code in a new terminal' }));
    await vi.waitFor(() => expect(mounts).toHaveLength(2));
    expect(mounts[1]?.initialInput).toBe('claude');

    act(() => mounts[1]?.onStatus('open'));
    expect(screen.queryByRole('button', { name: 'Start Claude Code' })).toBeNull();
  });
});
