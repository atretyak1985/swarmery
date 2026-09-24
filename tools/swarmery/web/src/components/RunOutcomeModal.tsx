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

import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type { PhaseDiagnosis, PhaseRunOutcome, PhaseVerifyVerdict } from '../api/types';
import {
  deleteOrphanBranch,
  deletePhaseRunBranch,
  fetchPhaseDiagnosis,
  startRevision,
  type RevisionStartError,
} from '../api';
import { fmtElapsed } from '../lib/format';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { ConfirmDialog } from './ui';

/** The reason a revise wizard starts from: the diagnosis itself, restated as
 * prose the planner can act on. Composed from what the daemon PROVED (outcome,
 * criteria delta, blockers) plus the executor's own last word. */
function diagnosisReason(diag: PhaseDiagnosis): string {
  const lines = [
    `Phase ${String(diag.seq)} "${diag.name}" run ended ${diag.runOutcome}: ${String(diag.criteriaAfter)}/${String(diag.criteriaTotal)} acceptance criteria ticked.`,
    ...diag.blockers.map((b) => `Blocker: ${b.summary}`),
  ];
  if (diag.verifyVerdict !== null)
    lines.push(
      `Verification verdict: ${diag.verifyVerdict}${diag.verifyDetail !== null ? ` — ${diag.verifyDetail}` : ''}`,
    );
  if (diag.runError !== null) lines.push(`Run error: ${diag.runError}`);
  if (diag.agentMessage !== null)
    lines.push(`Executor said: ${diag.agentMessage.text}${diag.agentMessage.truncated ? '…' : ''}`);
  lines.push('Revise the plan so this phase (and its dependents) can succeed.');
  return lines.join('\n');
}

/** Verdict chip copy + styling, matching the Plans list's own chip so the two
 * surfaces describe one grade the same way. The verdict sits BESIDE the outcome chip
 * in the header — the outcome answers "did work land?" (checkboxes, decision D5) and
 * the verdict answers "was it confirmed?". */
const VERDICT_CHIP: Record<PhaseVerifyVerdict, { label: string; cls: string; title: string }> = {
  pass: {
    label: 'verified',
    cls: 'border-green/40 bg-green/10 text-green',
    title: 'a read-only verifier confirmed this phase’s acceptance criteria',
  },
  fail: {
    label: 'verify failed',
    cls: 'border-red/40 bg-red/10 text-red',
    title: 'a read-only verifier could NOT confirm the ticked criteria — see the verify-failed blocker',
  },
  inconclusive: {
    label: 'verify inconclusive',
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: 'the verifier could not conclude (env or timeout) — this is not a failing grade',
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
const OUTCOME_CHIP: Record<PhaseRunOutcome, { label: string; cls: string; title: string }> = {
  completed: {
    label: 'completed',
    cls: 'border-green/40 bg-green/10 text-green',
    title: 'the run ticked every acceptance criterion',
  },
  partial: {
    label: 'partial',
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: 'the run ticked some criteria but did not finish the phase',
  },
  noop: {
    label: 'no progress',
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: 'the run finished but ticked no acceptance criteria',
  },
  failed: {
    label: 'failed',
    cls: 'border-red/40 bg-red/10 text-red',
    title: 'the run failed',
  },
  running: {
    label: 'running',
    cls: 'border-brand/40 bg-brand/10 text-brand',
    title: 'headless run in progress',
  },
  idle: {
    label: 'never run',
    cls: 'border-line text-ink-faint',
    title: 'this phase has not been run',
  },
};

export function RunOutcomeModal({
  taskId,
  phaseId,
  writesDisabled,
  writesDisabledReason,
  onClose,
  onRetry,
  onOpenRevisions,
}: {
  taskId: number;
  phaseId: number;
  /** A plan run owns the docs — the write actions stand down, the diagnosis does not. */
  writesDisabled: boolean;
  writesDisabledReason: string;
  onClose: () => void;
  /** Fires the caller's own phase-run handler (Plans.tsx owns the busy state). */
  onRetry: () => void;
  /** Jump to the plan's Revisions tab (the "revision already open" 409 path). */
  onOpenRevisions: () => void;
}): JSX.Element {
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const [busy, setBusy] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [confirmDiscardRevise, setConfirmDiscardRevise] = useState(false);

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

  // Esc closes, except while a branch delete is in flight. While the delete is
  // ARMED it disarms instead of closing — Esc is the universal "back out", and
  // closing the whole modal on it would leave the user unsure whether the branch
  // survived. Same collapse-then-close ladder backs the backdrop click via
  // `requestClose`, plus a guard on the typed revise reason at the point where
  // the modal would actually unmount.
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
    if (reviseReason.trim() !== '') {
      setConfirmDiscardRevise(true);
      return;
    }
    onClose();
  }, [busy, reviseBusy, confirmingDelete, revising, reviseReason, onClose]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape' || busy || reviseBusy) return;
      if (confirmDiscardRevise) {
        setConfirmDiscardRevise(false);
        return;
      }
      requestClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, reviseBusy, confirmDiscardRevise, requestClose]);

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

  const diag = phase.kind === 'ready' ? phase.diag : null;
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
  const dirty =
    diag?.blockers.find((b) => b.kind === 'branch-dirty') ??
    diag?.blockers.find((b) => b.kind === 'orphan-branch' && (b.branch ?? '') !== '') ??
    null;

  const endedMs =
    diag !== null && diag.runEndedAt !== null ? Date.parse(diag.runEndedAt) : Number.NaN;
  const duration =
    diag !== null && diag.runStartedAt !== null && !Number.isNaN(endedMs)
      ? fmtElapsed(diag.runStartedAt, endedMs)
      : null;

  function deleteBranch(): void {
    setBusy(true);
    setConfirmingDelete(false);
    setActionErr(null);
    // Routed by the blocker's kind, and the branch the SERVER named is what gets
    // deleted — never "swarm/phase-<this phase's id>" rebuilt client-side, which
    // for an orphan would name a branch that has nothing to do with the work.
    const req =
      dirty?.kind === 'orphan-branch'
        ? deleteOrphanBranch(taskId, dirty.branch ?? '')
        : deletePhaseRunBranch(taskId, phaseId);
    req
      .then(() => load())
      .catch((e: unknown) => setActionErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  }

  // The delete reaches `git branch -D`: the commits go, and nothing else holds
  // them. Two steps, in-modal (never window.confirm — a browser modal blocks the
  // extension-driven flows this repo uses), and the armed step names the branch and
  // the exact number of commits it is about to destroy. Same shape as KillButton's
  // confirm: an armed line + Confirm/Cancel replacing the trigger in place.
  const deleteSlot = ((): JSX.Element | null => {
    if (dirty === null) return null;
    const commits = dirty.commitsAhead ?? 0;
    const branch = dirty.branch ?? 'the run branch';
    if (!confirmingDelete) {
      return (
        <button
          type="button"
          disabled={busy || writesDisabled}
          data-tip={
            writesDisabled ? writesDisabledReason : 'delete the run branch so a retry can recreate it'
          }
          onClick={() => setConfirmingDelete(true)}
          className="rounded-lg border border-red/40 bg-red/5 px-3.5 py-1.5 font-mono text-[11.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? 'deleting…' : 'Delete branch'}
        </button>
      );
    }
    return (
      <span className="flex flex-wrap items-center justify-end gap-2">
        <span className="font-mono text-[10.5px] text-ink-dim">
          Delete {branch}?{' '}
          {commits > 0
            ? `${String(commits)} commit${commits === 1 ? '' : 's'} will be lost.`
            : 'Its commits will be lost.'}
        </span>
        <button
          type="button"
          disabled={busy || writesDisabled}
          onClick={deleteBranch}
          className="rounded-lg border border-red/40 bg-red/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? 'deleting…' : 'Delete permanently'}
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => setConfirmingDelete(false)}
          className="font-mono text-[11.5px] text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
        >
          Cancel
        </button>
      </span>
    );
  })();

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label="Phase run diagnosis"
      onClick={busy ? undefined : requestClose}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="font-mono text-[10px] text-ink-faint">
              Phase {diag !== null ? diag.seq : '—'}
            </div>
            <div className="font-display truncate text-[14px] font-bold text-ink">
              {diag?.name ?? 'run diagnosis'}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {duration !== null && (
              <span className="font-mono text-[10px] text-ink-faint" data-tip="run duration">
                ran {duration}
              </span>
            )}
            {diag !== null && (
              <span
                className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${chip.cls}`}
                data-tip={chip.title}
              >
                {chip.label}
              </span>
            )}
            {diag?.verifyVerdict != null && (
              <span
                className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${VERDICT_CHIP[diag.verifyVerdict].cls}`}
                data-tip={VERDICT_CHIP[diag.verifyVerdict].title}
              >
                {VERDICT_CHIP[diag.verifyVerdict].label}
              </span>
            )}
          </div>
        </div>

        {phase.kind === 'loading' && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">loading diagnosis…</div>
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
              {diag.criteriaAfter} of {diag.criteriaTotal} criteria ticked
              {diag.criteriaBefore !== null
                ? ` · ${String(diag.criteriaBefore)} before this run`
                : ' · baseline not measured'}
            </div>

            {diag.runError !== null && (
              <div className="rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[10.5px] break-words text-red">
                {diag.runError}
              </div>
            )}

            <div>
              <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                blockers
              </div>
              {diag.blockers.length === 0 ? (
                <div className="font-mono text-[11px] text-ink-faint">
                  No blockers detected — the executor exited without ticking criteria; open the
                  session to see why.
                </div>
              ) : (
                <ul className="space-y-2">
                  {diag.blockers.map((b, i) => (
                    <li
                      key={`${b.kind}-${String(i)}`}
                      className={`rounded-lg border px-2.5 py-2 ${
                        // verify-failed is the one blocker that reports a GRADE of the
                        // work rather than a state of the repo, and it is the actionable
                        // one on an otherwise-green phase — so it reads red like the
                        // verdict chip above instead of blending into the neutral rows.
                        b.kind === 'verify-failed'
                          ? 'border-red/25 bg-red/5'
                          : 'border-line bg-bg/40'
                      }`}
                    >
                      <div className="text-[11.5px] text-ink">{b.summary}</div>
                      <div className="mt-1 font-mono text-[10px] whitespace-pre-line text-ink-faint">
                        {b.detail}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            {diag.agentMessage !== null && (
              <div>
                <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  agent said
                </div>
                <div className="rounded-lg border border-line bg-bg/40 px-2.5 py-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
                  {diag.agentMessage.text}
                  {diag.agentMessage.truncated ? '…' : ''}
                </div>
                <Link
                  to={`/sessions/${diag.agentMessage.sessionUuid}`}
                  className="mt-1 inline-block font-mono text-[10px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
                >
                  open session
                </Link>
              </div>
            )}

            {revising && (
              <div className="rounded-lg border border-brand/30 bg-brand/5 px-3 py-2.5">
                <div className="mb-1 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  revise the plan
                </div>
                <p className="mb-2 text-[11.5px] leading-relaxed text-ink-dim">
                  A revise wizard interviews you against this plan and stages its changes as a
                  diff — nothing is written until you approve it. The reason below is prefilled
                  from this diagnosis; edit it before starting.
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
                        review the open revision →
                      </button>
                    )}
                  </div>
                )}
                <textarea
                  value={reviseReason}
                  onChange={(e) => setReviseReason(e.target.value)}
                  rows={5}
                  disabled={reviseBusy}
                  aria-label="why the plan must change"
                  className="w-full resize-y rounded-lg border border-line bg-field px-2.5 py-2 text-[12px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
                />
                <div className="mt-2 flex flex-wrap justify-end gap-2">
                  <button
                    type="button"
                    disabled={reviseBusy}
                    onClick={() => setRevising(false)}
                    className="rounded-lg border border-line px-3 py-1.5 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink disabled:opacity-50"
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    disabled={reviseBusy || reviseReason.trim() === ''}
                    onClick={startRevise}
                    className="rounded-lg border border-brand/45 bg-brand/12 px-3 py-1.5 font-mono text-[11px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
                  >
                    {reviseBusy ? 'starting…' : 'Start revision'}
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        <div className="mt-4 flex flex-wrap items-center justify-end gap-2">
          {deleteSlot}
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
                    : 'change the plan — interview + staged diff, applied only on your approval'
                }
                onClick={() => {
                  setReviseErr(null);
                  setReviseOpenRevId(null);
                  if (reviseReason.trim() === '') setReviseReason(diagnosisReason(diag));
                  setRevising(true);
                }}
                className="rounded-lg border border-brand/40 bg-brand/5 px-3.5 py-1.5 font-mono text-[11.5px] text-brand transition-colors hover:bg-brand/10 disabled:cursor-not-allowed disabled:opacity-50"
              >
                Revise plan
              </button>
            )}
          <button
            type="button"
            disabled={busy || writesDisabled}
            data-tip={writesDisabled ? writesDisabledReason : 'run this phase again'}
            onClick={() => {
              onRetry();
              onClose();
            }}
            className="rounded-lg border border-brand/40 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Retry run
          </button>
          <button
            type="button"
            onClick={requestClose}
            disabled={busy || reviseBusy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            Close
          </button>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDiscardRevise}
        title="Discard revise reason?"
        confirmLabel="discard"
        danger
        onConfirm={onClose}
        onCancel={() => setConfirmDiscardRevise(false)}
      >
        The reason you wrote for revising this plan will be lost.
      </ConfirmDialog>
    </div>
  );
}
