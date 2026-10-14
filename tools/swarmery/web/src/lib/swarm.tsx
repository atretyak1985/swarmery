// Swarm data hooks (mods-swarmery-link phase 6): the live message feed and
// the live file claims, plus the conflict badge session cards wear.
//
// Everything is "fetch, then refetch on a hint": the daemon's swarm_message /
// swarm_claim WS frames carry ids only (docs/ws-protocol.md), and a socket
// reopen or the bus's periodic convergence tick refetches too, so a lost frame
// costs at most a minute.

import { useLingui } from '@lingui/react/macro';
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { fetchSwarmClaims, fetchSwarmMessages } from '../api';
import type { SwarmClaims, SwarmConflict, SwarmMessage, WSMessage } from '../api/types';
import { useLiveUpdates } from './ws';

export interface SwarmResource<T> {
  /** null until the first answer (loading) or after a failed first fetch. */
  data: T | null;
  error: string | null;
  reload: () => void;
}

/** Fetches `fetcher` on mount and again whenever `matches` accepts a WS frame. */
function useSwarmResource<T>(
  fetcher: () => Promise<T>,
  matches: (msg: WSMessage) => boolean,
  key: unknown,
): SwarmResource<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);

  // `key` (the scope) is the only thing that changes what `fetcher` returns.
  const reload = useCallback((): void => {
    fetcher()
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  useEffect(() => {
    setData(null);
    setError(null);
    reload();
  }, [reload]);

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (matches(msg)) reload();
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [reload, key],
  );
  useLiveUpdates(onMessage, reload);
  return { data, error, reload };
}

/** The swarm's messages for a project (undefined = every project), newest first. */
export function useSwarmMessages(projectId?: number): SwarmResource<SwarmMessage[]> {
  return useSwarmResource(
    () => fetchSwarmMessages(projectId),
    (m) => m.type === 'swarm_message' && (projectId === undefined || m.payload.projectId === projectId),
    projectId ?? 'all',
  );
}

/** The live file claims for a project (undefined = every project). */
export function useSwarmClaims(projectId?: number): SwarmResource<SwarmClaims> {
  return useSwarmResource(
    () => fetchSwarmClaims(projectId),
    (m) => m.type === 'swarm_claim' && (projectId === undefined || m.payload.projectId === projectId),
    projectId ?? 'all',
  );
}

/** sessions.id -> the conflicted paths that session holds. */
export function conflictsBySession(conflicts: readonly SwarmConflict[]): ReadonlyMap<number, readonly string[]> {
  const out = new Map<number, string[]>();
  for (const c of conflicts) {
    for (const s of c.sessions) out.set(s.sessionId, [...(out.get(s.sessionId) ?? []), c.path]);
  }
  return out;
}

const EMPTY: ReadonlyMap<number, readonly string[]> = new Map();
const ConflictContext = createContext<ReadonlyMap<number, readonly string[]>>(EMPTY);

/**
 * Fetches the claims ONCE for a list view and hands the conflict map to every
 * card below it (refetched on swarm_claim). No `projectId` = every project.
 * A failed fetch hides the badge rather than the list: it is advisory.
 */
export function SwarmConflictProvider({
  projectId,
  children,
}: {
  projectId?: number | undefined;
  children: ReactNode;
}): JSX.Element {
  const { data } = useSwarmClaims(projectId);
  const map = useMemo(() => (data === null ? EMPTY : conflictsBySession(data.conflicts)), [data]);
  return <ConflictContext.Provider value={map}>{children}</ConflictContext.Provider>;
}

/** The conflicted paths one session holds; empty outside a provider. */
export function useSessionConflicts(sessionId: number): readonly string[] {
  return useContext(ConflictContext).get(sessionId) ?? [];
}

/** "conflict" badge for a session that holds a path another session holds too. */
export function ConflictBadge({ sessionId }: { sessionId: number }): JSX.Element | null {
  const { t } = useLingui();
  const paths = useSessionConflicts(sessionId);
  if (paths.length === 0) return null;
  const list = paths.join(', ');
  const tip = t`Another session holds a claim on the same file: ${list}`;
  return (
    <span
      role="img"
      aria-label={tip}
      data-tip={tip}
      data-testid="swarm-conflict"
      className="shrink-0 rounded-full border border-amber/40 bg-amber/10 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-amber"
    >
      {t`conflict`}
    </span>
  );
}
