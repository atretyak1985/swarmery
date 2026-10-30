// One phase's page, in place of the plan: its head (state, numbers, blocker), its
// actions, and the six tabs' bodies. PhasePanel is the shell around them.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { type ReactNode, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { togglePlanCheckbox } from '../../api';
import type { DecisionHoldRef, Epic, EpicPhase, PlanRevision, ProviderTerms, Session } from '../../api/types';
import { NumbersStrip, type NumberItem } from '../../components/place/NumbersStrip';
import type { GlyphState } from '../../components/place/StateGlyph';
import { actionClass } from '../../components/place/PageDetail';
import { Loading } from '../../components/ui';
import { fmtAgo, fmtElapsed } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { safeHref } from '../../lib/safeHref';
import { useSessionHref } from '../../lib/sessionHref';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { CriterionLint } from './CriterionLint';
import { ForecastStory } from './ForecastStory';
import { forecastsOf, ForecastSection, RailSection, SurpriseChip } from './ForecastVsActual';
import { LandCriterionAction } from './LandCriterionAction';
import { hasReviewTab, showsReviewTab } from './landingModel';
import { RunRow, runSpan, STAGE_TONE } from './PhaseLedger';
import {
  phaseCost,
  phaseNote,
  phaseRuns,
  phaseStage,
  phaseStateLine,
  heldByDecision,
  stageLabel,
} from './phaseLedgerModel';
import { PhaseCard } from './PhaseCard';
import { isUnresolvedOutcome, PhaseActivity, phaseChip, ReverifyButton, runButtonCls, RunCompletedChip, RunOutcomeChip } from './phaseChips';
import { PhasePanel } from './PhasePanel';
import { PhaseReopens } from './PhaseReopens';
import { PhaseReview } from './PhaseReview';
import {
  DocModelChip,
  type PhaseRunEffort,
  PhaseRunEffortPicker,
  type PhaseRunModel,
  PhaseRunModelPicker,
  RunModelChip,
} from './PhaseRunPickers';
import { PlainSummary } from './PlainSummary';
import { type Check, DocEditor, extractChecks } from './planDetailShell';
import { computeResolvedSeqs, extractSection, fmtSpent, phaseGoal, phaseNeedsYou, phaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { ReviewFindings } from './ReviewFindings';
import { RunsReviewBlock } from './RunsReviewBlock';
import { usePlanDoc } from './usePlanDoc';
import { reverifyAction, type Reverify } from './useReverify';
import { VerifyVerdictChip } from './VerifyVerdictChip';
import { WorkaroundHits } from './WorkaroundHits';

type CriterionKind = 'auto' | 'manual' | 'land';

const KIND_CHIP: Record<CriterionKind, string> = {
  auto: 'rounded-full border-line-strong text-ink-faint',
  manual: 'rounded-[3px] border-brand text-brand',
  land: 'rounded-full border-blue text-blue',
};

const KIND_MARK = {
  manual: '[MANUAL]', // i18n-ignore — the doc's own marker
  land: '[LAND]', // i18n-ignore — the doc's own marker
} as const;

const KIND_TAG = /\[(MANUAL|LAND)\]/i;

/** The doc's `[MANUAL]`/`[LAND]` marker, and the criterion without it. */
function criterionParts(text: string): { kind: CriterionKind; text: string } {
  const tag = KIND_TAG.exec(text)?.[1]?.toUpperCase();
  const kind: CriterionKind = tag === 'MANUAL' ? 'manual' : tag === 'LAND' ? 'land' : 'auto';
  return { kind, text: text.replace(KIND_TAG, '').replace(/\s+/g, ' ').trim() };
}

/** Acceptance criteria, ticked in the doc (the box flips that exact line). `lint`
 * renders under a row (the class-marker hint) and `action` under it (the [LAND]
 * action) — beside the box, never instead of it. */
function ChecksList({
  checks,
  onToggle,
  busyLine,
  lint,
  action,
}: {
  checks: Check[];
  onToggle: (c: Check) => void;
  busyLine: number | null;
  lint: (c: Check) => ReactNode;
  action: (c: Check) => ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <ul className="m-0 max-w-[720px] list-none p-0">
      {checks.map((c) => {
        const { kind, text } = criterionParts(c.text);
        return (
          <li
            key={c.line}
            className="grid grid-cols-[18px_minmax(0,1fr)_auto] items-start gap-3 border-b border-line py-[9px]"
          >
            <button
              type="button"
              disabled={busyLine === c.line}
              aria-pressed={c.done}
              aria-label={text}
              onClick={() => onToggle(c)}
              data-tip={c.done ? t`un-tick this criterion in the doc` : t`tick this criterion in the doc`}
              className={`mt-px size-4 cursor-pointer rounded-[4px] border p-0 text-center font-mono text-[11px] leading-4 disabled:opacity-50 ${
                c.done
                  ? 'border-green bg-green-bg text-green'
                  : `bg-transparent text-transparent ${kind === 'manual' ? 'border-brand' : 'border-line-strong'}`
              }`}
            >
              <span aria-hidden="true">✓</span>
            </button>
            <div className="min-w-0">
              <div className={`text-[13px] leading-normal ${c.done ? 'text-ink-dim' : 'text-ink'}`}>{text}</div>
              {lint(c)}
              {action(c)}
            </div>
            <span
              className={`border px-[7px] py-px font-mono text-[10px] leading-[normal] font-bold whitespace-nowrap ${KIND_CHIP[kind]}`}
            >
              {kind === 'auto' ? t`auto` : KIND_MARK[kind]}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** The state sentence's dot and lead, in the state's colour (handoff §4). */
const STATE_DOT: Partial<Record<GlyphState, string>> = {
  needs: 'animate-blink-dot bg-amber',
  working: 'animate-pulse bg-green',
  done: 'bg-ink-dim',
  failed: 'bg-red',
};
const STATE_TEXT: Partial<Record<GlyphState, string>> = {
  needs: 'text-amber',
  working: 'text-green',
  done: 'text-ink-dim',
  failed: 'text-red',
};

/** Run chip for the phase rail header. Switches on the run's OUTCOME, not its
 * process state, so the panel tells the same four-way story the list does — a
 * `runState: 'done'` process that ticked nothing reads `ran · no progress`,
 * clickable, instead of a green "Run done" the phase never earned. */
function RunStateChip({
  phase,
  onOpenOutcome,
}: {
  phase: EpicPhase;
  onOpenOutcome: () => void;
}): JSX.Element | null {
  const { t } = useLingui();
  if (phase.runOutcome === 'running')
    return (
      <span
        className="inline-flex items-center gap-1 rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
        data-tip={t`headless run in progress`}
      >
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-brand" />
        <Trans>Running</Trans>
      </span>
    );
  if (phase.runOutcome === 'completed') return <RunCompletedChip phase={phase} />;
  if (isUnresolvedOutcome(phase.runOutcome))
    return <RunOutcomeChip phase={phase} outcome={phase.runOutcome} onOpen={onOpenOutcome} />;
  return null;
}

/** One phase's page, tabbed: Story (what the phase is for and the PhaseCard's
 * holds and meta), Plain words (the doc's own section, else a generated one),
 * Criteria (interactive checks + the doc), Runs (its sessions, run state,
 * diagnosis, provenance), Review (the run branch's diff, verdict and landing —
 * plans/PhaseReview.tsx), Report (forecast against what happened, and what was
 * shipped), and Edit (raw markdown), reached by the `edit` action. */
export function PhaseDetailPanel({
  epic,
  phase,
  tab,
  onTab,
  tabHref,
  onStep,
  onBack,
  sessions,
  now,
  runBusy,
  planRunning,
  phaseRunModel,
  onPhaseRunModel,
  phaseRunEffort,
  onPhaseRunEffort,
  onRetry,
  onCancelRun,
  reverify,
  onReverify,
  onOpenOutcome,
  onDocChanged,
  revisions,
  onOpenRevisions,
  terms,
}: {
  epic: Epic;
  phase: EpicPhase;
  tab: PhaseDetailTab;
  onTab: (tab: PhaseDetailTab) => void;
  /** Each panel tab's URL — makes the tabs real links. */
  tabHref?: ((tab: PhaseDetailTab) => string) | undefined;
  /** Open the neighbouring phase by seq (↑/↓), keeping the tab. */
  onStep: (seq: number) => void;
  /** Back to the plan. */
  onBack: () => void;
  /** The plan's sessions; null until they load. */
  sessions: readonly Session[] | null;
  now: number;
  runBusy: number | null;
  /** A whole-plan run owns the phase docs — per-phase runs stand down. */
  planRunning: boolean;
  /** The picker's value, for DocModelChip — see PhaseActions' note. */
  phaseRunModel: PhaseRunModel;
  onPhaseRunModel: (m: PhaseRunModel) => void;
  phaseRunEffort: PhaseRunEffort;
  onPhaseRunEffort: (e: PhaseRunEffort) => void;
  onRetry: () => void;
  onCancelRun: () => void;
  reverify: Reverify;
  onReverify: (phaseId: number) => void;
  /** Open the run-diagnosis modal for this phase. */
  onOpenOutcome: () => void;
  onDocChanged: () => void;
  /** The plan's revisions (fetched by EpicDetail) — feeds the "revised" note. */
  revisions: PlanRevision[] | null;
  /** Jump to the plan's Revisions tab. */
  onOpenRevisions: () => void;
  /** The project's code-host vocabulary for the Review tab (null until loaded). */
  terms: ProviderTerms | null;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const { slug: projectSlug } = useProjectWorkspace();
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const sessionHref = useSessionHref();
  const status = phaseStatus(phase, resolvedSeqs);
  // `phaseStatus` reads the doc, `running` reads the process; only the second
  // may put the word `running` on screen.
  const running = phase.runState === 'running';
  const chip = phaseChip(status, running);
  const [doc, setDoc] = usePlanDoc(epic.taskId, phase.docRelPath, phase.docUpdatedAt);
  const checks = useMemo(() => (doc !== null ? extractChecks(doc) : null), [doc]);
  // A done phase retires its Edit tab (the doc is the record of shipped work); a
  // stale `edit` selection degrades to Report instead of a dead panel.
  const editable = status !== 'done';
  const activeTab: PhaseDetailTab = tab === 'edit' && !editable ? 'report' : tab;
  const at = epic.phases.findIndex((p) => p.seq === phase.seq);
  const prev = epic.phases[at - 1];
  const next = epic.phases[at + 1];
  const [busyLine, setBusyLine] = useState<number | null>(null);
  const [toggleErr, setToggleErr] = useState<string | null>(null);

  // The newest APPLIED revision that touched this phase's doc (by its current
  // path or as a rename source) — the one-line provenance note on Runs.
  const appliedRevision = useMemo(() => {
    const hits = (revisions ?? []).filter(
      (r) =>
        r.status === 'applied' &&
        r.files.some((f) => f.docPath === phase.docRelPath || f.renameFrom === phase.docRelPath),
    );
    return hits.length > 0 ? hits[0] : undefined; // list arrives newest first
  }, [revisions, phase.docRelPath]);

  // Toggling a criterion PATCHes that exact source line; the response is the
  // fresh doc, and the page refetch keeps the rollup/list in step.
  const toggle = (c: Check): void => {
    setBusyLine(c.line);
    setToggleErr(null);
    togglePlanCheckbox(epic.taskId, phase.docRelPath, c.line, !c.done)
      .then((d) => {
        setDoc(d.content);
        onDocChanged();
      })
      .catch((e: unknown) => setToggleErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusyLine(null));
  };

  const depsUnmet = phase.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
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
  // The card's ONE primary action (1d): Cancel while live, Run/Retry until done.
  const primary = running
    ? { label: t`Cancel`, onClick: onCancelRun, disabled: runBusy !== null }
    : status !== 'done'
      ? {
          label:
            runBusy === phase.id ? '…' : isUnresolvedOutcome(phase.runOutcome) ? t`Retry run` : t`Run phase`,
          onClick: onRetry,
          disabled: runDisabled,
          tip: runTitle,
        }
      : null;
  // Beside the primary, never instead of it — and on a `done` (failed-verdict)
  // phase too, which has no primary at all.
  const again = reverifyAction(phase, reverify, onReverify, i18n);
  const revisedAgo =
    appliedRevision !== undefined ? fmtAgo(appliedRevision.decidedAt ?? appliedRevision.createdAt) : '';

  const runs = phaseRuns(sessions, phase.id);
  const cost = phaseCost(runs);
  const forecast = forecastsOf(phase).at(-1);
  const forecastBand = forecast === undefined ? '' : [forecast.sizeBand, forecast.durationBand].filter((b) => b !== '').join(' · ');
  const done = phase.checkboxesDone;
  const total = phase.checkboxesTotal;
  // No forecast in dollars exists (plan D-5): the plan's band stands in that slot.
  const stats: NumberItem[] = [
    { label: t`spent`, value: cost === null ? '—' : fmtSpent(cost), tone: 'brand' },
    ...(forecastBand === '' ? [] : [{ label: t`forecast`, value: forecastBand, tone: 'faint' as const }]),
    { label: t`runs`, value: String(runs.length) },
    { label: t`criteria`, value: `${String(done)}/${String(total)}`, tone: total > 0 && done === total ? 'green' : 'default' },
    { label: t`duration`, value: runs.length === 0 ? '—' : runs.map((r) => runSpan(r, now)).join(' + ') },
  ];

  const stage = phaseStage(phase, status);
  const note = phaseNote(phase, status, depsUnmet, phase.runStartedAt === null ? '' : fmtElapsed(phase.runStartedAt, now), terms?.changeShort ?? null);
  const state = phaseStateLine(phase, status, depsUnmet, note);
  const needsYou = phaseNeedsYou(phase);
  const held = heldByDecision(phase);
  const needsHref = `/p/${encodeURIComponent(projectSlug)}/needs-you`;
  const manualOpen = phase.manualOpen;
  const blocker = !needsYou
    ? null
    : phase.guardDecision != null
      ? t`the guard stop waits on your answer`
      : held
        ? t`the phase waits on your answer`
        : plural(manualOpen, {
          one: '# manual check only you can close',
          few: '# manual checks only you can close',
          many: '# manual checks only you can close',
          other: '# manual checks only you can close',
        });
  const prUrl = phase.landing?.prUrl ?? null;
  const change = terms?.changeShort ?? '';
  const heldCount = (phase.decisionHold?.decisions.length ?? 0) + (phase.guardDecision != null ? 1 : 0);
  const findings = phase.reviewBlocks?.reduce((n, block) => n + block.findings.length, 0) ?? 0;
  const tabCounts: Partial<Record<PhaseDetailTab, string>> = {
    ...(heldCount > 0 ? { story: String(heldCount) } : {}),
    ...(total > 0 ? { criteria: `${String(done)}/${String(total)}` } : {}),
    ...(runs.length > 0 ? { runs: String(runs.length) } : {}),
    ...(findings > 0 ? { review: String(findings) } : {}),
  };
  const head = (
    <>
      <div className="mt-2 flex items-start gap-2 text-[13.5px] leading-normal text-ink-2">
        <span aria-hidden="true" className={`mt-[5px] size-[9px] shrink-0 rounded-full ${STATE_DOT[state.state] ?? 'bg-ink-faint'}`} />
        <span>
          <b className={`font-semibold ${STATE_TEXT[state.state] ?? 'text-ink-faint'}`}>{state.lead}</b> · {state.rest}
        </span>
      </div>
      <div className="mt-4">
        <NumbersStrip items={stats} />
      </div>
      {blocker !== null && (
        <Link
          to={needsHref}
          className="mt-3.5 inline-flex items-center gap-2 rounded-lg border border-brand bg-brand-bg px-3 py-[7px] text-[12.5px] leading-[normal] text-ink"
        >
          <span aria-hidden="true" className="font-mono text-brand">
            ●
          </span>
          {blocker}
          <span className="font-mono text-[10.5px] text-brand">
            <Trans>Needs you →</Trans>
          </span>
        </Link>
      )}
    </>
  );
  const actions = (
    <>
      {needsYou ? (
        held ? (
          <Link to={needsHref} className={actionClass('brand')}>
            <Trans>answer in Needs you</Trans>
          </Link>
        ) : (
          <button type="button" onClick={() => onTab('criteria')} className={actionClass('brand')}>
            <Trans>tick criteria</Trans>
          </button>
        )
      ) : running ? (
        <>
          {phase.runSessionUuid !== null && (
            <Link to={sessionHref(phase.runSessionUuid)} className={actionClass(undefined)}>
              <Trans>open run</Trans>
            </Link>
          )}
          <button type="button" disabled={runBusy !== null} onClick={onCancelRun} className={actionClass('red')}>
            <Trans>stop phase</Trans>
          </button>
        </>
      ) : status !== 'done' && primary !== null ? (
        <button
          type="button"
          disabled={primary.disabled}
          data-tip={'tip' in primary ? primary.tip : undefined}
          onClick={primary.onClick}
          className={actionClass('brand')}
        >
          {runBusy === phase.id ? '…' : isUnresolvedOutcome(phase.runOutcome) ? t`Retry run` : t`▶ run this phase now`}
        </button>
      ) : null}
      {isUnresolvedOutcome(phase.runOutcome) && !running && (
        <button type="button" onClick={onOpenOutcome} className={actionClass(undefined)}>
          <Trans>why it did not move</Trans>
        </button>
      )}
      <ReverifyButton action={again} size="panel" />
      {status === 'done' && prUrl !== null && safeHref(prUrl) !== null && (
        <a href={safeHref(prUrl) ?? undefined} target="_blank" rel="noreferrer" className={actionClass(undefined)}>
          <Trans>open {change} ↗</Trans>
        </a>
      )}
      {editable && (
        <button type="button" onClick={() => onTab('edit')} aria-pressed={activeTab === 'edit'} className={actionClass(undefined)}>
          <Trans>edit</Trans>
        </button>
      )}
    </>
  );
  const goal = doc === null ? null : phaseGoal(doc);
  const plainSection = doc === null ? null : extractSection(doc, 'In plain words'); // i18n-ignore — the doc's own heading

  return (
    <PhasePanel
      epic={epic}
      phase={phase}
      tab={activeTab}
      onTab={onTab}
      onPrev={prev !== undefined ? () => onStep(prev.seq) : undefined}
      onNext={next !== undefined ? () => onStep(next.seq) : undefined}
      onBack={onBack}
      stage={{ label: stageLabel(stage), tone: STAGE_TONE[stage] }}
      hrefFor={tabHref}
      head={head}
      tabCounts={tabCounts}
      actions={actions}
    >
      {activeTab === 'edit' ? (
        <DocEditor
          taskId={epic.taskId}
          path={phase.docRelPath}
          version={phase.docUpdatedAt}
          onSaved={onDocChanged}
        />
      ) : activeTab === 'report' ? (
        <div className="max-w-[760px] space-y-5">
          <ForecastStory phase={phase} />
          {phase.surprise == null && <ForecastSection phase={phase} />}
          <PhaseSummary taskId={epic.taskId} phase={phase} doc={doc} />
        </div>
      ) : activeTab === 'plain' ? (
        doc === null ? (
          <Loading label={t`doc…`} />
        ) : plainSection !== null ? (
          <div className="max-w-[66ch] text-[15px] leading-[1.65] text-pretty text-ink">
            <Markdown text={plainSection} />
          </div>
        ) : (
          <PlainSummary taskId={epic.taskId} kind="phase" path={phase.docRelPath} version={phase.docUpdatedAt} />
        )
      ) : activeTab === 'story' ? (
        <div className="space-y-5">
          {goal !== null && (
            <div className="max-w-[70ch] text-[13.5px] leading-[1.65] text-pretty text-ink-2">
              <Markdown text={goal} />
            </div>
          )}
          <PhaseCard
            embedded
            phase={phase}
            {...(phase.completionReport !== null ? { report: <Markdown text={phase.completionReport} /> } : {})}
            terms={terms}
            decisionHref={(d) => decisionTurnHref(projectSlug, d)}
            stopHref={`/p/${encodeURIComponent(projectSlug)}/needs-you`}
          />
          <WorkaroundHits
            phaseId={phase.id}
            // A daemon older than migration 0120 sends neither field.
            hits={phase.workaroundHits ?? []}
            total={phase.workaroundTotal ?? 0}
          />
        </div>
      ) : activeTab === 'review' ? (
        <div className="max-w-[760px] space-y-4">
          {/* Keyed by phase: ↑/↓ step navigation keeps the tab, and a land still in
              flight for the previous phase must not render onto this one. A phase
              the daemon never ran has nothing to land — only its findings. */}
          {hasReviewTab(phase) && (
            <PhaseReview key={phase.id} epic={epic} phase={phase} terms={terms} onLanded={onDocChanged} />
          )}
          {showsReviewTab(phase) ? (
            <ReviewFindings
              blocks={phase.reviewBlocks ?? []}
              legacy={phase.legacyReview === true}
              hold={phase.reviewHold ?? ''}
              decision={phase.reviewDecision ?? null}
              onAnswered={onDocChanged}
            />
          ) : (
            <div className="py-4 text-[12.5px] text-ink-dim">
              <Trans>No branch yet.</Trans>
            </div>
          )}
        </div>
      ) : activeTab === 'runs' ? (
        <>
          {runs.length > 0 ? (
            <ol aria-label={t`runs of this phase`} className="m-0 list-none p-0">
              {runs.map((run) => (
                <RunRow key={run.id} run={run} now={now} ruled />
              ))}
            </ol>
          ) : (
            sessions !== null && (
              <div className="py-5 text-[12.5px] text-ink-dim">
                <Trans>No runs yet — this phase starts when the previous one lands.</Trans>
              </div>
            )
          )}
          {status !== 'done' && !running && (
            <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5">
              <PhaseRunModelPicker
                value={phaseRunModel}
                onChange={onPhaseRunModel}
                disabled={runBusy !== null || planRunning}
              />
              <PhaseRunEffortPicker
                value={phaseRunEffort}
                onChange={onPhaseRunEffort}
                disabled={runBusy !== null || planRunning}
              />
            </div>
          )}
          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            <span className={`rounded border px-1.5 py-px font-mono text-[9px] ${chip.cls}`}>{i18n._(chip.label)}</span>
            {/* Only the idle half: while live, RunStateChip already pulses. */}
            {status === 'in_progress' && !running && (
              <PhaseActivity docUpdatedAt={phase.docUpdatedAt} running={false} />
            )}
            <RunStateChip phase={phase} onOpenOutcome={onOpenOutcome} />
            <RunModelChip phase={phase} />
            <DocModelChip phase={phase} picked={phaseRunModel} />
            <VerifyVerdictChip verdict={phase.verifyVerdict} detail={phase.verifyDetail} />
            <SurpriseChip phase={phase} onOpen={() => onTab('story')} />
            {phase.dependsOn.map((seq) => (
              <span
                key={seq}
                className={`rounded border px-1 py-px font-mono text-[9px] ${
                  resolvedSeqs.has(seq) ? 'border-green/40 text-green' : 'border-line text-ink-faint'
                }`}
                data-tip={resolvedSeqs.has(seq) ? t`dependency resolved` : t`dependency pending`}
              >
                ← #{seq}
              </span>
            ))}
            {/* The header's actions name what the page is waiting for, so a phase that
                waits on the operator keeps its run here. */}
            {needsYou && primary !== null && (
              <button
                type="button"
                disabled={primary.disabled}
                data-tip={'tip' in primary ? primary.tip : undefined}
                onClick={primary.onClick}
                className={`ml-auto rounded-md border px-2 py-1 font-mono text-[10px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${running ? 'border-red/40 text-red hover:bg-red/10' : runButtonCls(phase.runOutcome)}`}
              >
                {primary.label}
              </button>
            )}
          </div>
          {appliedRevision !== undefined && (
            <div className="mt-2 font-mono text-[10px] text-ink-faint">
              <Trans>
                this doc was changed by an applied revision {revisedAgo} —{' '}
                <button type="button" onClick={onOpenRevisions} className="text-brand hover:underline">
                  see Revisions
                </button>
              </Trans>
            </div>
          )}
          {phase.runState === 'failed' && (
            <div className="mt-3 rounded-md border border-red/40 bg-red/10 px-2.5 py-2 font-mono text-[10.5px] break-words text-red">
              {phase.runError ?? t`run failed`}
            </div>
          )}
          <PhaseReopens
            taskId={epic.taskId}
            phaseId={phase.id}
            // A daemon older than migration 0104 sends no `reopens`.
            reopens={phase.reopens ?? []}
            canReopen={status === 'done' && !running}
            onReopened={onDocChanged}
          />
          <RunsReviewBlock
            key={phase.id}
            taskId={epic.taskId}
            phaseId={phase.id}
            // A daemon older than migration 0106 sends no `reviews`.
            reviews={phase.reviews ?? []}
          />
        </>
      ) : (
        <>
          {toggleErr !== null && (
            <div
              role="alert"
              className="mb-4 rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[10.5px] text-red"
            >
              {toggleErr}
            </div>
          )}
          <div>
            {checks === null ? (
              <Loading label={t`criteria…`} />
            ) : checks.length === 0 ? (
              <div className="font-mono text-[11.5px] text-ink-faint">
                <Trans>no checkboxes in this doc</Trans>
              </div>
            ) : (
              <ChecksList
                checks={checks}
                onToggle={toggle}
                busyLine={busyLine}
                lint={(c) => (
                  <CriterionLint
                    taskId={epic.taskId}
                    path={phase.docRelPath}
                    line={c.line}
                    text={c.text}
                    done={c.done}
                    // `checks` is derived from `doc`, so it is never null here.
                    docText={doc ?? ''}
                    onMarked={(d) => {
                      setDoc(d.content);
                      onDocChanged();
                    }}
                  />
                )}
                action={(c) => (
                  <LandCriterionAction
                    epic={epic}
                    phase={phase}
                    text={c.text}
                    done={c.done}
                    terms={terms}
                    onOpenReview={() => onTab('review')}
                    onDocChanged={onDocChanged}
                  />
                )}
              />
            )}
            <div className="mt-2.5 font-mono text-[10.5px] leading-[normal] text-ink-faint">
              <Trans>auto criteria are checked by the verifier · [MANUAL] only you can tick · [LAND] closes on landing</Trans>
            </div>
          </div>
          {doc !== null && (
            <details className="group mt-4">
              <summary className="cursor-pointer list-none font-mono text-[11px] text-ink-dim transition-colors hover:text-brand [&::-webkit-details-marker]:hidden">
                <span aria-hidden="true" className="mr-1.5 inline-block transition-transform group-open:rotate-90">
                  ▸
                </span>
                <Trans>doc</Trans>
              </summary>
              <div className="mt-2">
                <Markdown text={doc} />
              </div>
            </details>
          )}
        </>
      )}
    </PhasePanel>
  );
}

/** Summary tab of one phase: a prose account of WHAT WAS DONE — the Completion
 * Report the executor wrote plus the doc's `## Execution record` section.
 *
 * Acceptance criteria are deliberately NOT listed here: they are the phase's
 * contract, not its summary, and they already live (interactive, in full) on
 * the Phase tab. All this tab keeps of them is the one-line score, as context
 * for the narrative. A phase whose executor wrote no report says so plainly
 * instead of padding the tab with a re-run of the checklist. */
function PhaseSummary({
  taskId,
  phase,
  doc,
}: {
  taskId: number;
  phase: EpicPhase;
  doc: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const checks = useMemo(() => (doc !== null ? extractChecks(doc) : []), [doc]);
  const execRecord = useMemo(
    // i18n-ignore — the doc's own `## Execution record` heading, matched in the markdown
    () => (doc !== null ? extractSection(doc, 'Execution record') : null),
    [doc],
  );

  if (doc === null) return <Loading label={t`summary…`} />;

  const done = checks.filter((c) => c.done).length;
  const total = checks.length;
  const score = (
    <div className="mb-3 font-mono text-[10.5px] text-ink-faint">
      <Trans>
        {done}/{total} acceptance criteria met · full list on the Criteria tab
      </Trans>
    </div>
  );

  if (phase.completionReport === null && execRecord === null) {
    return (
      <>
        {checks.length > 0 && score}
        <div className="font-mono text-[11.5px] text-ink-faint">
          <Trans>
            no summary of the work written — the executor left neither a{' '}
            <span className="text-ink-dim">## Completion Report</span> nor an{' '}
            <span className="text-ink-dim">## Execution record</span> section in this phase doc
          </Trans>
        </div>
      </>
    );
  }

  return (
    <>
      {phase.completionReport !== null && (
        <RailSection label={t`in plain words`}>
          <PlainSummary taskId={taskId} kind="phase-report" path={phase.docRelPath} version={phase.docUpdatedAt} />
        </RailSection>
      )}
      {checks.length > 0 && score}
      {phase.completionReport !== null && (
        <RailSection label={t`what was done`}>
          <Markdown text={phase.completionReport} />
        </RailSection>
      )}
      {execRecord !== null && (
        <RailSection label={t`execution record`}>
          <Markdown text={execRecord} />
        </RailSection>
      )}
    </>
  );
}

/** The planning page, opened on the turn a decision came from (PlanningMode
 *  renders it inline from ?planningSession=&turn=). null for a decision born in
 *  the README, which has no interview turn to open. */
function decisionTurnHref(slug: string, d: DecisionHoldRef): string | null {
  if (d.planningSessionId === null) return null;
  const q = new URLSearchParams({
    tab: 'new',
    planningSession: String(d.planningSessionId),
    turn: String(d.seq),
  });
  return `/p/${encodeURIComponent(slug)}/plans?${q.toString()}#planning-turn-${String(d.seq)}`;
}
