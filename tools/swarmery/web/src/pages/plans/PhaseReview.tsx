// A plan phase's Review tab: what the run committed on its branch, whether a
// verifier confirmed it, where the branch is on its way to the code host, and
// the three exits — Push, Push + open <change request>, Return to agent. Read
// top to bottom (plan D19-D26, phase 5): a one-sentence verdict, a next-step
// card with at most one primary and one secondary action, the state as rows
// (ReviewStateLedger), then ReviewFindings (rendered by the caller, right
// after this component).
//
// Every label that names the host's objects comes from `terms` (the project's
// GET /vcs answer, else the terms the review response itself carries): this
// file never branches on which provider a project uses (SC-11).
//
// Failures from an action just taken (push/pr/return) still render inline,
// the way the board card's Land shows them (workspace/task/TaskActions.tsx): a
// 422 is the server's `error` + `hint` + redacted `detail`, verbatim in a
// <pre>; a 409 is a refusal with a known code, mapped to one inline sentence.
// The phase's STORED last-landing-failure (`landing.error`, which can be stale
// — from a run that has since changed) is not live feedback on anything the
// operator just clicked, so it moves into the "what git said" disclosure
// instead of sitting on the tab as a loose red line (D24/D25).

import { t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { getPhaseReview, LandError, landPhase, refreshPhaseLanding } from '../../api';
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
import { answerGuardDecision, phaseGuard } from '../../api/guard';
import { DecisionReplyModal } from './DecisionReplyModal';
import { isEmptyGuardDecision, isStopDecision } from './guardDecisionView';
import { canReturn, NEUTRAL_TERMS } from './landingModel';
import { reviewNextStep, type ReviewActionId } from './reviewNextStep';
import { ReviewStateLedger } from './ReviewStateLedger';
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
  const change = terms?.change ?? t`change request`;
  switch (err.code) {
    case 'phase-running':
      return t`The phase is still running — land it once the run has finished.`;
    case 'no-run-branch':
      return t`This phase has no run branch yet — run it first; there is nothing to push.`;
    case 'run-branch-gone': {
      const branch = err.branch ?? '';
      return err.settled
        ? t`The run branch ${branch} no longer exists, so there is nothing to push — the phase is counted as landed (its work reached the base another way).`
        : t`The run branch ${branch} no longer exists, so there is nothing to push. The phase is not finished, so it is not counted as landed — run it again for a new branch.`;
    }
    case 'push-to-base-refused': {
      const branch = err.branch ?? '';
      const base = err.base !== undefined && err.base !== '' ? ` (${err.base})` : '';
      return t`Refused: the run branch ${branch} is the base branch${base}, so landing would push straight onto it. Set swarmery.vcs.allowPushToBase=true in .claude/settings.local.json to allow it.`;
    }
    case 'fork-workflow-unsupported':
      return t`This project sets vcs.forkRemote, and landing through a fork is not supported yet — remove it to land to origin, or push and open the ${change} by hand.`;
    default:
      return null;
  }
}

/** The sentence for a "Find <change>" that came back empty (409
 *  `no-change-request` from the landing refresh), or null for any other
 *  failure — those render as a LandFailure. */
export function findMissSentence(err: unknown, terms: ProviderTerms | null): string | null {
  // The refresh's codes (PhaseLandingRefreshErrorCode) are not the land
  // action's, so the discriminator is compared as a plain string.
  if (!(err instanceof LandError) || err.status !== 409 || String(err.code) !== 'no-change-request') return null;
  const changeShort = terms?.changeShort ?? t`change request`;
  const branch = err.branch ?? '';
  return branch !== ''
    ? t`No open or merged ${changeShort} found for ${branch} — push and open one here, or open it on the host and look again.`
    : t`No open or merged ${changeShort} found for this phase's run branch — push and open one here, or open it on the host and look again.`;
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

/** The plan's header names a branch the run did not use. Land always pushes the
 *  run branch (the header's is a reference only), so this says so before Push. */
function BranchMismatchNote({ headerBranch, runBranch }: { headerBranch: string; runBranch: string }): JSX.Element | null {
  if (headerBranch === '' || headerBranch === runBranch) return null;
  return (
    <div role="note" className="rounded-lg border border-amber/40 bg-amber/5 px-2.5 py-2 text-[11.5px] text-amber">
      <Trans>
        The plan names the branch <code className="font-mono">{headerBranch}</code>, but the daemon will land{' '}
        <code className="font-mono">{runBranch}</code>. Push and the change request always use the run's branch; the
        header's branch is a reference only.
      </Trans>
    </div>
  );
}

const DOT_TONE = { success: 'bg-green', warn: 'bg-amber', danger: 'bg-red', neutral: 'bg-ink-faint' } as const;

/** D19: a one-sentence verdict — a tone dot, a bold lead clause and the rest,
 *  the same shape as a session header's one-line state. The phase's state is
 *  named exactly once on the tab; nothing below repeats it as a sentence. */
function VerdictLine({ tone, lead, rest }: { tone: keyof typeof DOT_TONE; lead: string; rest: string }): JSX.Element {
  return (
    <p className="flex items-start gap-2 text-[13.5px] leading-[1.5] text-ink-2">
      <span aria-hidden="true" className={`mt-[5px] h-2 w-2 shrink-0 rounded-full ${DOT_TONE[tone]}`} />
      <span>
        <strong className="font-semibold text-ink">{lead}</strong> {rest}
      </span>
    </p>
  );
}

/** D21: the next-step card — at most one primary (filled) and one secondary
 *  (outlined) action, with a reason under the title, and the unavailable
 *  actions listed under it with why. Renders a quiet note instead when there
 *  is nothing to do. */
function NextStepCard({
  primary,
  secondary,
  note,
  unavailable,
  busy,
  busyAction,
  onAction,
}: {
  primary: ReturnType<typeof reviewNextStep>['primary'];
  secondary: ReturnType<typeof reviewNextStep>['secondary'];
  note: string | null;
  unavailable: ReturnType<typeof reviewNextStep>['unavailable'];
  busy: boolean;
  /** The action currently in flight, if any — its button's label switches to
   *  the "…ing" word and carries aria-busy. */
  busyAction: ReviewActionId | null;
  onAction: (id: ReviewActionId) => void;
}): JSX.Element {
  const label = (a: NonNullable<typeof primary>): string => (busyAction === a.id ? BUSY_LABEL[a.id]() : a.label);
  return (
    <div className="rounded-lg border border-brand/40 bg-brand-bg px-3 py-2.5">
      <div className="font-mono text-[10px] tracking-[0.14em] text-brand uppercase">
        <Trans>next step</Trans>
      </div>
      {primary !== null ? (
        <>
          <div className="mt-1 text-[13.5px] font-semibold text-ink">{primary.label}</div>
          <p className="mt-0.5 text-[12px] leading-[1.45] text-ink-2">{primary.reason}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <button
              type="button"
              className={PRIMARY}
              disabled={busy}
              aria-busy={busyAction === primary.id}
              onClick={() => onAction(primary.id)}
            >
              {label(primary)}
            </button>
            {secondary !== null && (
              <button
                type="button"
                className={PLAIN}
                disabled={busy}
                aria-busy={busyAction === secondary.id}
                onClick={() => onAction(secondary.id)}
              >
                {label(secondary)}
              </button>
            )}
          </div>
        </>
      ) : (
        note !== null && <p className="mt-1 text-[12.5px] leading-[1.5] text-ink-dim">{note}</p>
      )}
      {unavailable.length > 0 && (
        <ul className="mt-2 space-y-0.5 border-t border-brand/20 pt-2 font-mono text-[10.5px] text-ink-faint">
          {unavailable.map((u) => (
            <li key={u.id}>
              {UNAVAIL_LABEL[u.id]()} — {u.reason}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const UNAVAIL_LABEL: Record<'push' | 'pr' | 'return', () => string> = {
  push: () => t`Push`,
  pr: () => t`Push + open`,
  return: () => t`Return to agent`,
};

const BUSY_LABEL: Record<ReviewActionId, () => string> = {
  push: () => t`Pushing…`,
  pr: () => t`Opening…`,
  return: () => t`Return to agent…`,
  'answer-decision': () => t`Answering…`,
  'close-guard': () => t`Closing…`,
};

/** D25: the raw landing failure the daemon has stored, tucked away so it
 *  stops being a loose red line on the tab — visible only once opened. */
function GitSaidDisclosure({ text }: { text: string }): JSX.Element | null {
  const { t } = useLingui();
  if (text === '') return null;
  return (
    <details className="rounded-lg border border-line bg-surface/40 px-2.5 py-1.5">
      <summary className="cursor-pointer font-mono text-[10.5px] text-ink-dim hover:text-ink">{t`what git said`}</summary>
      <pre className="mt-1.5 max-h-[220px] overflow-auto font-mono text-[10.5px] whitespace-pre-wrap break-words text-amber">
        {text}
      </pre>
    </details>
  );
}

export function PhaseReview({ epic, phase, terms, onLanded }: PhaseReviewProps): JSX.Element {
  const { t } = useLingui();
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
  // "Find <change>": a change request opened outside the dashboard for the
  // run branch, looked up through the landing refresh.
  const [finding, setFinding] = useState(false);
  const [findError, setFindError] = useState<unknown>(null);
  // D24/D25: the in-place reply to an open guard decision, and D26's one-button
  // close for a decision that would add nothing to scope.
  const [decisionOpen, setDecisionOpen] = useState(false);
  const [closingGuard, setClosingGuard] = useState(false);
  const [guardCloseError, setGuardCloseError] = useState<unknown>(null);

  // The phase this instance currently shows. The panel keeps the instance
  // (and the tab) on ↑/↓ step navigation, so a response that comes back after
  // the operator moved on belongs to ANOTHER phase and must be dropped.
  // A layout effect, so it is current before the passive effects below run.
  const shownPhase = useRef(phase.id);
  useLayoutEffect(() => {
    shownPhase.current = phase.id;
  }, [phase.id]);

  const landingKey = phase.landing?.state ?? 'none';
  const load = useCallback((): (() => void) => {
    let live = true;
    const forPhase = phase.id;
    const current = (): boolean => live && shownPhase.current === forPhase;
    setLoading(true);
    setLoadError(null);
    getPhaseReview(epic.taskId, forPhase)
      .then((r) => {
        if (!current()) return;
        setReview(r);
        setLanded(null);
      })
      .catch((e: unknown) => {
        if (current()) setLoadError(e);
      })
      .finally(() => {
        if (current()) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [epic.taskId, phase.id]);

  // Re-read on a different phase, and whenever the run or the landing moved
  // (a finished run has new commits; a land elsewhere changed the strip).
  useEffect(() => load(), [load, phase.runState, landingKey]);

  // A different phase starts clean: no stale review, landing, in-flight action,
  // error, draft flag or feedback.
  useEffect(() => {
    setReview(null);
    setLanded(null);
    setInFlight(null);
    setActionError(null);
    setDraft(false);
    setReturnOpen(false);
    setFeedback('');
    setFinding(false);
    setFindError(null);
    setDecisionOpen(false);
    setClosingGuard(false);
    setGuardCloseError(null);
  }, [phase.id]);

  const vocab: ProviderTerms | null = terms ?? review?.terms ?? null;
  const landing: PhaseLanding | null = landed ?? review?.landing ?? phase.landing ?? null;
  const running = phase.runState === 'running';
  const noBranch = loadError instanceof LandError && loadError.code === 'no-run-branch';
  const busy = inFlight !== null || finding || closingGuard;

  const find = (): void => {
    const forPhase = phase.id;
    const current = (): boolean => shownPhase.current === forPhase;
    setFinding(true);
    setFindError(null);
    setActionError(null);
    refreshPhaseLanding(epic.taskId, forPhase)
      .then((next) => {
        // Found and recorded: the page refetches so the phase list follows.
        onLanded?.();
        if (current()) setLanded(next);
      })
      .catch((e: unknown) => {
        if (current()) setFindError(e);
      })
      .finally(() => {
        if (current()) setFinding(false);
      });
  };

  const act = (body: PhaseLandRequest): void => {
    const forPhase = phase.id;
    // Still showing the phase this action was for? Otherwise its outcome is
    // dropped here — it is not the shown phase's landing, error or spinner.
    const current = (): boolean => shownPhase.current === forPhase;
    setInFlight(body.action);
    setActionError(null);
    setFindError(null);
    landPhase(epic.taskId, forPhase, body)
      .then((res) => {
        // The land did happen: the page still refetches so the phase list follows.
        onLanded?.();
        if (!current()) return;
        setLanded(res.landing);
        setReturnOpen(false);
        setFeedback('');
      })
      .catch((e: unknown) => {
        // A gone run branch settled as merged did change the phase: refetch so
        // the page shows it landed, and keep the sentence that says why.
        if (e instanceof LandError && e.settled) onLanded?.();
        if (current()) setActionError(e);
      })
      .finally(() => {
        if (current()) setInFlight(null);
      });
  };

  const closeEmptyGuard = (): void => {
    const decision = phase.guardDecision;
    if (decision == null) return;
    setClosingGuard(true);
    setGuardCloseError(null);
    answerGuardDecision(decision.taskId, decision.phaseId, decision.decisionId, 'reject', '')
      .then(() => onLanded?.())
      .catch((e: unknown) => setGuardCloseError(e))
      .finally(() => setClosingGuard(false));
  };

  const change = vocab?.change ?? '';
  // No (or an empty) short term: the same neutral word findMissSentence uses,
  // never a bare "Find ".
  const changeShort = vocab?.changeShort || t`change request`;
  const findMiss = findMissSentence(findError, vocab);
  const landingError = landing?.error ?? '';
  const verdict = review?.verifyVerdict ?? phase.verifyVerdict;
  const verdictDetail = review?.verifyDetail ?? phase.verifyDetail;
  const guardDecision = phase.guardDecision ?? null;
  const findable = !noBranch && guardDecision == null && (landing?.state === 'ready' || landing?.state === 'pushed');

  const gitErrored = actionError !== null || (loadError !== null && !noBranch);
  const next = reviewNextStep({
    phase: {
      runState: phase.runState,
      runSessionUuid: phase.runSessionUuid,
      landing,
      guardDecision,
      manualOpen: phase.manualOpen,
      landOpen: phase.landOpen,
      checkboxesDone: phase.checkboxesDone,
      checkboxesTotal: phase.checkboxesTotal,
    },
    gitErrored,
    branchGone: noBranch,
    terms: vocab ?? NEUTRAL_TERMS,
  });

  const returnable =
    !noBranch && guardDecision == null && canReturn({ runState: phase.runState, runSessionUuid: phase.runSessionUuid, landing });
  const showSmallReturn = returnable && next.primary?.id !== 'return';

  const runAction = (id: ReviewActionId): void => {
    switch (id) {
      case 'push':
        act({ action: 'push' });
        break;
      case 'pr':
        act({ action: 'pr', draft });
        break;
      case 'return':
        setActionError(null);
        setReturnOpen(true);
        break;
      case 'answer-decision':
        setDecisionOpen(true);
        break;
      case 'close-guard':
        closeEmptyGuard();
        break;
    }
  };

  const showsDraft = (next.primary?.id === 'pr' || next.secondary?.id === 'pr') && !noBranch;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <VerifyVerdictChip verdict={verdict} detail={verdictDetail} />
      </div>

      <VerdictLine tone={next.verdict.tone} lead={next.verdict.lead} rest={next.verdict.rest} />

      {review !== null && <BranchMismatchNote headerBranch={review.headerBranch} runBranch={review.branch} />}

      <NextStepCard
        primary={next.primary}
        secondary={next.secondary}
        note={next.note}
        unavailable={next.unavailable}
        busy={busy || running}
        busyAction={inFlight ?? (closingGuard ? 'close-guard' : null)}
        onAction={runAction}
      />

      {showsDraft && (
        <label className="flex items-center gap-1.5 font-mono text-[11px] text-ink-2">
          <input type="checkbox" checked={draft} disabled={busy || running} onChange={(e) => setDraft(e.target.checked)} />
          <Trans>Draft</Trans>
        </label>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {!running && findable && (
          <button
            type="button"
            className={PLAIN}
            disabled={busy}
            aria-busy={finding}
            title={t`look up a ${change} already opened for this phase's run branch`}
            onClick={find}
          >
            {finding ? t`Looking…` : t`Find ${changeShort}`}
          </button>
        )}
        {showSmallReturn && (
          <button
            type="button"
            className={PLAIN}
            disabled={busy}
            aria-busy={inFlight === 'return'}
            onClick={() => {
              setActionError(null);
              setReturnOpen(true);
            }}
          >
            <Trans>Return to agent…</Trans>
          </button>
        )}
      </div>

      {actionError !== null && !returnOpen && <LandFailure err={actionError} terms={vocab} />}
      {findMiss !== null ? (
        <div role="status" className="font-mono text-[10.5px] break-words text-amber">
          {findMiss}
        </div>
      ) : (
        findError !== null && <LandFailure err={findError} terms={vocab} />
      )}
      {guardCloseError !== null && <LandFailure err={guardCloseError} terms={vocab} />}

      <GitSaidDisclosure text={landingError} />

      <div className="rounded-lg border border-line bg-surface/40 px-2.5 py-2">
        {loading && review === null ? (
          <Loading label={t`review…`} />
        ) : review !== null ? (
          <DiffView diff={review} />
        ) : loadError instanceof LandError && loadError.status === 409 ? (
          <div className="font-mono text-[10.5px] text-ink-faint">
            {conflictSentence(loadError, vocab) ?? loadError.message}
          </div>
        ) : loadError !== null ? (
          <LandFailure err={loadError} terms={vocab} />
        ) : null}
      </div>

      {vocab !== null && (
        <ReviewStateLedger
          taskId={epic.taskId}
          phaseId={phase.id}
          runBranch={noBranch ? '' : phase.runBranch}
          landing={landing}
          terms={vocab}
          checkboxesDone={phase.checkboxesDone}
          checkboxesTotal={phase.checkboxesTotal}
          manualOpen={phase.manualOpen}
          guardDecision={guardDecision}
          guard={phaseGuard(phase)}
          onRefreshed={(nextLanding) => {
            const moved = nextLanding.state !== landing?.state;
            setLanded(nextLanding);
            // A merge moves the phase list too (its chip, its landed date).
            if (moved) onLanded?.();
          }}
          {...(guardDecision != null && !isStopDecision(guardDecision) && isEmptyGuardDecision(guardDecision)
            ? { onCloseEmptyGuard: closeEmptyGuard, closingGuard }
            : {})}
        />
      )}

      {guardDecision != null && (
        <DecisionReplyModal
          open={decisionOpen}
          decision={guardDecision}
          onAnswered={() => {
            setDecisionOpen(false);
            onLanded?.();
          }}
          onClose={() => setDecisionOpen(false)}
        />
      )}

      <ConfirmDialog
        open={returnOpen}
        title={t`Return to agent?`}
        confirmLabel={t`return`}
        busy={inFlight === 'return'}
        confirmDisabled={feedback.trim() === ''}
        onConfirm={() => act({ action: 'return', feedback: feedback.trim() })}
        onCancel={() => setReturnOpen(false)}
      >
        <Trans>Sends the phase back to its agent: your feedback is appended to the next run's prompt.</Trans>
        <textarea
          value={feedback}
          onChange={(e) => setFeedback(e.target.value)}
          rows={4}
          aria-label={t`feedback for the agent`}
          placeholder={t`what to fix on the next pass`}
          className="mt-2 w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
        />
        {actionError !== null && (
          <div className="mt-2.5">
            <LandFailure err={actionError} terms={vocab} />
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}
