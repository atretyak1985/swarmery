// A closed terminal says why, not a bare "[terminal session ended]": the line
// written into the terminal and the status reported to the dock both carry the
// reason classified from the close frame (closeReason.ts).

import { render } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const written: string[] = [];

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: unknown;
    constructor(options: unknown) {
      this.options = options;
    }
    loadAddon(): void {}
    open(): void {}
    focus(): void {}
    write(data: string): void {
      written.push(data);
    }
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

import { XTerm } from './XTerm';

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket | null = null;
  readyState = 0;
  binaryType = '';
  onopen: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  constructor() {
    FakeSocket.last = this;
  }
  send(): void {}
  close(): void {}
}

class FakeResizeObserver {
  observe(): void {}
  disconnect(): void {}
}

describe('terminal close', () => {
  beforeEach(() => {
    written.length = 0;
    FakeSocket.last = null;
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('writes why the shell ended and reports it to the dock', () => {
    const onStatus = vi.fn();
    render(<XTerm cwd="/tmp/project" fontSize={14} onStatus={onStatus} />);
    const ws = FakeSocket.last;
    expect(ws).not.toBeNull();
    ws?.onopen?.();
    ws?.onclose?.({ code: 1000, reason: 'shell-exited' });

    expect(onStatus).toHaveBeenLastCalledWith('closed', 'shell-exited');
    const out = written.join('');
    expect(out).toContain('The shell in this tab exited.');
    expect(out).not.toContain('terminal session ended');
  });

  it('reads a drop without a close frame as a lost daemon connection', () => {
    const onStatus = vi.fn();
    render(<XTerm cwd="/tmp/project" fontSize={14} onStatus={onStatus} />);
    FakeSocket.last?.onopen?.();
    FakeSocket.last?.onclose?.({ code: 1006, reason: '' });

    expect(onStatus).toHaveBeenLastCalledWith('closed', 'connection-lost');
    expect(written.join('')).toContain('Lost the connection to the swarmery daemon');
  });
});
