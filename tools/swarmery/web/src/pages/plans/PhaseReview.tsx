// A plan phase's Review tab: what the run committed on its branch, whether a
// verifier confirmed it, where the branch is on its way to the code host, and
// the three exits — Push, Push + open <change request>, Return to agent.
//
// Every label that names the host's objects comes from `terms` (the project's
// GET /vcs answer, else the terms the review response itself carries): this
// file never branches on which provider a project uses (SC-11).
//
// Failures are shown the way the board card's Land shows them
// (workspace/task/TaskActions.tsx): a 422 is the server's `error` + `hint`
// (the exact commands to finish by hand) + redacted `detail`, verbatim in a
// <pre>; a 409 is a refusal with a known code, mapped to one inline sentence.

import { useCallback, useEffect, useState } from 'react';
import { getPhaseReview, LandError, landPhase } from '../../api';
import type {
  Epic,
  EpicPhase,
  PhaseLanding,
  PhaseLandRequest,
  PhaseReview as PhaseReviewData,
  ProviderTerms,
} from '../../api/types';
import { DiffView } from '../../components/DiffView';
import { ConfirmDialog, Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';
import { canLand, canReturn, landingLabel, prLinkText } from './landingModel';
import { VerifyVerdictChip } from './VerifyVerdictChip';

// The same button primitives as TaskActions.tsx.
const BTN = 'rounded-lg px-3 py-1.5 text-[12px] transition-colors disabled:opacity-40';
const PRIMARY = `${BTN} border border-brand/50 bg-brand/10 font-semibold text-brand hover:bg-brand/20 disabled:cursor-not-allowed`;
const PLAIN = `${BTN} border border-line bg-surface text-ink-2 hover:bg-surface2 disabled:cursor-not-allowed`;
const ERROR_BOX = 'rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red';

type LandAction = PhaseLandRequest['action'];

export interface PhaseReviewProps {
  epic: Epic;
  phase: EpicPhase;
  /** The project's code-host vocabulary; null until loaded (the review
   * response's own `terms` stand in). */
  terms: ProviderTerms | null;
  /** A land/return succeeded — the page refetches so the phase list follows. */
  onLanded?: (() => void) | undefined;
}

/** The inline sentence for a 409 refusal, or null for a code without one. */
export function conflictSentence(err: LandError, terms: ProviderTerms | null): string | null {
  const change = terms?.change ?? 'change request';
  switch (err.code) {
    case 'phase-running':
      return 'The phase is still running — land it once the run has finished.';
    case 'no-run-branch':
      return 'This phase has no run branch yet — run it first; there is nothing to push.';
    case 'push-to-base-refused':
      return `Refused: the run branch ${err.branch ?? ''} is the base branch${
        err.base !== undefined && err.base !== '' ? ` (${err.base})` : ''
      }, so landing would push straight onto it. Set swarmery.vcs.allowPushToBase=true in .claude/settings.local.json to allow it.`;
    case 'fork-workflow-unsupported':
      return `This project sets vcs.forkRemote, and landing through a fork is not supported yet — remove it to land to origin, or push and open the ${change} by hand.`;
    default:
      return null;
  }
}

/** One land/return failure, rendered by kind (also the deps-unmerged refusal's
 *  "Open <change>" failures — DepsUnmergedActions.tsx). */
export function LandFailure({ err, terms }: { err: unknown; terms: ProviderTerms | null }): JSX.Element {
  if (err instanceof LandError && err.status === 409) {
    return (
      <div role="alert" className={ERROR_BOX}>
        {conflictSentence(err, terms) ?? err.message}
      </div>
    );
  }
  if (err instanceof LandError && err.status === 422) {
    const text = [err.message, err.hint, err.detail].filter((s) => s !== '').join('\n\n');
    return (
      <pre role="alert" className={ERROR_BOX}>
        {text}
      </pre>
    );
  }
  return (
    <div role="alert" className={ERROR_BOX}>
      {err instanceof Error ? err.message : String(err)}
    </div>
  );
}

/** The mono meta line: state pill · change-request link · when · last error. */
function LandingStrip({ landing, terms }: { landing: PhaseLanding; terms: ProviderTerms }): JSX.Element {
  const link = prLinkText(landing, terms);
  const tone =
    landing.state === 'merged' || landing.state === 'pr_open'
      ? 'border-green/40 bg-green/10 text-green'
      : landing.state === 'ready' || landing.state === 'pushed'
        ? 'border-brand/40 bg-brand/10 text-brand'
        : landing.state === 'returned'
          ? 'border-amber/40 bg-amber/10 text-amber'
          : 'border-line text-ink-faint';
  return (
    <div className="flex flex-wrap items-center gap-2 font-mono text-[10.5px] text-ink-faint">
      <span className={`rounded border px-1.5 py-px text-[9.5px] ${tone}`}>{landingLabel(landing.state, terms)}</span>
      {link !== null && landing.prUrl !== null && (
        <a href={landing.prUrl} target="_blank" rel="noreferrer" className="text-brand hover:underline">
          {link}
        </a>
      )}
      {landing.landedAt !== null && <span>landed {fmtAgo(landing.landedAt)}</span>}
    </div>
  );
}

export function PhaseReview({ epic, phase, terms, onLanded }: PhaseReviewProps): JSX.Element {
  const [review, setReview] = useState<PhaseReviewData | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  // The landing a successful action returned — newer than both the review and
  // the phase row until the page refetch lands.
  const [landed, setLanded] = useState<PhaseLanding | null>(null);
  const [inFlight, setInFlight] = useState<LandAction | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const [draft, setDraft] = useState(false);
  const [returnOpen, setReturnOpen] = useState(false);
  const [feedback, setFeedback] = useState('');

  const landingKey = phase.landing?.state ?? 'none';
  const load = useCallback((): (() => void) => {
    let live = true;
    setLoading(true);
    setLoadError(null);
    getPhaseReview(epic.taskId, phase.id)
      .then((r) => {
        if (!live) return;
        setReview(r);
        setLanded(null);
      })
      .catch((e: unknown) => {
        if (live) setLoadError(e);
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [epic.taskId, phase.id]);

  // Re-read on a different phase, and whenever the run or the landing moved
  // (a finished run has new commits; a land elsewhere changed the strip).
  useEffect(() => load(), [load, phase.runState, landingKey]);

  // A different phase starts clean: no stale error, draft flag or feedback.
  useEffect(() => {
    setReview(null);
    setActionError(null);
    setDraft(false);
    setReturnOpen(false);
    setFeedback('');
  }, [phase.id]);

  const t: ProviderTerms | null = terms ?? review?.terms ?? null;
  const landing: PhaseLanding | null = landed ?? review?.landing ?? phase.landing ?? null;
  const gate = { runState: phase.runState, runSessionUuid: phase.runSessionUuid, landing };
  const running = phase.runState === 'running';
  const noBranch = loadError instanceof LandError && loadError.code === 'no-run-branch';
  const landable = !noBranch && canLand(gate);
  const returnable = !noBranch && canReturn(gate);
  const busy = inFlight !== null;

  const act = (body: PhaseLandRequest): void => {
    setInFlight(body.action);
    setActionError(null);
    landPhase(epic.taskId, phase.id, body)
      .then((res) => {
        setLanded(res.landing);
        setReturnOpen(false);
        setFeedback('');
        onLanded?.();
      })
      .catch((e: unknown) => setActionError(e))
      .finally(() => setInFlight(null));
  };

  const landTitle = running
    ? 'the run is still live — land it once it finishes'
    : !landable
      ? 'nothing to land in this state'
      : undefined;
  const verdict = review?.verifyVerdict ?? phase.verifyVerdict;
  const verdictDetail = review?.verifyDetail ?? phase.verifyDetail;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {landing !== null && t !== null && <LandingStrip landing={landing} terms={t} />}
        <VerifyVerdictChip verdict={verdict} detail={verdictDetail} />
      </div>
      {(landing?.error ?? '') !== '' && (
        <div className="font-mono text-[10.5px] break-words text-amber">last landing failure: {landing?.error}</div>
      )}

      <div className="rounded-lg border border-line bg-surface/40 px-2.5 py-2">
        {loading && review === null ? (
          <Loading label="review…" />
        ) : review !== null ? (
          <DiffView diff={review} />
        ) : loadError instanceof LandError && loadError.status === 409 ? (
          <div className="font-mono text-[10.5px] text-ink-faint">
            {conflictSentence(loadError, t) ?? loadError.message}
          </div>
        ) : loadError !== null ? (
          <LandFailure err={loadError} terms={t} />
        ) : null}
      </div>

      {t !== null && !noBranch && (
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            className={PRIMARY}
            disabled={busy || running || !landable}
            aria-busy={inFlight === 'push'}
            title={landTitle ?? 'push the run branch to origin'}
            onClick={() => act({ action: 'push' })}
          >
            {inFlight === 'push' ? 'Pushing…' : 'Push'}
          </button>
          <button
            type="button"
            className={PRIMARY}
            disabled={busy || running || !landable}
            aria-busy={inFlight === 'pr'}
            title={landTitle ?? `push the run branch and open a ${t.change}`}
            onClick={() => act({ action: 'pr', draft })}
          >
            {inFlight === 'pr' ? 'Opening…' : `Push + open ${t.change}`}
          </button>
          <label className="flex items-center gap-1.5 font-mono text-[11px] text-ink-2">
            <input
              type="checkbox"
              checked={draft}
              disabled={busy || running || !landable}
              onChange={(e) => setDraft(e.target.checked)}
            />
            Draft
          </label>
          <button
            type="button"
            className={`${PLAIN} ml-auto`}
            disabled={busy || running || !returnable}
            aria-busy={inFlight === 'return'}
            title={running ? 'the run is still live' : 'send the phase back to its agent with your feedback'}
            onClick={() => {
              setActionError(null);
              setReturnOpen(true);
            }}
          >
            Return to agent…
          </button>
        </div>
      )}

      {actionError !== null && !returnOpen && <LandFailure err={actionError} terms={t} />}

      <ConfirmDialog
        open={returnOpen}
        title="Return to agent?"
        confirmLabel="return"
        busy={inFlight === 'return'}
        confirmDisabled={feedback.trim() === ''}
        onConfirm={() => act({ action: 'return', feedback: feedback.trim() })}
        onCancel={() => setReturnOpen(false)}
      >
        Sends the phase back to its agent: your feedback is appended to the next run's prompt.
        <textarea
          value={feedback}
          onChange={(e) => setFeedback(e.target.value)}
          rows={4}
          aria-label="feedback for the agent"
          placeholder="what to fix on the next pass"
          className="mt-2 w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
        />
        {actionError !== null && (
          <div className="mt-2.5">
            <LandFailure err={actionError} terms={t} />
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}
