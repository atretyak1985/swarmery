// Planning Mode (interactive planning v2 — phase 3): "describe what you want
// to build" → a structured wizard interview → a plan in the private workspace.
//
// The backend (internal/planning state machine) drives everything through the
// extended GET /api/projects/{id}/planning DTO; this page renders by status:
//  · '' / cancelled      — idle intake (idea textarea + Start).
//  · generating /        — spinner + elapsed + the planner's last reasoning
//    proceeding            snippet, History drawer available.
//  · awaiting_answer     — the two-pane wizard: QuestionCard (left) + sticky
//                          RunningPlanPanel (right) with refine/proceed; when
//                          the reply failed the protocol parse (currentQuestion
//                          null + rawReply set) the raw-text fallback shows the
//                          prose and a free-text box that answers via the SAME
//                          answer endpoint (questionId "" + otherText — the
//                          endpoint owns the resume, NOT sendSessionMessage).
//  · done                — "Plan ready" + link to the Plans page (the plan→board
//                          activation flow was removed in phase 4) + "Start
//                          another plan", which swaps the card for the intake
//                          (the plan itself lives on the Plans page by then, so
//                          the card would only crowd the next idea box)
//                          (Service.Start accepts a new run over a done row —
//                          markCancelled only supersedes OPEN wizards).
//  · failed              — error card + intake prefilled to start again.
//
// Frozen WS bus: session_updated/task_updated → refetch, plus a 4s reconcile
// poll while the wizard is open and a settle-poll for the workspace task row.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import type { PlanRevision, PlanningStatus, TaskSummary, WSMessage } from '../api/types';
import {
  answerPlanning,
  cancelPlanning,
  fetchPlanning,
  fetchRevisions,
  fetchTasks,
  proceedPlanning,
  refinePlanning,
  startPlanning,
} from '../api';
import { fmtAgo } from '../lib/format';
import { useSessionHref } from '../lib/sessionHref';
import { useLiveUpdates } from '../lib/ws';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { Card, Empty, ErrorBox, ExpandButton, ExpandableSection, Loading } from '../components/ui';
import { Explain } from '../components/Explain';
import { HowItWorks } from '../components/HowItWorks';
import { QuestionCard } from './planning/QuestionCard';
import { RunningPlanPanel } from './planning/RunningPlanPanel';
import { HistoryDrawer } from './planning/HistoryDrawer';
import { RefineModal } from './planning/RefineModal';

// Settle-poll cadence for the "plan ready" workspace task (wsingest rescans on
// a 60s cadence, so we poll a little faster once the wizard is done).
const PLAN_POLL_MS = 15_000;

// Reconcile-poll cadence while the wizard is open (net under the WS refetch).
const STATUS_POLL_MS = 4_000;

/** The models an operator may plan with — the same closed set the daemon
 * accepts (planning.Models); the value is the short name the API resolves. */
const PLANNING_MODELS = [
  { value: 'opus', label: 'opus — default', id: 'claude-opus-5' },
  { value: 'sonnet', label: 'sonnet — faster, cheaper', id: 'claude-sonnet-5' },
  { value: 'fable', label: 'fable — most capable, ~2× cost', id: 'claude-fable-5-1' },
] as const;
type PlanningModel = (typeof PLANNING_MODELS)[number]['value'];
const DEFAULT_PLANNING_MODEL: PlanningModel = 'opus';
const MODEL_STORAGE_KEY = 'swarmery.planning.model';

function isPlanningModel(v: string | null): v is PlanningModel {
  return PLANNING_MODELS.some((m) => m.value === v);
}

/** Last-used choice; falls back to the default when storage is unavailable. */
function readStoredModel(): PlanningModel {
  try {
    const v = localStorage.getItem(MODEL_STORAGE_KEY);
    return isPlanningModel(v) ? v : DEFAULT_PLANNING_MODEL;
  } catch {
    return DEFAULT_PLANNING_MODEL;
  }
}

/** Short name for a full model ID from the status DTO (`claude-opus-5` → `opus`). */
function modelShortName(id: string): string {
  return PLANNING_MODELS.find((m) => m.id === id)?.value ?? id;
}

/** Compact elapsed string ("3m 12s") since an RFC3339 instant. */
function fmtElapsed(startedAt: string, nowMs: number): string {
  const s = Math.max(0, Math.floor((nowMs - new Date(startedAt).getTime()) / 1000));
  return s < 60 ? `${String(s)}s` : `${String(Math.floor(s / 60))}m ${String(s % 60)}s`;
}

export function PlanningMode(): JSX.Element {
  const { projectId, project, slug, loading } = useProjectWorkspace();
  const sessionHref = useSessionHref();
  const [searchParams, setSearchParams] = useSearchParams();

  const [status, setStatus] = useState<PlanningStatus | null>(null);
  const [idea, setIdea] = useState('');
  const [plannerModel, setPlannerModel] = useState<PlanningModel>(readStoredModel);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The workspace task row that appears once the plan dir is written.
  const [plan, setPlan] = useState<TaskSummary | null>(null);
  // Free-text reply for the raw fallback (no structured question parsed).
  const [rawAnswer, setRawAnswer] = useState('');
  const [historyOpen, setHistoryOpen] = useState(false);
  const [refineOpen, setRefineOpen] = useState(false);
  // Done-state escape hatch: the plan landed and the user wants another one.
  // Re-opens the intake under the "Plan ready" card without losing the link
  // to the plan that just shipped.
  const [newPlanMode, setNewPlanMode] = useState(false);
  // 1s tick that drives the elapsed timer while the planner is thinking.
  const [nowMs, setNowMs] = useState(() => Date.now());
  // Per-SECTION expand, not per-page: this route deliberately carries no
  // `handle.fill` (src/main.tsx) because the page is a vertical stack of cards
  // and reads as a document. The two sections that can outgrow the viewport —
  // a long option list and a raw planner reply — get their own fullscreen state
  // instead, so the page keeps scrolling normally while either is collapsed.
  const [interviewExpanded, setInterviewExpanded] = useState(false);
  const [replyExpanded, setReplyExpanded] = useState(false);

  const aliveRef = useRef(true);
  const ideaRef = useRef<HTMLTextAreaElement | null>(null);
  // Monotonic counter incremented on every optimistic state flip (answer,
  // refine, proceed, start). Each loadStatus() call captures the counter at
  // launch time; if the counter advanced by the time the response arrives the
  // mutation won and we discard the stale GET — optimistic state wins.
  const mutationSeqRef = useRef(0);

  const wstatus = status?.status ?? '';
  const sessionUuid = status?.sessionUuid ?? '';
  const history = useMemo(() => status?.history ?? [], [status]);
  // Legacy net: a pre-wizard in-memory run reports active with no status row.
  const thinking =
    wstatus === 'generating' || wstatus === 'proceeding' || (wstatus === '' && status?.active === true);
  const wizardOpen = thinking || wstatus === 'awaiting_answer';

  // Revise mode (plan-revision phase 4): the wizard interviews against an
  // EXISTING plan and stages a diff — this page renders the same interview
  // with a banner, no intake (a revise session starts from the Plans page),
  // and a "Review changes" hand-off once the staged revision lands.
  const isRevise = status?.mode === 'revise';
  const reviseTaskId = status?.reviseTaskId ?? null;

  const loadStatus = useCallback((): void => {
    if (projectId === null) return;
    // Capture the mutation counter at call time; discard the response if a
    // mutation (answer/refine/proceed/start) incremented it while we awaited.
    const seq = mutationSeqRef.current;
    fetchPlanning(projectId)
      .then((s) => {
        if (!aliveRef.current) return;
        if (mutationSeqRef.current !== seq) return; // stale GET — mutation won
        setStatus(s);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [projectId]);

  useEffect(() => {
    aliveRef.current = true;
    loadStatus();
    return () => {
      aliveRef.current = false;
    };
  }, [loadStatus]);

  // Reconcile poll while the wizard is open — the WS refetch is the fast path,
  // this is the net (missed frames, daemon restarts, stale-status heal).
  useEffect(() => {
    if (!wizardOpen) return undefined;
    const t = window.setInterval(loadStatus, STATUS_POLL_MS);
    return () => {
      window.clearInterval(t);
    };
  }, [wizardOpen, loadStatus]);

  // Elapsed-timer tick while the planner is thinking.
  useEffect(() => {
    if (!thinking) return undefined;
    const t = window.setInterval(() => {
      setNowMs(Date.now());
    }, 1_000);
    return () => {
      window.clearInterval(t);
    };
  }, [thinking]);

  // Settle-poll once the wizard is done: the workspace task row for the plan
  // dir appears on wsingest's next rescan. Scoped to rows that started at/after
  // this wizard run so a pre-existing plan is never matched. Task rows carry the
  // DB path slug, so match on the resolved project's slug — the route slug may
  // be the pretty name slug.
  const dbSlug = project?.slug ?? '';
  useEffect(() => {
    // A revise run saves no new plan — the staged-revision poll below owns done.
    if (projectId === null || dbSlug === '' || wstatus !== 'done' || plan !== null || isRevise)
      return undefined;
    const runStartedMs = status?.startedAt != null ? new Date(status.startedAt).getTime() : 0;
    let disposed = false;
    const poll = (): void => {
      fetchTasks()
        .then((tasks) => {
          if (disposed) return;
          const mine = tasks
            .filter((t) => t.projectSlug === dbSlug)
            .filter((t) => {
              const started = t.startedAt != null ? new Date(t.startedAt).getTime() : 0;
              return runStartedMs === 0 || started >= runStartedMs - 60_000;
            })
            .sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''));
          const newest = mine[0];
          if (newest !== undefined) setPlan(newest);
        })
        .catch(() => {
          /* tasks unavailable — retry next tick */
        });
    };
    poll();
    const t = window.setInterval(poll, PLAN_POLL_MS);
    return () => {
      disposed = true;
      window.clearInterval(t);
    };
  }, [projectId, dbSlug, wstatus, plan, status?.startedAt, isRevise]);

  // The "start another plan" intake belongs to the done state only — once a new
  // run (or any other status) takes over, drop the flag so the normal branches
  // decide what renders.
  useEffect(() => {
    if (wstatus !== 'done') setNewPlanMode(false);
  }, [wstatus]);

  // The plan a revise wizard is revising, for the banner (title + link).
  const [reviseTask, setReviseTask] = useState<TaskSummary | null>(null);
  useEffect(() => {
    if (!isRevise || reviseTaskId === null) {
      setReviseTask(null);
      return undefined;
    }
    let alive = true;
    fetchTasks()
      .then((tasks) => {
        if (alive) setReviseTask(tasks.find((t) => t.id === reviseTaskId) ?? null);
      })
      .catch(() => {
        /* banner degrades to "the plan" */
      });
    return () => {
      alive = false;
    };
  }, [isRevise, reviseTaskId]);

  // Once a revise session is done, its staged revision appears when the daemon
  // ingests the wizard's staging pass — poll until it (or a staging failure)
  // lands, then hand off to the review.
  const [stagedRevision, setStagedRevision] = useState<PlanRevision | null>(null);
  const [stagingError, setStagingError] = useState<string | null>(null);
  useEffect(() => {
    if (!isRevise || reviseTaskId === null || wstatus !== 'done') {
      setStagedRevision(null);
      setStagingError(null);
      return undefined;
    }
    let disposed = false;
    const poll = (): void => {
      fetchRevisions(reviseTaskId)
        .then((revs) => {
          if (disposed) return;
          const staged = revs.find((r) => r.status === 'staged');
          if (staged !== undefined) {
            setStagedRevision(staged);
            setStagingError(null);
            return;
          }
          // Newest first: a failed newest row means THIS run's staging died.
          const newest = revs[0];
          if (newest !== undefined && newest.status === 'failed')
            setStagingError(newest.error ?? 'staging the revision failed');
        })
        .catch(() => {
          /* revisions unavailable — retry next tick */
        });
    };
    poll();
    const t = window.setInterval(poll, STATUS_POLL_MS);
    return () => {
      disposed = true;
      window.clearInterval(t);
    };
  }, [isRevise, reviseTaskId, wstatus]);

  // ?idea= hand-off (Board's triage "Plan" action): seed the intake with the
  // card's text and put the cursor in it, so a suggestion too big to just Run
  // becomes a plan in one hop.
  //
  // The param is consumed and STRIPPED in the same pass, which makes this
  // effect self-disarming: a reload, a back-navigation, or a re-render after
  // the user edited the textarea can no longer overwrite what they typed. It
  // also refuses to fire while a wizard is open — a live interview must never
  // be clobbered by a stale link — and does not care whether the strip or the
  // read happens first, since the guard is `idea !== ''` on the raw param.
  useEffect(() => {
    const seed = searchParams.get('idea');
    if (seed === null) return;
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete('idea');
        return next;
      },
      { replace: true },
    );
    if (wizardOpen || seed.trim() === '') return;
    setIdea(seed);
    window.setTimeout(() => ideaRef.current?.focus(), 0);
  }, [searchParams, setSearchParams, wizardOpen]);

  // Live nudges: session_updated / task_updated → refresh the wizard DTO.
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'session_updated' || msg.type === 'task_updated') {
        loadStatus();
      }
    },
    [loadStatus],
  );
  useLiveUpdates(onMessage, loadStatus);

  /** Shared error sink: surface the message AND refetch — server state wins
   * (a 409 means the wizard moved under us; the forced load re-syncs). */
  const fail = useCallback(
    (e: unknown): void => {
      if (!aliveRef.current) return;
      setError(e instanceof Error ? e.message : String(e));
      loadStatus();
    },
    [loadStatus],
  );

  /** Optimistic status flip after a wizard action is accepted (202). Increments
   * mutationSeqRef so any in-flight loadStatus() calls initiated before this
   * mutation are discarded (stale-GET guard). */
  const optimistic = useCallback((next: PlanningStatus['status']): void => {
    mutationSeqRef.current += 1;
    setStatus((prev) => (prev === null ? prev : { ...prev, status: next }));
  }, []);

  const start = (): void => {
    if (projectId === null || idea.trim() === '') return;
    setBusy(true);
    setError(null);
    setPlan(null);
    try {
      localStorage.setItem(MODEL_STORAGE_KEY, plannerModel);
    } catch {
      // storage unavailable — the choice still applies to this run
    }
    startPlanning(projectId, idea.trim(), plannerModel)
      .then(() => {
        if (!aliveRef.current) return;
        // Optimistic flip BEFORE loadStatus so the stale-GET guard in
        // loadStatus treats a response that arrives after the flip as stale.
        mutationSeqRef.current += 1;
        setStatus((prev) =>
          prev === null
            ? prev
            : { ...prev, active: true, status: 'generating', startedAt: new Date().toISOString() },
        );
        loadStatus();
      })
      .catch(fail)
      .finally(() => {
        if (aliveRef.current) setBusy(false);
      });
  };

  const cancel = (): void => {
    if (projectId === null) return;
    setBusy(true);
    cancelPlanning(projectId)
      .then(() => aliveRef.current && loadStatus())
      .catch(fail)
      .finally(() => aliveRef.current && setBusy(false));
  };

  const submitAnswer = (selected: string[], otherText?: string): void => {
    if (projectId === null || status?.currentQuestion == null) return;
    setBusy(true);
    setError(null);
    answerPlanning(projectId, {
      questionId: status.currentQuestion.id,
      selectedOptionIds: selected,
      ...(otherText !== undefined ? { otherText } : {}),
    })
      .then(() => aliveRef.current && optimistic('generating'))
      .catch(fail)
      .finally(() => aliveRef.current && setBusy(false));
  };

  // Raw fallback: the whole free-text reply goes through the SAME answer
  // endpoint (questionId "" + otherText) — it owns the status flip + resume.
  const submitRawAnswer = (): void => {
    if (projectId === null || rawAnswer.trim() === '') return;
    setBusy(true);
    setError(null);
    answerPlanning(projectId, { questionId: '', selectedOptionIds: [], otherText: rawAnswer.trim() })
      .then(() => {
        if (!aliveRef.current) return;
        setRawAnswer('');
        optimistic('generating');
      })
      .catch(fail)
      .finally(() => aliveRef.current && setBusy(false));
  };

  const submitRefine = (instructions: string): void => {
    if (projectId === null) return;
    setBusy(true);
    setError(null);
    refinePlanning(projectId, instructions)
      .then(() => {
        if (!aliveRef.current) return;
        setRefineOpen(false);
        optimistic('generating');
      })
      .catch(fail)
      .finally(() => aliveRef.current && setBusy(false));
  };

  const submitProceed = (): void => {
    if (projectId === null) return;
    setBusy(true);
    setError(null);
    proceedPlanning(projectId)
      .then(() => aliveRef.current && optimistic('proceeding'))
      .catch(fail)
      .finally(() => aliveRef.current && setBusy(false));
  };

  const projectLabel = useMemo(() => project?.name ?? project?.slug ?? slug, [project, slug]);
  const lastReasoning = history.length > 0 ? (history[history.length - 1]?.reasoning ?? '') : '';

  if (loading && status === null) {
    return (
      <div className="px-4 pt-6 pb-10 desk:px-8">
        <Loading label="planning…" />
      </div>
    );
  }
  if (projectId === null) {
    return (
      <div className="px-4 pt-6 pb-10 desk:px-8">
        <Empty>unknown project — pick one from the switcher</Empty>
      </div>
    );
  }

  // Idle, cancelled AND failed all land here — failed shows the error card
  // above an intake prefilled with the same idea. A revise wizard never shows
  // the intake: revise sessions start from the Plans page, not from an idea box.
  const showIntake = !isRevise && !wizardOpen && (wstatus !== 'done' || newPlanMode);

  /** The page's own `⛶ expand` trigger — <ExpandButton> in this page's skin.
   *
   * The default affordance is sized to sit alone in an embedded page's toolbar
   * (a filled `bg-field` control); next to this header's `History` / `cancel`
   * buttons it reads as a different, heavier control. Only the class string
   * differs: the semantics — decorative glyph hidden so the accessible name is
   * just the word, aria-expanded on the trigger — stay in components/ui.tsx, so
   * this skin cannot drift away from the affordance everywhere else.
   *
   * Note on lifetime: submitting an answer unmounts this whole block while the
   * planner thinks, and ExpandableSection resets its flag when it unmounts — so
   * the next question opens collapsed and is expanded again on demand, rather
   * than the overlay reappearing over a spinner. */
  const expandTrigger = (onClick: () => void, expanded: boolean): JSX.Element => (
    <ExpandButton
      onClick={onClick}
      expanded={expanded}
      className="rounded-lg border border-line px-3 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-brand"
    />
  );

  /** Shared run header: pulse dot, started-ago, session link, History, cancel.
   *
   * `trailing` opens the right-hand group to a section-specific control (the
   * expand trigger) without giving every caller of this helper one. */
  const runHeader = (label: string, pulse: boolean, trailing?: JSX.Element): JSX.Element => (
    <div className="flex flex-wrap items-center gap-2.5">
      <span
        className={`inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-brand ${pulse ? 'animate-pulse' : ''}`}
        aria-hidden="true"
      />
      <span className="text-[13px] font-semibold text-ink">{label}</span>
      {status != null && status.model !== '' && (
        <span
          className="rounded border border-line px-1.5 py-px font-mono text-[10.5px] text-ink-dim"
          title={status.model}
        >
          {modelShortName(status.model)}
        </span>
      )}
      {status?.startedAt != null && (
        <span className="font-mono text-[10.5px] text-ink-faint">started {fmtAgo(status.startedAt)}</span>
      )}
      {sessionUuid !== '' && (
        <Link
          to={sessionHref(sessionUuid)}
          className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
        >
          open session →
        </Link>
      )}
      <div className="ml-auto flex items-center gap-2">
        {trailing}
        {history.length > 0 && (
          <button
            type="button"
            onClick={() => setHistoryOpen(true)}
            className="rounded-lg border border-line px-3 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
          >
            History
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={cancel}
          className="rounded-lg border border-red/40 px-3 py-1 font-mono text-[11px] text-red transition-colors hover:bg-red/10 disabled:opacity-50"
        >
          cancel
        </button>
      </div>
    </div>
  );

  return (
    <div className="min-w-0 px-4 pt-6 pb-10 desk:px-8 desk:pt-8 desk:pb-[60px]">
      {/* The explainer rides the subtitle, not the h1 — the idiom Playbooks.tsx
          uses for `worktree`. This heading is long enough to wrap at phone
          width, and an inline-flex h1 shrink-wraps: the text would wrap inside
          the anonymous flex item and items-center would park the trigger in the
          vertical middle of the right margin, touching neither line. Flowing in
          the sentence, it wraps with the prose instead. */}
      <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
        Transform your idea into a plan
      </h1>
      <p className="mt-1.5 max-w-[70ch] text-[13px] text-ink-dim">
        Describe what you want to build for{' '}
        <span className="font-mono text-ink">{projectLabel}</span>. A planner session interviews you
        with structured questions, keeps a running plan, and writes the full plan when you tell it to
        proceed. <Explain id="planning-mode" />
      </p>

      {error !== null && (
        <div className="mt-3">
          <ErrorBox message={error} onRetry={() => setError(null)} />
        </div>
      )}

      {/* LAST ERROR — the reply that never reached the planner. The wizard is
          back on the SAME question in that case, which without this banner reads
          as the planner repeating itself, so the operator re-answers in a loop.
          Cleared server-side the moment the next action starts. */}
      {status?.lastError != null && (
        <div
          className="mt-3 flex flex-wrap items-start gap-2 rounded-lg border border-red/30 bg-red/5 px-3 py-2"
          role="alert"
        >
          <span className="rounded border border-red/40 bg-red/10 px-1.5 py-px font-mono text-[9.5px] text-red">
            not delivered
          </span>
          <span className="min-w-0 text-[12.5px] leading-relaxed text-ink-2">
            Your last reply did not reach the planner: {status.lastError}. The question below is the
            same one — answering it again is the retry.
          </span>
        </div>
      )}

      {/* REVISE banner — above whatever card the wizard state renders, so at
          every step the operator knows this interview edits nothing directly. */}
      {isRevise && (
        <div className="mt-3 flex flex-wrap items-center gap-2 rounded-lg border border-amber/40 bg-amber/5 px-3 py-2">
          <span className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber">
            revising
          </span>
          <span className="text-[12.5px] leading-relaxed text-ink-2">
            Revising{' '}
            {reviseTaskId !== null ? (
              <Link
                to={`/p/${slug}/plans?task=${String(reviseTaskId)}`}
                className="text-brand hover:underline"
              >
                {reviseTask?.title ?? 'the plan'}
              </Link>
            ) : (
              'the plan'
            )}{' '}
            — nothing is written until you approve the diff.
          </span>
        </div>
      )}

      {/* FAILED — the run died; the intake below is prefilled to start again */}
      {wstatus === 'failed' && (
        <Card>
          <div className="flex flex-wrap items-center gap-2.5">
            <span className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-red" aria-hidden="true" />
            <span className="text-[13px] font-semibold text-ink">Planning run failed</span>
            {sessionUuid !== '' && (
              <Link
                to={sessionHref(sessionUuid)}
                className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
              >
                open session →
              </Link>
            )}
          </div>
          <div className="mt-2 text-[12.5px] leading-relaxed text-ink-2">
            The planner run ended without a reply. Adjust the idea below (it is prefilled) and start
            again — a new run supersedes this one.
          </div>
        </Card>
      )}

      {/* DONE — plan ready. This card is the completion signal (where the plan
          landed + the link to it), so it stays until the user acts on it: once
          "Start another plan" is pressed the plan is already on the Plans page
          and the card only crowds the next idea box, so it gives way to the
          intake. "keep the plan card" brings it back. */}
      {/* DONE (revise) — no plan was saved: the proposal sits STAGED behind the
          plan's Revisions tab, and the review is the only next step offered. */}
      {wstatus === 'done' && isRevise && (
        <Card>
          <div className="flex flex-wrap items-center gap-2.5">
            <span
              className={`inline-block h-[7px] w-[7px] shrink-0 rounded-full ${
                stagingError !== null ? 'bg-red' : stagedRevision !== null ? 'bg-green' : 'bg-amber animate-pulse'
              }`}
              aria-hidden="true"
            />
            <span className="text-[13px] font-semibold text-ink">
              {stagingError !== null
                ? 'Staging the revision failed'
                : stagedRevision !== null
                  ? 'Revision staged'
                  : 'Interview done — staging the revision'}
            </span>
            {sessionUuid !== '' && (
              <Link
                to={sessionHref(sessionUuid)}
                className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
              >
                open session →
              </Link>
            )}
            {stagedRevision !== null && reviseTaskId !== null && (
              <Link
                to={`/p/${slug}/plans?task=${String(reviseTaskId)}&tab=revisions`}
                className="ml-auto rounded-lg border border-green/45 bg-green/12 px-3 py-1 font-mono text-[11px] font-semibold text-green transition-colors hover:bg-green/20"
              >
                Review changes →
              </Link>
            )}
          </div>
          <div className="mt-2 text-[12.5px] leading-relaxed text-ink-2">
            {stagingError !== null ? (
              <span className="font-mono text-[11px] text-red">{stagingError}</span>
            ) : stagedRevision !== null ? (
              <>
                {stagedRevision.files.length} plan doc
                {stagedRevision.files.length === 1 ? '' : 's'} staged as a diff — review it per
                file, then Apply or Reject. Nothing has been written yet.
              </>
            ) : (
              'the wizard finished; its staged diff is being picked up by the daemon…'
            )}
          </div>
        </Card>
      )}

      {wstatus === 'done' && !isRevise && !newPlanMode && (
        <Card>
          <div className="flex flex-wrap items-center gap-2.5">
            <span className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-green" aria-hidden="true" />
            <span className="text-[13px] font-semibold text-ink">Plan ready</span>
            {plan !== null ? (
              <Link
                to={`/p/${slug}/plans`}
                className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
              >
                {plan.title}
              </Link>
            ) : (
              <Link
                to={`/p/${slug}/plans`}
                className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
              >
                open Plans →
              </Link>
            )}
            <div className="ml-auto flex flex-wrap items-center gap-2">
              {history.length > 0 && (
                <button
                  type="button"
                  onClick={() => setHistoryOpen(true)}
                  className="rounded-lg border border-line px-3 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
                >
                  History
                </button>
              )}
              <button
                type="button"
                onClick={() => {
                  setIdea('');
                  setError(null);
                  setNewPlanMode(true);
                  // Let the intake mount before we reach for it.
                  window.setTimeout(() => ideaRef.current?.focus(), 0);
                }}
                className="rounded-lg border border-brand/45 bg-brand/12 px-3 py-1 font-mono text-[11px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
              >
                Start another plan
              </button>
            </div>
          </div>
          <div className="mt-2 font-mono text-[11px] text-ink-dim">
            {status?.planDir != null && status.planDir !== '' ? (
              <>
                plan saved at <span className="text-ink">{status.planDir}</span>
              </>
            ) : (
              'the plan directory is being picked up by the workspace scan…'
            )}
            {plan !== null && (
              <>
                {' '}
                — tracked as <span className="text-ink">{plan.externalId}</span>
              </>
            )}
          </div>
          <div className="mt-2 text-[12.5px] leading-relaxed text-ink-2">
            Review it on the{' '}
            <Link to={`/p/${slug}/plans`} className="text-brand hover:underline">
              Plans page
            </Link>{' '}
            — phases run from there.
          </div>
        </Card>
      )}

      {/* IDLE / FAILED / "start another plan" — idea intake */}
      {showIntake && (
        <div className="mt-5 max-w-[80ch]">
          {newPlanMode && (
            <div className="mb-1.5 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
              next idea
            </div>
          )}
          <textarea
            ref={ideaRef}
            value={idea}
            onChange={(e) => setIdea(e.target.value)}
            rows={5}
            placeholder="e.g. Add a bulk-export button to the reports page that streams a CSV…"
            aria-label="describe what you want to build"
            className="w-full resize-y rounded-xl border border-line bg-field px-3.5 py-3 text-[13.5px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50"
          />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={busy || idea.trim() === ''}
              onClick={start}
              className="rounded-lg border border-brand/50 bg-brand/12 px-4 py-2 text-[13px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
            >
              {busy ? 'starting…' : wstatus === 'failed' ? 'Start again' : 'Start planning'}
            </button>
            <select
              value={plannerModel}
              disabled={busy}
              onChange={(e) => {
                if (isPlanningModel(e.target.value)) setPlannerModel(e.target.value);
              }}
              aria-label="planner model"
              title="the model that runs the planning interview and writes the plan"
              className="rounded-lg border border-line bg-field px-2 py-2 font-mono text-[11px] text-ink-dim outline-none transition-colors hover:text-ink focus:border-brand/50 disabled:opacity-50"
            >
              {PLANNING_MODELS.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
            {newPlanMode && (
              <button
                type="button"
                disabled={busy}
                onClick={() => setNewPlanMode(false)}
                className="rounded-lg border border-line px-3 py-2 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink disabled:opacity-50"
              >
                keep the plan card
              </button>
            )}
          </div>
        </div>
      )}

      {/* GENERATING / PROCEEDING — the planner is thinking: spinner in the left
          column, RunningPlanPanel in the right column (buttons disabled because
          status !== 'awaiting_answer'). This keeps the running plan visible
          between questions — matching the Fusion reference UX. */}
      {thinking && (
        <div className="mt-3 grid items-start gap-3 desk:grid-cols-3">
          <div className="min-w-0 desk:col-span-2">
            <Card>
              {runHeader(wstatus === 'proceeding' ? 'Writing the plan' : 'Planner thinking', true)}
              <div className="mt-3 flex items-center gap-2 font-mono text-[11.5px] text-ink-dim">
                <span
                  className="inline-block h-3 w-3 animate-spin rounded-full border border-line border-t-brand"
                  aria-hidden="true"
                />
                {wstatus === 'proceeding'
                  ? 'interview closed — writing the full plan into the workspace…'
                  : 'reading the repo and preparing the next question…'}
                {status?.startedAt != null && <span>· {fmtElapsed(status.startedAt, nowMs)}</span>}
              </div>
              {lastReasoning !== '' && (
                <div className="mt-3">
                  <div className="mb-1 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
                    latest reasoning
                  </div>
                  <pre className="max-h-40 overflow-y-auto rounded-lg border border-line bg-bg px-3 py-2.5 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
                    {lastReasoning}
                  </pre>
                </div>
              )}
            </Card>
          </div>
          {status?.runningPlan != null && (
            <div className="min-w-0 desk:sticky desk:top-4">
              <RunningPlanPanel
                plan={status.runningPlan}
                status={wstatus}
                busy={busy}
                onRefine={() => setRefineOpen(true)}
                onProceed={submitProceed}
              />
            </div>
          )}
        </div>
      )}

      {/* AWAITING — the two-pane wizard (structured question) */}
      {wstatus === 'awaiting_answer' && status?.currentQuestion != null && (
        <>
          <Card>
            {runHeader(
              'Planner is asking',
              false,
              expandTrigger(() => setInterviewExpanded(true), interviewExpanded),
            )}
          </Card>
          {/* Expanding swaps CLASSES ONLY — same elements, same positions, same
              `key`. QuestionCard owns the answer being typed (its selection and
              the "Other" textarea are local state), so a wrapper that remounted
              it would silently throw away the answer the moment the user asked
              for more room to read the options. */}
          <ExpandableSection
            expanded={interviewExpanded}
            onToggle={setInterviewExpanded}
            label="planner interview"
            className="mt-3"
          >
            <div
              className={`grid gap-3 desk:grid-cols-3 ${
                interviewExpanded
                  ? // desk:grid-rows-1 is what makes the columns scroll: it pins
                    // the single row to the container height (minmax(0,1fr)), so
                    // each column is exactly as tall as the overlay and its own
                    // overflow takes over. Without it the row is content-sized
                    // and simply spills out of the fixed overlay. Below `desk`
                    // the grid is one column of two stacked rows, where side-by-
                    // side scrollers are meaningless — the grid scrolls instead.
                    'min-h-0 flex-1 items-stretch overflow-y-auto desk:grid-rows-1 desk:overflow-hidden'
                  : 'items-start'
              }`}
            >
              <div
                className={`min-w-0 desk:col-span-2 ${
                  interviewExpanded ? 'desk:min-h-0 desk:overflow-y-auto' : ''
                }`}
              >
                <QuestionCard
                  key={status.currentQuestion.id}
                  question={status.currentQuestion}
                  busy={busy}
                  onSubmit={submitAnswer}
                />
              </div>
              {/* Sticky is a SCROLLING-PAGE affordance — it keeps the plan in
                  view as the document scrolls past. Expanded there is no page
                  scroll to stick against (body is locked); the column becomes
                  its own scroller instead. */}
              <div
                className={`min-w-0 ${
                  interviewExpanded
                    ? 'desk:min-h-0 desk:overflow-y-auto'
                    : 'desk:sticky desk:top-4'
                }`}
              >
                <RunningPlanPanel
                  plan={status.runningPlan}
                  status={wstatus}
                  busy={busy}
                  onRefine={() => setRefineOpen(true)}
                  onProceed={submitProceed}
                />
              </div>
            </div>
          </ExpandableSection>
        </>
      )}

      {/* AWAITING — raw-text fallback (reply failed the protocol parse) */}
      {wstatus === 'awaiting_answer' && status?.currentQuestion == null && (
        <Card>
          {runHeader(
            'Planner replied',
            false,
            expandTrigger(() => setReplyExpanded(true), replyExpanded),
          )}
          {/* Same wrapper-only rule as the interview above: the reply body and
              the answer field keep their identity across the toggle, so a
              half-typed reply survives a trip to fullscreen. */}
          <ExpandableSection
            expanded={replyExpanded}
            onToggle={setReplyExpanded}
            label="planner reply"
            className="mt-3"
          >
            <div className="mb-1.5 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
              planner
            </div>
            {/* Collapsed the reply is one card in a scrolling page, so it is
                capped and scrolls inside that cap. Expanded the cap is the
                whole point of expanding — the body takes the leftover height
                above the reply form instead. */}
            <div
              className={`overflow-y-auto rounded-lg border border-line bg-bg px-3 py-2.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2 ${
                replyExpanded ? 'min-h-0 flex-1' : 'max-h-72'
              }`}
            >
              {status?.rawReply ?? '(no reply text)'}
            </div>
            <form
              className="mt-2 flex shrink-0 flex-wrap gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                submitRawAnswer();
              }}
            >
              <input
                type="text"
                value={rawAnswer}
                onChange={(e) => setRawAnswer(e.target.value)}
                placeholder="answer the planner…"
                aria-label="answer the planner"
                className="min-w-0 flex-1 basis-[240px] rounded-lg border border-line bg-field px-2.5 py-[7px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50"
              />
              <button
                type="submit"
                disabled={busy || rawAnswer.trim() === ''}
                className="rounded-lg border border-brand/45 bg-brand/12 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
              >
                reply
              </button>
            </form>
          </ExpandableSection>
        </Card>
      )}

      {/* How it works — outside the state branches so the explainer stays visible
          during active runs and after the plan lands, not just on the intake screen. */}
      <HowItWorks id="planning-mode" className="mt-6 max-w-[80ch]" />

      <HistoryDrawer turns={history} open={historyOpen} onClose={() => setHistoryOpen(false)} />
      <RefineModal
        open={refineOpen}
        busy={busy}
        onClose={() => setRefineOpen(false)}
        onApply={submitRefine}
      />
    </div>
  );
}
