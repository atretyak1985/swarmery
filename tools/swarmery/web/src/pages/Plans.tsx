// Plans (Swarmery v4 handoff §6): a workspace plan IS an epic. The list column
// (plans/PlanList) holds every plan of the project in groups, the detail column a
// plan: its header (plans/PlanHeader), then the phases. Plan lifecycle (Pause /
// Resume / Archive / Restore) is a file operation on the daemon side, and any plan
// doc opens for EDITING inline — the workspace folder becomes invisible
// infrastructure (read, edit, track from the platform; files stay the storage).
//
// A detail REPLACES the phase list, under the plan header — plan docs and
// completion reports get the whole column instead of a 360px rail. The "← all
// phases" control (or Escape) sits ABOVE the panel, not inside its header,
// where it used to get lost against the panel's own identity block.
//
// The parts live under pages/plans/: PlanList, EpicDetail (a plan on screen),
// PhaseLedger, PhaseDetailPanel, PlanDetailPanel, PlanSessions. This file owns what
// has to outlive a plan switch: the URL as the selection, the loaded epics and the
// run handlers.
//
// Running work: a phase runs headlessly from its own doc (the phase-run
// mechanism), and the WHOLE plan can be handed to one agent instead — "run
// plan" in the header opens an inline agent + mode picker and POSTs
// /api/epics/{taskId}/run. That run drives core's `run-plan` skill; its progress
// shows up as the phase docs' checkboxes ticking, not as per-phase run chips, so
// per-phase Run buttons stand down while it owns the docs.
//
// Legacy chip: phases that were activated into a board task before the
// plan↔board decoupling (interactive-planning-v2 phase 4) still show the
// "activated · <column>" chip from the DTO's boardTaskExternalId/boardColumn
// fields. No new activations can be created; the Board page is exclusively for
// tasks created on the board. Phase runs are handled by the phase-run mechanism
// (phase 5).
//
// Liveness: the epic list refetches on the board's `task_updated` WS signal
// (column moves on legacy-linked board tasks) AND on `plan_updated` (checkbox
// flips, lifecycle transitions, plan rescans) so progress ticks without a reload.
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useNavigationType, useParams } from 'react-router-dom';
import { cancelEpicPhaseRun, cancelEpicPlanRun, epicLifecycle, fetchEpics, runEpicPhase, runEpicPlan, type EpicLifecycleAction, type PhaseRunBranchError } from '../api';
import type { Epic, PlanRunMode, WSMessage } from '../api/types';
import { PlanBranchDirtyModal, type PlanBranchDirty } from '../components/PlanBranchDirtyModal';
import { RunOutcomeModal } from '../components/RunOutcomeModal';
import { tabParamHref } from '../components/Tabs';
import { PlaceLayout } from '../components/place/PlaceLayout';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { useDocumentTitle } from '../lib/useDocumentTitle';
import { useProjectVcs } from '../lib/useProjectVcs';
import { useLiveUpdates } from '../lib/ws';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { EpicDetail } from './plans/EpicDetail';
import { PHASE_RUN_EFFORT_KEY, PHASE_RUN_MODEL_KEY, type PhaseRunEffort, type PhaseRunModel, readStoredPhaseRunEffort, readStoredPhaseRunModel } from './plans/PhaseRunPickers';
import { PlanList } from './plans/PlanList';
import { canonicalPlansTarget, FILTER_LABEL, planFilterOf, plansTitle, revisionNotFound, staleNotice, type DetailSel, type DetailTarget, type EpicFilter } from './plans/plansTarget';
import { parsePlansRoute, plansHref, sameHref, type PlansRouteParams, type PlansTarget } from './plans/plansUrl';
import { reverifyAction, useReverify } from './plans/useReverify';

export function Plans(): JSX.Element {
  const { t, i18n } = useLingui();
  const { slug, project, projectId, loading: projLoading } = useProjectWorkspace();
  // The project's code host, fetched once per plan page (cached 60s by the
  // daemon): its `terms` label every landing control in the Review tab.
  const { vcs } = useProjectVcs(projectId);
  // The loaded epics, stamped with the project they belong to. Plans is not
  // remounted when /p/:slug changes and projectId follows the slug at once, so
  // until the new project's fetch lands the rows are the PREVIOUS project's — and
  // no URL may be judged against them (a correction would rewrite the new URL,
  // and on Back the old entry). `epics` is therefore null until they match.
  const [loaded, setLoaded] = useState<{ projectId: number | null; rows: Epic[] } | null>(null);
  const epics = loaded !== null && loaded.projectId === projectId ? loaded.rows : null;
  // The project the latest fetch was for: a slower fetch for a project we have
  // since left must not land over it.
  const fetchingFor = useRef<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyLifecycle, setBusyLifecycle] = useState(false);

  // The URL IS the selection (plans-deep-links phase 1): the plan by externalId,
  // the filter, the open phase (by seq) or plan-details tab, and the revision.
  // Nothing here is useState any more — every operator click is a push
  // navigation and every automatic correction a replace (see `go` / `correct`).
  const params = useParams<keyof PlansRouteParams>();
  const location = useLocation();
  const navigate = useNavigate();
  const route = useMemo(
    () =>
      parsePlansRoute(
        {
          plan: params.plan,
          seq: params.seq,
          phaseTab: params.phaseTab,
          planTab: params.planTab,
          revId: params.revId,
        },
        location.search,
      ),
    [params.plan, params.seq, params.phaseTab, params.planTab, params.revId, location.search],
  );
  const urlEpic = useMemo(
    () => (route.plan !== null ? ((epics ?? []).find((e) => e.externalId === route.plan) ?? null) : null),
    [epics, route.plan],
  );
  // A named plan decides its own filter (a lifecycle move to Archived keeps the
  // URL on the plan and the filter follows); otherwise `?status=`, default Active.
  const filter: EpicFilter = urlEpic !== null ? planFilterOf(urlEpic) : (route.status ?? 'active');
  const selected = urlEpic?.taskId ?? null; // taskId — everything keyed by it keeps working
  const detailTarget: DetailTarget | null =
    route.detail === null
      ? null
      : route.detail.kind === 'phase'
        ? { kind: 'phase', seq: route.detail.seq, tab: route.detail.tab }
        : { kind: 'plan', tab: route.detail.tab };
  const urlRevId = route.detail?.kind === 'plan' ? (route.detail.revId ?? null) : null;

  const here = `${location.pathname}${location.search}`;
  /** An operator click: one history entry — none when it names where we are
   * (react-router 7 would otherwise push a duplicate of the same location). */
  const go = useCallback(
    (t: PlansTarget): void => {
      const href = plansHref(slug, t, location.search);
      if (!sameHref(href, here)) navigate(href);
    },
    [slug, location.search, here, navigate],
  );
  /** An automatic correction: replace, and only when it changes something, so
   * it can neither add a Back step nor loop. A stale link's correction carries
   * the notice naming what was not found (SC-8) in the history state. */
  const correct = useCallback(
    (t: PlansTarget, notice: string | null = null): void => {
      const href = plansHref(slug, t, location.search);
      if (sameHref(href, here)) return;
      navigate(href, notice !== null ? { replace: true, state: { plansNotice: notice } } : { replace: true });
    },
    [slug, location.search, here, navigate],
  );

  // The stale-link notice. Held here, not read from `location.state` on each
  // render: a follow-up replace (another correction, ScopeProvider's ?scope=
  // rewrite) carries no state and must not drop it. It clears on the next PUSH —
  // an operator click — and on Back/Forward to an entry that carries none.
  const navType = useNavigationType();
  const stateNotice = (location.state as { plansNotice?: unknown } | null)?.plansNotice;
  const [notice, setNotice] = useState<string | null>(
    typeof stateNotice === 'string' ? stateNotice : null,
  );
  useEffect(() => {
    if (typeof stateNotice === 'string') setNotice(stateNotice);
    else if (navType !== 'REPLACE') setNotice(null);
  }, [stateNotice, navType]);

  const reload = useCallback((): void => {
    fetchingFor.current = projectId;
    if (projectId === null) {
      setLoaded({ projectId: null, rows: [] });
      return;
    }
    fetchEpics(projectId)
      .then((rows) => {
        if (fetchingFor.current !== projectId) return;
        setLoaded({ projectId, rows });
        setError(null);
      })
      .catch((e: unknown) => {
        if (fetchingFor.current !== projectId) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [projectId]);

  useEffect(() => {
    reload();
  }, [reload]);

  // A board task changing (activation, a move to done) can change a phase's
  // gate, and plan_updated fires on checkbox flips / lifecycle transitions —
  // refetch the epics on either signal for this project.
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type !== 'task_updated' && msg.type !== 'plan_updated') return;
      if (projectId !== null && msg.payload.projectId !== projectId) return;
      reload();
    },
    [projectId, reload],
  );
  useLiveUpdates(onMessage, reload);

  // Reconcile net while any visible phase run (or re-verification) is live:
  // plan_updated covers the run edges and the verdict stamp, this 5s poll covers
  // a missed frame mid-run. Cleared when none run.
  const anyRunning = useMemo(
    () =>
      (epics ?? []).some(
        (e) =>
          e.planRun?.runState === 'running' ||
          e.phases.some((p) => p.runState === 'running' || p.verifyRunning === true),
      ),
    [epics],
  );
  useEffect(() => {
    if (!anyRunning) return;
    const id = setInterval(reload, 5000);
    return () => {
      clearInterval(id);
    };
  }, [anyRunning, reload]);

  const filtered = useMemo(
    () => (epics ?? []).filter((e) => planFilterOf(e) === filter),
    [epics, filter],
  );

  // Machine corrections, all replace (a correction is not a step): `/plans` →
  // the first plan of its filter (an EMPTY filter stays put, empty state);
  // `?task=`/`?plan=` [&phase=][&tab=revisions] → the canonical externalId path
  // (the numeric hand-off is permanent — PlanningMode, PlanRunCard and old
  // bookmarks use it); an unknown plan / phase → the nearest valid level; an
  // unavailable or malformed tab → the tab its panel falls back to. Only once
  // THIS project's epics have loaded (`epics` is null while the rows are another
  // project's — see `loaded`): [] also means "project not resolved yet" on a cold
  // visit, and ScopeProvider is still rewriting ?scope= then (main.tsx
  // ProjectTabRedirect). Idempotent: `correct` no-ops when nothing changes.
  useEffect(() => {
    if (epics === null || epics.length === 0) return;
    // A plan / phase / legacy id the link named and this project does not have
    // rides this replace as a notice (SC-8). A canonicalised tab (malformed, or
    // unavailable on this plan / phase) is expected and stays silent.
    const { target, missing } = canonicalPlansTarget(route, epics);
    correct(target, staleNotice(missing, location.search));
  }, [epics, route, correct, location.search]);

  const activeEpic = useMemo(
    () => (selected !== null ? (filtered.find((e) => e.taskId === selected) ?? null) : null),
    [filtered, selected],
  );

  // The browser tab names what is open (SC-11), most specific first.
  useDocumentTitle(plansTitle(activeEpic, detailTarget, project?.name ?? slug));

  const lifecycle = (epic: Epic, action: EpicLifecycleAction): void => {
    if (
      action === 'archive' &&
      !window.confirm(t`Archive this plan? The task folder moves to the archive/ zone.`)
    )
      return;
    setBusyLifecycle(true);
    setActionError(null);
    epicLifecycle(epic.taskId, action)
      .then(() => reload())
      .catch((e: unknown) => {
        setActionError(e instanceof Error ? e.message : String(e));
        // A 409 usually means the UI acted on stale state — refresh to reconcile.
        reload();
      })
      .finally(() => setBusyLifecycle(false));
  };

  // Phase-run controls (interactive-planning-v2 phase 6), lifted here so the
  // phase rail can offer Retry on a failed run. The server re-checks every
  // gate; the client-side disable is a courtesy, and a 409 body (unmet deps,
  // already running) surfaces in the inline error strip in EpicDetail.
  const [runBusy, setRunBusy] = useState<number | null>(null); // phase id
  // Keyed by the plan the failure belongs to. A bare string here was rendered under
  // whichever epic happened to be selected, so one plan's acquire error appeared to
  // afflict every plan in the workspace — and survived switching between them.
  // forcePhaseId is set only for the one refusal `force` overrides
  // (`blocked-unchanged`): the phase the strip's "Run anyway" re-runs.
  // branches is set only for `deps-unmerged`: the diverged dependency branches
  // the strip offers to open a change request for.
  const [runMsg, setRunMsg] = useState<{
    taskId: number;
    text: string;
    forcePhaseId: number | null;
    branches: string[];
  } | null>(null);
  const failRunMsg = useCallback(
    (taskId: number) =>
      (e: unknown): void =>
        setRunMsg({ taskId, text: e instanceof Error ? e.message : String(e), forcePhaseId: null, branches: [] }),
    [],
  );
  // Which phase's run diagnosis is open (phase id) — the modal is read-only, so
  // it can be open over any state, including a live plan run.
  const [outcomeFor, setOutcomeFor] = useState<number | null>(null);
  // Switching plans closes the run-diagnosis modal: its phase id belongs to the
  // old plan. (The detail panel closes by itself — the new plan's URL names none.)
  useEffect(() => {
    setOutcomeFor(null);
  }, [selected]);

  // Which model the next phase run starts with. Lives HERE, with startRun, so the
  // three places that can start one — the phase row's Run button, the detail
  // panel's Retry and the diagnosis modal's Retry — cannot disagree about it.
  const [phaseRunModel, setPhaseRunModel] = useState<PhaseRunModel>(readStoredPhaseRunModel);
  // The effort twin, owned here for the same reason: the three places that can
  // start a run must not disagree about how hard it thinks either.
  const [phaseRunEffort, setPhaseRunEffort] = useState<PhaseRunEffort>(readStoredPhaseRunEffort);

  const startRun = useCallback(
    (taskId: number, phaseId: number, force = false): void => {
      setRunBusy(phaseId);
      setRunMsg(null);
      try {
        localStorage.setItem(PHASE_RUN_MODEL_KEY, phaseRunModel);
        localStorage.setItem(PHASE_RUN_EFFORT_KEY, phaseRunEffort);
      } catch {
        // storage unavailable — the choices still apply to this run
      }
      // `default` means send no key at all: undefined, not ''. For both, that is
      // what hands the decision to the phase doc's own header line.
      runEpicPhase(
        taskId,
        phaseId,
        phaseRunModel === 'default' ? undefined : phaseRunModel,
        phaseRunEffort === 'default' ? undefined : phaseRunEffort,
        force,
      )
        .then(() => reload())
        .catch((e: unknown) => {
          failRunMsg(taskId)(e);
          // Keyed off the 409's `code`, never off which fields arrived: a
          // presence sniff silently re-classifies every future case that
          // happens to carry `branch`. The branch-holds-commits 409 is not a
          // message to read, it is a blocker with an action, so land the user
          // on the diagnosis (which offers Delete branch) instead of a toast.
          const code = e instanceof Error ? (e as PhaseRunBranchError).code : undefined;
          if (code === 'branch-dirty') setOutcomeFor(phaseId);
          // The refusal `force` overrides: the strip offers to run it anyway.
          if (code === 'blocked-unchanged' && e instanceof Error)
            setRunMsg({ taskId, text: e.message, forcePhaseId: phaseId, branches: [] });
          // Stranded dependency work: the strip offers "Open <change>" per branch.
          if (code === 'deps-unmerged' && e instanceof Error)
            setRunMsg({ taskId, text: e.message, forcePhaseId: null, branches: (e as PhaseRunBranchError).branches ?? [] });
        })
        .finally(() => setRunBusy(null));
    },
    [reload, failRunMsg, phaseRunModel, phaseRunEffort],
  );
  // The strip's "Run anyway" for the plan on screen, or null when its refusal is
  // not the one `force` overrides.
  const forceRunFor = (taskId: number): (() => void) | null => {
    if (runMsg === null || runMsg.taskId !== taskId) return null;
    const phaseId = runMsg.forcePhaseId;
    return phaseId === null ? null : () => startRun(taskId, phaseId, true);
  };
  const cancelRun = useCallback(
    (taskId: number, phaseId: number): void => {
      setRunBusy(phaseId);
      setRunMsg(null);
      cancelEpicPhaseRun(taskId, phaseId)
        .then(() => reload())
        .catch(failRunMsg(taskId))
        .finally(() => setRunBusy(null));
    },
    [reload, failRunMsg],
  );

  // "Verify again": grade a finished phase's run branch with the read-only
  // verifier, no executor. One hook for every site that offers it (row, detail
  // panel, diagnosis modal); a refusal lands in the same strip as a run's.
  const clearRunMsg = useCallback((): void => setRunMsg(null), []);
  const refuseReverify = useCallback((taskId: number, e: unknown): void => failRunMsg(taskId)(e), [failRunMsg]);
  const reverify = useReverify({ onStart: clearRunMsg, onAccepted: reload, onRefused: refuseReverify });
  // The phase the diagnosis modal is open on — its Verify again reads the same row.
  const outcomePhase = activeEpic?.phases.find((p) => p.id === outcomeFor);

  // Whole-plan runs: one agent driving core's run-plan skill over every phase.
  // Same error strip as phase runs — the server re-checks every gate and its
  // 409 body (a phase run holds the docs, plan not active, already complete)
  // says which one bit.
  const [planRunBusy, setPlanRunBusy] = useState(false);
  // The branch-holds-commits 409, once it has happened. That refusal is not a
  // message to read — it is a decision (merge the commits or destroy them), so it
  // gets a modal instead of the error strip. The agent/mode of the refused call
  // ride along so "Run plan again" replays it exactly rather than re-deriving it.
  const [planDirty, setPlanDirty] = useState<{
    dirty: PlanBranchDirty;
    taskId: number;
    agent: string;
    mode: PlanRunMode;
  } | null>(null);

  const startPlanRun = useCallback(
    (taskId: number, agent: string, mode: PlanRunMode): void => {
      setPlanRunBusy(true);
      setRunMsg(null);
      runEpicPlan(taskId, { agent, mode })
        .then(() => reload())
        .catch((e: unknown) => {
          failRunMsg(taskId)(e);
          // Keyed off the 409's `code`, never off which fields arrived: a
          // presence sniff silently re-classifies every future case that
          // happens to carry `branch`. The fields stay display data.
          const branchErr = e as PhaseRunBranchError;
          if (e instanceof Error && branchErr.code === 'branch-dirty')
            setPlanDirty({
              dirty: {
                branch: branchErr.branch ?? '',
                commitsAhead: branchErr.commitsAhead ?? 0,
                base: branchErr.base ?? '',
                message: e.message,
              },
              taskId,
              agent,
              mode,
            });
        })
        .finally(() => setPlanRunBusy(false));
    },
    [reload, failRunMsg],
  );
  const cancelPlanRun = useCallback(
    (taskId: number): void => {
      setPlanRunBusy(true);
      setRunMsg(null);
      cancelEpicPlanRun(taskId)
        .then(() => reload())
        .catch(failRunMsg(taskId))
        .finally(() => setPlanRunBusy(false));
    },
    [reload, failRunMsg],
  );

  if (projLoading) return <Loading label={t`workspace…`} />;
  if (project === null) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>
          <Trans>unknown project — pick one from the switcher</Trans>
        </Empty>
      </div>
    );
  }
  if (error !== null) {
    return (
      <div className="px-4 py-6 desk:px-8">
        <ErrorBox message={error} onRetry={reload} />
      </div>
    );
  }
  if (epics === null) return <Loading label={t`epics…`} />;

  // Resolve the selection against the CURRENT epic — a refetch can drop the
  // referenced phase (plan rescan), in which case the panel closes itself and
  // the phase list comes back.
  const filterLabel = i18n._(FILTER_LABEL[filter]);
  let detail: DetailSel | null = null;
  if (activeEpic !== null && detailTarget !== null) {
    if (detailTarget.kind === 'plan') {
      detail = { kind: 'plan', tab: detailTarget.tab };
    } else {
      const p = activeEpic.phases.find((x) => x.seq === detailTarget.seq);
      detail = p !== undefined ? { kind: 'phase', phase: p, tab: detailTarget.tab } : null;
    }
  }

  const openDetails = (): void => {
    if (activeEpic !== null) go({ plan: activeEpic.externalId, detail: { kind: 'plan', tab: 'plan' } });
  };
  const newPlanHref = `/p/${encodeURIComponent(slug)}/plans${tabParamHref(location.search, 'tab', 'new', 'plans')}`;

  return (
    <>
      <PlaceLayout
        narrow={detail !== null ? 'detail' : 'both'}
        list={
          <PlanList
            epics={epics}
            filter={filter}
            onFilter={(status) => go({ plan: null, status })}
            selectedTaskId={selected}
            // A row is a real link (Cmd-click opens a new tab). The selected one links
            // to where we are, so re-clicking it keeps its open details.
            hrefOf={(e) => (e.taskId === selected ? here : plansHref(slug, { plan: e.externalId }, location.search))}
            onStep={(e) => go({ plan: e.externalId })}
            onPrimary={openDetails}
            onNew={() => navigate(newPlanHref)}
          />
        }
        detail={
          // No padding above: the plan's and the phase's bands start at the column's top.
          <div className="px-4 pb-10 desk:px-7">
            {actionError !== null && (
              <div
                role="alert"
                className="my-3 flex items-center gap-2 rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11px] text-red"
              >
                <span className="min-w-0 flex-1">{actionError}</span>
                <button type="button" onClick={() => setActionError(null)} aria-label={t`dismiss`} className="text-red/70">
                  ×
                </button>
              </div>
            )}
            {/* A stale link's notice (SC-8): the actionError strip's shape, in amber —
                the page did land somewhere valid, nothing failed. */}
            {notice !== null && (
              <div
                role="status"
                className="my-3 flex items-center gap-2 rounded-lg border border-amber/40 bg-amber/10 px-3 py-1.5 font-mono text-[11px] text-amber"
              >
                <span className="min-w-0 flex-1">{notice}</span>
                <button type="button" onClick={() => setNotice(null)} aria-label={t`dismiss notice`} className="text-amber/70">
                  ×
                </button>
              </div>
            )}
            {activeEpic === null ? (
              epics.length === 0 ? null : (
                <div className="pt-[22px]">
                  <Empty>
                    {filtered.length === 0 ? <Trans>no {filterLabel} plans</Trans> : <Trans>select an epic</Trans>}
                  </Empty>
                </div>
              )
            ) : (
            <EpicDetail
              epic={activeEpic}
              detail={detail}
              busyLifecycle={busyLifecycle}
              onLifecycle={lifecycle}
              onDocChanged={reload}
              onOpenPhase={(seq, tab) => go({ plan: activeEpic.externalId, detail: { kind: 'phase', seq, tab } })}
              onOpenPlan={(tab) => go({ plan: activeEpic.externalId, detail: { kind: 'plan', tab } })}
              onCloseDetail={() => go({ plan: activeEpic.externalId })}
              phaseHref={(seq, tab) =>
                plansHref(slug, { plan: activeEpic.externalId, detail: { kind: 'phase', seq, tab } }, location.search)
              }
              planHref={(tab) =>
                plansHref(slug, { plan: activeEpic.externalId, detail: { kind: 'plan', tab } }, location.search)
              }
              revId={urlRevId}
              onOpenRevision={(revId) =>
                go({ plan: activeEpic.externalId, detail: { kind: 'plan', tab: 'revisions', revId } })
              }
              onRevisionMissing={() => {
                // The revision the link named is not this plan's (SC-8).
                correct(
                  { plan: activeEpic.externalId, detail: { kind: 'plan', tab: 'revisions' } },
                  urlRevId !== null ? revisionNotFound(urlRevId) : null,
                );
              }}
              runBusy={runBusy}
              runMsg={runMsg !== null && runMsg.taskId === activeEpic.taskId ? runMsg.text : null}
              runBranches={runMsg !== null && runMsg.taskId === activeEpic.taskId ? runMsg.branches : []}
              onForceRun={forceRunFor(activeEpic.taskId)}
              onRun={(phaseId) => startRun(activeEpic.taskId, phaseId)}
              onCancelRun={(phaseId) => cancelRun(activeEpic.taskId, phaseId)}
              reverify={reverify}
              onReverify={(phaseId) => reverify.start(activeEpic.taskId, phaseId)}
              phaseRunModel={phaseRunModel}
              onPhaseRunModel={setPhaseRunModel}
              phaseRunEffort={phaseRunEffort}
              onPhaseRunEffort={setPhaseRunEffort}
              planRunBusy={planRunBusy}
              onRunPlan={(agent, mode) => startPlanRun(activeEpic.taskId, agent, mode)}
              onCancelPlanRun={() => cancelPlanRun(activeEpic.taskId)}
              terms={vcs?.terms ?? null}
              onOpenOutcome={setOutcomeFor}
            />
            )}
          </div>
        }
      />

      {/* Why a run did not move the phase. Read-only, so it opens over any
          state — including a live plan run, where only its write actions
          (Delete branch / Retry run) stand down. */}
      {outcomeFor !== null && activeEpic !== null && (
        <RunOutcomeModal
          taskId={activeEpic.taskId}
          phaseId={outcomeFor}
          writesDisabled={
            activeEpic.planRun?.runState === 'running' ||
            activeEpic.status !== 'active' ||
            runBusy !== null
          }
          writesDisabledReason={
            activeEpic.planRun?.runState === 'running'
              ? t`a whole-plan run is executing this plan`
              : activeEpic.status !== 'active'
                ? t`plan is not active`
                : t`a phase run is already starting`
          }
          onClose={() => setOutcomeFor(null)}
          onRetry={() => startRun(activeEpic.taskId, outcomeFor)}
          reverify={
            outcomePhase === undefined
              ? null
              : reverifyAction(outcomePhase, reverify, (id) => reverify.start(activeEpic.taskId, id), i18n)
          }
          onOpenRevisions={() => {
            setOutcomeFor(null);
            go({ plan: activeEpic.externalId, detail: { kind: 'plan', tab: 'revisions' } });
          }}
        />
      )}

      {/* The plan run refused to start because its branch still holds commits.
          Not an error strip: the user has to choose between merging that work and
          destroying it, and the modal is where both choices live. */}
      {planDirty !== null && (
        <PlanBranchDirtyModal
          taskId={planDirty.taskId}
          dirty={planDirty.dirty}
          onClose={() => setPlanDirty(null)}
          onRetry={() => startPlanRun(planDirty.taskId, planDirty.agent, planDirty.mode)}
        />
      )}
    </>
  );
}
