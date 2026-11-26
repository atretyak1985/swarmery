// PTY WebSocket helper (fusion phase 15). The terminal stream is a DEDICATED
// socket — separate infrastructure from the frozen event bus in lib/ws.ts — so
// it has its own tiny URL builder rather than sharing that shared connection.
// One socket per open terminal tab (not multiplexed): the daemon caps live PTYs.

/** ws(s)://<host>/api/term/ws?cwd=<abs path> for the given working directory. */
export function termWsUrl(cwd: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/term/ws?cwd=${encodeURIComponent(cwd)}`;
}

/**
 * ws(s)://<host>/api/term/ws?account=<key> — an ACCOUNT-scoped terminal: the
 * PTY runs under that account's CLAUDE_CONFIG_DIR in the operator's home. The
 * connect flow's interactive login fallback; mutually exclusive with ?cwd.
 */
export function termWsUrlForAccount(account: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/term/ws?account=${encodeURIComponent(account)}`;
}

/**
 * ws(s)://<host>/api/term/ws?attach=<id> — reattach to a session that is
 * still alive on the daemon (phase 4: a dropped socket detaches rather than
 * kills the PTY, for term.Config.DetachGrace). The first frame on the
 * resulting socket is still the hello frame (see {@link parseTermHello}),
 * followed by one binary frame replaying whatever accumulated while nobody
 * was listening, then live output.
 */
export function termWsUrlForAttach(id: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/term/ws?attach=${encodeURIComponent(id)}`;
}

/**
 * Picks the ws(s) URL for a tab's PTY: attach when it already has a `ptyId`
 * (a page reload, or taking a background/elsewhere session over), else a
 * fresh cwd or account PTY. `ptyId` wins over `cwd`/`account` — a tab that
 * once connected always reattaches to that same session, never starts a new
 * one silently.
 */
export function termWsUrlFor(target: {
  ptyId?: string | undefined;
  cwd?: string | undefined;
  account?: string | undefined;
}): string {
  if (target.ptyId !== undefined && target.ptyId !== '') return termWsUrlForAttach(target.ptyId);
  if (target.account !== undefined) return termWsUrlForAccount(target.account);
  return termWsUrl(target.cwd ?? '');
}

/** A resize control frame — the only text-frame message the PTY protocol has. */
export function resizeFrame(cols: number, rows: number): string {
  return JSON.stringify({ resize: { cols, rows } });
}

/** A hello frame: the mandatory first text frame on every successful upgrade
 *  (new session or reattach alike), naming the session id to reattach to. */
export interface TermHello {
  type: 'hello';
  id: string;
}

/** Parses a hello frame's text payload, or returns null when it isn't one. */
export function parseTermHello(text: string): TermHello | null {
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed === 'object' && parsed.type === 'hello' && typeof parsed.id === 'string') {
      return parsed as TermHello;
    }
  } catch {
    // not JSON — not a hello frame
  }
  return null;
}
