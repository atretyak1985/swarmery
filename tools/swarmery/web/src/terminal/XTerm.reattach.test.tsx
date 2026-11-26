// A tab that already has a ptyId reattaches instead of starting a fresh PTY
// (terminal-dock-webstorm phase 5): the socket opens with ?attach=<ptyId>
// rather than ?cwd=, and the hello frame that is always the first message on
// the resulting socket is handed up to the dock, never written to the screen.

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
  url: string;
  readyState = 0;
  binaryType = '';
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeSocket.last = this;
  }
  send(): void {}
  close(): void {}
}

class FakeResizeObserver {
  observe(): void {}
  disconnect(): void {}
}

describe('XTerm reattach', () => {
  beforeEach(() => {
    written.length = 0;
    FakeSocket.last = null;
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('opens a fresh cwd PTY when the tab has no ptyId', () => {
    render(<XTerm cwd="/tmp/project" fontSize={14} />);
    expect(FakeSocket.last?.url).toContain('cwd=');
    expect(FakeSocket.last?.url).not.toContain('attach=');
  });

  it('opens ?attach=<ptyId> instead of a new PTY when the tab already has one', () => {
    render(<XTerm cwd="/tmp/project" ptyId="pty-123" fontSize={14} />);
    expect(FakeSocket.last?.url).toContain('attach=pty-123');
    expect(FakeSocket.last?.url).not.toContain('cwd=');
  });

  it('hands the hello frame id up and never writes it to the screen', () => {
    const onHello = vi.fn();
    render(<XTerm cwd="/tmp/project" fontSize={14} onHello={onHello} />);
    FakeSocket.last?.onopen?.();
    FakeSocket.last?.onmessage?.({ data: JSON.stringify({ type: 'hello', id: 'pty-abc' }) });

    expect(onHello).toHaveBeenCalledWith('pty-abc');
    expect(written.join('')).not.toContain('hello');
  });

  it('still writes ordinary text output that merely looks like JSON but isn’t a hello frame', () => {
    const onHello = vi.fn();
    render(<XTerm cwd="/tmp/project" fontSize={14} onHello={onHello} />);
    FakeSocket.last?.onopen?.();
    FakeSocket.last?.onmessage?.({ data: '{"not":"hello"}' });

    expect(onHello).not.toHaveBeenCalled();
    expect(written.join('')).toContain('{"not":"hello"}');
  });
});
