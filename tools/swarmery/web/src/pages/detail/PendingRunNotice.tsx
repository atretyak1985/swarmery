import type { JSX } from 'react';
import type { PendingSession } from '../../api/types';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';

/** Human name of the run kind that minted the uuid (server `source`). */
export function pendingSourceLabel(source: PendingSession['source']): string {
  switch (source) {
    case 'phase':
      return 'phase run';
    case 'plan':
      return 'plan run';
    case 'dispatch':
      return 'dispatched task';
    case 'verify':
      return 'verification run';
    case 'planning':
      return 'planning session';
    case 'revision':
      return 'plan revision';
    default:
      return 'run';
  }
}

/**
 * What the session detail page shows for a uuid the daemon minted whose
 * transcript is not ingested yet (GET → 202). Two honest states:
 *  - the run is still going: a spinner that says what it is waiting for (the
 *    page re-polls and jumps to the real detail on the session_started frame);
 *  - the run already ended and no transcript ever landed: an error naming the
 *    run, with retry — nothing to wait for any more.
 */
export function PendingRunNotice({
  run,
  onRetry,
}: {
  run: PendingSession;
  onRetry: () => void;
}): JSX.Element {
  const kind = pendingSourceLabel(run.source);
  const started = run.startedAt !== null ? `started ${fmtAgo(run.startedAt)}` : null;
  if (run.running) {
    return (
      <div data-testid="pending-run" className="py-6">
        <Loading label={`${kind} is starting — waiting for its transcript…`} />
        <p className="text-center text-[12px] text-ink-dim">
          <span className="text-ink">{run.label}</span>
          {started !== null && <span className="ml-2 font-mono text-[11px] text-ink-faint">{started}</span>}
        </p>
      </div>
    );
  }
  return (
    <ErrorBox
      message={`the ${kind} “${run.label}” ended without writing a transcript${
        started !== null ? ` (${started})` : ''
      } — the session never started or its transcript is gone`}
      onRetry={onRetry}
    />
  );
}
