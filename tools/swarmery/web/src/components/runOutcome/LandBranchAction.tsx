// The keep-the-work action on a run-outcome blocker card: push the branch the
// blocker names and open a change request for it, so the work reaches base
// through the code host's review and CI instead of a hand-typed local merge.
//
// Two routes, chosen by the guide (blockerGuide.land):
//   • `phase`  — the branch is a phase's own run branch (this phase's leftover,
//                or a dependency's unmerged one): POST …/phases/{id}/land
//                {action:'pr'}, the same call DepsUnmergedActions makes, so the
//                phase's landing lifecycle follows it;
//   • `orphan` — the branch's id matches no phase row: POST …/orphan-branch/land,
//                which stores nothing; the change request's link is the record.
//
// A refusal renders through LandFailure: a 409's sentence, or a 422's error +
// the exact manual commands it carries.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { landOrphanBranch, landPhase } from '../../api';
import type { ProviderTerms } from '../../api/types';
import { safeHref } from '../../lib/safeHref';
import { branchToPhaseId, prLinkText } from '../../pages/plans/landingModel';
import { LandFailure } from '../../pages/plans/PhaseReview';

type State =
  | { kind: 'idle' }
  | { kind: 'busy' }
  | { kind: 'opened'; url: string; text: string }
  | { kind: 'failed'; err: unknown };

export interface LandBranchActionProps {
  taskId: number;
  branch: string;
  mode: 'phase' | 'orphan';
  terms: ProviderTerms;
  disabled: boolean;
  disabledReason: string;
  /** A change request was opened — the caller refetches the diagnosis/page. */
  onLanded?: (() => void) | undefined;
}

export function LandBranchAction({
  taskId,
  branch,
  mode,
  terms,
  disabled,
  disabledReason,
  onLanded,
}: LandBranchActionProps): JSX.Element | null {
  const { t } = useLingui();
  const [state, setState] = useState<State>({ kind: 'idle' });
  const phaseId = mode === 'phase' ? branchToPhaseId(branch) : null;
  // A phase-mode branch that does not parse back to a phase id has no route that
  // could land it — offer nothing rather than a button that can only 4xx.
  if (branch === '' || (mode === 'phase' && phaseId === null)) return null;

  const changeShort = terms.changeShort;
  const change = terms.change.toLowerCase();

  const open = (): void => {
    setState({ kind: 'busy' });
    const req =
      phaseId !== null
        ? landPhase(taskId, phaseId, { action: 'pr' }).then((res) => ({
            url: res.landing.prUrl ?? '',
            text: prLinkText(res.landing, terms) ?? t`${changeShort} opened`,
          }))
        : landOrphanBranch(taskId, branch).then((res) => ({
            url: res.url,
            text: res.number !== null ? `${changeShort} #${String(res.number)}` : t`${changeShort} opened`,
          }));
    req
      .then((opened) => {
        setState({ kind: 'opened', ...opened });
        onLanded?.();
      })
      .catch((err: unknown) => setState({ kind: 'failed', err }));
  };

  if (state.kind === 'opened') {
    const href = safeHref(state.url);
    return (
      <span className="font-mono text-[11px] text-green">
        <Trans>Pushed, {change} opened:</Trans>{' '}
        {href !== null ? (
          <a href={href} target="_blank" rel="noreferrer" className="text-brand hover:underline">
            {state.text}
          </a>
        ) : (
          state.text
        )}
      </span>
    );
  }

  const busy = state.kind === 'busy';
  return (
    <span className="flex flex-col gap-1.5">
      <button
        type="button"
        disabled={busy || disabled}
        aria-busy={busy}
        data-tip={disabled ? disabledReason : t`push ${branch} and open a ${change} for it — CI and review run as usual`}
        onClick={open}
        className="self-start rounded-lg border border-brand/40 bg-brand/5 px-3 py-1 font-mono text-[11px] text-brand transition-colors hover:bg-brand/10 disabled:cursor-not-allowed disabled:opacity-50"
      >
        {busy ? t`pushing…` : t`Open ${changeShort}`}
      </button>
      {state.kind === 'failed' && <LandFailure err={state.err} terms={terms} />}
    </span>
  );
}
