// @xterm/xterm wrapper (fusion phase 15). Attaches an xterm instance to a PTY
// WebSocket: binary frames from the daemon are raw terminal output; the user's
// keystrokes (onData) go back as binary frames; a fit-driven resize sends the
// {resize:{cols,rows}} control frame. On unmount the socket is closed, which
// makes the daemon SIGHUP the PTY's process group — no orphan shells.
//
// This module statically imports xterm; it is loaded through React.lazy from the
// dock, so the heavy xterm bundle lands in its own chunk fetched only when a
// terminal is first opened.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useImperativeHandle, useRef, forwardRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { reportSocketRefused, wsProtocols } from '../lib/dashboardSession';
import { parseTermHello, resizeFrame, termWsUrlFor } from './termSocket';
import { classifyTermClose, TERM_CLOSE_MESSAGE, type TermCloseKind } from './closeReason';

export type TermStatus = 'connecting' | 'open' | 'closed';

/** Imperative handle the dock uses for toolbar actions (Clear, refit, type a command). */
export interface XTermHandle {
  clear: () => void;
  fit: () => void;
  /** Types `text` into the live PTY as keystrokes; a no-op while it is not open. */
  send: (text: string) => void;
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

/** Open a terminal hyperlink only when it is an http(s) URL; anything else is a no-op. */
export function openWebLink(uri: string): void {
  if (/^https?:\/\//i.test(uri)) window.open(uri, '_blank', 'noopener,noreferrer');
}

export const XTerm = forwardRef<
  XTermHandle,
  {
    /** Working directory for a project/worktree PTY. Ignored when `ptyId` or `account` is set. */
    cwd?: string;
    /** Account key for an ACCOUNT-scoped PTY (the connect flow's login fallback). Ignored when `ptyId` is set. */
    account?: string;
    /**
     * A session id to reattach to (phase 5) instead of starting a fresh PTY —
     * set once this tab's `onHello` fires, and persisted on the tab so a page
     * reload or a remount reattaches rather than opening a new shell. Wins
     * over `cwd`/`account` (see {@link termWsUrlFor}).
     */
    ptyId?: string | undefined;
    /** A command typed into the shell (followed by Enter) once the PTY connects. */
    initialInput?: string | undefined;
    fontSize: number;
    /** `closed` comes with why the socket closed (closeReason.ts). */
    onStatus?: (s: TermStatus, closed?: TermCloseKind) => void;
    /**
     * The hello frame's session id — the FIRST frame on every successful
     * upgrade, new session or reattach alike (internal/api/term.go). The dock
     * stores it on the tab so a later reload/remount reattaches instead of
     * starting a new PTY.
     */
    onHello?: (id: string) => void;
  }
>(
  function XTerm({ cwd, account, ptyId, initialInput, fontSize, onStatus, onHello }, ref): JSX.Element {
    const { t } = useLingui();
    // Read at close time, so the line follows the current locale.
    const tRef = useRef(t);
    tRef.current = t;
    const hostRef = useRef<HTMLDivElement>(null);
    const termRef = useRef<Terminal | null>(null);
    const fitRef = useRef<FitAddon | null>(null);
    const wsRef = useRef<WebSocket | null>(null);
    // onStatus may change identity each render; call the latest without
    // re-running the mount effect (which would recreate the PTY).
    const statusRef = useRef(onStatus);
    statusRef.current = onStatus;
    // Read once at connect time; a later prop change must not recreate the PTY.
    const initialInputRef = useRef(initialInput);
    initialInputRef.current = initialInput;
    const onHelloRef = useRef(onHello);
    onHelloRef.current = onHello;

    useImperativeHandle(ref, () => ({
      clear: () => termRef.current?.clear(),
      fit: () => fitRef.current?.fit(),
      send: (text: string) => {
        const ws = wsRef.current;
        if (ws !== null && ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(text));
      },
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
        // OSC 8 hyperlinks come from whatever runs in the PTY, so their target is
        // untrusted: only an ordinary web link opens, in a new tab with no handle
        // back to this window. Every other scheme is ignored rather than handed
        // to xterm's default (a confirm() and then window.open of anything).
        linkHandler: { activate: (_event, uri) => openWebLink(uri) },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(host);
      fit.fit();
      termRef.current = term;
      fitRef.current = fit;

      statusRef.current?.('connecting');
      // The session rides as a subprotocol: the daemon refuses a terminal
      // upgrade without one. ptyId is read once here, at connect time, same as
      // cwd/account below: a hello frame sets it on the tab AFTER this socket
      // is already open (see onmessage), and that must never tear this one
      // down and reconnect — only the NEXT mount (a reload, a remount) reattaches.
      const ws = new WebSocket(termWsUrlFor({ ptyId, cwd, account }), wsProtocols());
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;
      let opened = false;

      const sendResize = (): void => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(resizeFrame(term.cols, term.rows));
        }
      };

      ws.onopen = () => {
        opened = true;
        // Read before reporting `open`: the dock disarms a one-shot command on
        // that status, so it must already be captured here.
        const run = initialInputRef.current;
        statusRef.current?.('open');
        sendResize(); // tell the PTY our initial window size
        // The PTY buffers the bytes until the shell finishes its rc files and
        // reads stdin, so the command runs as if typed at the first prompt.
        if (run !== undefined && run !== '') {
          ws.send(new TextEncoder().encode(`${run}\r`));
        }
        term.focus();
      };
      ws.onmessage = (ev: MessageEvent<ArrayBuffer | string>) => {
        if (typeof ev.data === 'string') {
          // The hello frame is the one text frame the protocol defines (every
          // other text message would be malformed); everything else is PTY
          // output and goes straight to the terminal, binary or not.
          const hello = parseTermHello(ev.data);
          if (hello !== null) {
            onHelloRef.current?.(hello.id);
            return;
          }
          term.write(ev.data);
        } else {
          term.write(new Uint8Array(ev.data));
        }
      };
      ws.onclose = (ev: CloseEvent) => {
        const kind = classifyTermClose(opened, ev.code, ev.reason);
        if (kind === 'refused') reportSocketRefused();
        statusRef.current?.('closed', kind);
        term.write(`\r\n\x1b[2m[${tRef.current(TERM_CLOSE_MESSAGE[kind])}]\x1b[0m\r\n`);
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
      // cwd/account identify the PTY; ptyId is read once above (see the comment
      // at the WebSocket construction) and deliberately excluded here — it
      // changes value, via a hello frame, on an ALREADY-open connection, and
      // must never be the trigger for tearing that connection down. fontSize
      // is applied live via the effect below.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [cwd, account]);

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
