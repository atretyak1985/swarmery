// The phase ledger (handoff §6): one line per phase, in the design's grid, so a
// plan's state reads top to bottom. A click on a line opens it in place to its runs,
// its criteria and every action the phase offers; the phase's name opens the phase's
// own page. The running phase, else the next runnable one, is open until the operator
// picks another: that is the one a person came to look at. What a run started now
// will use is on the plan's metadata line, above the ledger.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import type { Epic, EpicPhase, ProviderTerms, Session, TerminalSession } from '../../api/types';
import { type GlyphState, StateGlyph } from '../../components/place/StateGlyph';
import type { OffPlanBand } from '../../lib/offPlan';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { modelShortName } from '../../lib/sessionModelChip';
import { compactSpan } from '../../lib/sessionsView';
import { isUnresolvedOutcome, ReverifyButton, RunCompletedChip, RunOutcomeChip } from './phaseChips';
import {
  defaultOpenSeq,
  phaseCost,
  phaseCostBand,
  phaseGlyph,
  phaseLead,
  phaseNote,
  phaseRuns,
  phaseStage,
  phaseVerdict,
  type PhaseVerdict,
  sessionGlyph,
  stageLabel,
  type PhaseStage,
} from './phaseLedgerModel';
import {
  DocModelChip,
  PhaseRunEffortPicker,
  PhaseRunModelPicker,
  RunModelChip,
  type PhaseRunEffort,
  type PhaseRunModel,
} from './PhaseRunPickers';
import { fmtSpent, phaseNeedsYou, phasesLanded, phaseStatus, type PhaseStatus } from './planModel';
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

const RUN_WORD: Partial<Record<GlyphState, MessageDescriptor>> = {
  needs: msg`needs you`,
  working: msg`running`,
  done: msg`finished`,
  failed: msg`failed`,
  stopped: msg`stopped`,
  pending: msg`queued`,
};

/** A run on an open line. Its title is the prompt it was started with, so the line
 * says what the run is: the role it played, then how it stands. */
function LedgerRun({ run, now }: { run: Session; now: number }): JSX.Element {
  const { t, i18n } = useLingui();
  const sessionHref = useSessionHref();
  const state = sessionGlyph(run.status);
  const facts = [
    run.model === null ? null : modelShortName(run.model),
    run.costUsd == null ? null : fmtSpent(run.costUsd),
    runSpan(run, now),
  ].filter((fact) => fact !== null);
  return (
    <li>
      <Link
        to={sessionHref(run.sessionUuid)}
        className="grid grid-cols-[18px_minmax(0,1fr)] items-start gap-3 py-1.5 leading-[normal] transition-colors hover:text-brand"
      >
        <StateGlyph state={state} size={12} />
        <span className="min-w-0">
          <span className="block truncate text-[12.5px] text-ink-2">
            {run.planGroup?.role === 'controller' ? t`controller` : t`executor`}{' '}
            <span className="text-ink-3">· {i18n._(RUN_WORD[state] ?? msg`queued`)}</span>
          </span>
          <span className="mt-0.5 block truncate font-mono text-[10.5px] text-ink-faint">{facts.join(' · ')}</span>
        </span>
      </Link>
    </li>
  );
}

const LINE_BUTTON = 'rounded-[7px] border px-3 py-1.5 font-mono text-[11.5px] leading-[normal] whitespace-nowrap transition-colors';
const LINE_PLAIN = `${LINE_BUTTON} border-line-strong text-ink-2 hover:border-brand hover:text-brand`;
const LINE_ACCENT = `${LINE_BUTTON} border-brand bg-brand-bg font-bold text-brand disabled:cursor-not-allowed disabled:opacity-50`;
const VERDICT_TONE: Record<PhaseVerdict['tone'], string> = {
  red: 'text-red',
  green: 'text-green',
  brand: 'text-brand',
  faint: 'text-ink-faint',
};

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
  onRun: (phaseId: number) => void;
  reverify: Reverify;
  onReverify: (phaseId: number) => void;
  onOpenOutcome: (phaseId: number) => void;
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
}

/** The one next move, then the way into the phase. A run that has ended adds what is
 * known of it: its model, its outcome, the way to grade it again. While a phase runs or
 * waits on a decision there is nothing to add, and stopping it lives on its page. The
 * server re-checks every gate; the disable here is a courtesy and its tip says why. */
function PhaseActions({
  epic,
  phase: p,
  status,
  depsUnmet,
  runBusy,
  planRunning,
  phaseRunModel,
  onRun,
  reverify,
  onReverify,
  onOpenOutcome,
  phaseHref,
}: PhaseActionProps): JSX.Element {
  const { t, i18n } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const sessionHref = useSessionHref();
  const stage = phaseStage(p, status);
  const waitingOn = depsUnmet.join(', ');
  const runDisabled = runBusy !== null || planRunning || epic.status !== 'active' || depsUnmet.length > 0;
  const runTitle = planRunning
    ? t`a whole-plan run is executing this plan`
    : epic.status !== 'active'
      ? t`plan is not active`
      : depsUnmet.length > 0
        ? t`waiting on phase ${waitingOn}`
        : t`run this phase headlessly in an isolated worktree`;
  const needsHref = `${slug === undefined ? '' : `/p/${encodeURIComponent(slug)}`}/needs-you`;
  return (
    <>
      {stage === 'decision' ? (
        <Link to={needsHref} className={LINE_ACCENT}>
          <Trans>answer in Needs you</Trans>
        </Link>
      ) : stage === 'manual' ? (
        <Link to={phaseHref(p.seq, 'criteria')} className={LINE_ACCENT}>
          <Trans>tick criteria</Trans>
        </Link>
      ) : p.runState === 'running' ? (
        p.runSessionUuid !== null && (
          <Link to={sessionHref(p.runSessionUuid)} className={LINE_PLAIN}>
            <Trans>open run</Trans>
          </Link>
        )
      ) : (
        status !== 'done' &&
        stage !== 'review' && (
          <button
            type="button"
            disabled={runDisabled}
            data-tip={runTitle}
            onClick={() => onRun(p.id)}
            className={LINE_ACCENT}
          >
            {runBusy === p.id ? '…' : isUnresolvedOutcome(p.runOutcome) ? t`Retry run` : t`Run phase`}
          </button>
        )
      )}
      <Link to={phaseHref(p.seq, 'story')} className={LINE_PLAIN}>
        <Trans>open phase →</Trans>
      </Link>
      {stage !== 'decision' && p.runState !== 'running' && (
        <>
          <RunModelChip phase={p} />
          <DocModelChip phase={p} picked={phaseRunModel} />
          {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
          {isUnresolvedOutcome(p.runOutcome) && (
            <RunOutcomeChip phase={p} outcome={p.runOutcome} onOpen={() => onOpenOutcome(p.id)} />
          )}
          <ReverifyButton action={reverifyAction(p, reverify, onReverify, i18n)} size="row" />
        </>
      )}
    </>
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

  const total = epic.phases.length;
  const landed = phasesLanded(epic, resolvedSeqs);

  return (
    <div>
      <div className="flex items-baseline gap-2 font-mono text-[10px] leading-[normal] text-ink-faint">
        <span className="tracking-[0.12em] uppercase">
          <Trans>Phases</Trans>
        </span>
        <span>{t`${total} · ${landed} landed`}</span>
        <span className="flex-1" />
        <span className="hidden desk:inline">
          <Trans>click a name to open · ▸ to expand runs</Trans>
        </span>
      </div>
      <div
        className={`${GRID} mt-2.5 border-b border-line pb-1.5 font-mono text-[10px] leading-[normal] tracking-[0.1em] text-ink-faint uppercase`}
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
          const verdict = phaseVerdict(p, status, runs.length > 0);
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
                <div className="border-b border-line pt-1.5 pb-3 pl-7 desk:pl-[58px]">
                  <p className="m-0 max-w-[72ch] text-[13px] leading-[1.55] text-ink-2">
                    {phaseLead(p, status, depsUnmet, phaseNote(p, status, depsUnmet, elapsed, terms?.changeShort ?? null))}
                  </p>
                  {sessions !== null && runs.length > 0 && (
                    <ol aria-label={t`runs of phase ${seq}`} className="mt-1.5">
                      {runs.map((run) => (
                        <LedgerRun key={run.id} run={run} now={now} />
                      ))}
                    </ol>
                  )}
                  <div className="mt-2.5 flex flex-wrap items-center gap-2">
                    <PhaseActions
                      {...actions}
                      epic={epic}
                      phase={p}
                      status={status}
                      depsUnmet={depsUnmet}
                      phaseHref={phaseHref}
                    />
                    {verdict !== null && (
                      <span className={`ml-auto font-mono text-[10.5px] leading-[normal] whitespace-nowrap ${VERDICT_TONE[verdict.tone]}`}>
                        {verdict.text}
                      </span>
                    )}
                  </div>
                  {/* They govern the Run above, so they show where starting a run is
                      the next move. */}
                  {status !== 'done' && p.runState !== 'running' && !phaseNeedsYou(p) && phaseStage(p, status) !== 'review' && (
                    <div className="mt-2 flex flex-wrap items-center justify-end gap-x-3 gap-y-1">
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
    </div>
  );
}
