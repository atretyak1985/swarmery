// The way out of a `deps-unmerged` run refusal (Phase 5, SC-13). The daemon
// refused to start a phase because the run branches it depends on diverged and
// were never merged; the fix is a merge on the code host. For each named branch
// that is one of THIS plan's phase-run branches, offer `Open <changeShort>`:
// it pushes that phase's branch and opens a change request
// (POST …/land {action:'pr'}), then shows the link — or the refusal, with the
// manual-command hint a 422 carries. A branch that maps to no phase of the plan
// stays a name only: there is nothing here that could land it.
//
// Every label comes from `terms` (SC-11).

import { useState } from 'react';
import { landPhase } from '../../api';
import type { EpicPhase, ProviderTerms } from '../../api/types';
import { branchToPhaseId, prLinkText } from './landingModel';
import { LandFailure } from './PhaseReview';

export interface DepsUnmergedActionsProps {
  taskId: number;
  /** The plan's phases — a branch is actionable only when it names one of them. */
  phases: readonly Pick<EpicPhase, 'id' | 'seq'>[];
  /** The refusal's `branches`. */
  branches: readonly string[];
  terms: ProviderTerms;
  /** A change request was opened — the page refetches so the phase follows. */
  onLanded?: (() => void) | undefined;
}

type BranchResult =
  | { kind: 'busy' }
  | { kind: 'opened'; url: string | null; text: string }
  | { kind: 'failed'; err: unknown };

const BTN =
  'shrink-0 rounded-md border border-red/40 px-2 py-0.5 font-mono text-[10.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50';

export function DepsUnmergedActions({
  taskId,
  phases,
  branches,
  terms,
  onLanded,
}: DepsUnmergedActionsProps): JSX.Element | null {
  const [results, setResults] = useState<Readonly<Record<string, BranchResult>>>({});

  const rows = branches.flatMap((branch) => {
    const phaseId = branchToPhaseId(branch);
    const phase = phaseId === null ? undefined : phases.find((p) => p.id === phaseId);
    return phase === undefined ? [] : [{ branch, phase }];
  });
  if (rows.length === 0) return null;

  const open = (branch: string, phaseId: number): void => {
    setResults((r) => ({ ...r, [branch]: { kind: 'busy' } }));
    landPhase(taskId, phaseId, { action: 'pr' })
      .then((res) => {
        const text = prLinkText(res.landing, terms) ?? `${terms.changeShort} opened`;
        setResults((r) => ({ ...r, [branch]: { kind: 'opened', url: res.landing.prUrl, text } }));
        onLanded?.();
      })
      .catch((err: unknown) => {
        setResults((r) => ({ ...r, [branch]: { kind: 'failed', err } }));
      });
  };

  return (
    <ul aria-label={`Open a ${terms.change} for each unmerged branch`} className="mt-1.5 space-y-1.5">
      {rows.map(({ branch, phase }) => {
        const result = results[branch];
        const busy = result?.kind === 'busy';
        return (
          <li key={branch} className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <code className="text-ink-2">{branch}</code>
              <span className="text-ink-faint">phase {phase.seq}</span>
              {result?.kind === 'opened' ? (
                result.url !== null && result.url !== '' ? (
                  <a href={result.url} target="_blank" rel="noreferrer" className="text-brand hover:underline">
                    {result.text}
                  </a>
                ) : (
                  <span className="text-green">{result.text}</span>
                )
              ) : (
                <button
                  type="button"
                  className={BTN}
                  disabled={busy}
                  aria-busy={busy}
                  aria-label={`Open ${terms.changeShort} for ${branch}`}
                  onClick={() => open(branch, phase.id)}
                >
                  Open {terms.changeShort}
                </button>
              )}
            </div>
            {result?.kind === 'failed' && <LandFailure err={result.err} terms={terms} />}
          </li>
        );
      })}
    </ul>
  );
}
