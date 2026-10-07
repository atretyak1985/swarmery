// The Inbox's triage state: the agent's open suggestions, what it closed in the
// last 7 days, the active run and the audit of its samples.
//
// The run itself (start, the 2 s poll, `lastRun`) lives in useTriageRun; when a
// run ends the lists reload and `onRunEnd` fires once so the Inbox can refetch
// its items.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  fetchTriageAudit,
  fetchTriageVerdicts,
  type StartTriageOptions,
  type TriageAudit,
  type TriageRun,
  type TriageVerdict,
} from '../../api/triage';
import { useTriageRun } from './useTriageRun';

export { TRIAGE_POLL_MS } from './useTriageRun';

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
  const [audit, setAudit] = useState<TriageAudit | null>(null);

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

  // Lists first, then the host: the Inbox refetches its items after the lists.
  const { run, lastRun, start, startError } = useTriageRun(
    scope,
    () => {
      reload();
      onRunEndRef.current?.();
    },
    enabled,
  );

  const handled = useMemo(() => {
    const since = Date.now() - HANDLED_WINDOW_MS;
    return applied.filter((v) => Date.parse(v.createdAt) >= since);
  }, [applied]);

  return { open, handled, run, lastRun, audit, reload, start, startError };
}
