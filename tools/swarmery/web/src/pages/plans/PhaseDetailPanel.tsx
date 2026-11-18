// One phase's page, in place of the plan: its blocker, its name, its
// actions, and the six tabs' bodies. PhasePanel is the shell around them.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { type ReactNode, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { togglePlanCheckbox } from '../../api';
import { phaseGuard } from '../../api/guard';
import type { DecisionHoldRef, Epic, EpicPhase, PlanRevision, ProviderTerms, Session } from '../../api/types';
import { BlockerBanner } from '../../components/place/DetailHeader';
import { actionClass } from '../../components/place/PageDetail';
import type { NumberItem } from '../../components/place/NumbersStrip';
import { Loading } from '../../components/ui';
import { fmtAgo, fmtElapsed } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { safeHref } from '../../lib/safeHref';
import { useSessionHref } from '../../lib/sessionHref';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { REVIEW_VERDICT_UI } from '../inbox/inboxModel';
import { CriterionLint } from './CriterionLint';
import { ForecastStory } from './ForecastStory';
import { ForecastSection, forecastsOf, RailSection } from './ForecastVsActual';
import { LandCriterionAction } from './LandCriterionAction';
import { hasReviewTab, showsReviewTab } from './landingModel';
import { runSpan, STAGE_TONE } from './PhaseLedger';
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
import {
  isUnresolvedOutcome,
  phaseChip,
  ReverifyButton,
  RunCompletedChip,
  RunOutcomeChip,
} from './phaseChips';
import { PhasePanel } from './PhasePanel';
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
import { DOT, guardRowView, Row, type RowTone } from './ReviewStateLedger';
import { runsVerdict } from './runsModel';
import { usePlanDoc } from './usePlanDoc';
import { reverifyAction, type Reverify } from './useReverify';
import { WorkaroundHits } from './WorkaroundHits';
import { Chevron } from '../../components/Chevron';

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
    <ul className="m-0 list-none p-0">
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
  now,
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
  runBusy: number | null;
  /** A whole-plan run owns the phase docs — per-phase runs stand down. */
  planRunning: boolean;
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
  /** The page's clock tick: run spans and the elapsed time follow it. */
  now: number;
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
  const done = phase.checkboxesDone;
  const total = phase.checkboxesTotal;

  // The header's own state sentence and numbers (D-16) — the same facts the
  // Runs tab's chips and the ledger row already carry, surfaced where every tab
  // can see them without switching to Runs.
  const cost = phaseCost(runs);
  const forecast = forecastsOf(phase).at(-1);
  const forecastBand =
    forecast === undefined ? '' : [forecast.sizeBand, forecast.durationBand].filter((b) => b !== '').join(' · ');
  const stats: NumberItem[] = [
    { label: t`spent`, value: cost === null ? '—' : fmtSpent(cost), tone: 'brand' },
    ...(forecastBand === '' ? [] : [{ label: t`forecast`, value: forecastBand, tone: 'faint' as const }]),
    { label: t`runs`, value: String(runs.length) },
    { label: t`criteria`, value: `${String(done)}/${String(total)}`, tone: total > 0 && done === total ? 'green' : 'default' },
    { label: t`duration`, value: runs.length === 0 ? '—' : runs.map((r) => runSpan(r, now)).join(' + ') },
  ];
  const elapsed = phase.runStartedAt === null ? '' : fmtElapsed(phase.runStartedAt, now);
  const note = phaseNote(phase, status, depsUnmet, elapsed, terms?.changeShort ?? null);
  const stateLine = phaseStateLine(phase, status, depsUnmet, note);

  // Runs tab (plan D-27…D-31): the verdict sentence and the "last result"
  // rows, one per stage of the pipeline — never the badge strip they replace.
  const verdict = runsVerdict(phase, status, running);
  const executorRow: { tone: RowTone; status: ReactNode } = {
    tone: running
      ? 'neutral'
      : phase.runOutcome === 'completed'
        ? 'success'
        : phase.runOutcome === 'failed'
          ? 'danger'
          : phase.runOutcome === 'noop' || phase.runOutcome === 'partial'
            ? 'warn'
            : 'neutral',
    status:
      running
        ? t`running`
        : phase.runOutcome === 'completed'
          ? <RunCompletedChip phase={phase} />
          : isUnresolvedOutcome(phase.runOutcome)
            ? <RunOutcomeChip phase={phase} outcome={phase.runOutcome} onOpen={onOpenOutcome} />
            : t`not run yet`,
  };
  const guardRow = guardRowView(phaseGuard(phase));
  const verifierRow: { tone: RowTone; status: string } = {
    tone:
      phase.verifyVerdict === 'pass'
        ? 'success'
        : phase.verifyVerdict === 'fail'
          ? 'danger'
          : phase.verifyVerdict === 'inconclusive'
            ? 'warn'
            : 'neutral',
    status:
      phase.verifyVerdict === 'pass'
        ? t`verified`
        : phase.verifyVerdict === 'fail'
          ? t`verify failed`
          : phase.verifyVerdict === 'inconclusive'
            ? t`verify inconclusive`
            : t`not graded yet`,
  };
  const latestReview = phase.reviews?.[0] ?? null;
  const reviewRow: { tone: RowTone; status: string } = {
    tone:
      latestReview === null
        ? 'neutral'
        : latestReview.verdict === 'pass'
          ? 'success'
          : latestReview.verdict === 'fail'
            ? 'danger'
            : 'warn',
    status: latestReview === null ? t`not reviewed yet` : t`review ${REVIEW_VERDICT_UI[latestReview.verdict] ?? latestReview.verdict}`,
  };
  const complianceRow: { tone: RowTone; status: string; detail: string } = {
    tone: status === 'done' ? 'success' : status === 'blocked' ? 'danger' : status === 'unverified' || status === 'unreported' ? 'warn' : 'neutral',
    status: i18n._(chip.label),
    detail: phase.completionBlockers.join(' · '),
  };

  const stage = phaseStage(phase, status);
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
  const actions = (
    <>
      {needsYou ? (
        held ? (
          // A guard decision now answers in place on the Review tab's
          // next-step card (plan D24/D25) — this link would duplicate that
          // action, so it is omitted for exactly that case; a plain
          // decisionHold or reviewDecision still points at Needs you.
          phase.guardDecision != null ? null : (
            <Link to={needsHref} className={actionClass('brand')}>
              <Trans>answer in Needs you</Trans>
            </Link>
          )
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
      banner={blocker !== null ? <BlockerBanner to={needsHref} lead={blocker} /> : undefined}
      stateLine={stateLine}
      stats={stats}
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
        <div className="space-y-5">
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
        <div className="space-y-4">
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
          <div className="text-[13px] leading-[1.5]">
            <span className={`font-semibold ${DOT[verdict.tone]}`}>{verdict.lead}</span>{' '}
            <span className="text-ink-dim">{verdict.rest}</span>
          </div>
          {/* A phase that never ran has nothing to report yet — the verdict
              sentence alone says why, with no empty "last result" block under it. */}
          {phase.runOutcome !== 'idle' && (
            <section aria-label={t`last result`} className="mt-3 rounded-lg border border-line bg-surface/40 px-2.5 py-2">
              <h3 className="mb-1 font-mono text-[10px] tracking-[0.14em] text-ink-2 uppercase">
                <Trans>Last result</Trans>
              </h3>
              <Row label={t`Executor`} tone={executorRow.tone} status={executorRow.status} testId="runs-last-executor" />
              <Row label={t`Guard check`} tone={guardRow.tone} status={guardRow.status} testId="runs-last-guard" />
              <Row label={t`Verifier`} tone={verifierRow.tone} status={verifierRow.status} testId="runs-last-verifier" />
              <Row
                label={t`Review`}
                tone={reviewRow.tone}
                status={reviewRow.status}
                {...(latestReview !== null ? { link: { onClick: () => onTab('review'), text: t`open →` } } : {})}
                testId="runs-last-review"
              />
              <Row
                label={t`Plan compliance`}
                tone={complianceRow.tone}
                status={complianceRow.status}
                detail={complianceRow.detail}
                link={{ onClick: () => onTab('report'), text: t`open →` }}
                testId="runs-last-compliance"
              />
            </section>
          )}
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5">
            {status !== 'done' && !running && (
              <>
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
                <DocModelChip phase={phase} picked={phaseRunModel} />
              </>
            )}
            <RunModelChip phase={phase} />
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
                <Chevron open={false} className="mr-1.5 text-ink-dim group-open:rotate-90" />
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
