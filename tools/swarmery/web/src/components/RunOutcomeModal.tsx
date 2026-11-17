// Phase-run diagnosis modal — mirrors ImproveModal's structure. Opened from the
// outcome chip on a phase whose run ended without completing it (noop / partial
// / failed), it answers the one question the chip cannot: WHY.
//
// On open it fetches GET /api/epics/{taskId}/phases/{phaseId}/diagnosis — the
// derived outcome, the criteria delta, the deterministic blockers the daemon can
// prove (unmerged dependency, dirty run branch, no criteria at all) and, for
// everything it cannot, the executor's own last word.
//
// Two things this modal refuses to do:
//   • render a fabricated baseline — `criteriaBefore: null` means the run's
//     starting count was never measured, and it says so instead of printing 0;
//   • hide behind a plan run — the diagnosis is READ-ONLY, so it stays open and
//     fetchable while a whole-plan run owns the docs. Only the write actions
//     (Delete branch / Retry run) stand down.

import type { I18n, MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Plural, Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type {
  NeedsYouDecision,
  PhaseBlocker,
  ProviderTerms,
  PhaseDiagnosis,
  PhaseRunOutcome,
  PhaseVerifyVerdict,
} from '../api/types';
import { answerPhaseDecision } from '../api/guard';
import {
  deleteOrphanBranch,
  deletePhaseRunBranch,
  fetchPhaseDiagnosis,
  startRevision,
  type RevisionStartError,
} from '../api';
import { fmtElapsed } from '../lib/format';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { NEUTRAL_TERMS } from '../pages/plans/landingModel';
import { LandBranchAction } from './runOutcome/LandBranchAction';
import { ConfirmDialog } from './ui';
import { useDiscardGuard } from './useDiscardGuard';
import {
  type BlockerGuide,
  type BlockerTier,
  guideFor,
  nextStep,
  retryRefusal,
  TIER_ORDER,
  withoutStopBlock,
} from './runOutcome/blockerGuide';

/** Section heading + frame per tier: what the group of blockers MEANS. */
const TIER_COPY: Record<BlockerTier, { heading: MessageDescriptor; frame: string }> = {
  decide: { heading: msg`needs your decision`, frame: 'border-amber/40 bg-amber/5' },
  'blocks-retry': { heading: msg`blocks a retry`, frame: 'border-red/30 bg-red/5' },
  'blocks-done': { heading: msg`keeps the phase from counting as done`, frame: 'border-amber/30 bg-bg/40' },
  advisory: { heading: msg`for information — does not block this phase`, frame: 'border-line bg-bg/40' },
};

/** The detail a `done` run carries when only classed criteria are left
 * (phaserun settle: "N [LAND] criteria left for landing[; N [MANUAL] criteria
 * left for the operator]"). It rides in `runError` but is a note, not an error:
 * the run finished everything it could. */
const CLASSED_LEFT_RE = /^\d+ \[(LAND|MANUAL)\] criteria left for /;

export function isRunNote(runError: string): boolean {
  return CLASSED_LEFT_RE.test(runError);
}

/** The reason a revise wizard starts from: the diagnosis itself, restated as
 * prose the planner can act on. Composed from what the daemon PROVED (outcome,
 * criteria delta, blockers) plus the executor's own last word. */
function diagnosisReason(diag: PhaseDiagnosis, i18n: I18n): string {
  const { seq, name, runOutcome: outcome, criteriaAfter: after, criteriaTotal: total } = diag;
  const lines = [
    i18n._(msg`Phase ${seq} "${name}" run ended ${outcome}: ${after}/${total} acceptance criteria ticked.`),
    ...diag.blockers.map((b) => {
      const summary = b.summary;
      return i18n._(msg`Blocker: ${summary}`);
    }),
  ];
  if (diag.verifyVerdict !== null) {
    const verdict = diag.verifyVerdict;
    const detail = diag.verifyDetail !== null ? ` — ${diag.verifyDetail}` : '';
    lines.push(i18n._(msg`Verification verdict: ${verdict}${detail}`));
  }
  if (diag.runError !== null) {
    const runError = diag.runError;
    lines.push(isRunNote(diag.runError) ? i18n._(msg`Note: ${runError}`) : i18n._(msg`Run error: ${runError}`));
  }
  if (diag.agentMessage !== null) {
    const said = `${diag.agentMessage.text}${diag.agentMessage.truncated ? '…' : ''}`;
    lines.push(i18n._(msg`Executor said: ${said}`));
  }
  lines.push(i18n._(msg`Revise the plan so this phase (and its dependents) can succeed.`));
  return lines.join('\n');
}

/** Verdict chip copy + styling, matching the Plans list's own chip so the two
 * surfaces describe one grade the same way. The verdict sits BESIDE the outcome chip
 * in the header — the outcome answers "did work land?" (checkboxes, decision D5) and
 * the verdict answers "was it confirmed?". */
interface ChipCopy {
  label: MessageDescriptor;
  cls: string;
  title: MessageDescriptor;
}

const VERDICT_CHIP: Record<PhaseVerifyVerdict, ChipCopy> = {
  pass: {
    label: msg`verified`,
    cls: 'border-green/40 bg-green/10 text-green',
    title: msg`a read-only verifier confirmed this phase’s acceptance criteria`,
  },
  fail: {
    label: msg`verify failed`,
    cls: 'border-red/40 bg-red/10 text-red',
    title: msg`a read-only verifier could NOT confirm the ticked criteria — see the verify-failed blocker`,
  },
  inconclusive: {
    label: msg`verify inconclusive`,
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: msg`the verifier could not conclude (env or timeout) — this is not a failing grade`,
  },
};

type Phase =
  | { kind: 'loading' }
  | { kind: 'ready'; diag: PhaseDiagnosis }
  | { kind: 'error'; message: string };

/** Chip styling + prose for each terminal outcome. `amber` is the app's
 * semantic "needs a human look" color (the same token workspace/TaskCard.tsx
 * uses for inconclusive) — a run that ended without finishing the phase is
 * exactly that, and must never read green. */
const OUTCOME_CHIP: Record<PhaseRunOutcome, ChipCopy> = {
  completed: {
    label: msg`completed`,
    cls: 'border-green/40 bg-green/10 text-green',
    title: msg`the run ticked every acceptance criterion`,
  },
  partial: {
    label: msg`partial`,
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: msg`the run ticked some criteria but did not finish the phase`,
  },
  noop: {
    label: msg`no progress`,
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: msg`the run finished but ticked no acceptance criteria`,
  },
  failed: {
    label: msg`failed`,
    cls: 'border-red/40 bg-red/10 text-red',
    title: msg`the run failed`,
  },
  running: {
    label: msg`running`,
    cls: 'border-brand/40 bg-brand/10 text-brand',
    title: msg`headless run in progress`,
  },
  idle: {
    label: msg`never run`,
    cls: 'border-line text-ink-faint',
    title: msg`this phase has not been run`,
  },
};

export function RunOutcomeModal({
  taskId,
  phaseId,
  writesDisabled,
  writesDisabledReason,
  onClose,
  onRetry,
  reverify = null,
  onOpenRevisions,
  decision = null,
  onChanged,
  terms = NEUTRAL_TERMS,
}: {
  taskId: number;
  phaseId: number;
  /** A plan run owns the docs — the write actions stand down, the diagnosis does not. */
  writesDisabled: boolean;
  writesDisabledReason: string;
  onClose: () => void;
  /** Fires the caller's own phase-run handler (Plans.tsx owns the busy state). */
  onRetry: () => void;
  /** "Verify again", offered on the `unverified` blocker — the caller's own action
   *  (Plans.tsx's shared reverify hook builds label, busy state and tip); null when
   *  the server says this phase is not re-gradable. */
  reverify?: { label: string; onClick: () => void; disabled?: boolean; tip?: string } | null;
  /** Jump to the plan's Revisions tab (the "revision already open" 409 path). */
  onOpenRevisions: () => void;
  /** The phase's open stop/guard decision (EpicPhase.guardDecision), answered in
   *  place on the decision-open blocker; null/absent ⇒ the blocker links out. */
  decision?: NeedsYouDecision | null;
  /** The page's own reload, fired after an answer or a delete changed the plan. */
  onChanged?: () => void;
  /** The project's code-host vocabulary (PR / MR …) for the Open-change action;
   *  the neutral words until the project's vcs has loaded. */
  terms?: ProviderTerms | undefined;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const [busy, setBusy] = useState(false);
  // The branch whose delete is armed — per blocker, because several branch
  // blockers can be listed at once and each needs its own unambiguous button.
  const [confirmingBranch, setConfirmingBranch] = useState<string | null>(null);
  const confirmingDelete = confirmingBranch !== null;
  const setConfirmingDelete = (on: boolean): void => {
    if (!on) setConfirmingBranch(null);
  };
  const [actionErr, setActionErr] = useState<string | null>(null);

  // Revise-plan flow (plan-revision phase 4): the diagnosis is exactly the
  // moment the operator knows the PLAN is wrong, so the modal can hand it to a
  // revise wizard with the reason prefilled from what it just showed.
  const navigate = useNavigate();
  const { slug } = useProjectWorkspace();
  const [revising, setRevising] = useState(false);
  const [reviseReason, setReviseReason] = useState('');
  const [reviseBusy, setReviseBusy] = useState(false);
  const [reviseErr, setReviseErr] = useState<string | null>(null);
  const [reviseOpenRevId, setReviseOpenRevId] = useState<number | null>(null);

  // One fetch path for both the mount load and the post-delete refetch: the
  // `alive` box lets the effect drop a response that lands after unmount.
  const load = useCallback(
    (alive: { current: boolean } = { current: true }): Promise<void> =>
      fetchPhaseDiagnosis(taskId, phaseId)
        .then((diag) => {
          if (alive.current) setPhase({ kind: 'ready', diag });
        })
        .catch((e: unknown) => {
          if (alive.current)
            setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) });
        }),
    [taskId, phaseId],
  );

  // Fetch the diagnosis on mount / whenever the target phase changes.
  useEffect(() => {
    const alive = { current: true };
    setPhase({ kind: 'loading' });
    void load(alive);
    return () => {
      alive.current = false;
    };
  }, [load]);

  // The revise reason is pre-filled from the diagnosis when the panel opens, so
  // "dirty" means it differs from THAT text (or from '' before it was filled) —
  // an untouched pre-fill is not the operator's writing and must not nag.
  const diag = phase.kind === 'ready' ? phase.diag : null;
  const reasonBaseline = diag !== null ? diagnosisReason(diag, i18n).trim() : '';
  const reasonNow = reviseReason.trim();
  const reviseDirty = reasonNow !== '' && reasonNow !== reasonBaseline;
  const discard = useDiscardGuard(reviseDirty, onClose, { disabled: busy || reviseBusy });

  // Esc closes, except while a branch delete is in flight. While the delete is
  // ARMED it disarms instead of closing — Esc is the universal "back out", and
  // closing the whole modal on it would leave the user unsure whether the branch
  // survived. The same collapse-then-close ladder backs the backdrop click and
  // the Close button; only the final step — the modal unmounting — goes
  // through the discard guard.
  const guardedClose = discard.requestClose;
  const requestClose = useCallback((): void => {
    if (busy || reviseBusy) return;
    if (confirmingDelete) {
      setConfirmingDelete(false);
      return;
    }
    if (revising) {
      setRevising(false);
      return;
    }
    guardedClose();
  }, [busy, reviseBusy, confirmingDelete, revising, guardedClose]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') requestClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [requestClose]);

  const startRevise = (): void => {
    const reason = reviseReason.trim();
    if (reason === '') return;
    setReviseBusy(true);
    setReviseErr(null);
    setReviseOpenRevId(null);
    startRevision(taskId, reason, phaseId)
      .then(() => {
        // The revise wizard lives on the planning page; the modal's job is done.
        navigate(`/p/${slug}/planning`);
      })
      .catch((e: unknown) => {
        setReviseErr(e instanceof Error ? e.message : String(e));
        const openId = (e as RevisionStartError).revisionId;
        if (typeof openId === 'number') setReviseOpenRevId(openId);
      })
      .finally(() => setReviseBusy(false));
  };

  const chip = OUTCOME_CHIP[diag?.runOutcome ?? 'idle'];
  // The branch is only reclaimable when the daemon actually proved it dirty —
  // offering the delete otherwise would invite destroying an unrelated branch. The
  // blocker itself (not a client-side reconstruction of the name) is what the
  // confirmation quotes.
  //
  // Two kinds are legitimate delete targets, and they take DIFFERENT routes:
  // branch-dirty is this phase's own branch (the phase-scoped route derives the
  // name), while orphan-branch names a branch whose id has no row at all, which
  // only the explicit orphan-cleanup route can reach. own-worktree is deliberately
  // NOT here: a delete 409s for that state, so offering one would advise the single
  // action that cannot work.

  const endedMs =
    diag !== null && diag.runEndedAt !== null ? Date.parse(diag.runEndedAt) : Number.NaN;
  const duration =
    diag !== null && diag.runStartedAt !== null && !Number.isNaN(endedMs)
      ? fmtElapsed(diag.runStartedAt, endedMs)
      : null;

  function deleteBranch(target: PhaseBlocker): void {
    setBusy(true);
    setConfirmingBranch(null);
    setActionErr(null);
    // Routed by the blocker's kind, and the branch the SERVER named is what gets
    // deleted — never "swarm/phase-<this phase's id>" rebuilt client-side, which
    // for an orphan would name a branch that has nothing to do with the work.
    const req =
      target.kind === 'orphan-branch'
        ? deleteOrphanBranch(taskId, target.branch ?? '')
        : deletePhaseRunBranch(taskId, phaseId);
    req
      .then(() => {
        onChanged?.();
        return load();
      })
      .catch((e: unknown) => setActionErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  }

  // The delete reaches `git branch -D`: the commits go, and nothing else holds
  // them. Two steps, in-modal (never window.confirm — a browser modal blocks the
  // extension-driven flows this repo uses), and the armed step names the branch and
  // the exact number of commits it is about to destroy. Same shape as KillButton's
  // confirm: an armed line + Confirm/Cancel replacing the trigger in place.
  const deleteSlot = (target: PhaseBlocker, mode: 'fix' | 'discard'): JSX.Element | null => {
    const branchName = target.branch ?? '';
    if (branchName === '') return null;
    const commits = target.commitsAhead ?? 0;
    const branch = branchName;
    if (confirmingBranch !== branchName) {
      return (
        <button
          type="button"
          disabled={busy || writesDisabled}
          data-tip={
            writesDisabled
              ? writesDisabledReason
              : mode === 'discard'
                ? t`throw this work away and start the phase from scratch`
                : t`delete ${branch} so a retry can recreate it`
          }
          onClick={() => setConfirmingBranch(branchName)}
          className="rounded-lg border border-red/40 bg-red/5 px-3 py-1 font-mono text-[11px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? t`deleting…` : mode === 'discard' ? t`Discard this work` : t`Delete branch`}
        </button>
      );
    }
    return (
      <span className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[10.5px] text-ink-dim">
          <Trans>Delete {branch}?</Trans>{' '}
          {commits > 0 ? (
            <Plural
              value={commits}
              one="# commit will be lost."
              few="# commits will be lost."
              many="# commits will be lost."
              other="# commits will be lost."
            />
          ) : (
            <Trans>Its commits will be lost.</Trans>
          )}
        </span>
        <button
          type="button"
          disabled={busy || writesDisabled}
          onClick={() => deleteBranch(target)}
          className="rounded-lg border border-red/40 bg-red/10 px-3 py-1 font-mono text-[11px] font-semibold text-red transition-colors hover:bg-red/20 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? t`deleting…` : t`Delete permanently`}
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => setConfirmingBranch(null)}
          className="font-mono text-[11px] text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
        >
          <Trans>Cancel</Trans>
        </button>
      </span>
    );
  };

  // Blockers grouped by what they mean for the operator, most urgent group first.
  const guided: { b: PhaseBlocker; g: BlockerGuide }[] =
    diag?.blockers.map((b) => ({ b, g: guideFor(b, i18n, terms) })) ?? [];
  const tiers = new Set(guided.map((x) => x.g.tier));
  const refusal = diag !== null ? retryRefusal(diag.blockers, i18n) : null;

  const seqLabel = diag !== null ? String(diag.seq) : '—';
  const criteriaAfter = diag?.criteriaAfter ?? 0;
  const criteriaTotal = diag?.criteriaTotal ?? 0;
  const criteriaBefore = diag?.criteriaBefore ?? null;
  const runNote = diag?.runError ?? '';

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Phase run diagnosis`}
      onClick={busy ? undefined : requestClose}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="font-mono text-[10px] text-ink-faint">
              <Trans>Phase {seqLabel}</Trans>
            </div>
            <div className="font-display truncate text-[14px] font-bold text-ink">
              {diag?.name ?? t`run diagnosis`}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {duration !== null && (
              <span className="font-mono text-[10px] text-ink-faint" data-tip={t`run duration`}>
                <Trans>ran {duration}</Trans>
              </span>
            )}
            {diag !== null && (
              <span
                className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${chip.cls}`}
                data-tip={i18n._(chip.title)}
              >
                {i18n._(chip.label)}
              </span>
            )}
            {diag?.verifyVerdict != null && (
              <span
                className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${VERDICT_CHIP[diag.verifyVerdict].cls}`}
                data-tip={i18n._(VERDICT_CHIP[diag.verifyVerdict].title)}
              >
                {i18n._(VERDICT_CHIP[diag.verifyVerdict].label)}
              </span>
            )}
          </div>
        </div>

        {phase.kind === 'loading' && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">
            <Trans>loading diagnosis…</Trans>
          </div>
        )}

        {phase.kind === 'error' && (
          <div className="mt-3 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
            {phase.message}
          </div>
        )}

        {actionErr !== null && (
          <div
            role="alert"
            className="mt-3 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red"
          >
            {actionErr}
          </div>
        )}

        {diag !== null && (
          <div className="mt-3 min-h-0 flex-1 space-y-4 overflow-y-auto">
            {/* Criteria delta. `criteriaBefore === null` prints "baseline not
                measured" rather than a 0 nobody observed. */}
            <div className="font-mono text-[11.5px] text-ink-2">
              <Trans>
                {criteriaAfter} of {criteriaTotal} criteria ticked
              </Trans>
              {criteriaBefore !== null ? t` · ${criteriaBefore} before this run` : t` · baseline not measured`}
            </div>

            {diag.runError !== null &&
              (isRunNote(diag.runError) ? (
                <div
                  data-testid="run-note"
                  className="rounded-lg border border-line bg-surface2/40 px-2.5 py-2 font-mono text-[10.5px] break-words text-ink-3"
                >
                  <Trans>Note: {runNote}</Trans>
                </div>
              ) : (
                <div className="rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[10.5px] break-words text-red">
                  {diag.runError}
                </div>
              ))}

            {/* The one thing to do first — derived from the most urgent tier, so the
                operator never has to work out the order from the list. */}
            <div
              data-testid="next-step"
              className="rounded-lg border border-brand/30 bg-brand/5 px-2.5 py-2 text-[12px] text-ink"
            >
              <span className="mr-1.5 font-mono text-[10px] tracking-[0.16em] text-brand uppercase">
                <Trans>next step</Trans>
              </span>
              {nextStep(tiers, diag.runOutcome, i18n)}
            </div>

            {guided.length === 0 && (
              <div className="font-mono text-[11px] text-ink-faint">
                <Trans>
                  No blockers detected — the executor exited without ticking criteria; open the
                  session to see why.
                </Trans>
              </div>
            )}

            {TIER_ORDER.filter((tier) => tiers.has(tier)).map((tier) => (
              <div key={tier}>
                <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  {i18n._(TIER_COPY[tier].heading)}
                </div>
                <ul className="space-y-2">
                  {guided
                    .filter((x) => x.g.tier === tier)
                    .map(({ b, g }, i) => (
                      <li
                        key={`${b.kind}-${b.branch ?? ''}-${String(i)}`}
                        data-kind={b.kind}
                        className={`rounded-lg border px-2.5 py-2 ${
                          b.kind === 'verify-failed' ? 'border-red/25 bg-red/5' : TIER_COPY[tier].frame
                        }`}
                      >
                        <div className="text-[12px] font-semibold text-ink">{g.title}</div>
                        {g.steps.length > 0 && (
                          <>
                            <div className="mt-1.5 font-mono text-[9.5px] tracking-[0.14em] text-ink-faint uppercase">
                              <Trans>how to resolve</Trans>
                            </div>
                            <ol className="mt-0.5 list-decimal space-y-0.5 pl-4 text-[11.5px] leading-relaxed text-ink-2">
                              {g.steps.map((step) => (
                                <li key={step}>{step}</li>
                              ))}
                            </ol>
                          </>
                        )}
                        {/* With an in-dashboard action the commands are the
                            fallback, folded away; without one they ARE the fix. */}
                        {g.commands.length > 0 && g.land === null && <CommandBlock commands={g.commands} />}

                        {b.kind === 'decision-open' && (
                          <DecisionPanel
                            blocker={b}
                            decision={decision}
                            disabled={writesDisabled}
                            disabledReason={writesDisabledReason}
                            onAnswer={(option, note) => {
                              if (decision === null) return Promise.resolve();
                              return answerPhaseDecision(
                                decision.taskId,
                                decision.phaseId,
                                decision.decisionId,
                                option,
                                note,
                              ).then(() => {
                                onChanged?.();
                                return load();
                              });
                            }}
                          />
                        )}

                        {/* The server's own evidence: what it measured, verbatim. */}
                        <details className="mt-1.5">
                          <summary className="cursor-pointer font-mono text-[10px] text-ink-faint hover:text-ink-dim">
                            {b.summary}
                          </summary>
                          {b.detail !== '' && (
                            <div className="mt-1 font-mono text-[10px] whitespace-pre-line text-ink-faint">
                              {b.detail}
                            </div>
                          )}
                        </details>

                        {(g.land !== null || g.deletable !== null || (b.kind === 'unverified' && reverify !== null)) && (
                          <div className="mt-2 flex flex-wrap items-start gap-2">
                            {g.land !== null && (
                              <LandBranchAction
                                taskId={taskId}
                                branch={b.branch ?? ''}
                                mode={g.land}
                                terms={terms}
                                disabled={writesDisabled}
                                disabledReason={writesDisabledReason}
                                onLanded={onChanged}
                              />
                            )}
                            {g.deletable !== null && deleteSlot(b, g.deletable)}
                            {/* The remedy for a missing grade is a grade: re-run the
                                read-only verifier on the branch as it stands. Closes the
                                modal like Retry does — busy state and any refusal show
                                on the page, where the action lives. */}
                            {b.kind === 'unverified' && reverify !== null && (
                              <button
                                type="button"
                                disabled={reverify.disabled}
                                data-tip={reverify.tip}
                                onClick={() => {
                                  reverify.onClick();
                                  onClose();
                                }}
                                className="rounded-lg border border-brand/40 bg-brand/5 px-3 py-1 font-mono text-[11px] text-brand transition-colors hover:bg-brand/10 disabled:cursor-not-allowed disabled:opacity-50"
                              >
                                {reverify.label}
                              </button>
                            )}
                          </div>
                        )}
                        {g.commands.length > 0 && g.land !== null && (
                          <details className="mt-1.5">
                            <summary className="cursor-pointer font-mono text-[10px] text-ink-faint hover:text-ink-dim">
                              <Trans>or do it by hand in the repo</Trans>
                            </summary>
                            <CommandBlock commands={g.commands} />
                          </details>
                        )}
                      </li>
                    ))}
                </ul>
              </div>
            ))}

            {diag.agentMessage !== null && (
              <div>
                <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  <Trans>agent said</Trans>
                </div>
                <div className="rounded-lg border border-line bg-bg/40 px-2.5 py-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
                  {/* With a decision-open blocker, the stop block's options are
                      already on the decision card — repeating its YAML here (cut
                      off mid-option) is noise. */}
                  {tiers.has('decide')
                    ? withoutStopBlock(diag.agentMessage.text)
                    : `${diag.agentMessage.text}${diag.agentMessage.truncated ? '…' : ''}`}
                </div>
                <Link
                  to={`/sessions/${diag.agentMessage.sessionUuid}`}
                  className="mt-1 inline-block font-mono text-[10px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
                >
                  <Trans>open session</Trans>
                </Link>
              </div>
            )}

            {revising && (
              <div className="rounded-lg border border-brand/30 bg-brand/5 px-3 py-2.5">
                <div className="mb-1 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  <Trans>revise the plan</Trans>
                </div>
                <p className="mb-2 text-[11.5px] leading-relaxed text-ink-dim">
                  <Trans>
                    A revise wizard interviews you against this plan and stages its changes as a
                    diff — nothing is written until you approve it. The reason below is prefilled
                    from this diagnosis; edit it before starting.
                  </Trans>
                </p>
                {reviseErr !== null && (
                  <div
                    role="alert"
                    className="mb-2 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[10.5px] text-red"
                  >
                    {reviseErr}
                    {reviseOpenRevId !== null && (
                      <button
                        type="button"
                        onClick={onOpenRevisions}
                        className="mt-1 block font-mono text-[10.5px] text-brand underline-offset-2 hover:underline"
                      >
                        <Trans>review the open revision →</Trans>
                      </button>
                    )}
                  </div>
                )}
                <textarea
                  value={reviseReason}
                  onChange={(e) => setReviseReason(e.target.value)}
                  rows={5}
                  disabled={reviseBusy}
                  aria-label={t`why the plan must change`}
                  className="w-full resize-y rounded-lg border border-line bg-field px-2.5 py-2 text-[12px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
                />
                <div className="mt-2 flex flex-wrap justify-end gap-2">
                  <button
                    type="button"
                    disabled={reviseBusy}
                    onClick={() => setRevising(false)}
                    className="rounded-lg border border-line px-3 py-1.5 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink disabled:opacity-50"
                  >
                    <Trans>Cancel</Trans>
                  </button>
                  <button
                    type="button"
                    disabled={reviseBusy || reviseReason.trim() === ''}
                    onClick={startRevise}
                    className="rounded-lg border border-brand/45 bg-brand/12 px-3 py-1.5 font-mono text-[11px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
                  >
                    {reviseBusy ? t`starting…` : t`Start revision`}
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        <div className="mt-4 flex flex-wrap items-center justify-end gap-2">
          {/* The plan-change escape hatch, offered exactly where "this didn't
              work" is being read. Only for runs that ended without completing
              the phase — a completed phase needs no plan surgery. */}
          {diag !== null &&
            (diag.runOutcome === 'failed' ||
              diag.runOutcome === 'noop' ||
              diag.runOutcome === 'partial') &&
            !revising && (
              <button
                type="button"
                disabled={busy || reviseBusy || writesDisabled}
                data-tip={
                  writesDisabled
                    ? writesDisabledReason
                    : t`change the plan — interview + staged diff, applied only on your approval`
                }
                onClick={() => {
                  setReviseErr(null);
                  setReviseOpenRevId(null);
                  if (reviseReason.trim() === '') setReviseReason(diagnosisReason(diag, i18n));
                  setRevising(true);
                }}
                className="rounded-lg border border-brand/40 bg-brand/5 px-3.5 py-1.5 font-mono text-[11.5px] text-brand transition-colors hover:bg-brand/10 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Trans>Revise plan</Trans>
              </button>
            )}
          <button
            type="button"
            disabled={busy || writesDisabled || refusal !== null}
            data-tip={writesDisabled ? writesDisabledReason : (refusal ?? t`run this phase again`)}
            onClick={() => {
              onRetry();
              onClose();
            }}
            className="rounded-lg border border-brand/40 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Trans>Retry run</Trans>
          </button>
          <button
            type="button"
            onClick={requestClose}
            disabled={busy || reviseBusy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            <Trans>Close</Trans>
          </button>
        </div>
      </div>

      <ConfirmDialog
        {...discard.confirmProps}
        title={t`Discard revise reason?`}
        confirmLabel={t`discard`}
        danger
      >
        <Trans>The reason you wrote for revising this plan will be lost.</Trans>
      </ConfirmDialog>
    </div>
  );
}

/** Copyable git commands for a fix that happens outside the dashboard. */
function CommandBlock({ commands }: { commands: string[] }): JSX.Element {
  const { t } = useLingui();
  const [copied, setCopied] = useState(false);
  const text = commands.join('\n');
  return (
    <div className="mt-1.5 flex items-start gap-2 rounded-md border border-line bg-bg/60 px-2 py-1.5">
      <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-[10.5px] leading-relaxed text-ink-2">{text}</pre>
      <button
        type="button"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(text)
            .then(() => setCopied(true))
            .catch(() => setCopied(false));
        }}
        className="shrink-0 font-mono text-[10px] text-ink-dim transition-colors hover:text-brand"
        data-tip={t`run in the project's repo`}
      >
        {copied ? t`copied` : t`copy`}
      </button>
    </div>
  );
}

/** Answers the phase's open stop/guard decision in place: the option list with
 *  each option's consequence, an optional note, and the operator's own answer
 *  when the stop allows it. Same endpoint as the Needs-you card. */
function DecisionPanel({
  blocker,
  decision,
  disabled,
  disabledReason,
  onAnswer,
}: {
  blocker: PhaseBlocker;
  decision: NeedsYouDecision | null;
  disabled: boolean;
  disabledReason: string;
  onAnswer: (option: string, note: string) => Promise<void>;
}): JSX.Element {
  const { t } = useLingui();
  const [pick, setPick] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [sending, setSending] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Two sources say who is waiting — the diagnosis (fresh) and the page's phase
  // DTO (which carries the options). Answer only when they name the same one.
  if (decision === null || decision.decisionId !== (blocker.decisionId ?? '')) {
    return (
      <div className="mt-2 font-mono text-[10.5px] text-ink-dim">
        <Trans>The options of this decision are on the Needs you page.</Trans>{' '}
        <Link to="/needs-you" className="text-brand underline-offset-2 hover:underline">
          <Trans>open Needs you →</Trans>
        </Link>
      </div>
    );
  }
  const reason = decision.reason !== undefined && decision.reason !== '' ? decision.reason : decision.question;
  const own = note.trim();
  const send = (option: string): void => {
    setSending(true);
    setErr(null);
    onAnswer(option, own)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setSending(false));
  };
  const off = disabled || sending;

  return (
    <div className="mt-2 rounded-md border border-amber/30 bg-surface px-2.5 py-2">
      <div className="text-[11.5px] leading-relaxed text-ink-2">
        <span className="font-mono text-[10px] text-ink-faint">
          <Trans>executor's reason:</Trans>
        </span>{' '}
        {reason}
      </div>
      <fieldset className="mt-2 space-y-1.5" disabled={off}>
        <legend className="sr-only">{t`choose an answer`}</legend>
        {decision.options.map((o) => (
          <label
            key={o.id}
            className={`flex cursor-pointer gap-2 rounded-md border px-2 py-1.5 transition-colors ${
              pick === o.id ? 'border-brand/50 bg-brand/5' : 'border-line hover:bg-surface2'
            }`}
          >
            <input
              type="radio"
              name={`decision-${decision.decisionId}`}
              value={o.id}
              checked={pick === o.id}
              onChange={() => setPick(o.id)}
              className="mt-0.5"
            />
            <span className="min-w-0">
              <span className="block text-[11.5px] text-ink">{o.label}</span>
              {o.consequence !== undefined && o.consequence !== '' && (
                <span className="block text-[10.5px] leading-relaxed text-ink-dim">
                  <Trans>then:</Trans> {o.consequence}
                </span>
              )}
            </span>
          </label>
        ))}
      </fieldset>
      <textarea
        value={note}
        onChange={(e) => setNote(e.target.value)}
        rows={2}
        disabled={off}
        aria-label={t`note for the answer`}
        placeholder={
          decision.freeText === true
            ? t`note, or your own answer instead of the options — recorded in the phase doc`
            : t`note — recorded with the answer in the phase doc (optional)`
        }
        className="mt-2 w-full resize-y rounded-md border border-line bg-field px-2 py-1.5 text-[11.5px] text-ink outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
      />
      {err !== null && (
        <div role="alert" className="mt-1.5 font-mono text-[10.5px] text-red">
          {err}
        </div>
      )}
      <div className="mt-1.5 flex flex-wrap justify-end gap-2">
        {decision.freeText === true && (
          <button
            type="button"
            disabled={off || own === ''}
            onClick={() => send('other')}
            className="rounded-lg border border-line px-3 py-1 font-mono text-[11px] text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Trans>Answer in my own words</Trans>
          </button>
        )}
        <button
          type="button"
          disabled={off || pick === null}
          data-tip={disabled ? disabledReason : undefined}
          onClick={() => {
            if (pick !== null) send(pick);
          }}
          className="rounded-lg border border-brand/45 bg-brand/12 px-3 py-1 font-mono text-[11px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {sending ? t`answering…` : t`Answer`}
        </button>
      </div>
    </div>
  );
}
