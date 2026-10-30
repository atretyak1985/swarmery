// The phase ledger (handoff §6): one line per phase, in the design's grid, so a
// plan's state reads top to bottom. A click on a line opens it in place to its runs,
// its criteria and every action the phase offers; the phase's name opens the phase's
// own page. The running phase, else the next runnable one, is open until the operator
// picks another: that is the one a person came to look at. Under the ledger, what a
// run started now will use.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import type { Epic, EpicPhase, ProviderTerms, Session, TerminalSession } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import type { OffPlanBand } from '../../lib/offPlan';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { modelShortName } from '../../lib/sessionModelChip';
import { compactSpan } from '../../lib/sessionsView';
import { SurpriseChip } from './ForecastVsActual';
import { showsReviewTab } from './landingModel';
import { ContinuationChip, isUnresolvedOutcome, PhaseActivity, ReverifyButton, RunCompletedChip, runButtonCls, RunOutcomeChip } from './phaseChips';
import {
  criteriaLine,
  defaultOpenSeq,
  phaseCost,
  phaseCostBand,
  phaseGlyph,
  phaseNote,
  phaseRuns,
  phaseStage,
  sessionGlyph,
  stageLabel,
  type PhaseStage,
} from './phaseLedgerModel';
import {
  DocModelChip,
  PhaseRunEffortPicker,
  phaseRunEffortLine,
  phaseRunModelLine,
  PhaseRunModelPicker,
  RunModelChip,
  type PhaseRunEffort,
  type PhaseRunModel,
} from './PhaseRunPickers';
import { fmtSpent, phaseNeedsYou, phaseStatus, type PhaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { TerminalBadge } from './TerminalBadge';
import { reverifyAction, type Reverify } from './useReverify';

/** Below `desk` the stage and the cost drop out: the note still says where it is. */
const GRID =
  'grid grid-cols-[18px_24px_minmax(0,1fr)_40px] gap-[10px] desk:grid-cols-[18px_24px_minmax(0,1.7fr)_minmax(56px,0.4fr)_minmax(88px,0.7fr)_40px]';

export const STAGE_TONE: Record<PhaseStage, string> = {
  decision: 'text-brand',
  manual: 'text-brand',
  review: 'text-green',
  landed: 'text-ink-faint',
  exec: 'text-ink-faint',
  none: 'text-ink-faint',
};

const BAND_FILL: Record<OffPlanBand, string> = {
  'on plan': 'bg-green',
  'off plan': 'bg-amber', // i18n-ignore — band key
  'far off plan': 'bg-red', // i18n-ignore — band key
};

function StageLabel({ stage }: { stage: PhaseStage }): JSX.Element {
  useLingui();
  return (
    <span className={`hidden font-mono text-[11px] leading-[normal] desk:block ${STAGE_TONE[stage]}`}>
      {stageLabel(stage)}
    </span>
  );
}

/** The cost and a bar of its share against the plan's costliest phase. No tick: a
 * forecast in dollars does not exist (plan D-5). */
function CostCell({ cost, maxCost, band }: { cost: number | null; maxCost: number; band: OffPlanBand | null }): JSX.Element {
  const width = cost === null || maxCost === 0 ? 0 : Math.round((cost / maxCost) * 100);
  return (
    <div className="hidden flex-col gap-[3px] desk:flex">
      <span className="font-mono text-[11px] leading-[normal] text-ink-dim">{cost === null ? '—' : fmtSpent(cost)}</span>
      <span className="relative block h-1 overflow-hidden rounded-[2px] bg-line">
        <span
          className={`absolute top-0 left-0 h-full ${band === null ? 'bg-ink-faint' : BAND_FILL[band]}`}
          style={{ width: `${String(width)}%` }}
        />
      </span>
    </div>
  );
}

/** How long a run took, or has been going. */
export function runSpan(run: Session, now: number): string {
  const end = run.endedAt === null ? now : new Date(run.endedAt).getTime();
  return compactSpan(end - new Date(run.startedAt).getTime());
}

/** One run. `ruled` is the phase page's line: larger, with a hairline under it. */
export function RunRow({ run, now, ruled = false }: { run: Session; now: number; ruled?: boolean }): JSX.Element {
  const sessionHref = useSessionHref();
  const label = run.title ?? run.why ?? run.sessionUuid.slice(0, 8);
  const span = runSpan(run, now);
  return (
    <li>
      <Link
        to={sessionHref(run.sessionUuid)}
        className={`grid grid-cols-[18px_minmax(0,1fr)_auto] items-start gap-3 leading-[normal] transition-colors hover:text-brand ${
          ruled ? 'border-b border-line py-[9px]' : 'py-1.5'
        }`}
      >
        <StateGlyph state={sessionGlyph(run.status)} size={12} />
        <span className="min-w-0">
          <span className={`block truncate ${ruled ? 'text-[13px] font-medium text-ink' : 'text-[12.5px] text-ink-2'}`}>
            {label}
          </span>
          {run.model !== null && (
            <span className="mt-0.5 block font-mono text-[10.5px] text-ink-faint">{modelShortName(run.model)}</span>
          )}
        </span>
        <span className="text-right font-mono text-[10.5px] whitespace-nowrap text-ink-faint">
          {run.costUsd != null && <span className="text-ink-dim">{fmtSpent(run.costUsd)} · </span>}
          {span}
        </span>
      </Link>
    </li>
  );
}

interface PhaseActionProps {
  epic: Epic;
  phase: EpicPhase;
  status: PhaseStatus;
  now: number;
  depsUnmet: readonly number[];
  runBusy: number | null;
  planRunning: boolean;
  phaseRunModel: PhaseRunModel;
  onPhaseRunModel: (m: PhaseRunModel) => void;
  phaseRunEffort: PhaseRunEffort;
  onPhaseRunEffort: (e: PhaseRunEffort) => void;
  onOpenPhase: (seq: number, tab: PhaseDetailTab) => void;
  onRun: (phaseId: number) => void;
  onCancelRun: (phaseId: number) => void;
  reverify: Reverify;
  onReverify: (phaseId: number) => void;
  onOpenOutcome: (phaseId: number) => void;
}

/** Everything the phase offers beyond its runs. The server re-checks every gate; the
 * disable here is a courtesy and its tip says why. */
function PhaseActions({
  epic,
  phase: p,
  status,
  depsUnmet,
  runBusy,
  planRunning,
  phaseRunModel,
  onOpenPhase,
  onRun,
  onCancelRun,
  reverify,
  onReverify,
  onOpenOutcome,
}: PhaseActionProps): JSX.Element {
  const { t, i18n } = useLingui();
  const sessionHref = useSessionHref();
  const running = p.runState === 'running';
  const waitingOn = depsUnmet.join(', ');
  const runDisabled = runBusy !== null || planRunning || epic.status !== 'active' || depsUnmet.length > 0;
  const runTitle = planRunning
    ? t`a whole-plan run is executing this plan`
    : epic.status !== 'active'
      ? t`plan is not active`
      : depsUnmet.length > 0
        ? t`waiting on phase ${waitingOn}`
        : t`run this phase headlessly in an isolated worktree`;
  // Offered on the server's word alone: a `fail` phase reads done and is still
  // re-gradable, so every branch below renders it.
  const again = reverifyAction(p, reverify, onReverify, i18n);
  const outcome = (
    <>
      <RunModelChip phase={p} />
      <DocModelChip phase={p} picked={phaseRunModel} />
      {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
      {isUnresolvedOutcome(p.runOutcome) && (
        <RunOutcomeChip phase={p} outcome={p.runOutcome} onOpen={() => onOpenOutcome(p.id)} />
      )}
      <ReverifyButton action={again} size="row" />
    </>
  );
  return (
    // The chips are taller than the criteria line and must not push the rows below.
    <div className="-my-1 ml-auto flex flex-wrap items-center gap-1.5">
      {/* Legacy chip: phases activated into a board task before the plan↔board
          decoupling still show their board column for historical context. */}
      {p.activatedAt !== null && (
        <span
          className="rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
          data-tip={p.boardTaskExternalId ?? undefined}
        >
          <Trans>activated</Trans>
          {p.boardColumn !== null ? ` · ${p.boardColumn}` : ''}
        </span>
      )}
      <ContinuationChip events={p.runEvents} />
      <SurpriseChip phase={p} onOpen={() => onOpenPhase(p.seq, 'story')} />
      {status === 'in_progress' && <PhaseActivity docUpdatedAt={p.docUpdatedAt} running={running} />}
      {running ? (
        <>
          {/* The line's note says how long it has run. */}
          {p.runSessionUuid !== null && (
            <Link
              to={sessionHref(p.runSessionUuid)}
              data-tip={t`headless run in progress`}
              className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
            >
              <Trans>session</Trans>
            </Link>
          )}
          <button
            type="button"
            disabled={runBusy !== null}
            onClick={() => onCancelRun(p.id)}
            className="rounded-md border border-red/40 px-1.5 py-px font-mono text-[9.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Trans>Cancel</Trans>
          </button>
        </>
      ) : status === 'done' ? (
        <>
          {outcome}
          <button
            type="button"
            onClick={() => onOpenPhase(p.seq, 'report')}
            data-tip={t`what was done — the executor's Completion Report and execution record`}
            className="font-mono text-[9.5px] text-green underline-offset-2 transition-colors hover:underline"
          >
            <Trans>✓ summary</Trans>
          </button>
        </>
      ) : (
        <>
          {outcome}
          <button
            type="button"
            disabled={runDisabled}
            data-tip={runTitle}
            onClick={() => onRun(p.id)}
            className={`rounded-md border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint ${runButtonCls(p.runOutcome)}`}
          >
            {runBusy === p.id ? '…' : isUnresolvedOutcome(p.runOutcome) ? t`Retry run` : t`Run phase`}
          </button>
        </>
      )}
    </div>
  );
}

/** The links into the phase's details. A done phase is history: its doc describes
 * work already shipped, so it offers no Edit. */
function PhaseLinks({
  phase,
  status,
  phaseHref,
}: {
  phase: EpicPhase;
  status: PhaseStatus;
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
}): JSX.Element {
  const { t } = useLingui();
  const links: { tab: PhaseDetailTab; label: string }[] = [
    { tab: 'story', label: t`story` },
    { tab: 'runs', label: t`runs` },
    ...(showsReviewTab(phase) ? [{ tab: 'review' as const, label: t`review & land` }] : []),
    { tab: 'report', label: t`report` },
    ...(status === 'done' ? [] : [{ tab: 'edit' as const, label: t`edit doc` }]),
  ];
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1 font-mono text-[10.5px] leading-[normal] text-ink-faint">
      {links.map(({ tab, label }) => (
        <Link key={tab} to={phaseHref(phase.seq, tab)} className="transition-colors hover:text-brand">
          {label}
        </Link>
      ))}
    </div>
  );
}

export function PhaseLedger({
  epic,
  resolvedSeqs,
  sessions,
  terminalSessions,
  terms,
  phaseHref,
  ...actions
}: Omit<PhaseActionProps, 'phase' | 'status' | 'depsUnmet'> & {
  resolvedSeqs: Set<number>;
  /** The plan's sessions; null until they load. */
  sessions: readonly Session[] | null;
  /** The plan's terminal sessions; a phase shows the ones that picked it. */
  terminalSessions: readonly TerminalSession[];
  /** The project's code-host vocabulary; null until it loads. */
  terms: ProviderTerms | null;
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
}): JSX.Element {
  const { t } = useLingui();
  const { now } = actions;
  // Keyed by plan like the rest of this page's picks, so it cannot leak to another
  // plan and needs no reset. `seq`, because a rescan churns phase ids.
  const [picked, setPicked] = useState<{ taskId: number; seq: number | null } | null>(null);
  const openSeq =
    picked !== null && picked.taskId === epic.taskId
      ? picked.seq
      : defaultOpenSeq(epic.phases, resolvedSeqs, epic.status === 'active');

  const costs = epic.phases.map((p) => phaseCost(phaseRuns(sessions, p.id)));
  const maxCost = Math.max(0, ...costs.flatMap((c) => (c === null ? [] : [c])));

  return (
    <div>
      <div
        className={`${GRID} border-b border-line pb-1.5 font-mono text-[10px] leading-[normal] tracking-[0.1em] text-ink-faint uppercase`}
      >
        <span />
        <span>#</span>
        <span>
          <Trans>phase</Trans>
        </span>
        <span className="hidden desk:block">
          <Trans>stage</Trans>
        </span>
        <span className="hidden desk:block">
          <Trans>cost</Trans>
        </span>
        <span className="text-right">
          <Trans>runs</Trans>
        </span>
      </div>
      <ol>
        {epic.phases.map((p, i) => {
          const status = phaseStatus(p, resolvedSeqs);
          const depsUnmet = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
          const runs = phaseRuns(sessions, p.id);
          const open = openSeq === p.seq;
          const pending = status === 'pending' || status === 'blocked';
          const elapsed = p.runStartedAt === null ? '' : fmtElapsed(p.runStartedAt, now);
          const seq = String(p.seq);
          const name = p.name;
          const toggle = (): void => setPicked({ taskId: epic.taskId, seq: open ? null : p.seq });
          return (
            <li key={p.id}>
              {/* The row holds a link, so the anchor is the NAME, not the row. */}
              <div
                role="button"
                tabIndex={0}
                aria-expanded={open}
                aria-label={t`Phase ${seq} — ${name}`}
                onClick={toggle}
                onKeyDown={(e) => {
                  if (e.target !== e.currentTarget) return; // the name handles its own keys
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    toggle();
                  }
                }}
                className={`${GRID} cursor-pointer items-center border-b border-line py-[10px] transition-colors hover:bg-surface2 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand ${
                  open ? 'bg-surface2' : ''
                }`}
              >
                <StateGlyph state={phaseGlyph(p, status)} size={12} />
                <span className="font-mono text-[11px] leading-[normal] text-ink-faint">{seq}</span>
                <div className="min-w-0">
                  <div className="flex min-w-0 items-baseline gap-2">
                    <Link
                      to={phaseHref(p.seq, 'story')}
                      onClick={(e) => e.stopPropagation()}
                      title={t`open phase`}
                      className={`min-w-0 text-[13px] leading-[1.35] font-medium text-pretty transition-colors hover:text-brand ${
                        pending ? 'text-ink-dim' : 'text-ink'
                      }`}
                    >
                      {name}
                    </Link>
                    {/* A live signal, so it stays on the line rather than in the expansion. */}
                    <span className="shrink-0" onClick={(e) => e.stopPropagation()}>
                      <TerminalBadge sessions={terminalSessions.filter((s) => s.phaseId === String(p.id))} scope="phase" />
                    </span>
                  </div>
                  <div className={`mt-0.5 truncate text-[12px] leading-[normal] ${phaseNeedsYou(p) ? 'text-brand' : 'text-ink-dim'}`}>
                    {phaseNote(p, status, depsUnmet, elapsed, terms?.changeShort ?? null)}
                  </div>
                </div>
                <StageLabel stage={phaseStage(p, status)} />
                <CostCell cost={costs[i] ?? null} maxCost={maxCost} band={phaseCostBand(p)} />
                <span className="text-right font-mono text-[11px] leading-[normal] text-ink-faint">
                  {runs.length > 0 ? `${String(runs.length)} ` : ''}
                  {open ? '▾' : '▸'}
                </span>
              </div>
              {open && (
                <div className="border-b border-line pt-1 pb-[10px] pl-7 desk:pl-[58px]">
                  {sessions === null ? null : runs.length === 0 ? (
                    <div className="py-1.5 font-mono text-[10.5px] leading-[normal] text-ink-faint">
                      <Trans>no runs yet</Trans>
                    </div>
                  ) : (
                    <ol aria-label={t`runs of phase ${seq}`}>
                      {runs.map((run) => (
                        <RunRow key={run.id} run={run} now={now} />
                      ))}
                    </ol>
                  )}
                  {p.dependsOn.length > 0 && (
                    <div className="mt-1.5 flex flex-wrap gap-1.5 font-mono text-[10.5px] leading-[normal] text-ink-faint">
                      <Trans>depends on</Trans>
                      {p.dependsOn.map((dep) => (
                        <span
                          key={dep}
                          className={resolvedSeqs.has(dep) ? 'text-green' : undefined}
                          data-tip={resolvedSeqs.has(dep) ? t`dependency resolved` : t`dependency pending`}
                        >
                          #{dep}
                        </span>
                      ))}
                    </div>
                  )}
                  {/* One line, as the design's criteria line is: a running phase's
                      expansion keeps the design's height. */}
                  <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
                    {/* The criteria tab is where they are ticked. */}
                    <Link
                      to={phaseHref(p.seq, 'criteria')}
                      className="font-mono text-[10.5px] leading-[normal] text-ink-faint transition-colors hover:text-brand"
                    >
                      <Trans>criteria</Trans> · {criteriaLine(p)}
                    </Link>
                    <PhaseLinks phase={p} status={status} phaseHref={phaseHref} />
                    <PhaseActions {...actions} epic={epic} phase={p} status={status} depsUnmet={depsUnmet} />
                  </div>
                  {/* They govern exactly the Run and Retry on the line above, so they
                      show where starting a run is the next move: not while the phase
                      waits on the operator. An open phase has them too. */}
                  {status !== 'done' && p.runState !== 'running' && !phaseNeedsYou(p) && (
                    <div className="mt-1.5 flex flex-wrap items-center justify-end gap-x-3 gap-y-1">
                      <PhaseRunModelPicker
                        value={actions.phaseRunModel}
                        onChange={actions.onPhaseRunModel}
                        disabled={actions.runBusy !== null || actions.planRunning}
                      />
                      <PhaseRunEffortPicker
                        value={actions.phaseRunEffort}
                        onChange={actions.onPhaseRunEffort}
                        disabled={actions.runBusy !== null || actions.planRunning}
                      />
                    </div>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ol>
      <div className="mt-3 flex flex-wrap gap-x-[22px] gap-y-1.5 font-mono text-[10.5px] leading-[normal] text-ink-faint">
        <span>
          {t`run model`} · <span className="text-ink-dim">{phaseRunModelLine(actions.phaseRunModel)}</span>
        </span>
        <span>
          {t`effort`} · <span className="text-ink-dim">{phaseRunEffortLine(actions.phaseRunEffort)}</span>
        </span>
        <span>
          {t`isolation`} · <span className="text-ink-dim">{t`worktree per phase`}</span>
        </span>
      </div>
    </div>
  );
}
