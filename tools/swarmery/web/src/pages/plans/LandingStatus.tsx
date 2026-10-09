// A landed phase's change-request status (phase-landing plan, phase 7, SC-13):
// state, CI and review chips from the daemon's last read (`landing.prStatus`),
// when that read was, and a Refresh button that asks the code host now.
//
// The status is the daemon's normalized vocabulary (repoprovider.ChangeStatus),
// identical for every provider, so nothing here branches on which provider a
// project uses (SC-11). Shown only once a change request exists — an open or a
// merged one; before that there is nothing to read.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { LandError, refreshPhaseLanding } from '../../api';
import type { PhaseChangeStatus, PhaseLanding, PhaseLandingRefreshErrorCode } from '../../api/types';
import { fmtAgo } from '../../lib/format';

const CHIP = 'inline-flex items-center gap-1 rounded border px-1.5 py-px font-mono text-[9.5px]';
const GOOD = 'border-green/40 bg-green/10 text-green';
const BAD = 'border-red/40 bg-red/10 text-red';
const WAIT = 'border-amber/40 bg-amber/10 text-amber';
const QUIET = 'border-line text-ink-faint';

type Chip = { glyph?: string; text: string; tone: string };

/** The state chip: an open change request marked draft reads "draft". */
export function stateChip(s: PhaseChangeStatus): Chip {
  if (s.state === 'merged') return { glyph: '✓', text: 'merged', tone: GOOD };
  if (s.state === 'closed') return { text: 'closed', tone: BAD };
  return s.draft ? { text: 'draft', tone: QUIET } : { text: 'open', tone: GOOD };
}

/** The CI chip, or null when the change request has no checks. */
export function ciChip(ci: PhaseChangeStatus['ci']): Chip | null {
  switch (ci) {
    case 'success':
      return { glyph: '✓', text: 'CI passed', tone: GOOD };
    case 'failure':
      return { glyph: '✗', text: 'CI failed', tone: BAD };
    case 'pending':
      return { glyph: '◌', text: 'CI running', tone: WAIT };
    case 'none':
      return null;
  }
}

/** The review chip, or null when no review is asked for or given. */
export function reviewChip(review: PhaseChangeStatus['review']): Chip | null {
  switch (review) {
    case 'approved':
      return { glyph: '✓', text: 'approved', tone: GOOD };
    case 'changes_requested':
      return { glyph: '✗', text: 'changes requested', tone: BAD };
    case 'review_required':
      return { text: 'review required', tone: WAIT };
    case 'none':
      return null;
  }
}

function ChipView({ chip, testId }: { chip: Chip; testId: string }): JSX.Element {
  return (
    <span data-testid={testId} className={`${CHIP} ${chip.tone}`}>
      {chip.glyph !== undefined && <span aria-hidden="true">{chip.glyph}</span>}
      {chip.text}
    </span>
  );
}

/** The failure text of a refused refresh: the server's sentence, plus the
 *  manual hint a 422 carries. */
function refreshFailureText(err: unknown): string {
  if (err instanceof LandError) {
    const code = err.code as PhaseLandingRefreshErrorCode | undefined;
    if (code === 'no-change-request') return 'Nothing to refresh: this phase has no open change request.';
    return [err.message, err.hint].filter((s) => s !== '').join('\n');
  }
  return err instanceof Error ? err.message : String(err);
}

export interface LandingStatusProps {
  taskId: number;
  phaseId: number;
  landing: PhaseLanding;
  /** A refresh answered: the phase's landing after the read. */
  onRefreshed?: ((landing: PhaseLanding) => void) | undefined;
}

export function LandingStatus({ taskId, phaseId, landing, onRefreshed }: LandingStatusProps): JSX.Element | null {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // A refresh that answers after the panel moved to another phase is dropped.
  const shownPhase = useRef(phaseId);
  useLayoutEffect(() => {
    shownPhase.current = phaseId;
  }, [phaseId]);
  useEffect(() => {
    setBusy(false);
    setError(null);
  }, [phaseId]);

  if (landing.state !== 'pr_open' && landing.state !== 'merged') return null;
  const status = landing.prStatus;

  const refresh = (): void => {
    const forPhase = phaseId;
    const current = (): boolean => shownPhase.current === forPhase;
    setBusy(true);
    setError(null);
    refreshPhaseLanding(taskId, forPhase)
      .then((next) => {
        if (current()) onRefreshed?.(next);
      })
      .catch((e: unknown) => {
        if (current()) setError(e);
      })
      .finally(() => {
        if (current()) setBusy(false);
      });
  };

  const ci = status === null ? null : ciChip(status.ci);
  const review = status === null ? null : reviewChip(status.review);

  return (
    <div data-testid="landing-status" className="space-y-1">
      <div className="flex flex-wrap items-center gap-1.5 font-mono text-[10.5px] text-ink-faint">
        {status !== null && <ChipView chip={stateChip(status)} testId="landing-status-state" />}
        {ci !== null && <ChipView chip={ci} testId="landing-status-ci" />}
        {review !== null && <ChipView chip={review} testId="landing-status-review" />}
        <span data-testid="landing-status-checked">
          {status === null ? 'status not read yet' : `checked ${fmtAgo(status.checkedAt)}`}
        </span>
        <button
          type="button"
          onClick={refresh}
          disabled={busy}
          aria-busy={busy}
          title="read the change request's status from the code host now"
          className="rounded border border-line px-1.5 py-px text-[10px] text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {busy ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>
      {error !== null && (
        <pre role="alert" className="font-mono text-[10.5px] whitespace-pre-wrap break-words text-red">
          {refreshFailureText(error)}
        </pre>
      )}
    </div>
  );
}
