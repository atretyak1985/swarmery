// The action under an open `[LAND]` criterion on a phase's Criteria tab: a way
// to the Review tab, Push + open <change> when the run branch is landable, the
// change request's link once one is open, or — for a phase the daemon never
// ran — why there is nothing to push. The rules are landingModel's
// (`landCriterionAction`, the same `canLand` the Review tab uses — plan D1),
// and the land call is the Review tab's own (`landPhase` with action "pr").
//
// It sits under the row's toggle and never replaces it: ticking the criterion
// by hand stays available in every state.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';
import { landPhase } from '../../api';
import type { Epic, EpicPhase, ProviderTerms } from '../../api/types';
import { safeHref } from '../../lib/safeHref';
import { criterionClassOf } from './CriterionLint';
import { hasReviewTab, landCriterionAction, NEUTRAL_TERMS } from './landingModel';
import { LandFailure } from './PhaseReview';

const BTN =
  'rounded border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand';

export interface LandCriterionActionProps {
  epic: Pick<Epic, 'taskId'>;
  phase: EpicPhase;
  /** The criterion's label (the text after `- [ ] `) and tick state. */
  text: string;
  done: boolean;
  /** The project's code-host vocabulary; null until loaded (neutral terms stand in). */
  terms: ProviderTerms | null;
  /** Switch the phase panel to its Review tab. */
  onOpenReview: () => void;
  /** A land succeeded — the page refetches so the phase (and this row) follow. */
  onDocChanged: () => void;
}

/** Renders nothing unless the criterion is an open `[LAND]` one. */
export function LandCriterionAction({
  epic,
  phase,
  text,
  done,
  terms,
  onOpenReview,
  onDocChanged,
}: LandCriterionActionProps): JSX.Element | null {
  const { t } = useLingui();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  // The phase this row currently shows: a land's outcome for another phase
  // (the panel moved on mid-request) is dropped, as PhaseReview drops it.
  const shownPhase = useRef(phase.id);
  useEffect(() => {
    shownPhase.current = phase.id;
    setBusy(false);
    setErr(null);
  }, [phase.id]);

  if (done || criterionClassOf(text) !== 'LAND') return null;

  const vocab = terms ?? NEUTRAL_TERMS;
  const action = landCriterionAction(phase, vocab);
  const reviewable = hasReviewTab(phase);
  // review-only with no Review tab (a branch stamped, but no run trace) has nothing to offer.
  if (action.kind === 'review-only' && !reviewable) return null;
  const changeShort = vocab.changeShort;
  const runBranch = phase.runBranch;

  const land = (): void => {
    const forPhase = phase.id;
    setBusy(true);
    setErr(null);
    landPhase(epic.taskId, forPhase, { action: 'pr', draft: false })
      .then(() => onDocChanged())
      .catch((e: unknown) => {
        if (shownPhase.current === forPhase) setErr(e);
      })
      .finally(() => {
        if (shownPhase.current === forPhase) setBusy(false);
      });
  };

  return (
    <div data-testid="land-criterion-action" className="mt-0.5 ml-6 space-y-1 font-mono text-[10px] text-ink-faint">
      <div className="flex flex-wrap items-center gap-1.5">
        {reviewable && (
          <button
            type="button"
            onClick={onOpenReview}
            data-tip={t`the run's diff, verdict and landing controls`}
            className={`${BTN} border-line text-ink-2 hover:bg-surface2`}
          >
            <Trans>Open in Review →</Trans>
          </button>
        )}
        {action.kind === 'land' && (
          <button
            type="button"
            disabled={busy}
            aria-busy={busy}
            onClick={land}
            data-tip={t`push the run branch ${runBranch} and open a ${changeShort}`}
            className={`${BTN} border-brand/40 text-brand hover:bg-brand/10`}
          >
            {busy ? t`Opening…` : t`Push + open ${changeShort}`}
          </button>
        )}
        {action.kind === 'pr-link' && (
          <a href={safeHref(action.href) ?? undefined} target="_blank" rel="noreferrer" className="text-brand hover:underline">
            {action.text}
          </a>
        )}
        {action.kind === 'no-run-branch' && (
          <span>
            <Trans>
              This phase was not run by the daemon — there is no branch to land. Tick the criterion by hand after the
              merge.
            </Trans>
          </span>
        )}
      </div>
      {err !== null && <LandFailure err={err} terms={terms} />}
    </div>
  );
}
