// Running-plan side panel (interactive planning v2 — phase 3): the plan the
// wizard rebuilds after every answer, plus the two interview controls —
// "Refine" (course-correct via RefineModal) and "Continue with the plan"
// (inline confirm → end the interview, write the plan). Both are disabled
// unless the wizard is awaiting an answer — everything else is mid-generation
// or terminal.

import { Trans } from '@lingui/react/macro';
import { useState } from 'react';
import type { PlanningStatus, PlanningSummary } from '../../api/types';

export function RunningPlanPanel({
  plan,
  status,
  busy,
  onRefine,
  onProceed,
}: {
  plan: PlanningSummary | null;
  status: PlanningStatus['status'];
  busy: boolean;
  onRefine: () => void;
  onProceed: () => void;
}): JSX.Element {
  // Inline proceed confirmation — the first click arms, the second commits.
  const [confirming, setConfirming] = useState(false);
  const actionable = status === 'awaiting_answer' && !busy;

  return (
    <div className="rounded-xl border border-line bg-surface px-4 py-4">
      <div className="mb-1 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
        <Trans>running plan</Trans>
      </div>

      {plan === null ? (
        <p className="text-[12.5px] leading-relaxed text-ink-dim">
          <Trans>The plan takes shape here as you answer — each reply rebuilds it.</Trans>
        </p>
      ) : (
        <>
          <div className="flex items-start justify-between gap-2">
            <div className="font-display text-[14px] font-semibold text-ink">{plan.title}</div>
            {plan.suggestedSize !== undefined && plan.suggestedSize !== '' && (
              <span className="shrink-0 rounded-full border border-line px-2 py-0.5 font-mono text-[10.5px] text-ink-dim">
                {plan.suggestedSize}
              </span>
            )}
          </div>
          <p className="mt-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
            {plan.description}
          </p>

          {plan.proposedChanges !== undefined && plan.proposedChanges.length > 0 && (
            <div className="mt-3">
              <div className="mb-1 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
                <Trans>proposed changes</Trans>
              </div>
              <ul className="space-y-1">
                {plan.proposedChanges.map((c) => (
                  <li key={c} className="flex gap-1.5 text-[12px] leading-relaxed text-ink-2">
                    <span aria-hidden="true" className="shrink-0 font-mono text-ink-faint">
                      ·
                    </span>
                    {c}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {plan.acceptanceCriteria !== undefined && plan.acceptanceCriteria.length > 0 && (
            <div className="mt-3">
              <div className="mb-1 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
                <Trans>acceptance criteria</Trans>
              </div>
              <ul className="space-y-1">
                {plan.acceptanceCriteria.map((c) => (
                  <li key={c} className="flex gap-1.5 text-[12px] leading-relaxed text-ink-2">
                    <span aria-hidden="true" className="shrink-0 font-mono text-green">
                      ✓
                    </span>
                    {c}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}

      <div className="mt-4 border-t border-line pt-3">
        {confirming ? (
          <div>
            <div className="text-[12px] leading-relaxed text-ink-2">
              <Trans>End the interview? The planner will write the full plan to the workspace.</Trans>
            </div>
            <div className="mt-2 flex flex-wrap gap-2">
              <button
                type="button"
                disabled={!actionable}
                onClick={() => {
                  setConfirming(false);
                  onProceed();
                }}
                className="rounded-lg border border-green/45 bg-green/12 px-3.5 py-1.5 text-[12.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
              >
                <Trans>Yes, write the plan</Trans>
              </button>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 text-[12.5px] text-ink-2 transition-colors hover:bg-surface2"
              >
                <Trans>Back</Trans>
              </button>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={!actionable}
              onClick={onRefine}
              className="rounded-lg border border-line bg-surface px-3.5 py-1.5 text-[12.5px] font-semibold text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
            >
              <Trans>Refine</Trans>
            </button>
            <button
              type="button"
              disabled={!actionable}
              onClick={() => setConfirming(true)}
              className="rounded-lg border border-green/45 bg-green/12 px-3.5 py-1.5 text-[12.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
            >
              <Trans>Continue with the plan</Trans>
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
