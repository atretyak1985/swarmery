import { Trans, useLingui } from '@lingui/react/macro';
import { useState, type MouseEvent } from 'react';
import { stopSession } from '../api';
import type { Session } from '../api/types';

/**
 * Graceful stop: SIGTERM (server escalates to SIGKILL after the grace period)
 * and the row is recorded as 'completed', not 'killed'. Unlike Kill it needs
 * no PID — a zombie row can always be closed out.
 */
export function StopButton({ session }: { session: Session }): JSX.Element {
  const { t } = useLingui();
  const [confirming, setConfirming] = useState(false);
  const [stopping, setStopping] = useState(false);

  const halt = (e: MouseEvent): void => { e.stopPropagation(); };

  const doStop = async (): Promise<void> => {
    setStopping(true);
    setConfirming(false);
    try {
      await stopSession(session.id);
    } catch (err) {
      console.error('stop failed', err);
    } finally {
      setStopping(false);
    }
  };

  if (confirming) {
    const label = session.gitBranch ?? session.sessionUuid.slice(0, 8);
    return (
      <span className="flex items-center gap-1.5" onClick={halt}>
        <span className="font-mono text-[10.5px] text-ink-dim">
          <Trans>Stop {label} and mark done?</Trans>
        </span>
        <button
          type="button"
          disabled={stopping}
          onClick={(e) => { halt(e); void doStop(); }}
          className="rounded border border-amber/50 bg-amber/10 px-2 py-0.5 font-mono text-[10.5px] font-medium text-amber transition-colors hover:bg-amber/20 disabled:opacity-50"
        >
          {stopping ? t`stopping…` : t`Confirm`}
        </button>
        <button
          type="button"
          onClick={(e) => { halt(e); setConfirming(false); }}
          className="font-mono text-[10.5px] text-ink-dim hover:text-ink"
        >
          <Trans>Cancel</Trans>
        </button>
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={(e) => { halt(e); setConfirming(true); }}
      className="rounded border border-ink-dim/30 px-2 py-0.5 font-mono text-[10.5px] font-medium text-ink-dim transition-colors hover:border-amber/40 hover:text-amber"
    >
      <Trans>Stop</Trans>
    </button>
  );
}
