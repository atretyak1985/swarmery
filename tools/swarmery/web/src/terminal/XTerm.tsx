// @xterm/xterm wrapper (fusion phase 15). Attaches an xterm instance to a PTY
// WebSocket: binary frames from the daemon are raw terminal output; the user's
// keystrokes (onData) go back as binary frames; a fit-driven resize sends the
// {resize:{cols,rows}} control frame. On unmount the socket is closed, which
// makes the daemon SIGHUP the PTY's process group — no orphan shells.
//
// This module statically imports xterm; it is loaded through React.lazy from the
// dock, so the heavy xterm bundle lands in its own chunk fetched only when a
// terminal is first opened.

import { useEffect, useImperativeHandle, useRef, forwardRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { resizeFrame, termWsUrl } from './termSocket';

export type TermStatus = 'connecting' | 'open' | 'closed';

/** Imperative handle the dock uses for toolbar actions (Clear, refit). */
export interface XTermHandle {
  clear: () => void;
  fit: () => void;
}

// Palette matched to the app's Tailwind tokens (see index.css :root) so the
// terminal reads as part of the surface, not a foreign white box.
const THEME = {
  background: '#0b0d10',
  foreground: '#d6dae0',
  cursor: '#7c9cff',
  selectionBackground: '#2a2f3a',
  black: '#0b0d10',
  brightBlack: '#5b6472',
} as const;

export const XTerm = forwardRef<XTermHandle, { cwd: string; fontSize: number; onStatus?: (s: TermStatus) => void }>(
  function XTerm({ cwd, fontSize, onStatus }, ref): JSX.Element {
    const hostRef = useRef<HTMLDivElement>(null);
    const termRef = useRef<Terminal | null>(null);
    const fitRef = useRef<FitAddon | null>(null);
    const wsRef = useRef<WebSocket | null>(null);
    // onStatus may change identity each render; call the latest without
    // re-running the mount effect (which would recreate the PTY).
    const statusRef = useRef(onStatus);
    statusRef.current = onStatus;

    useImperativeHandle(ref, () => ({
      clear: () => termRef.current?.clear(),
      fit: () => fitRef.current?.fit(),
    }));

    // Mount once per cwd: build the terminal, open the socket, wire both ways.
    useEffect(() => {
      const host = hostRef.current;
      if (host === null) return;

      const term = new Terminal({
        fontSize,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        theme: THEME,
        cursorBlink: true,
        scrollback: 5000,
        convertEol: false,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(host);
      fit.fit();
      termRef.current = term;
      fitRef.current = fit;

      statusRef.current?.('connecting');
      const ws = new WebSocket(termWsUrl(cwd));
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      const sendResize = (): void => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(resizeFrame(term.cols, term.rows));
        }
      };

      ws.onopen = () => {
        statusRef.current?.('open');
        sendResize(); // tell the PTY our initial window size
        term.focus();
      };
      ws.onmessage = (ev: MessageEvent<ArrayBuffer | string>) => {
        if (typeof ev.data === 'string') {
          term.write(ev.data);
        } else {
          term.write(new Uint8Array(ev.data));
        }
      };
      ws.onclose = () => {
        statusRef.current?.('closed');
        term.write('\r\n\x1b[2m[terminal session ended]\x1b[0m\r\n');
      };

      // Keystrokes → PTY as binary frames.
      const dataSub = term.onData((data: string) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(data));
        }
      });
      // Local fit → resize control frame.
      const resizeSub = term.onResize(() => sendResize());

      // Refit when the container changes size (dock drag, fullscreen toggle).
      const ro = new ResizeObserver(() => {
        try {
          fit.fit();
        } catch {
          /* host detached mid-teardown — ignore */
        }
      });
      ro.observe(host);

      return () => {
        ro.disconnect();
        dataSub.dispose();
        resizeSub.dispose();
        // Close first so the daemon reaps the PTY, then drop the xterm instance.
        ws.onclose = null;
        ws.close();
        term.dispose();
        termRef.current = null;
        fitRef.current = null;
        wsRef.current = null;
      };
      // cwd identifies the PTY; fontSize is applied live via the effect below.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [cwd]);

    // Apply a font-size change without recreating the PTY, then refit.
    useEffect(() => {
      const term = termRef.current;
      if (term === null) return;
      term.options.fontSize = fontSize;
      fitRef.current?.fit();
    }, [fontSize]);

    return <div ref={hostRef} className="h-full w-full" />;
  },
);
