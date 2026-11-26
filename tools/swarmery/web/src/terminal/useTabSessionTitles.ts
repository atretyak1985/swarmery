// The daemon's view of each Claude dock tab's session: its effective title
// (a dashboard rename included) and whether the daemon has ingested it yet.
// One GET /api/sessions/{uuid} per new session id seeds the entry; after that
// session_started / session_updated frames on the shared live socket keep it
// current, so a rename shows in the tab at once.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { fetchSession, isPendingSession } from '../api';
import type { WSMessage } from '../api/types';
import { useLiveUpdates } from '../lib/ws';

/** What the daemon knows about one tab's session. */
export interface TabSessionTitle {
  /** The session's effective title, or null while it has none. */
  title: string | null;
  /** The daemon has ingested the session: the tab must `--resume` it. */
  known: boolean;
}

/** The fields of a dock tab this hook reads. */
interface SessionTab {
  id: string;
  sessionId?: string | undefined;
}

const UNKNOWN: TabSessionTitle = { title: null, known: false };

/** `GET …: 404` — the shape api.ts's get() throws for a 404. */
function isNotFound(err: unknown): boolean {
  return err instanceof Error && /: 404$/.test(err.message);
}

/** Maps tab id → its session's title and ingest state (tabs without a session id are absent). */
export function useTabSessionTitles(tabs: readonly SessionTab[]): Record<string, TabSessionTitle> {
  // Keyed by session uuid: a frame names the session, not the tab.
  const [bySession, setBySession] = useState<Record<string, TabSessionTitle>>({});
  const requested = useRef<Set<string>>(new Set());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const sessionIds = useMemo(
    () => tabs.map((t) => t.sessionId).filter((id): id is string => id !== undefined && id !== ''),
    [tabs],
  );

  // A live frame or an earlier answer wins over a fetch that resolves later.
  const seed = useCallback((uuid: string, entry: TabSessionTitle): void => {
    if (!mounted.current) return;
    setBySession((prev) => (prev[uuid] !== undefined ? prev : { ...prev, [uuid]: entry }));
  }, []);

  // Runs on every tab-list change; `requested` makes it one fetch per uuid.
  useEffect(() => {
    for (const uuid of sessionIds) {
      if (requested.current.has(uuid)) continue;
      requested.current.add(uuid);
      fetchSession(uuid).then(
        (r) => seed(uuid, isPendingSession(r) ? UNKNOWN : { title: r.title, known: true }),
        (err: unknown) => {
          if (isNotFound(err)) seed(uuid, UNKNOWN);
          // Any other failure (daemon down, network): keep what we have.
        },
      );
    }
  }, [sessionIds, seed]);

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type !== 'session_started' && msg.type !== 'session_updated') return;
      const uuid = msg.payload.sessionUuid;
      if (uuid === undefined || uuid === '' || !sessionIds.includes(uuid)) return;
      const entry: TabSessionTitle = { title: msg.payload.title ?? null, known: true };
      setBySession((prev) => {
        const cur = prev[uuid];
        return cur !== undefined && cur.known && cur.title === entry.title ? prev : { ...prev, [uuid]: entry };
      });
    },
    [sessionIds],
  );
  useLiveUpdates(onMessage, noop);

  return useMemo(() => {
    const out: Record<string, TabSessionTitle> = {};
    for (const tab of tabs) {
      if (tab.sessionId === undefined || tab.sessionId === '') continue;
      out[tab.id] = bySession[tab.sessionId] ?? UNKNOWN;
    }
    return out;
  }, [tabs, bySession]);
}

/** A reconnect needs no refetch: the next frame for the session brings its title. */
function noop(): void {}
