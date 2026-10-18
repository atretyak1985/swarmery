// "Verify again" (phase-reverify plan, D8): grade a finished phase's run branch
// again with the read-only verifier — no executor runs. ONE hook owns the request
// and its in-flight state, and ONE builder turns a phase into the button's
// label/disabled/tip, so the phase row, the detail panel (Story card and Runs
// tab) and the run-diagnosis modal cannot drift apart about when the action is
// offered or how it reads.
//
// Eligibility is the server's answer (`phase.canReverify`, from
// phaserun.ReverifyEligible) and never re-derived here: a `fail` phase reads
// `done` in the status pill yet IS re-gradable, and a status-based gate would
// hide the button exactly there.

import type { I18n } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { reverifyPhase } from '../../api/phasereport';
import type { EpicPhase } from '../../api/types';
import type { PhaseCardAction } from './PhaseCard';

/** How long a phase stays busy after a 202 before the page trusts the server's
 *  `verifyRunning` alone. Exported for tests. */
export const ACCEPT_GRACE_MS = 2000;

export interface ReverifyHandlers {
  /** The request is about to go out — clear a stale error for this plan. */
  onStart?: (taskId: number) => void;
  /** 202: the verifier started; refetch so `verifyRunning` shows. */
  onAccepted: () => void;
  /** The server refused (or the request failed): surface its message. */
  onRefused: (taskId: number, e: unknown) => void;
}

export interface Reverify {
  /** Phase ids whose POST is in flight. */
  busy: ReadonlySet<number>;
  start: (taskId: number, phaseId: number) => void;
}

/** The request and its in-flight state. Lives in the page so every site that
 *  offers the action shares it. */
export function useReverify(handlers: ReverifyHandlers): Reverify {
  const [busy, setBusy] = useState<ReadonlySet<number>>(() => new Set());
  // The handlers are read through a ref at call time, never captured at press
  // time: the second refetch fires ACCEPT_GRACE_MS later, and by then the page
  // may have switched project — a stale `reload` would point the page's fetch
  // guard back at the old project and leave the new one loading.
  const handlersRef = useRef(handlers);
  useEffect(() => {
    handlersRef.current = handlers;
  });
  const start = useCallback((taskId: number, phaseId: number): void => {
    const clear = (): void =>
      setBusy((prev) => {
        const next = new Set(prev);
        next.delete(phaseId);
        return next;
      });
    setBusy((prev) => new Set(prev).add(phaseId));
    handlersRef.current.onStart?.(taskId);
    reverifyPhase(taskId, phaseId).then(
      () => {
        // Accepted. The server writes its `running` verification row from its
        // own goroutine, so the first refetch can still read verifyRunning=false.
        // Stay busy through a grace period and refetch once more, so the button
        // does not flash back to enabled (a second press would only earn a 409
        // already-running) before the server state catches up.
        // Timer first, so a throwing refetch cannot leave the phase busy.
        setTimeout(() => {
          clear();
          handlersRef.current.onAccepted();
        }, ACCEPT_GRACE_MS);
        handlersRef.current.onAccepted();
      },
      (e: unknown) => {
        clear();
        handlersRef.current.onRefused(taskId, e);
      },
    );
  }, []);
  return { busy, start };
}

const LABEL = msg`Verify again`;
const BUSY_LABEL = msg`verifying…`;
const TIP = msg`grade this phase's run branch again with the read-only verifier — no executor runs`;

/** The **Verify again** action for one phase, or null when the server says the
 *  phase is not re-gradable. Busy (label `verifying…`, disabled) while this
 *  page's request is in flight or the server reports a verification running. */
export function reverifyAction(
  phase: Pick<EpicPhase, 'id' | 'canReverify' | 'verifyRunning'>,
  reverify: Pick<Reverify, 'busy'>,
  onReverify: (phaseId: number) => void,
  i18n: I18n,
): PhaseCardAction | null {
  if (phase.canReverify !== true) return null;
  const busy = reverify.busy.has(phase.id) || phase.verifyRunning === true;
  return {
    label: i18n._(busy ? BUSY_LABEL : LABEL),
    onClick: () => onReverify(phase.id),
    disabled: busy,
    tip: i18n._(TIP),
  };
}
