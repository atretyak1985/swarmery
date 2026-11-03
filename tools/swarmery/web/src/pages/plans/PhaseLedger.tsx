// The phase ledger (handoff §6): one line per phase, in the design's grid, so a
// plan's state reads top to bottom. A line opens the phase's own page, where its runs,
// criteria and every action it offers live. The criteria column counts ticked of all
// acceptance criteria and links to them. The last column carries the one action the
// line needs now (LedgerAction): run or retry, stop, answer, tick, or the green link
// to a landed phase's report. What a run started now will use is on the plan's
// metadata line, above the ledger.

import { Trans, useLingui } from '@lingui/react/macro';
import { Link, useNavigate } from 'react-router-dom';
import type { Epic, EpicPhase, ProviderTerms, Session, TerminalSession } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import type { OffPlanBand } from '../../lib/offPlan';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { modelShortName } from '../../lib/sessionModelChip';
import { compactSpan } from '../../lib/sessionsView';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { isUnresolvedOutcome } from './phaseChips';
import {
  heldByDecision,
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
import { fmtSpent, phaseNeedsYou, phasesLanded, phaseStatus, type PhaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { TerminalBadge } from './TerminalBadge';

/** Below `desk` the stage and the cost drop out: the note still says where it is. */
const GRID =
  'grid grid-cols-[18px_24px_minmax(0,1fr)_auto] gap-[10px] desk:grid-cols-[18px_24px_minmax(0,1fr)_72px_56px_48px_minmax(92px,auto)]';

const PILL =
  'inline-flex items-center rounded-md border px-2 py-[3px] font-mono text-[11px] leading-[normal] whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50';

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

/** Ticked of all acceptance criteria. Green once every one is ticked, brand while a
 * `[MANUAL]` one waits on a person. A link to the phase's criteria tab. */
function CriteriaCell({
  phase,
  href,
}: {
  phase: EpicPhase;
  href: string;
}): JSX.Element {
  const { t } = useLingui();
  const { checkboxesDone: done, checkboxesTotal: total, manualOpen } = phase;
  if (total === 0) {
    return <span className="hidden font-mono text-[11px] leading-[normal] text-ink-faint desk:block">—</span>;
  }
  const tone = done >= total ? 'text-green' : manualOpen > 0 ? 'text-brand' : 'text-ink-dim';
  const tip =
    manualOpen > 0
      ? t`${done} of ${total} criteria ticked · ${manualOpen} [MANUAL] wait on you`
      : t`${done} of ${total} criteria ticked`;
  return (
    <Link
      to={href}
      onClick={(e) => e.stopPropagation()}
      data-tip={tip}
      aria-label={tip}
      className={`hidden font-mono text-[11px] leading-[normal] whitespace-nowrap underline-offset-2 hover:underline desk:block ${tone}`}
    >
      {done}/{total}
    </Link>
  );
}

export interface LedgerRunControls {
  /** The phase a run request is in flight for, or null. */
  runBusy: number | null;
  /** A whole-plan run owns the plan: no phase starts beside it. */
  planRunning: boolean;
  onRun: (phaseId: number) => void;
  onCancelRun: (phaseId: number) => void;
}

/** The one action a line needs now, or nothing when the phase can only wait. */
function LedgerAction({
  phase,
  status,
  depsUnmet,
  active,
  controls,
  phaseHref,
  needsHref,
}: {
  phase: EpicPhase;
  status: PhaseStatus;
  depsUnmet: readonly number[];
  active: boolean;
  controls: LedgerRunControls;
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
  needsHref: string;
}): JSX.Element | null {
  const { t } = useLingui();
  const { runBusy, planRunning, onRun, onCancelRun } = controls;
  const stop = (e: { stopPropagation: () => void }): void => e.stopPropagation();
  if (phaseNeedsYou(phase)) {
    return heldByDecision(phase) || phase.guardDecision != null ? (
      <Link to={needsHref} onClick={stop} className={`${PILL} border-brand/60 bg-brand-bg text-brand hover:border-brand`}>
        <Trans>answer</Trans>
      </Link>
    ) : (
      <Link
        to={phaseHref(phase.seq, 'criteria')}
        onClick={stop}
        className={`${PILL} border-brand/60 bg-brand-bg text-brand hover:border-brand`}
      >
        <Trans>tick criteria</Trans>
      </Link>
    );
  }
  if (phase.runState === 'running') {
    return (
      <button
        type="button"
        disabled={runBusy !== null}
        onClick={(e) => {
          stop(e);
          onCancelRun(phase.id);
        }}
        className={`${PILL} border-red/50 text-red hover:bg-red/10`}
      >
        {runBusy === phase.id ? '…' : <Trans>stop</Trans>}
      </button>
    );
  }
  if (status === 'done') {
    return (
      <Link
        to={phaseHref(phase.seq, 'report')}
        onClick={stop}
        data-tip={t`what was done — the executor's Completion Report and execution record`}
        className="font-mono text-[11px] leading-[normal] whitespace-nowrap text-green underline-offset-2 hover:underline"
      >
        <Trans>✓ report</Trans>
      </Link>
    );
  }
  if (!active || depsUnmet.length > 0) return null;
  const retry = isUnresolvedOutcome(phase.runOutcome);
  return (
    <button
      type="button"
      disabled={runBusy !== null || planRunning}
      data-tip={planRunning ? t`a whole-plan run is executing this plan` : t`run this phase headlessly in an isolated worktree`}
      onClick={(e) => {
        stop(e);
        onRun(phase.id);
      }}
      className={`${PILL} border-brand/60 text-brand hover:bg-brand-bg`}
    >
      {runBusy === phase.id ? '…' : retry ? <Trans>↻ retry</Trans> : <Trans>▶ run</Trans>}
    </button>
  );
}

/** Which phases this one waits for: a landed one in green, an open one faint. */
function DependsOn({ deps, resolvedSeqs }: { deps: readonly number[]; resolvedSeqs: Set<number> }): JSX.Element {
  const { t } = useLingui();
  return (
    <span className="flex shrink-0 items-baseline gap-1 font-mono text-[10.5px] leading-[normal] whitespace-nowrap text-ink-faint">
      <Trans>depends on</Trans>
      {deps.map((dep) => (
        <span
          key={dep}
          className={resolvedSeqs.has(dep) ? 'text-green' : 'text-ink-dim'}
          data-tip={resolvedSeqs.has(dep) ? t`dependency resolved` : t`dependency pending`}
        >
          #{dep}
        </span>
      ))}
    </span>
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

export function PhaseLedger({
  epic,
  resolvedSeqs,
  sessions,
  terminalSessions,
  terms,
  phaseHref,
  now,
  ...controls
}: LedgerRunControls & {
  epic: Epic;
  resolvedSeqs: Set<number>;
  /** The plan's sessions; null until they load. */
  sessions: readonly Session[] | null;
  /** The plan's terminal sessions; a phase shows the ones that picked it. */
  terminalSessions: readonly TerminalSession[];
  /** The project's code-host vocabulary; null until it loads. */
  terms: ProviderTerms | null;
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
  now: number;
}): JSX.Element {
  const { t } = useLingui();
  const navigate = useNavigate();
  const { slug } = useProjectWorkspace();
  const needsHref = `/p/${encodeURIComponent(slug)}/needs-you`;

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
        <span className="hidden desk:block">
          <Trans>criteria</Trans>
        </span>
        <span className="text-right">
          <Trans>action</Trans>
        </span>
      </div>
      <ol>
        {epic.phases.map((p, i) => {
          const status = phaseStatus(p, resolvedSeqs);
          const depsUnmet = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
          const pending = status === 'pending' || status === 'blocked';
          const elapsed = p.runStartedAt === null ? '' : fmtElapsed(p.runStartedAt, now);
          const seq = String(p.seq);
          const name = p.name;
          return (
            <li key={p.id}>
              {/* The whole row opens the phase; the name is its link, for the keyboard and
                  a new tab. */}
              <div
                onClick={() => navigate(phaseHref(p.seq, 'story'))}
                className={`${GRID} cursor-pointer items-center border-b border-line py-[10px] transition-colors hover:bg-surface2`}
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
                    {/* A live signal, and a control of its own: it does not open the phase. */}
                    <span className="shrink-0" onClick={(e) => e.stopPropagation()}>
                      <TerminalBadge sessions={terminalSessions.filter((s) => s.phaseId === String(p.id))} scope="phase" />
                    </span>
                  </div>
                  <div className="mt-0.5 flex min-w-0 items-baseline gap-2">
                    <span
                      className={`min-w-0 truncate text-[12px] leading-[normal] ${phaseNeedsYou(p) ? 'text-brand' : 'text-ink-dim'}`}
                    >
                      {phaseNote(p, status, depsUnmet, elapsed, terms?.changeShort ?? null)}
                    </span>
                    {p.dependsOn.length > 0 && <DependsOn deps={p.dependsOn} resolvedSeqs={resolvedSeqs} />}
                  </div>
                </div>
                <StageLabel stage={phaseStage(p, status)} />
                <CostCell cost={costs[i] ?? null} maxCost={maxCost} band={phaseCostBand(p)} />
                <CriteriaCell phase={p} href={phaseHref(p.seq, 'criteria')} />
                <span className="flex justify-end">
                  <LedgerAction
                    phase={p}
                    status={status}
                    depsUnmet={depsUnmet}
                    active={epic.status === 'active'}
                    controls={controls}
                    phaseHref={phaseHref}
                    needsHref={needsHref}
                  />
                </span>
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
