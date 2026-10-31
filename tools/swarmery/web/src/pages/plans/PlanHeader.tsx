// The head of a plan (handoff §6), one band with the blocker banner over it when
// phases wait on the operator, and a notice when the plan is paused or archived.
// Eyebrow, title and goal beside the actions; a bar of the phases, each segment
// coloured by its state and opening that phase, with a legend that counts only the
// states present; and a line of metadata. One button runs, pauses or resumes,
// whichever the state calls for; what the design leaves out of that row goes behind ⋯.
//
// Forecast has no number here on purpose: phases carry only size and duration bands,
// no dollars, so the metadata says what the API does have (plan D-5, D-10), and what
// a run started now would use.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import type { Epic, PlanRunMode, TerminalSession } from '../../api/types';
import type { EpicLifecycleAction } from '../../api';
import { CopyIdBadge } from '../../components/CopyIdBadge';
import { BlockerBanner, DetailHeader, NoticeBand } from '../../components/place/DetailHeader';
import { projectLabel } from '../../lib/format';
import { useProjectColor } from '../../lib/projectColors';
import { compactSpan } from '../../lib/sessionsView';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { phaseRunEffortLine, phaseRunModelLine, type PhaseRunEffort, type PhaseRunModel } from './PhaseRunPickers';
import { PlanRunDialog, PlanRunLive } from './PlanRunControls';
import { TerminalBadge } from './TerminalBadge';
import {
  blockerDetail,
  fmtSpent,
  needYouCount,
  planBranch,
  planGoal,
  planLegend,
  planSpent,
  planUpdatedAt,
  segmentLabel,
  segmentState,
  segmentWord,
  type SegmentState,
} from './planModel';
import { plansHref, type PlanDetailTab } from './plansUrl';
import { usePlanDoc } from './usePlanDoc';

/** The design's bar: state by colour, in the band's own vocabulary. */
const SEGMENT_TONE: Record<SegmentState, { fill: string; text: string }> = {
  landed: { fill: 'bg-green', text: 'text-green' },
  running: { fill: 'bg-blue', text: 'text-blue' },
  guard: { fill: 'bg-red', text: 'text-red' },
  decision: { fill: 'bg-red', text: 'text-red' },
  failed: { fill: 'bg-red', text: 'text-red' },
  manual: { fill: 'bg-amber', text: 'text-amber' },
  blocked: { fill: 'bg-ink-dim', text: 'text-ink-dim' },
  started: { fill: 'bg-ink-faint', text: 'text-ink-faint' },
  pending: { fill: 'bg-line-strong', text: 'text-line-strong' },
};

const BTN =
  'rounded-[7px] border px-3 py-1.5 font-mono text-[11.5px] leading-[normal] whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50';
const BTN_PLAIN = `${BTN} border-line-strong bg-transparent text-ink-2 hover:border-ink-faint hover:text-ink`;
/** The main action: filled when it starts something, an outline when it would stop it. */
const BTN_MAIN = `${BTN} border-brand bg-brand-bg font-bold text-brand`;
const BTN_STOPS = `${BTN} border-brand bg-transparent font-medium text-brand hover:bg-brand-bg`;

interface MenuItem {
  label: string;
  hint: string;
  disabled?: boolean;
  onSelect: () => void;
}

function PlanMenu({ items }: { items: readonly MenuItem[] }): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent): void => {
      if (!(e.target instanceof Node) || root.current?.contains(e.target) !== true) setOpen(false);
    };
    // In capture, ahead of Esc backing out of the plan's open details.
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey, true);
    };
  }, [open]);
  return (
    <span ref={root} className="relative inline-flex">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t`plan actions`}
        onClick={() => setOpen((v) => !v)}
        className={`${BTN} border-line-strong bg-transparent !px-[10px] text-ink-2 hover:border-ink-faint hover:text-ink`}
      >
        ⋯
      </button>
      {open && (
        <div
          role="menu"
          aria-label={t`plan actions`}
          className="absolute top-[calc(100%+6px)] right-0 z-30 flex min-w-[220px] flex-col rounded-[9px] border border-line-strong bg-surface p-1 shadow-[0_12px_30px_rgba(0,0,0,0.35)]"
        >
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              aria-label={item.label}
              disabled={item.disabled === true}
              onClick={() => {
                setOpen(false);
                item.onSelect();
              }}
              className="cursor-pointer rounded-[6px] border-none bg-transparent px-[10px] py-[7px] text-left text-[12.5px] leading-[normal] text-ink hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {item.label}
              <div className="text-[11px] text-ink-dim">{item.hint}</div>
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

export interface PlanHeaderProps {
  epic: Epic;
  resolvedSeqs: Set<number>;
  complete: boolean;
  now: number;
  /** Sessions with this plan open in a terminal. */
  terminal: TerminalSession[];
  busyLifecycle: boolean;
  onLifecycle: (epic: Epic, action: EpicLifecycleAction) => void;
  planRunBusy: boolean;
  phaseRunActive: boolean;
  onRunPlan: (agent: string, mode: PlanRunMode) => void;
  onCancelPlanRun: () => void;
  onOpenPlan: (tab: PlanDetailTab) => void;
  /** Revisions waiting for a decision. */
  stagedCount: number;
  reviseBusy: boolean;
  onRevise: () => void;
  /** What a phase run started now uses, as the ledger's pickers hold it. */
  phaseRunModel: PhaseRunModel;
  phaseRunEffort: PhaseRunEffort;
}

export function PlanHeader({
  epic,
  resolvedSeqs,
  complete,
  now,
  terminal,
  busyLifecycle,
  onLifecycle,
  planRunBusy,
  phaseRunActive,
  onRunPlan,
  onCancelPlanRun,
  onOpenPlan,
  stagedCount,
  reviseBusy,
  onRevise,
  phaseRunModel,
  phaseRunEffort,
}: PlanHeaderProps): JSX.Element {
  const { t } = useLingui();
  const { project, slug } = useProjectWorkspace();
  const { search } = useLocation();
  const colorFor = useProjectColor();
  const [dialogOpen, setDialogOpen] = useState(false);
  const updated = planUpdatedAt(epic);
  const ago = updated === null ? null : compactSpan(now - new Date(updated).getTime());
  const card = epic.cardExternalId;
  const [readme] = usePlanDoc(epic.taskId, 'README.md', updated);
  const goal = readme ? planGoal(readme) : null;
  const branch = planBranch(epic);
  const run = epic.planRun;
  const planRunning = run?.runState === 'running';
  const running = planRunning || phaseRunActive;
  const runFailed = run?.runState === 'failed';
  const needs = needYouCount(epic);
  const spent = planSpent(epic);
  const segments = epic.phases.map((phase) => ({ phase, state: segmentState(phase, resolvedSeqs) }));
  const legend = planLegend(segments.map((s) => s.state));

  const lifecycle = (action: EpicLifecycleAction) => (): void => onLifecycle(epic, action);
  const menu: MenuItem[] = [];
  if (epic.status === 'active' && !running) {
    menu.push({
      label: t`Pause plan`,
      hint: t`nothing new starts until you resume`,
      disabled: busyLifecycle,
      onSelect: lifecycle('pause'),
    });
  }
  menu.push({
    label: t`Revise plan`,
    hint: running
      ? t`a run owns the plan docs — revise once it finishes`
      : t`interview against this plan and stage the changes as a reviewable diff`,
    disabled: reviseBusy || running,
    onSelect: onRevise,
  });
  menu.push({
    label: t`Edit phases`,
    hint: t`the README holds the phase table: reorder, rename by hand`,
    onSelect: () => onOpenPlan('edit'),
  });
  menu.push(
    epic.status === 'archived'
      ? {
          label: t`Unarchive`,
          hint: t`back in the list; runs and history stayed`,
          disabled: busyLifecycle,
          onSelect: lifecycle('restore'),
        }
      : {
          label: t`Archive plan`,
          hint: t`moves it to Archived; runs and history stay`,
          disabled: busyLifecycle,
          onSelect: lifecycle('archive'),
        },
  );

  const specCovered = epic.spec?.covered ?? 0;
  const specTotal = epic.spec?.total ?? 0;

  const banner = (
    <>
      {needs > 0 && (
        <BlockerBanner
          to={`/p/${encodeURIComponent(slug)}/needs-you`}
          lead={plural(needs, {
            one: '# phase waits on a decision from you',
            few: '# phases wait on a decision from you',
            many: '# phases wait on a decision from you',
            other: '# phases wait on a decision from you',
          })}
          detail={blockerDetail(legend)}
        />
      )}
      {/* Pausing only writes a status: it refuses new runs and stops none. */}
      {epic.status === 'paused' && (
        <NoticeBand>
          {running
            ? t`paused · the run in flight keeps going; nothing new starts after it`
            : t`paused · nothing new starts until you resume`}
        </NoticeBand>
      )}
      {epic.status === 'archived' && <NoticeBand>{t`archived · shown under Archived in the list; nothing runs`}</NoticeBand>}
    </>
  );

  return (
    <header>
      <DetailHeader bleed flush banner={banner}>
        <div className="flex flex-wrap items-start gap-5">
          <div className="min-w-0 flex-[1_1_360px]">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[10.5px] leading-[normal] text-ink-faint">
              <span className="inline-flex items-center gap-[5px]">
                <span
                  aria-hidden="true"
                  className="h-1.5 w-1.5 rounded-full"
                  style={{ backgroundColor: colorFor(epic.projectSlug) }}
                />
                {projectLabel(project?.name, epic.projectSlug || slug)}
              </span>
              <span aria-hidden="true">·</span>
              <span>{t`plan`}</span>
              {branch !== null && (
                <>
                  <span aria-hidden="true">·</span>
                  <span>{branch}</span>
                </>
              )}
              {ago !== null && (
                <>
                  <span aria-hidden="true">·</span>
                  <span>{t`updated ${ago} ago`}</span>
                </>
              )}
              {card !== null && (
                <>
                  <span aria-hidden="true">·</span>
                  <span>{t`board card ${card}`}</span>
                </>
              )}
              {/* The chip is taller than this line and must not push the title down. */}
              <span className="-my-[2px] ml-auto flex items-center gap-2">
                <TerminalBadge sessions={terminal} scope="plan" />
                <CopyIdBadge id={epic.externalId} label={t`plan`} />
              </span>
            </div>

            {/* The title opens the plan's details, as a phase's name opens the phase. */}
            <h2 className="mt-1.5 text-[22px] leading-[1.25] font-semibold tracking-[-0.015em] text-ink">
              <button
                type="button"
                onClick={() => onOpenPlan('plan')}
                data-tip={t`open plan details`}
                className="text-left transition-colors hover:text-brand"
              >
                {epic.title}
              </button>
            </h2>
            {goal !== null && <p className="mt-1.5 max-w-[72ch] text-[13px] leading-[1.55] text-ink-dim">{goal}</p>}
          </div>

          <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
            {epic.status === 'archived' ? (
              <button type="button" disabled className={BTN_PLAIN}>
                {t`archived`}
              </button>
            ) : epic.status === 'paused' ? (
              <button type="button" disabled={busyLifecycle} onClick={lifecycle('resume')} className={BTN_MAIN}>
                {busyLifecycle ? '…' : t`▶ resume plan`}
              </button>
            ) : running ? (
              <button
                type="button"
                disabled={busyLifecycle}
                onClick={lifecycle('pause')}
                data-tip={t`stops new runs from starting; the run in flight keeps going`}
                className={BTN_STOPS}
              >
                {busyLifecycle ? '…' : t`❚❚ pause plan`}
              </button>
            ) : (
              !complete && (
                <button
                  type="button"
                  disabled={planRunBusy || epic.status !== 'active'}
                  aria-expanded={dialogOpen}
                  data-tip={
                    epic.status !== 'active'
                      ? t`plan is not active`
                      : t`hand the whole plan to one agent, headlessly, in an isolated worktree`
                  }
                  onClick={() => setDialogOpen((v) => !v)}
                  className={BTN_MAIN}
                >
                  {planRunBusy ? '…' : runFailed ? t`▶ retry plan` : t`▶ run plan`}
                </button>
              )
            )}
            {complete && (
              <button
                type="button"
                onClick={() => onOpenPlan('summary')}
                data-tip={t`what was shipped — per-phase executed work`}
                className={`${BTN} border-green bg-transparent text-green hover:bg-green/10`}
              >
                <Trans>✓ summary</Trans>
              </button>
            )}
            <button
              type="button"
              onClick={() => onOpenPlan('plan')}
              data-tip={t`the plan's README: goal, phases, decisions`}
              className={BTN_PLAIN}
            >
              <Trans>❐ README</Trans>
            </button>
            <PlanMenu items={menu} />
          </div>
        </div>

        {dialogOpen && (
          <PlanRunDialog
            initialAgent={run?.agent ?? ''}
            initialMode={run?.mode ?? 'auto'}
            onCancel={() => setDialogOpen(false)}
            onRun={(agent, mode) => {
              setDialogOpen(false);
              onRunPlan(agent, mode);
            }}
          />
        )}

        {segments.length > 0 && (
          <div className="mt-4">
            <div className="flex h-2.5 gap-[3px]">
              {segments.map(({ phase, state }) => {
                const seq = String(phase.seq);
                const name = phase.name;
                const word = segmentWord(state);
                return (
                  <Link
                    key={phase.seq}
                    to={plansHref(slug, { plan: epic.externalId, detail: { kind: 'phase', seq: phase.seq, tab: 'story' } }, search)}
                    aria-label={t`phase ${seq} · ${name} · ${word}`}
                    data-tip={t`${seq} · ${name} · ${word}`}
                    data-state={state}
                    className={`flex-1 rounded-[2px] ${SEGMENT_TONE[state].fill}`}
                  />
                );
              })}
            </div>
            <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px] leading-[normal] text-ink-dim">
              {legend.map((entry) => (
                <span key={entry.state}>
                  <span aria-hidden="true" className={SEGMENT_TONE[entry.state].text}>
                    ■
                  </span>{' '}
                  {segmentLabel(entry.state, entry.count)}
                </span>
              ))}
            </div>
          </div>
        )}

        <div className="mt-3.5 flex flex-wrap gap-x-6 gap-y-1 border-t border-line py-2.5 font-mono text-[11px] leading-[normal] text-ink-dim">
          {/* An unknown spend is not $0. No forecast: no dollar figure exists for one. */}
          {spent !== null && (
            <span>
              <Trans>
                <span className="text-brand">{fmtSpent(spent)}</span> spent
              </Trans>
            </span>
          )}
          <span>{t`${epic.rollup.done}/${epic.rollup.total} criteria`}</span>
          <span data-tip={t`what a phase run started now uses`}>
            {phaseRunModelLine(phaseRunModel)} · {phaseRunEffortLine(phaseRunEffort)} · {t`worktree per phase`}
          </span>
          {epic.hasSpec && epic.spec !== null && (
            <button
              type="button"
              onClick={() => onOpenPlan('spec')}
              data-tip={t`spec coverage — criteria covered by at least one phase`}
              className={`p-0 hover:underline ${specCovered < specTotal ? 'text-amber' : 'text-green'}`}
            >
              <Trans>
                spec {specCovered}/{specTotal}
              </Trans>
            </button>
          )}
        </div>

        {run != null && (
          <div className="pb-3">
            <PlanRunLive run={run} now={now} busy={planRunBusy} onCancel={onCancelPlanRun} />
          </div>
        )}
        {stagedCount > 0 && (
          <button
            type="button"
            onClick={() => onOpenPlan('revisions')}
            className="mb-3 block font-mono text-[11px] leading-[normal] text-ink-dim transition-colors hover:text-brand"
          >
            {plural(stagedCount, {
              one: '# revision staged · review →',
              few: '# revisions staged · review →',
              many: '# revisions staged · review →',
              other: '# revisions staged · review →',
            })}
          </button>
        )}
      </DetailHeader>
    </header>
  );
}
