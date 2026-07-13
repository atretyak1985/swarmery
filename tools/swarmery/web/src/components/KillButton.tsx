import { useState, type MouseEvent } from 'react';
import { killSession } from '../api';
import type { Session } from '../api/types';
import { fmtCost } from '../lib/format';

const KILLABLE = new Set(['running', 'orphaned']);

export type KillSlotKind = 'killable' | 'exited' | 'none';

/**
 * Decide what the action slot next to a session should show, purely from
 * session fields (no state, no rendering) — kept separate from the component
 * so the decision table is unit-testable without a DOM/test-renderer.
 *
 * - 'none': no known PID, or no proc_state yet reported — nothing to render.
 * - 'exited': PID known but the process already exited (`procState: 'dead'`).
 *   There is nothing left to signal (and re-signaling a recycled PID would be
 *   unsafe), but the row must still say *why* there is no action rather than
 *   silently dropping the slot and looking stuck with no controls at all.
 * - 'killable': process is confirmed alive (`running`/`orphaned`) — render
 *   the interactive Kill button.
 */
export function killSlotKind(session: Session): KillSlotKind {
  if (!session.procPid || !session.procState) return 'none';
  if (KILLABLE.has(session.procState)) return 'killable';
  if (session.procState === 'dead') return 'exited';
  return 'none';
}

/**
 * Kill button for live sessions with a known PID.
 * Flow: Kill → confirmation dialog → SIGTERM sent → if still alive after 10 s
 * the button becomes "Force kill" → SIGKILL.
 * Sessions from remote machines (procPid null) never show a button.
 *
 * Terminal proc states (e.g. `dead`) render a disabled "exited" tag instead of
 * disappearing outright — see `killSlotKind`.
 */
export function KillButton({ session }: { session: Session }): JSX.Element | null {
  const [confirming, setConfirming] = useState(false);
  const [killing, setKilling] = useState(false);
  const [forceReady, setForceReady] = useState(false);

  const kind = killSlotKind(session);
  if (kind === 'none') return null;

  if (kind === 'exited') {
    return (
      <span
        className="rounded border border-ink-dim/20 px-2 py-0.5 font-mono text-[10.5px] font-medium text-ink-dim"
        title="Process already exited — nothing to kill"
      >
        exited
      </span>
    );
  }

  const doKill = async (force: boolean) => {
    setKilling(true);
    setConfirming(false);
    try {
      await killSession(session.id, force);
      if (!force) {
        // If still alive after 10 s (procState stays running via WS), offer force kill
        setTimeout(() => setForceReady(true), 10_000);
      }
    } catch (err) {
      console.error('kill failed', err);
    } finally {
      setKilling(false);
    }
  };

  // Prevent the enclosing <Link> from navigating when Kill is clicked.
  // stopImmediatePropagation blocks native DOM handlers; stopPropagation
  // blocks React's synthetic bubbling — both are needed inside a <Link>.
  const stop = (e: MouseEvent): void => {
    e.stopPropagation();
    e.nativeEvent.stopImmediatePropagation();
  };

  if (forceReady && session.procState && KILLABLE.has(session.procState)) {
    return (
      <button
        type="button"
        disabled={killing}
        onClick={(e) => { stop(e); void doKill(true); }}
        className="rounded border border-red-500/50 bg-red-500/10 px-2 py-0.5 font-mono text-[10.5px] font-medium text-red-500 transition-colors hover:bg-red-500/20 disabled:opacity-50"
      >
        {killing ? 'killing…' : 'Force kill'}
      </button>
    );
  }

  if (confirming) {
    const costLine = session.costUsd != null ? ` · ${fmtCost(session.costUsd)} so far` : '';
    const label = session.gitBranch ?? session.sessionUuid.slice(0, 8);
    return (
      <span className="flex items-center gap-1.5" onClick={stop}>
        <span className="font-mono text-[10.5px] text-ink-dim">
          Kill {label}{costLine}?
        </span>
        <button
          type="button"
          disabled={killing}
          onClick={(e) => { stop(e); void doKill(false); }}
          className="rounded border border-red-500/50 bg-red-500/10 px-2 py-0.5 font-mono text-[10.5px] font-medium text-red-500 transition-colors hover:bg-red-500/20 disabled:opacity-50"
        >
          {killing ? 'killing…' : 'Confirm'}
        </button>
        <button
          type="button"
          onClick={(e) => { stop(e); setConfirming(false); }}
          className="font-mono text-[10.5px] text-ink-dim hover:text-ink"
        >
          Cancel
        </button>
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={(e) => { stop(e); setConfirming(true); }}
      className="rounded border border-ink-dim/30 px-2 py-0.5 font-mono text-[10.5px] font-medium text-ink-dim transition-colors hover:border-red-500/40 hover:text-red-500"
    >
      Kill
    </button>
  );
}
