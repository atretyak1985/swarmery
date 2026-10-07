// The Inbox's triage state: the agent's open suggestions, what it closed in the
// last 7 days, the active run and the audit of its samples.
//
// Polling: only while a run is active, every 2 s (no WS frame carries run
// progress). When the run stops being active it is fetched once into `lastRun`,
// the lists reload and `onRunEnd` fires once so the Inbox can refetch its items.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  TriageBusyError,
  fetchActiveTriageRun,
  fetchTriageAudit,
  fetchTriageRun,
  fetchTriageVerdicts,
  startTriageRun,
  type StartTriageOptions,
  type TriageAudit,
  type TriageRun,
  type TriageVerdict,
} from '../../api/triage';

export const TRIAGE_POLL_MS = 2000;
const HANDLED_WINDOW_MS = 7 * 24 * 3600 * 1000;
const HANDLED_LIMIT = 200;

export interface TriageState {
  /** Suggestions and audit samples waiting for the operator. */
  open: TriageVerdict[];
  /** Decisions the agent applied in the last 7 days (undoable). */
  handled: TriageVerdict[];
  /** The active run, or null. */
  run: TriageRun | null;
  /** The run that ended last during this visit. */
  lastRun: TriageRun | null;
  audit: TriageAudit | null;
  reload: () => void;
  start: (opts?: StartTriageOptions) => Promise<void>;
  startError: string | null;
}

/**
 * `enabled: false` makes the hook inert (no requests, empty state): the badge
 * and the Today page read the Inbox items without paying for triage calls.
 */
export function useTriage(scope: string | null, onRunEnd?: () => void, enabled = true): TriageState {
  const [open, setOpen] = useState<TriageVerdict[]>([]);
  const [applied, setApplied] = useState<TriageVerdict[]>([]);
  const [run, setRun] = useState<TriageRun | null>(null);
  const [lastRun, setLastRun] = useState<TriageRun | null>(null);
  const [audit, setAudit] = useState<TriageAudit | null>(null);
  const [startError, setStartError] = useState<string | null>(null);

  const onRunEndRef = useRef(onRunEnd);
  onRunEndRef.current = onRunEnd;
  const generation = useRef(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const reload = useCallback((): void => {
    if (!enabled) return;
    generation.current += 1;
    const mine = generation.current;
    void Promise.allSettled([
      fetchTriageVerdicts(['suggested', 'sample'], scope),
      fetchTriageVerdicts(['applied'], scope, HANDLED_LIMIT),
      fetchTriageAudit(),
    ]).then(([o, a, au]) => {
      if (!mounted.current || mine !== generation.current) return;
      if (o.status === 'fulfilled') setOpen(o.value);
      if (a.status === 'fulfilled') setApplied(a.value);
      if (au.status === 'fulfilled') setAudit(au.value);
    });
  }, [scope, enabled]);

  useEffect(() => {
    reload();
  }, [reload]);

  // Pick up a run already in flight (started elsewhere, or before this page opened).
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    fetchActiveTriageRun().then(
      (r) => {
        if (!cancelled) setRun(r);
      },
      () => undefined,
    );
    return () => {
      cancelled = true;
    };
  }, [enabled]);

  const runId = run?.id ?? null;
  useEffect(() => {
    if (runId === null) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const tick = (): void => {
      timer = null;
      fetchActiveTriageRun().then(
        async (active) => {
          if (cancelled) return;
          if (active !== null) {
            setRun(active);
            timer = setTimeout(tick, TRIAGE_POLL_MS);
            return;
          }
          // The run is over: fetch it once for its final numbers, then refresh.
          // An unknown outcome is shown as no result, never as an older run's.
          const final = await fetchTriageRun(runId).catch(() => null);
          if (cancelled) return;
          setLastRun(final);
          setRun(null);
          reload();
          onRunEndRef.current?.();
        },
        () => {
          if (!cancelled) timer = setTimeout(tick, TRIAGE_POLL_MS);
        },
      );
    };
    timer = setTimeout(tick, TRIAGE_POLL_MS);
    return () => {
      cancelled = true;
      if (timer !== null) clearTimeout(timer);
    };
  }, [runId, reload]);

  const start = useCallback(async (opts?: StartTriageOptions): Promise<void> => {
    // A new attempt forgets the previous run's result and the previous error.
    setStartError(null);
    setLastRun(null);
    let startedId: number | null = null;
    try {
      startedId = (await startTriageRun(scope, opts)).id;
    } catch (e) {
      // A run already in flight is not the operator's problem: just follow it.
      if (!(e instanceof TriageBusyError)) {
        if (mounted.current) setStartError(e instanceof Error ? e.message : String(e));
        return;
      }
      startedId = e.activeRunId;
    }
    const active = await fetchActiveTriageRun().catch(() => null);
    if (!mounted.current) return;
    if (active !== null || startedId === null) {
      setRun(active);
      return;
    }
    // No active run was reported: either a fast run that ended before the first
    // poll, or the "active" request failed. The run itself tells which.
    const final = await fetchTriageRun(startedId).catch(() => null);
    if (!mounted.current) return;
    if (final !== null && final.status === 'running') {
      setRun(final);
      return;
    }
    // Over (or unknown): report it the way the poller would.
    setLastRun(final);
    reload();
    onRunEndRef.current?.();
  }, [scope, reload]);

  const handled = useMemo(() => {
    const since = Date.now() - HANDLED_WINDOW_MS;
    return applied.filter((v) => Date.parse(v.createdAt) >= since);
  }, [applied]);

  return { open, handled, run, lastRun, audit, reload, start, startError };
}
