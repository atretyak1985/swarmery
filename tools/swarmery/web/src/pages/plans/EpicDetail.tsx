// A plan on screen: its header, then the phases or the plan's details. An open
// phase is a page of its own and takes the whole column.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { fetchRevisions, startRevision, type EpicLifecycleAction, type RevisionStartError } from '../../api';
import type { Epic, PlanRevision, PlanRunMode, ProviderTerms } from '../../api/types';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { ReviseModal } from '../planning/ReviseModal';
import { DepsUnmergedActions } from './DepsUnmergedActions';
import { NEUTRAL_TERMS } from './landingModel';
import { useNow } from './phaseChips';
import { PhaseDetailPanel } from './PhaseDetailPanel';
import { PhaseLedger } from './PhaseLedger';
import type { PhaseRunEffort, PhaseRunModel } from './PhaseRunPickers';
import { PlanDetailPanel } from './PlanDetailPanel';
import { PlanHeader } from './PlanHeader';
import { computeResolvedSeqs, planComplete } from './planModel';
import { PlanSessions, usePlanSessions } from './PlanSessions';
import type { DetailSel, PhaseDetailTab } from './plansTarget';
import type { PlanDetailTab } from './plansUrl';
import { useTerminalSessions } from './TerminalBadge';
import type { Reverify } from './useReverify';

export function EpicDetail({
  epic,
  detail,
  busyLifecycle,
  onLifecycle,
  onDocChanged,
  onOpenPhase,
  onOpenPlan,
  onCloseDetail,
  phaseHref,
  planHref,
  revId,
  onOpenRevision,
  onRevisionMissing,
  runBusy,
  runMsg,
  runBranches,
  onForceRun,
  onRun,
  onCancelRun,
  reverify,
  onReverify,
  phaseRunModel,
  onPhaseRunModel,
  phaseRunEffort,
  onPhaseRunEffort,
  planRunBusy,
  onRunPlan,
  onCancelPlanRun,
  onOpenOutcome,
  terms,
}: {
  epic: Epic;
  detail: DetailSel | null;
  busyLifecycle: boolean;
  onLifecycle: (epic: Epic, action: EpicLifecycleAction) => void;
  /** A doc write (save or checkbox toggle) landed — refetch so the rollup follows. */
  onDocChanged: () => void;
  onOpenPhase: (seq: number, tab: PhaseDetailTab) => void;
  onOpenPlan: (tab: PlanDetailTab) => void;
  onCloseDetail: () => void;
  /** The URLs of a phase panel tab / a plan-details tab — what their links point at. */
  phaseHref: (seq: number, tab: PhaseDetailTab) => string;
  planHref: (tab: PlanDetailTab) => string;
  /** The revision the URL names (`/details/revisions/<revId>`), or null. */
  revId: number | null;
  /** Open one revision on the Revisions tab (a push). */
  onOpenRevision: (revId: number) => void;
  /** The URL's revision is not among this plan's — correct to Revisions. */
  onRevisionMissing: () => void;
  runBusy: number | null;
  runMsg: string | null;
  /** A `deps-unmerged` refusal's diverged dependency branches; [] otherwise. */
  runBranches: string[];
  /** Re-run the refused phase with `force` — set only for a `blocked-unchanged`
   *  refusal, the one `force` overrides; null hides the action. */
  onForceRun: (() => void) | null;
  onRun: (phaseId: number) => void;
  onCancelRun: (phaseId: number) => void;
  /** "Verify again" — the page's one hook, shared by the list and the panel. */
  reverify: Reverify;
  onReverify: (phaseId: number) => void;
  /** The model every per-phase run on this plan starts with — owned by the page
   * so the list's Run, the detail panel's Retry and the diagnosis modal's Retry
   * all spend the same way. */
  phaseRunModel: PhaseRunModel;
  onPhaseRunModel: (m: PhaseRunModel) => void;
  /** How hard every per-phase run on this plan thinks — owned by the page for
   * the same reason the model is. */
  phaseRunEffort: PhaseRunEffort;
  onPhaseRunEffort: (e: PhaseRunEffort) => void;
  planRunBusy: boolean;
  onRunPlan: (agent: string, mode: PlanRunMode) => void;
  onCancelPlanRun: () => void;
  /** Open the run-diagnosis modal for a phase id. */
  onOpenOutcome: (phaseId: number) => void;
  /** The project's code-host vocabulary (GET /api/projects/{id}/vcs), null
   * until it loads — the Review tab then uses the terms its own response carries. */
  terms: ProviderTerms | null;
}): JSX.Element {
  const { t } = useLingui();
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const complete = planComplete(epic, resolvedSeqs);
  const now = useNow(1000);
  // Sessions with this plan (or one of its phases) open in a terminal.
  const terminalSessions = useTerminalSessions(epic.taskId);
  const planTerminal = useMemo(
    () => terminalSessions.filter((s) => s.phaseId === null),
    [terminalSessions],
  );
  const { sessions, error: sessionsErr } = usePlanSessions(epic);
  const phaseRunActive = epic.phases.some((p) => p.runState === 'running');
  // A plan run owns every phase doc for its duration — offering per-phase runs
  // next to it would invite two worktrees editing the same files.
  const planRunning = epic.planRun?.runState === 'running';

  // Plan revisions (plan-revision phase 4), fetched once per selected plan and
  // shared by the Revisions tab, its count badge, and the phase panels'
  // "revised — see Revisions" note. `null` while loading; a fetch failure (e.g.
  // 503 planning not attached) surfaces in the tab, not as a page error.
  const [revisions, setRevisions] = useState<PlanRevision[] | null>(null);
  const [revisionsErr, setRevisionsErr] = useState<string | null>(null);
  // Which plan `revisions` belongs to: this component is not keyed by plan, so
  // for one render after a plan switch the list is still the OLD plan's — and a
  // revId must never be judged against another plan's revisions.
  const [revisionsOf, setRevisionsOf] = useState<number | null>(null);
  // Reloads still out. While one is, the list may predate a revision the URL
  // already names (ReviseModal links to a just-staged one as it reloads), so
  // an unknown revId is not judged until they are all back.
  const [revisionsInFlight, setRevisionsInFlight] = useState(0);
  // The plan the latest reload was for: a slower fetch for a plan we have since
  // left must not land over the current plan's list (the epics fetch's guard).
  const revisionsFetchingFor = useRef<number | null>(null);
  const reloadRevisions = useCallback((): void => {
    const taskId = epic.taskId;
    revisionsFetchingFor.current = taskId;
    setRevisionsInFlight((n) => n + 1);
    fetchRevisions(taskId)
      .then((rs) => {
        if (revisionsFetchingFor.current !== taskId) return;
        setRevisions(rs);
        setRevisionsErr(null);
        setRevisionsOf(taskId);
      })
      .catch((e: unknown) => {
        if (revisionsFetchingFor.current !== taskId) return;
        setRevisions([]);
        setRevisionsErr(e instanceof Error ? e.message : String(e));
        setRevisionsOf(taskId);
      })
      .finally(() => setRevisionsInFlight((n) => n - 1));
  }, [epic.taskId]);
  useEffect(() => {
    setRevisions(null);
    setRevisionsErr(null);
    reloadRevisions();
  }, [reloadRevisions]);
  const stagedCount = useMemo(
    () => (revisions ?? []).filter((r) => r.status === 'staged').length,
    [revisions],
  );
  // A link to a revision this plan does not have → the Revisions tab (replace).
  // Judged only against THIS plan's loaded list; a failed fetch proves nothing.
  const revisionsReady =
    revisions !== null && revisionsOf === epic.taskId && revisionsErr === null && revisionsInFlight === 0;
  const revisionKnown = revId === null || (revisions ?? []).some((r) => r.id === revId);
  useEffect(() => {
    if (revisionsReady && !revisionKnown) onRevisionMissing();
  }, [revisionsReady, revisionKnown, onRevisionMissing]);

  // Revise-plan entry point: reason modal → POST → land on the planning page,
  // where the revise wizard interviews against this plan.
  const navigate = useNavigate();
  const { slug } = useProjectWorkspace();
  const [reviseOpen, setReviseOpen] = useState(false);
  const [reviseBusy, setReviseBusy] = useState(false);
  const [reviseErr, setReviseErr] = useState<string | null>(null);
  const [reviseOpenRevId, setReviseOpenRevId] = useState<number | null>(null);
  const submitRevise = (reason: string): void => {
    setReviseBusy(true);
    setReviseErr(null);
    setReviseOpenRevId(null);
    startRevision(epic.taskId, reason)
      .then(() => {
        navigate(`/p/${slug}/planning`);
      })
      .catch((e: unknown) => {
        setReviseErr(e instanceof Error ? e.message : String(e));
        const openId = (e as RevisionStartError).revisionId;
        if (typeof openId === 'number') setReviseOpenRevId(openId);
      })
      .finally(() => setReviseBusy(false));
  };

  // Escape backs out of the details — the same affordance the rail's ✕ had,
  // without stealing the key while the phase list is showing.
  const open = detail !== null;
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onCloseDetail();
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
    };
  }, [open, onCloseDetail]);

  const onPhasePage = detail?.kind === 'phase';

  return (
    <div>
      {!onPhasePage && <PlanHeader
        epic={epic}
        resolvedSeqs={resolvedSeqs}
        complete={complete}
        now={now}
        terminal={planTerminal}
        busyLifecycle={busyLifecycle}
        onLifecycle={onLifecycle}
        planRunBusy={planRunBusy}
        phaseRunActive={phaseRunActive}
        onRunPlan={onRunPlan}
        onCancelPlanRun={onCancelPlanRun}
        onOpenPlan={onOpenPlan}
        stagedCount={stagedCount}
        reviseBusy={reviseBusy}
        onRevise={() => {
          setReviseErr(null);
          setReviseOpenRevId(null);
          setReviseOpen(true);
        }}
      />}

      {/* Leaving the details is the first thing offered while they are open. */}
      {detail?.kind === 'plan' && (
        <div className="mt-[22px]">
          <BackToPhases onBack={onCloseDetail} />
        </div>
      )}

      {runMsg !== null && (
        <div className={`${onPhasePage ? 'mb-3' : 'mt-3'} flex items-start gap-2 rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[10.5px] text-red`}>
          <div className="min-w-0 flex-1">
            <span>{runMsg}</span>
            {runBranches.length > 0 && (
              <DepsUnmergedActions
                taskId={epic.taskId}
                phases={epic.phases}
                branches={runBranches}
                // A failed /vcs fetch must not hide the way out: neutral words.
                terms={terms ?? NEUTRAL_TERMS}
                onLanded={onDocChanged}
              />
            )}
          </div>
          {onForceRun !== null && (
            <button
              type="button"
              disabled={runBusy !== null}
              onClick={onForceRun}
              data-tip={t`run this phase again even though nothing has changed since it blocked`}
              className="shrink-0 rounded-md border border-red/40 px-2 py-0.5 font-mono text-[10.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
            >
              <Trans>Run anyway</Trans>
            </button>
          )}
        </div>
      )}

      {/* The plan's details open in place of the ledger; "← all phases" (or Escape)
          backs out to it. The sessions stay under either. A phase's page has its own
          way back and lists its own runs. */}
      <div className={`${onPhasePage ? '' : 'mt-[22px]'} min-w-0`}>
          {detail?.kind === 'phase' ? (
            <PhaseDetailPanel
              epic={epic}
              phase={detail.phase}
              tab={detail.tab}
              onTab={(t) => onOpenPhase(detail.phase.seq, t)}
              tabHref={(t) => phaseHref(detail.phase.seq, t)}
              onStep={(seq) => onOpenPhase(seq, detail.tab)}
              onBack={onCloseDetail}
              sessions={sessions}
              now={now}
              runBusy={runBusy}
              planRunning={planRunning}
              phaseRunModel={phaseRunModel}
              onPhaseRunModel={onPhaseRunModel}
              phaseRunEffort={phaseRunEffort}
              onPhaseRunEffort={onPhaseRunEffort}
              onRetry={() => onRun(detail.phase.id)}
              onCancelRun={() => onCancelRun(detail.phase.id)}
              reverify={reverify}
              onReverify={onReverify}
              onOpenOutcome={() => onOpenOutcome(detail.phase.id)}
              onDocChanged={onDocChanged}
              revisions={revisions}
              onOpenRevisions={() => onOpenPlan('revisions')}
              terms={terms}
            />
          ) : detail?.kind === 'plan' ? (
            <PlanDetailPanel
              epic={epic}
              tab={detail.tab}
              onTab={onOpenPlan}
              tabHref={planHref}
              onDocChanged={onDocChanged}
              revisions={revisions}
              revisionsErr={revisionsErr}
              stagedCount={stagedCount}
              focusRevId={revisionKnown ? revId : null}
              onRevisionsChanged={() => {
                reloadRevisions();
                onDocChanged();
              }}
            />
          ) : (
            <PhaseLedger
              epic={epic}
              resolvedSeqs={resolvedSeqs}
              sessions={sessions}
              now={now}
              runBusy={runBusy}
              planRunning={planRunning}
              terminalSessions={terminalSessions}
              terms={terms}
              phaseRunModel={phaseRunModel}
              onPhaseRunModel={onPhaseRunModel}
              phaseRunEffort={phaseRunEffort}
              onPhaseRunEffort={onPhaseRunEffort}
              onOpenPhase={onOpenPhase}
              phaseHref={phaseHref}
              onRun={onRun}
              onCancelRun={onCancelRun}
              reverify={reverify}
              onReverify={onReverify}
              onOpenOutcome={onOpenOutcome}
            />
          )}
        {!onPhasePage && <PlanSessions epic={epic} activePhaseId={null} sessions={sessions} err={sessionsErr} />}
      </div>

      <ReviseModal
        open={reviseOpen}
        planTitle={epic.title}
        busy={reviseBusy}
        error={reviseErr}
        openRevisionId={reviseOpenRevId}
        onClose={() => setReviseOpen(false)}
        onSubmit={submitRevise}
        onOpenRevision={() => {
          setReviseOpen(false);
          reloadRevisions();
          // The 409 named the open revision — link straight to it.
          if (reviseOpenRevId !== null) onOpenRevision(reviseOpenRevId);
          else onOpenPlan('revisions');
        }}
      />
    </div>
  );
}

/** The back-out control. Lives in the plan's action row (first, ahead of "open
 * plan README" / lifecycle), not inside the detail panel's header where it used
 * to get lost against the panel's own identity block. Sized like its row
 * siblings, weighted like the lifecycle buttons so it still reads as the
 * primary way out. */
function BackToPhases({ onBack }: { onBack: () => void }): JSX.Element {
  const { t } = useLingui();
  return (
    <button
      type="button"
      onClick={onBack}
      data-tip={t`back to the phase list (Esc)`}
      className="inline-flex items-center gap-1.5 rounded-md border border-line-strong bg-surface2 px-2 py-1 font-mono text-[10.5px] text-ink transition-colors hover:bg-surface2/70 hover:text-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
    >
      <span aria-hidden="true">←</span> <Trans>all phases</Trans>
    </button>
  );
}
