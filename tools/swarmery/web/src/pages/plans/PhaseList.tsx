// The phases of a plan.

import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import type { Epic, TerminalSession } from '../../api/types';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { SurpriseChip } from './ForecastVsActual';
import type { PhaseCardAction } from './PhaseCard';
import { ContinuationChip, isUnresolvedOutcome, PhaseActivity, phaseChip, ProgressBar, runButtonCls, RunCompletedChip, RunOutcomeChip } from './phaseChips';
import { DocModelChip, type PhaseRunModel, RunModelChip } from './PhaseRunPickers';
import { phaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { TerminalBadge } from './TerminalBadge';
import { reverifyAction, type Reverify } from './useReverify';

/** The **Verify again** button of the phase row and the panel's Runs tab. The
 *  action itself (label, busy, tip) comes from `reverifyAction`, the one builder
 *  every site shares; null renders nothing. */
export function ReverifyButton({
  action,
  size,
  className = '',
}: {
  action: PhaseCardAction | null;
  size: 'row' | 'panel';
  className?: string;
}): JSX.Element | null {
  if (action === null) return null;
  return (
    <button
      type="button"
      disabled={action.disabled}
      data-tip={action.tip}
      onClick={(e) => {
        e.stopPropagation();
        action.onClick();
      }}
      className={`rounded-md border border-line-strong font-mono text-ink-3 transition-colors hover:text-ink disabled:cursor-not-allowed disabled:opacity-50 ${
        size === 'row' ? 'px-1.5 py-px text-[9.5px]' : 'px-2 py-1 text-[10px]'
      } ${className}`}
    >
      {action.label}
    </button>
  );
}

/** The phase timeline — seq order, depends-on badges, per-phase progress and
 * run controls. A row opens that phase's details, which replace this list. */
export function PhaseList({
  epic,
  resolvedSeqs,
  now,
  runBusy,
  planRunning,
  terminalSessions,
  phaseRunModel,
  onOpenPhase,
  phaseHref,
  onRun,
  onCancelRun,
  reverify,
  onReverify,
  onOpenOutcome,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
  now: number;
  runBusy: number | null;
  /** A whole-plan run owns the phase docs — per-phase runs stand down. */
  planRunning: boolean;
  /** The plan's terminal sessions; a phase shows the ones that picked it. */
  terminalSessions: readonly TerminalSession[];
  /** The picker's current value — read ONLY to tell whether a phase doc's own
   * declaration is in effect or overridden (DocModelChip). The resolution itself
   * stays server-side, in phaserun.resolveModel: a second ladder here would be a
   * second thing to drift. */
  phaseRunModel: PhaseRunModel;
  onOpenPhase: (seq: number, tab: PhaseDetailTab) => void;
  /** The URL a phase opens at (its panel's Story) — what its name links to. */
  phaseHref: (seq: number) => string;
  onRun: (phaseId: number) => void;
  onCancelRun: (phaseId: number) => void;
  reverify: Reverify;
  onReverify: (phaseId: number) => void;
  onOpenOutcome: (phaseId: number) => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const sessionHref = useSessionHref();
  return (
    <ol className="space-y-2">
      {epic.phases.map((p) => {
        const activated = p.activatedAt !== null;
        const status = phaseStatus(p, resolvedSeqs);
        // Process state, kept out of `phaseStatus` on purpose: only a live run
        // may say `running`, and only a live run may claim liveness.
        const running = p.runState === 'running';
        const chip = phaseChip(status, running);
        const depsUnmet = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
        const runDisabled =
          runBusy !== null || planRunning || epic.status !== 'active' || depsUnmet.length > 0;
        const waitingOn = depsUnmet.join(', ');
        const runTitle = planRunning
          ? t`a whole-plan run is executing this plan`
          : epic.status !== 'active'
            ? t`plan is not active`
            : depsUnmet.length > 0
              ? t`waiting on phase ${waitingOn}`
              : t`run this phase headlessly in an isolated worktree`;
        const openPhase = (): void => {
          onOpenPhase(p.seq, 'story');
        };
        // Offered on the server's word alone — a `fail` phase reads `done` in the
        // pill and is still re-gradable, so both branches below render it.
        const again = reverifyAction(p, reverify, onReverify, i18n);
        const seq = String(p.seq);
        const name = p.name;
        return (
          <li
            key={p.id}
            role="button"
            tabIndex={0}
            aria-label={t`open Phase ${seq} — ${name} details`}
            onClick={openPhase}
            onKeyDown={(e) => {
              if (e.target !== e.currentTarget) return; // inner buttons handle their own keys
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                openPhase();
              }
            }}
            className="cursor-pointer rounded-lg border border-line bg-surface/40 px-3 py-2.5 transition-colors hover:border-line-strong focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                {/* Wraps: in a narrow card the activity note drops under the
                    name instead of running beneath the run chips beside it. */}
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="shrink-0 font-mono text-[10px] text-ink-faint">
                    <Trans>Phase {seq}</Trans>
                  </span>
                  {/* The row holds buttons, so the anchor is the NAME, not the row
                      (no <a> around inner buttons). Its click is the Link's push;
                      stopPropagation keeps the row from opening it a second time. */}
                  <Link
                    to={phaseHref(p.seq)}
                    onClick={(e) => e.stopPropagation()}
                    className="truncate text-[13px] font-medium text-ink"
                  >
                    {p.name}
                  </Link>
                  <span
                    className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9px] ${chip.cls}`}
                  >
                    {i18n._(chip.label)}
                  </span>
                  {status === 'in_progress' && (
                    <PhaseActivity docUpdatedAt={p.docUpdatedAt} running={running} />
                  )}
                  <TerminalBadge
                    sessions={terminalSessions.filter((s) => s.phaseId === String(p.id))}
                    scope="phase"
                  />
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5">
                  {p.dependsOn.map((seq) => (
                    <span
                      key={seq}
                      className={`rounded border px-1 py-px font-mono text-[9px] ${
                        resolvedSeqs.has(seq)
                          ? 'border-green/40 text-green'
                          : 'border-line text-ink-faint'
                      }`}
                      data-tip={resolvedSeqs.has(seq) ? t`dependency resolved` : t`dependency pending`}
                    >
                      ← #{seq}
                    </span>
                  ))}
                  <span className="font-mono text-[10px] text-ink-faint">
                    {p.checkboxesDone}/{p.checkboxesTotal || 0}
                  </span>
                </div>
                <ProgressBar done={p.checkboxesDone} total={p.checkboxesTotal} className="mt-2 max-w-[220px]" />
              </div>

              <div className="flex shrink-0 flex-col items-end gap-1.5">
                {/* Legacy chip: phases activated into a board task before the plan↔board
                    decoupling still show their board column for historical context. */}
                {activated && (
                  <span
                    className="rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
                    data-tip={p.boardTaskExternalId ?? undefined}
                  >
                    <Trans>activated</Trans>
                    {p.boardColumn !== null ? ` · ${p.boardColumn}` : ''}
                  </span>
                )}

                {/* Direct phase run (no board task): running shows elapsed +
                    Cancel + session link; a DONE phase retires the Run button
                    and offers ✓ summary (opens the details rail); idle/failed
                    offer Run/Retry. */}
                <ContinuationChip events={p.runEvents} />
                <SurpriseChip phase={p} onOpen={() => onOpenPhase(p.seq, 'story')} />

                {p.runState === 'running' ? (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <span
                      className="inline-flex items-center gap-1 rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
                      data-tip={t`headless run in progress`}
                    >
                      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-brand" />
                      <Trans>Running</Trans>
                      {p.runStartedAt !== null ? ` · ${fmtElapsed(p.runStartedAt, now)}` : ''}
                    </span>
                    {p.runSessionUuid !== null && (
                      <Link
                        to={sessionHref(p.runSessionUuid)}
                        onClick={(e) => e.stopPropagation()}
                        className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
                      >
                        <Trans>session</Trans>
                      </Link>
                    )}
                    <button
                      type="button"
                      disabled={runBusy !== null}
                      onClick={(e) => {
                        e.stopPropagation();
                        onCancelRun(p.id);
                      }}
                      className="rounded-md border border-red/40 px-1.5 py-px font-mono text-[9.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      <Trans>Cancel</Trans>
                    </button>
                  </span>
                ) : status === 'done' ? (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <RunModelChip phase={p} />
                    <DocModelChip phase={p} picked={phaseRunModel} />
                    {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
                    {isUnresolvedOutcome(p.runOutcome) && (
                      <RunOutcomeChip
                        phase={p}
                        outcome={p.runOutcome}
                        onOpen={() => onOpenOutcome(p.id)}
                      />
                    )}
                    <ReverifyButton action={again} size="row" />
                    <button
                      type="button"
                      onClick={() => {
                        onOpenPhase(p.seq, 'report');
                      }}
                      data-tip={t`what was done — the executor's Completion Report and execution record`}
                      className="font-mono text-[9.5px] text-green underline-offset-2 transition-colors hover:underline"
                    >
                      <Trans>✓ summary</Trans>
                    </button>
                  </span>
                ) : (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <RunModelChip phase={p} />
                    <DocModelChip phase={p} picked={phaseRunModel} />
                    {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
                    {isUnresolvedOutcome(p.runOutcome) && (
                      <RunOutcomeChip
                        phase={p}
                        outcome={p.runOutcome}
                        onOpen={() => onOpenOutcome(p.id)}
                      />
                    )}
                    <ReverifyButton action={again} size="row" />
                    <button
                      type="button"
                      disabled={runDisabled}
                      data-tip={runTitle}
                      onClick={(e) => {
                        e.stopPropagation();
                        onRun(p.id);
                      }}
                      className={`rounded-md border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint ${runButtonCls(p.runOutcome)}`}
                    >
                      {runBusy === p.id
                        ? '…'
                        : isUnresolvedOutcome(p.runOutcome)
                          ? t`Retry run`
                          : t`Run phase`}
                    </button>
                  </span>
                )}
                {/* A done phase is history: its doc describes work already
                    shipped, so editing it would rewrite the record rather than
                    steer the work. The ✓ summary button above is the only
                    affordance it keeps. */}
                {status !== 'done' && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation();
                      onOpenPhase(p.seq, 'edit');
                    }}
                    className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-ink hover:underline"
                  >
                    <Trans>edit doc</Trans>
                  </button>
                )}
              </div>
            </div>
          </li>
        );
      })}
    </ol>
  );
}
