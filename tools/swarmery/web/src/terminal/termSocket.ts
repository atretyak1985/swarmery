// PTY WebSocket helper (fusion phase 15). The terminal stream is a DEDICATED
// socket — separate infrastructure from the frozen event bus in lib/ws.ts — so
// it has its own tiny URL builder rather than sharing that shared connection.
// One socket per open terminal tab (not multiplexed): the daemon caps live PTYs.

/** ws(s)://<host>/api/term/ws?cwd=<abs path> for the given working directory. */
export function termWsUrl(cwd: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/term/ws?cwd=${encodeURIComponent(cwd)}`;
}

/** A resize control frame — the only text-frame message the PTY protocol has. */
export function resizeFrame(cols: number, rows: number): string {
  return JSON.stringify({ resize: { cols, rows } });
}
