// The run half of a triage trigger: the active run, the result of the run that
// ended during this visit, and `start`. Shared by the Inbox (through useTriage)
// and Health → Friction, which start runs over different kinds.
//
// Polling: only while a run is active, every 2 s (no WS frame carries run
// progress). When the run stops being active it is fetched once into `lastRun`
// and `onRunEnd` fires once so the host can refetch what the run changed.

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  TriageBusyError,
  fetchActiveTriageRun,
  fetchTriageRun,
  startTriageRun,
  type StartTriageOptions,
  type TriageRun,
} from '../../api/triage';

export const TRIAGE_POLL_MS = 2000;

export interface TriageRunState {
  /** The active run, or null. */
  run: TriageRun | null;
  /** The run that ended last during this visit. */
  lastRun: TriageRun | null;
  start: (opts?: StartTriageOptions) => Promise<void>;
  startError: string | null;
}

/** `enabled: false` makes the hook inert: no requests, no run. */
export function useTriageRun(scope: string | null, onRunEnd?: () => void, enabled = true): TriageRunState {
  const [run, setRun] = useState<TriageRun | null>(null);
  const [lastRun, setLastRun] = useState<TriageRun | null>(null);
  const [startError, setStartError] = useState<string | null>(null);

  const onRunEndRef = useRef(onRunEnd);
  onRunEndRef.current = onRunEnd;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

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
  }, [runId]);

  const start = useCallback(
    async (opts?: StartTriageOptions): Promise<void> => {
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
      onRunEndRef.current?.();
    },
    [scope],
  );

  return { run, lastRun, start, startError };
}
