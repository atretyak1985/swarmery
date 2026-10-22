// Terminal hyperlinks (OSC 8) carry a target chosen by whatever runs in the
// PTY. The XTerm wrapper must hand xterm a link handler that opens only
// http(s) URLs — never a script or other scheme.

import { render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

interface CapturedOptions {
  linkHandler?: { activate: (event: MouseEvent, uri: string) => void } | null;
}

const captured: { options: CapturedOptions | null } = { options: null };

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: CapturedOptions;
    constructor(options: CapturedOptions) {
      this.options = options;
      captured.options = options;
    }
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

import { openWebLink, XTerm } from './XTerm';

class FakeSocket {
  static OPEN = 1;
  readyState = 0;
  binaryType = '';
  onopen: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  onclose: (() => void) | null = null;
  send(): void {}
  close(): void {}
}

class FakeResizeObserver {
  observe(): void {}
  disconnect(): void {}
}

describe('terminal hyperlinks', () => {
  let open: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    captured.options = null;
    open = vi.spyOn(window, 'open').mockImplementation(() => null);
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.stubGlobal('ResizeObserver', FakeResizeObserver);
  });

  afterEach(() => {
    open.mockRestore();
    vi.unstubAllGlobals();
  });

  it('openWebLink opens http(s) in a new tab without an opener', () => {
    openWebLink('https://example.test/docs');
    openWebLink('HTTP://example.test/');
    expect(open).toHaveBeenCalledTimes(2);
    expect(open).toHaveBeenNthCalledWith(1, 'https://example.test/docs', '_blank', 'noopener,noreferrer');
  });

  it('openWebLink ignores every other scheme', () => {
    for (const uri of [
      'javascript:alert(1)',
      'data:text/html,<p>x</p>',
      'file:///etc/hosts',
      'vbscript:x',
      '/relative',
      ' https://leading-space.test/',
    ]) {
      openWebLink(uri);
    }
    expect(open).not.toHaveBeenCalled();
  });

  it('XTerm wires the guarded handler into the terminal options', () => {
    render(<XTerm cwd="/tmp" fontSize={12} />);
    const handler = captured.options?.linkHandler;
    expect(handler).toBeTruthy();
    handler?.activate(new MouseEvent('click'), 'javascript:alert(1)');
    expect(open).not.toHaveBeenCalled();
    handler?.activate(new MouseEvent('click'), 'https://example.test/');
    expect(open).toHaveBeenCalledWith('https://example.test/', '_blank', 'noopener,noreferrer');
  });
});
