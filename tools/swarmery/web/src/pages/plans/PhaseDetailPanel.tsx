// One phase's details, opened inline in place of the phase list.

import { Trans, useLingui } from '@lingui/react/macro';
import { type ReactNode, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { togglePlanCheckbox } from '../../api';
import type { DecisionHoldRef, Epic, EpicPhase, PlanRevision, ProviderTerms } from '../../api/types';
import { Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { useSessionHref } from '../../lib/sessionHref';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { CriterionLint } from './CriterionLint';
import { ForecastStory } from './ForecastStory';
import { ForecastSection, RailSection, SurpriseChip } from './ForecastVsActual';
import { LandCriterionAction } from './LandCriterionAction';
import { hasReviewTab, showsReviewTab } from './landingModel';
import { PhaseCard } from './PhaseCard';
import { isUnresolvedOutcome, PhaseActivity, phaseChip, runButtonCls, RunCompletedChip, RunOutcomeChip } from './phaseChips';
import { ReverifyButton } from './PhaseList';
import { PhasePanel } from './PhasePanel';
import { PhaseReopens } from './PhaseReopens';
import { PhaseReview } from './PhaseReview';
import { DocModelChip, type PhaseRunModel, RunModelChip } from './PhaseRunPickers';
import { PlainSummary } from './PlainSummary';
import { type Check, DocEditor, extractChecks } from './planDetailShell';
import { computeResolvedSeqs, extractSection, phaseStatus } from './planModel';
import type { PhaseDetailTab } from './plansTarget';
import { ReviewFindings } from './ReviewFindings';
import { RunsReviewBlock } from './RunsReviewBlock';
import { usePlanDoc } from './usePlanDoc';
import { reverifyAction, type Reverify } from './useReverify';
import { VerifyVerdictChip } from './VerifyVerdictChip';
import { WorkaroundHits } from './WorkaroundHits';

/** Acceptance-criteria list with tick state (✓ done / ○ open). With `onToggle`
 * the rows become buttons that flip the criterion in the doc (the affordance
 * the retired doc modal owned); without it the list is read-only. `lint`
 * renders under a toggleable row (the Criteria tab's class-marker hint), and
 * `action` under it (the Criteria tab's [LAND] action) — beside the toggle,
 * never instead of it. */
function ChecksList({
  checks,
  onToggle,
  busyLine,
  lint,
  action,
}: {
  checks: Check[];
  onToggle?: (c: Check) => void;
  busyLine?: number | null;
  lint?: (c: Check) => ReactNode;
  action?: (c: Check) => ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <ul className="space-y-1.5">
      {checks.map((c) => {
        const mark = (
          <span
            aria-hidden="true"
            className={`mt-px shrink-0 font-mono text-[12px] ${
              c.done ? 'text-green' : 'text-ink-faint'
            }`}
          >
            {c.done ? '✓' : '○'}
          </span>
        );
        if (onToggle === undefined) {
          return (
            <li key={c.line} className="flex items-start gap-2">
              {mark}
              <span className={c.done ? 'text-ink-dim' : 'text-ink'}>{c.text}</span>
            </li>
          );
        }
        return (
          <li key={c.line}>
            <button
              type="button"
              disabled={busyLine === c.line}
              aria-pressed={c.done}
              onClick={() => onToggle(c)}
              data-tip={c.done ? t`un-tick this criterion in the doc` : t`tick this criterion in the doc`}
              className="flex w-full items-start gap-2 rounded px-1 py-0.5 text-left transition-colors hover:bg-surface2/50 disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
            >
              {mark}
              <span className={c.done ? 'text-ink-dim line-through' : 'text-ink'}>{c.text}</span>
            </button>
            {lint?.(c)}
            {action?.(c)}
          </li>
        );
      })}
    </ul>
  );
}

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

/** One phase's body inside PhasePanel (inline, in place of the phase list), tabbed: Story (PhaseCard + the
 * forecast story), Criteria (interactive checks + the doc), Runs (run state,
 * diagnosis, provenance), Review (the run branch's diff, verdict and landing —
 * plans/PhaseReview.tsx), Report (what was shipped) and Edit (raw markdown). */
export function PhaseDetailPanel({
  epic,
  phase,
  tab,
  onTab,
  tabHref,
  onStep,
  runBusy,
  planRunning,
  phaseRunModel,
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
  runBusy: number | null;
  /** A whole-plan run owns the phase docs — per-phase runs stand down. */
  planRunning: boolean;
  /** The picker's value, for DocModelChip — see PhaseList's note. */
  phaseRunModel: PhaseRunModel;
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
  // Likewise a phase that never ran has no Review tab; `review` degrades to Runs.
  const activeTab: PhaseDetailTab =
    tab === 'edit' && !editable ? 'report' : tab === 'review' && !showsReviewTab(phase) ? 'runs' : tab;
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

  return (
    <PhasePanel
      epic={epic}
      phase={phase}
      tab={activeTab}
      onTab={onTab}
      onPrev={prev !== undefined ? () => onStep(prev.seq) : undefined}
      onNext={next !== undefined ? () => onStep(next.seq) : undefined}
      editable={editable}
      hrefFor={tabHref}
    >
      {activeTab === 'edit' ? (
        <DocEditor
          taskId={epic.taskId}
          path={phase.docRelPath}
          version={phase.docUpdatedAt}
          onSaved={onDocChanged}
        />
      ) : activeTab === 'report' ? (
        <PhaseSummary taskId={epic.taskId} phase={phase} doc={doc} />
      ) : activeTab === 'plain' ? (
        <PlainSummary taskId={epic.taskId} kind="phase" path={phase.docRelPath} version={phase.docUpdatedAt} />
      ) : activeTab === 'story' ? (
        <div className="space-y-5">
          <PhaseCard
            phase={phase}
            {...(primary !== null ? { primary } : {})}
            secondaries={[
              ...(again !== null ? [again] : []),
              ...(isUnresolvedOutcome(phase.runOutcome) ? [{ label: t`why it did not move`, onClick: onOpenOutcome }] : []),
            ]}
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
          <ForecastStory phase={phase} />
          {phase.surprise == null && <ForecastSection phase={phase} />}
        </div>
      ) : activeTab === 'review' ? (
        <div className="space-y-4">
          {/* Keyed by phase: ↑/↓ step navigation keeps the tab, and a land still in
              flight for the previous phase must not render onto this one. A phase
              the daemon never ran has nothing to land — only its findings. */}
          {hasReviewTab(phase) && (
            <PhaseReview key={phase.id} epic={epic} phase={phase} terms={terms} onLanded={onDocChanged} />
          )}
          <ReviewFindings
            blocks={phase.reviewBlocks ?? []}
            legacy={phase.legacyReview === true}
            hold={phase.reviewHold ?? ''}
            decision={phase.reviewDecision ?? null}
            onAnswered={onDocChanged}
          />
        </div>
      ) : activeTab === 'runs' ? (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
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
            <ReverifyButton action={again} size="panel" className="ml-auto" />
            {primary !== null && (
              <button
                type="button"
                disabled={primary.disabled}
                data-tip={'tip' in primary ? primary.tip : undefined}
                onClick={primary.onClick}
                className={`${again === null ? 'ml-auto' : ''} rounded-md border px-2 py-1 font-mono text-[10px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${running ? 'border-red/40 text-red hover:bg-red/10' : runButtonCls(phase.runOutcome)}`}
              >
                {primary.label}
              </button>
            )}
          </div>
          {running && phase.runSessionUuid !== null && (
            <Link to={sessionHref(phase.runSessionUuid)} className="mt-2 inline-block font-mono text-[10px] text-ink-dim hover:text-brand hover:underline">
              <Trans>session →</Trans>
            </Link>
          )}
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
          <RailSection label={t`acceptance criteria`}>
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
          </RailSection>
          <RailSection label={t`doc`}>{doc === null ? <Loading label={t`doc…`} /> : <Markdown text={doc} />}</RailSection>
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
