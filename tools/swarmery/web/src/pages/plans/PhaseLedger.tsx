// The phase ledger (handoff §6): one line per phase, in the design's grid, so a
// plan's state reads top to bottom. A line opens the phase's own page, where its runs,
// criteria and every action it offers live. What a run started now will use is on the
// plan's metadata line, above the ledger.

import { Trans, useLingui } from '@lingui/react/macro';
import { Link, useNavigate } from 'react-router-dom';
import type { Epic, ProviderTerms, Session, TerminalSession } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import type { OffPlanBand } from '../../lib/offPlan';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { modelShortName } from '../../lib/sessionModelChip';
import { compactSpan } from '../../lib/sessionsView';
import {
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
import { fmtSpent, phaseNeedsYou, phasesLanded, phaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { TerminalBadge } from './TerminalBadge';

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

export function PhaseLedger({
  epic,
  resolvedSeqs,
  sessions,
  terminalSessions,
  terms,
  phaseHref,
  now,
}: {
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
        <span className="text-right">
          <Trans>runs</Trans>
        </span>
      </div>
      <ol>
        {epic.phases.map((p, i) => {
          const status = phaseStatus(p, resolvedSeqs);
          const depsUnmet = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
          const runs = phaseRuns(sessions, p.id);
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
                  <div className={`mt-0.5 truncate text-[12px] leading-[normal] ${phaseNeedsYou(p) ? 'text-brand' : 'text-ink-dim'}`}>
                    {phaseNote(p, status, depsUnmet, elapsed, terms?.changeShort ?? null)}
                  </div>
                </div>
                <StageLabel stage={phaseStage(p, status)} />
                <CostCell cost={costs[i] ?? null} maxCost={maxCost} band={phaseCostBand(p)} />
                <span className="text-right font-mono text-[11px] leading-[normal] text-ink-faint">
                  {runs.length > 0 ? String(runs.length) : ''}
                </span>
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
