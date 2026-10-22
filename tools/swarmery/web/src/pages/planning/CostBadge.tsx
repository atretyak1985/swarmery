// "Cheap / expensive to change" badge for one interview question (decision
// ids, migration 0122), with a toggle that PATCHes the stored turn. The cost
// decides what a SKIPPED answer becomes at PROCEED: cheap is taken at the
// recommended option, expensive stays an undecided D-n that holds every phase
// depending on it — so the operator gets the last word on it.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { setPlanningTurnCost } from '../../api';
import type { PlanningCost, PlanningQuestion } from '../../api/types';

export function CostBadge({
  planningSessionId,
  seq,
  cost,
  reason,
  onChanged,
}: {
  planningSessionId: number;
  /** The planning turn (and so the D-n) this question is. */
  seq: number;
  /** Missing on questions stored before the field existed — read as cheap. */
  cost: PlanningCost | undefined;
  reason?: string | undefined;
  onChanged?: (q: PlanningQuestion) => void;
}): JSX.Element {
  const { t } = useLingui();
  const [current, setCurrent] = useState<PlanningCost>(cost ?? 'cheap');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const expensive = current === 'expensive';
  const next: PlanningCost = expensive ? 'cheap' : 'expensive';
  const decisionId = `D-${String(seq)}`;

  const toggle = (): void => {
    setBusy(true);
    setError(null);
    setPlanningTurnCost(planningSessionId, seq, next)
      .then((q) => {
        setCurrent(q.costToChange ?? next);
        onChanged?.(q);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div data-testid="cost-badge" className="flex flex-wrap items-center gap-2 text-[12px]">
      <span className="font-mono text-[10.5px] text-ink-faint">{decisionId}</span>
      <span
        data-testid="cost-badge-label"
        className={`rounded border px-1.5 py-px font-mono text-[10.5px] ${
          expensive ? 'border-amber/40 bg-amber/10 text-amber' : 'border-line bg-bg text-ink-dim'
        }`}
      >
        {expensive ? <Trans>expensive to change</Trans> : <Trans>cheap to change</Trans>}
      </span>
      {reason !== undefined && reason !== '' && <span className="text-ink-3">{reason}</span>}
      <button
        type="button"
        onClick={toggle}
        disabled={busy}
        aria-label={expensive ? t`Mark ${decisionId} cheap to change` : t`Mark ${decisionId} expensive to change`}
        className="rounded border border-line px-2 py-px font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/50 hover:text-ink disabled:opacity-50"
      >
        {expensive ? <Trans>mark cheap</Trans> : <Trans>mark expensive</Trans>}
      </button>
      {error !== null && (
        <span role="alert" className="text-red">
          {error}
        </span>
      )}
    </div>
  );
}
