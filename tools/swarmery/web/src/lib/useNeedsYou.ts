// The "Needs you" queue's data: GET /api/needs-you for a scope (null = fleet),
// refetched over the shared WS stream (lib/ws.ts) on session_updated and the
// permission_* frames — debounced, since session_updated fires on every turn —
// plus the reconnect/reconcile resync. Same shape as pages/inbox/useInboxItems.
//
// Dismissals are per browser (localStorage, every access wrapped: storage can
// be disabled). A dismissal names one blocking episode (lib/needsYou dismissKey),
// so the same session blocking again re-surfaces. Every mounted instance — the
// page and the sidebar badge — re-reads the set on DISMISS_EVENT, so the badge
// drops the moment a row is dismissed.

import { useCallback, useEffect, useRef, useState } from 'react';
import { getNeedsYou } from '../api';
import type { NeedsYouItem, WSMessage } from '../api/types';
import { dismissKey, oldestFirst, visibleItems } from './needsYou';
import { useLiveUpdates } from './ws';

const STORAGE_KEY = 'swarmery.needsYou.dismissed';
const DISMISS_EVENT = 'swarmery:needs-you-dismissed';
/** Old dismissals are dropped past this many — episodes do not come back. */
const DISMISS_CAP = 200;
const REFETCH_DEBOUNCE_MS = 400;

const REFETCH_ON: ReadonlySet<WSMessage['type']> = new Set([
  'session_updated',
  'permission_requested',
  'permission_resolved',
]);

function loadDismissed(): string[] {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw === null) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((k): k is string => typeof k === 'string') : [];
  } catch {
    return [];
  }
}

function saveDismissed(keys: readonly string[]): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(keys.slice(-DISMISS_CAP)));
  } catch {
    // storage disabled — the dismissal holds until the next read only
  }
}

export interface NeedsYouState {
  /** Undismissed items, oldest blocker first. */
  items: NeedsYouItem[];
  count: number;
  loading: boolean;
  error: string | null;
  reload: () => void;
  dismiss: (item: NeedsYouItem) => void;
}

export function useNeedsYou(scope: string | null): NeedsYouState {
  const [all, setAll] = useState<NeedsYouItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(() => new Set(loadDismissed()));
  // Drops a slow response that lands after a newer load (or a scope switch).
  const generation = useRef(0);

  const reload = useCallback((): void => {
    generation.current += 1;
    const mine = generation.current;
    getNeedsYou(scope)
      .then((r) => {
        if (mine !== generation.current) return;
        setAll(r.items);
        setError(null);
      })
      .catch((e: unknown) => {
        if (mine !== generation.current) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (mine === generation.current) setLoading(false);
      });
  }, [scope]);

  useEffect(() => {
    setLoading(true);
    reload();
  }, [reload]);

  useEffect(() => {
    // Merge, never replace: with storage disabled loadDismissed() is empty and
    // must not wipe the dismissal this instance just applied in memory.
    const sync = (): void => setDismissed((prev) => new Set([...prev, ...loadDismissed()]));
    window.addEventListener(DISMISS_EVENT, sync);
    return () => window.removeEventListener(DISMISS_EVENT, sync);
  }, []);

  const dismiss = useCallback((item: NeedsYouItem): void => {
    const key = dismissKey(item);
    const keys = loadDismissed().filter((k) => k !== key);
    saveDismissed([...keys, key]);
    // Applied locally as well, so a dismissal holds even with storage disabled.
    setDismissed((prev) => new Set([...prev, key]));
    window.dispatchEvent(new Event(DISMISS_EVENT));
  }, []);

  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Cleared on unmount AND whenever reload changes (a scope switch): a pending
  // debounce still holds the old reload, which would fire last and win the
  // generation guard with the previous project's items.
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = null;
    },
    [reload],
  );
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (!REFETCH_ON.has(msg.type)) return;
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = setTimeout(reload, REFETCH_DEBOUNCE_MS);
    },
    [reload],
  );
  useLiveUpdates(onMessage, reload);

  const items = oldestFirst(visibleItems(all, dismissed));
  return { items, count: items.length, loading, error, reload, dismiss };
}
