// The dock's window controls (terminal-dock-webstorm phase 1): a taller header
// ending in maximize / full screen / minimize. None of them may remount XTerm —
// an unmount closes the PTY — so the FakeXTerm below counts mounts.

import { useState } from 'react';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TermStatus } from './XTerm';

let mountCount = 0;

vi.mock('./XTerm', async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import('react');
  return {
    XTerm: forwardRef<unknown, { onStatus?: (s: TermStatus) => void }>(function FakeXTerm(_props, ref) {
      useImperativeHandle(ref, () => ({ clear: () => undefined, fit: () => undefined, send: () => undefined }));
      useEffect(() => {
        mountCount += 1;
      }, []);
      // xterm.js renders into a `.xterm` element that holds a focusable textarea.
      return (
        <div className="xterm" data-testid="xterm">
          <textarea aria-label="terminal input" />
        </div>
      );
    }),
  };
});

vi.mock('../components/VcsAuthBanner', () => ({ VcsAuthBanner: () => null }));
vi.mock('../lib/health', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../lib/health')>()),
  useHealth: () => ({ health: null }),
}));
vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  fetchDispatchStatus: () => Promise.reject(new Error('not attached')),
}));

import { StatusBar } from '../workspace/StatusBar';
import { TerminalDock, dockCoversPage, type DockState } from './TerminalDock';

const OPEN: DockState = {
  open: true,
  tabs: [{ id: 't1', label: 'shell 1', cwd: '/tmp/project' }],
  activeId: 't1',
};

/** The dock wired the way ProjectWorkspaceLayout wires it, plus a reopen button. */
function Harness({ onMax }: { onMax?: (next: boolean) => void }): JSX.Element {
  const [state, setState] = useState(OPEN);
  const [maximized, setMaximized] = useState(false);
  return (
    <>
      <main hidden={dockCoversPage(state, maximized)}>page</main>
      <TerminalDock
        projectSlug="proj"
        projectPath="/tmp/project"
        state={state}
        onChange={setState}
        maximized={maximized}
        onMaximizedChange={(next) => {
          onMax?.(next);
          setMaximized(next);
        }}
      />
      <button type="button" onClick={() => setState((s) => ({ ...s, open: true }))}>
        reopen
      </button>
      <span data-testid="open">{String(state.open)}</span>
    </>
  );
}

function controls(): HTMLElement {
  return screen.getByRole('group', { name: 'window controls' });
}

beforeEach(() => {
  mountCount = 0;
});
afterEach(cleanup);

describe('header and window controls (SC-1)', () => {
  it('raises the header and ends it with maximize, full screen, minimize after the toolbar', async () => {
    render(<Harness />);
    await screen.findByTestId('xterm');

    const header = screen.getByTestId('dock-header');
    expect(header.className).toContain('h-9');
    expect(header.className).toContain('desk:h-10');

    const group = controls();
    expect(group.parentElement).toBe(header);
    expect(header.lastElementChild).toBe(group);
    const clear = screen.getByRole('button', { name: 'Clear' });
    expect(clear.compareDocumentPosition(group) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    const labels = Array.from(group.querySelectorAll('button')).map((b) => b.getAttribute('aria-label'));
    expect(labels).toEqual(['maximize terminal', 'full screen', 'minimize terminal']);
  });
});

describe('minimize (SC-2)', () => {
  it('hides the dock without remounting the terminal, and reopens it intact', async () => {
    render(<Harness />);
    await screen.findByTestId('xterm');
    expect(mountCount).toBe(1);

    fireEvent.click(screen.getByRole('button', { name: 'minimize terminal' }));
    expect(screen.getByTestId('open').textContent).toBe('false');
    // A hidden element has no accessible name, so look it up by attribute.
    expect(document.querySelector<HTMLElement>('[aria-label="terminal dock"]')?.hidden).toBe(true);
    expect(screen.getByTestId('xterm')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'reopen' }));
    expect(screen.getByRole('region', { name: 'terminal dock' }).hidden).toBe(false);
    expect(mountCount).toBe(1);
  });

  it('also restores a maximized dock', async () => {
    const onMax = vi.fn();
    render(<Harness onMax={onMax} />);
    await screen.findByTestId('xterm');
    fireEvent.click(screen.getByRole('button', { name: 'maximize terminal' }));
    fireEvent.click(screen.getByRole('button', { name: 'minimize terminal' }));
    expect(onMax).toHaveBeenLastCalledWith(false);
    expect(screen.getByText('page').hidden).toBe(false);
  });
});

describe('maximize (SC-4)', () => {
  it('covers the page, drops the resize handle, and leaves on Esc outside the terminal only', async () => {
    const onMax = vi.fn();
    render(<Harness onMax={onMax} />);
    await screen.findByTestId('xterm');
    expect(screen.getByText('page').hidden).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: 'maximize terminal' }));
    expect(onMax).toHaveBeenCalledWith(true);
    expect(screen.getByText('page', { selector: 'main' }).hidden).toBe(true);
    expect(screen.queryByRole('separator', { name: 'resize terminal' })).toBeNull();
    expect(screen.getByRole('region', { name: 'terminal dock' }).className).toContain('flex-1');
    expect(screen.getByRole('button', { name: 'restore terminal size' })).toBeTruthy();

    // Esc typed into the terminal belongs to the program running there.
    const input = screen.getByRole('textbox', { name: 'terminal input' });
    input.focus();
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(onMax).not.toHaveBeenCalledWith(false);

    input.blur();
    expect(document.activeElement).toBe(document.body);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(onMax).toHaveBeenLastCalledWith(false);
    expect(screen.getByText('page', { selector: 'main' }).hidden).toBe(false);
    expect(screen.getByRole('separator', { name: 'resize terminal' })).toBeTruthy();
    expect(mountCount).toBe(1);
  });

  it('only covers the page while the dock is visible', () => {
    expect(dockCoversPage(OPEN, true)).toBe(true);
    expect(dockCoversPage(OPEN, false)).toBe(false);
    expect(dockCoversPage({ ...OPEN, open: false }, true)).toBe(false);
    expect(dockCoversPage({ open: true, tabs: [], activeId: null }, true)).toBe(false);
  });
});

describe('full screen (SC-5)', () => {
  const proto = HTMLElement.prototype as { requestFullscreen?: unknown };
  const original = proto.requestFullscreen;
  let fullscreenElement: Element | null = null;

  beforeEach(() => {
    fullscreenElement = null;
    Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => fullscreenElement });
  });
  afterEach(() => {
    proto.requestFullscreen = original;
    Reflect.deleteProperty(document, 'fullscreenElement');
  });

  it('asks the browser for full screen and follows fullscreenchange', async () => {
    const request = vi.fn(() => Promise.resolve());
    proto.requestFullscreen = request;
    render(<Harness />);
    await screen.findByTestId('xterm');

    fireEvent.click(screen.getByRole('button', { name: 'full screen' }));
    expect(request).toHaveBeenCalledTimes(1);

    fullscreenElement = screen.getByRole('region', { name: 'terminal dock' });
    act(() => {
      document.dispatchEvent(new Event('fullscreenchange'));
    });
    const exit = screen.getByRole('button', { name: 'exit full screen' });
    expect(exit.getAttribute('title')).toBe('Hold Esc to exit full screen');

    fullscreenElement = null;
    act(() => {
      document.dispatchEvent(new Event('fullscreenchange'));
    });
    expect(screen.getByRole('button', { name: 'full screen' })).toBeTruthy();
    expect(mountCount).toBe(1);
  });

  describe('Keyboard Lock', () => {
    const lock = vi.fn((_keys?: string[]) => Promise.resolve());
    const unlock = vi.fn();

    beforeEach(() => {
      lock.mockClear();
      unlock.mockClear();
      Object.defineProperty(navigator, 'keyboard', { configurable: true, value: { lock, unlock } });
      proto.requestFullscreen = vi.fn(() => Promise.resolve());
    });
    afterEach(() => {
      Reflect.deleteProperty(navigator, 'keyboard');
    });

    it('holds Escape in full screen and releases it on exit', async () => {
      render(<Harness />);
      await screen.findByTestId('xterm');

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'full screen' }));
        await Promise.resolve();
      });
      expect(lock).toHaveBeenCalledWith(['Escape']);

      fullscreenElement = screen.getByRole('region', { name: 'terminal dock' });
      act(() => {
        document.dispatchEvent(new Event('fullscreenchange'));
      });
      expect(unlock).not.toHaveBeenCalled();

      fullscreenElement = null;
      act(() => {
        document.dispatchEvent(new Event('fullscreenchange'));
      });
      expect(unlock).toHaveBeenCalled();
    });

    it('releases Escape when the dock unmounts in full screen', async () => {
      const { unmount } = render(<Harness />);
      await screen.findByTestId('xterm');
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'full screen' }));
        await Promise.resolve();
      });
      fullscreenElement = screen.getByRole('region', { name: 'terminal dock' });
      act(() => {
        document.dispatchEvent(new Event('fullscreenchange'));
      });

      unmount();
      expect(unlock).toHaveBeenCalled();
    });
  });
});

describe('StatusBar terminal count (SC-3)', () => {
  const counts = { waiting: 0, running: 0, blocked: 0 } as Parameters<typeof StatusBar>[0]['counts'];

  it('shows the open-tab count and names it in the aria-label', () => {
    render(<StatusBar counts={counts} projectId={null} terminalCount={3} onToggleTerminal={() => undefined} />);
    const btn = screen.getByRole('button', { name: 'show terminal (3 open)' });
    expect(btn.textContent).toContain('3');
  });

  it('says hide while the dock is open', () => {
    render(
      <StatusBar counts={counts} projectId={null} terminalOpen terminalCount={2} onToggleTerminal={() => undefined} />,
    );
    expect(screen.getByRole('button', { name: 'hide terminal (2 open)' })).toBeTruthy();
  });

  it('shows no count without tabs', () => {
    render(<StatusBar counts={counts} projectId={null} onToggleTerminal={() => undefined} />);
    const btn = screen.getByRole('button', { name: 'show terminal' });
    expect(btn.querySelector('.tabular-nums')).toBeNull();
  });
});
