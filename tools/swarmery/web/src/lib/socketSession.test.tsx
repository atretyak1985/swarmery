// @vitest-environment jsdom
//
// Both dashboard sockets — the shared live-updates socket (lib/ws.ts) and the
// terminal (terminal/XTerm.tsx) — offer the dashboard session as a
// subprotocol, and a socket refused before it opened, with no session to
// offer, raises the locked notice.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    loadAddon(): void {}
    open(): void {}
    focus(): void {}
    write(): void {}
    clear(): void {}
    dispose(): void {}
    onData(): { dispose: () => void } {
      return { dispose: () => undefined };
    }
    onResize(): { dispose: () => void } {
      return { dispose: () => undefined };
    }
  },
}));
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit(): void {}
  },
}));
vi.mock('@xterm/xterm/css/xterm.css', () => ({}));

import { cleanup, render, renderHook } from '../test/render';
import { XTerm } from '../terminal/XTerm';
import { SESSION_STORAGE_KEY, isLockedNoticeShown, resetDashboardSessionForTests } from './dashboardSession';
import { useLiveUpdates } from './ws';

class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = 0;
  binaryType = '';
  onopen: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(
    readonly url: string,
    readonly protocols?: string | string[],
  ) {
    FakeSocket.instances.push(this);
  }
  send(): void {}
  close(): void {}
}

class FakeResizeObserver {
  observe(): void {}
  disconnect(): void {}
}

beforeEach(() => {
  FakeSocket.instances = [];
  window.localStorage.clear();
  resetDashboardSessionForTests();
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('ResizeObserver', FakeResizeObserver);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('live-updates socket', () => {
  it('offers swarmery.v1 and the session subprotocol when a session exists', () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-ws');
    const { unmount } = renderHook(() => useLiveUpdates(() => undefined, () => undefined));
    const ws = FakeSocket.instances.at(-1);
    expect(ws?.url).toMatch(/\/api\/ws$/);
    expect(ws?.protocols).toEqual(['swarmery.v1', 'swarmery.session.sess-ws']);
    unmount();
  });

  it('offers only swarmery.v1 without one, and a refusal raises the notice', () => {
    const { unmount } = renderHook(() => useLiveUpdates(() => undefined, () => undefined));
    const ws = FakeSocket.instances.at(-1);
    expect(ws?.protocols).toEqual(['swarmery.v1']);
    ws?.onclose?.();
    expect(isLockedNoticeShown()).toBe(true);
    unmount();
  });
});

describe('terminal socket', () => {
  it('offers swarmery.v1 and the session subprotocol when a session exists', () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-term');
    render(<XTerm cwd="/tmp/x" fontSize={12} />);
    const ws = FakeSocket.instances.at(-1);
    expect(ws?.url).toContain('/api/term/ws');
    expect(ws?.protocols).toEqual(['swarmery.v1', 'swarmery.session.sess-term']);
  });

  it('a terminal refused before it opened, with no session, raises the notice', () => {
    render(<XTerm cwd="/tmp/x" fontSize={12} />);
    const ws = FakeSocket.instances.at(-1);
    expect(ws?.protocols).toEqual(['swarmery.v1']);
    ws?.onclose?.();
    expect(isLockedNoticeShown()).toBe(true);
  });
});
