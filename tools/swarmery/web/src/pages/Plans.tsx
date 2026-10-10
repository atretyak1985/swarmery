// Plans / Epics (fusion phase 10 + plans-page-lifecycle phase 2): a workspace
// plan IS an epic. This tab lists the project's epics (plan dirs the ingester
// parsed) behind Active/Done/Archived filter tabs, drills into a phase
// timeline (seq order, depends-on badges, per-phase progress, derived status
// chips), offers plan lifecycle controls (Pause / Resume / Archive / Restore —
// file operations on the daemon side), and opens any plan doc for EDITING in
// the existing drawer — the workspace folder becomes invisible infrastructure
// (read, edit, track from the platform; files stay the storage).
//
// Details are INLINE and full-width: the plan list column stays visible on the
// left, and the detail panel REPLACES the phase list in the main column, under
// the plan header (title, status, progress, lifecycle buttons) — plan docs and
// completion reports get the whole column instead of a 360px rail. The "← all
// phases" control (or Escape) sits ABOVE the panel, not inside its header,
// where it used to get lost against the panel's own identity block.
//
// BOTH detail panels are tabbed, with the same tab-bar idiom:
//   phase → Phase (run state, interactive acceptance criteria, full doc)
//         | Summary (what was done: Completion Report + `## Execution record`)
//         | Edit (raw markdown + Save) — retired once the phase is done
//   plan  → Plan (README markdown)
//         | Spec (rendered spec.md + per-criterion coverage; only when the
//           plan has a spec.md)
//         | Summary (per-phase executed work; only when every phase is done)
//         | Edit (raw README + Save)
// Editing is inline on the Edit tab — there is no doc modal anymore; the
// checkbox toggling the old modal offered lives on the phase's Phase tab
// (clicking a criterion PATCHes that exact `- [ ]`↔`- [x]` line).
//
// Running work: a phase runs headlessly from its own doc (the phase-run
// mechanism), and the WHOLE plan can be handed to one agent instead — "Run
// plan" in the action row opens an inline agent + mode picker and POSTs
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

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  type BlockerFunction,
  Link,
  useBlocker,
  useLocation,
  useNavigate,
  useNavigationType,
  useParams,
} from 'react-router-dom';
import type {
  BoardColumn,
  Epic,
  EpicPhase,
  PhaseRunOutcome,
  PlanRevision,
  ProviderTerms,
  Session,
  SessionStatus,
  WSMessage,
} from '../api/types';
import {
  cancelEpicPhaseRun,
  cancelEpicPlanRun,
  epicLifecycle,
  fetchEpics,
  fetchPlanDoc,
  fetchRevisions,
  fetchSessions,
  runEpicPhase,
  runEpicPlan,
  savePlanDoc,
  startRevision,
  togglePlanCheckbox,
  type EpicLifecycleAction,
  type PhaseRunBranchError,
  type RevisionStartError,
} from '../api';
import { fetchSystemItems } from '../api/system';
import type { PlanRunMode, RunEvent } from '../api/types';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { useLiveUpdates } from '../lib/ws';
import { Markdown } from '../lib/markdown';
import { fmtAgo, fmtCost, fmtDateTime, fmtElapsed } from '../lib/format';
import { useSessionHref } from '../lib/sessionHref';
import { useDocumentTitle } from '../lib/useDocumentTitle';
import { scopePlanSessions, type PlanSessionScope } from '../lib/planSessionScope';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { CopyIdBadge } from '../components/CopyIdBadge';
import { RunOutcomeModal } from '../components/RunOutcomeModal';
import { PlanBranchDirtyModal, type PlanBranchDirty } from '../components/PlanBranchDirtyModal';
import { RevisionReview, ORIGIN_LABEL } from './planning/RevisionReview';
import { ReviseModal } from './planning/ReviseModal';
import { ForecastSection, RailSection, SurpriseChip } from './plans/ForecastVsActual';
import { ForecastStory } from './plans/ForecastStory';
import { DepsUnmergedActions } from './plans/DepsUnmergedActions';
import { CriterionLint } from './plans/CriterionLint';
import { PhaseCard } from './plans/PhaseCard';
import { PHASE_TABS, PhasePanel, type PhaseTab } from './plans/PhasePanel';
import { PhaseReopens } from './plans/PhaseReopens';
import { PhaseReview } from './plans/PhaseReview';
import { VerifyVerdictChip } from './plans/VerifyVerdictChip';
import { hasReviewTab, NEUTRAL_TERMS } from './plans/landingModel';
import { useProjectVcs } from '../lib/useProjectVcs';
import {
  parsePlansRoute,
  plansHref,
  sameHref,
  type ParsedPlansRoute,
  type PlanDetailTab,
  type PlansDetail,
  type PlansRouteParams,
  type PlansStatus,
  type PlansTarget,
} from './plans/plansUrl';

/** A board column that counts as "resolved" for the dependency gate. */
function isResolvedColumn(col: BoardColumn | null): boolean {
  return col === 'done' || col === 'archived';
}

type PhaseStatus = 'pending' | 'in_progress' | 'done' | 'unverified' | 'unreported' | 'blocked';

/** Derives a phase's display status from checkbox progress, a live run, and the
 * dependency gate. The result stays a coarse LIFECYCLE state — whether a process
 * is attached to it is resolved by `phaseChip` at the call site. */
function phaseStatus(p: EpicPhase, resolvedSeqs: Set<number>): PhaseStatus {
  // An activated phase whose board task is resolved is done regardless of
  // checkbox progress — the board is the source of truth once dispatched.
  if (isResolvedColumn(p.boardColumn)) return 'done';
  // THE completion gate, computed by the daemon (internal/phasegate) — not a
  // client-side `done === total`, which is how this file used to answer and how it
  // came to disagree with the daemon about the same row. `unverified` means the
  // criteria are all ticked and the verification the doc asked for never landed.
  if (p.completionState === 'complete') return 'done';
  if (p.completionState === 'unverified') return 'unverified';
  if (p.completionState === 'unreported') return 'unreported';
  // The doc's own `Status: In progress` marker wins over the dependency gate —
  // an executor writing it is literally working on the phase right now.
  // (`done` must be earned by ticking every checkbox; a `done` marker alone is ignored.)
  if (p.docStatus === 'in_progress') return 'in_progress';
  // A run in flight is the strongest statement that can be made about a phase —
  // stronger than a dependency gate computed from checkboxes the run is in the
  // middle of ticking. Without this a phase whose dependency sits at 11/12
  // renders `blocked` while its executor is demonstrably working.
  if (p.runState === 'running') return 'in_progress';
  if (p.dependsOn.some((seq) => !resolvedSeqs.has(seq))) return 'blocked';
  if (p.checkboxesDone > 0 || p.boardColumn === 'in_progress' || p.boardColumn === 'in_review')
    return 'in_progress';
  return 'pending';
}

/** Which seq numbers are "resolved" — their board task is done/archived OR the
 * daemon's completion gate calls them complete. Renders depends-on badges and
 * feeds the phase-status derivation.
 *
 * The gate, never a local derivation: a process that exited 0 having ticked
 * nothing is not a completed phase, and neither is one whose criteria are all
 * ticked while the verification it asked for never landed. Re-deriving either
 * rule here is what let the client offer a Run button the daemon then refuses. */
function computeResolvedSeqs(phases: EpicPhase[]): Set<number> {
  const s = new Set<number>();
  for (const p of phases) {
    // `completionState`, never a local checkbox comparison: the daemon's dependency
    // gate refuses a fully-ticked phase whose verification never landed, and a
    // client that marked it resolved would offer a Run button the daemon then
    // rejects. One gate, one answer.
    if (isResolvedColumn(p.boardColumn) || p.completionState === 'complete') s.add(p.seq);
  }
  return s;
}

/** A plan is complete when every phase reads done — the gate for the plan
 * rail's Summary tab. */
function planComplete(epic: Epic, resolvedSeqs: Set<number>): boolean {
  return (
    epic.phases.length > 0 &&
    epic.phases.every((p) => phaseStatus(p, resolvedSeqs) === 'done')
  );
}

/** Re-renders on an interval so elapsed labels (doc activity, running phases)
 * stay fresh. */
function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => {
      setNow(Date.now());
    }, intervalMs);
    return () => {
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}

function formatAgo(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${String(m)}m`;
  const h = Math.floor(m / 60);
  return `${String(h)}h ${String(m % 60)}m`;
}

/** Past this silence window an in-progress phase is flagged as possibly stuck. */
const STALL_AFTER_MS = 15 * 60_000;

/** Liveness pulse for a phase with a LIVE run. Every executor edit (checkbox
 * tick, Status flip) touches the phase doc, so "time since last doc edit"
 * answers the question the chip alone can't: is the session actually working
 * right now, or has it silently died?
 *
 * That question only exists while a process is attached. With `running={false}`
 * there is no executor to be stuck, so the pulse and the stuck-executor tooltip
 * are dropped for a neutral `edited <ago>` — the doc's mtime, stated as such.
 * The old markup showed `no activity · 1h 7m` in amber on phases whose run had
 * exited cleanly an hour earlier, which read as an alarm about nothing. */
function PhaseActivity({
  docUpdatedAt,
  running,
}: {
  docUpdatedAt: string | null;
  running: boolean;
}): JSX.Element | null {
  const now = useNow(30_000);
  if (docUpdatedAt === null) return null;
  const elapsed = now - Date.parse(docUpdatedAt);
  if (!running)
    return (
      <span
        className="inline-flex shrink-0 items-center gap-1 font-mono text-[9.5px] text-ink-faint"
        data-tip="when this phase doc was last edited — no run is attached to it"
      >
        edited {formatAgo(elapsed)} ago
      </span>
    );
  const stalled = elapsed > STALL_AFTER_MS;
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1 font-mono text-[9.5px] ${
        stalled ? 'text-amber' : 'text-brand'
      }`}
      data-tip={
        stalled
          ? 'no phase-doc edits for a while — the executor may be stuck'
          : 'the executor is actively editing this phase doc'
      }
    >
      <span className={`h-1.5 w-1.5 rounded-full ${stalled ? 'bg-amber' : 'animate-pulse bg-brand'}`} />
      {stalled ? `no activity · ${formatAgo(elapsed)}` : `active · ${formatAgo(elapsed)} ago`}
    </span>
  );
}

/** Status chips. `in_progress` says `started` because that is all the DOCUMENT
 * can support — checkboxes ticked, or a `Status: In progress` marker. `running`
 * is reserved for a live process and is resolved by `phaseChip` below. */
const PHASE_CHIP: Record<PhaseStatus, { label: string; cls: string }> = {
  done: { label: 'done', cls: 'border-green/40 text-green' },
  // Every criterion ticked and the grade the doc asked for never arrived. Amber,
  // the app's needs-a-human colour — NOT green, because nobody confirmed this, and
  // NOT red, because nothing says the work is wrong.
  unverified: { label: 'unverified', cls: 'border-amber/40 bg-amber/10 text-amber' },
  // Work that landed and was never written down. Amber like `unverified` — both
  // need a human — but its own label, because the fix is different: write the
  // Completion Report and record the lesson, not re-run a grader.
  unreported: { label: 'unreported', cls: 'border-amber/40 bg-amber/10 text-amber' },
  in_progress: { label: 'started', cls: 'border-brand/40 text-brand' },
  blocked: { label: 'blocked', cls: 'border-red/40 text-red' },
  pending: { label: 'pending', cls: 'border-line text-ink-faint' },
};

/** The chip a phase row actually renders. The `running` variant lives HERE and
 * not in `PhaseStatus`, so the union — and every dependency/completion decision
 * derived from it — stays a pure function of the plan documents. */
function phaseChip(status: PhaseStatus, running: boolean): { label: string; cls: string } {
  if (running && status === 'in_progress')
    return { label: 'running', cls: PHASE_CHIP.in_progress.cls };
  return PHASE_CHIP[status];
}

/** Terminal run outcomes that did NOT complete the phase. Each renders as a
 * chip BUTTON opening the diagnosis modal, because "why?" is the only useful
 * next question — the old markup showed a green "Run done" here whenever the
 * process exited 0, which is precisely the claim these outcomes refute. */
type UnresolvedOutcome = 'noop' | 'partial' | 'failed';

function isUnresolvedOutcome(o: PhaseRunOutcome): o is UnresolvedOutcome {
  return o === 'noop' || o === 'partial' || o === 'failed';
}

/** Chip copy + styling per unresolved outcome. `amber` is the app's semantic
 * needs-a-human color (same token as workspace/TaskCard.tsx's inconclusive). */
const OUTCOME_CHIP: Record<UnresolvedOutcome, { cls: string; title: string }> = {
  noop: {
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: 'the run finished but ticked no acceptance criteria — click for why',
  },
  partial: {
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: 'the run ticked some criteria but did not finish the phase — click for why',
  },
  failed: {
    cls: 'border-red/40 bg-red/10 text-red',
    title: 'the run failed — click for why',
  },
};

/** Run/Retry button styling — keyed on the OUTCOME, so a retry after a
 * ticked-nothing run reads amber like its chip instead of neutral brand. */
function runButtonCls(outcome: PhaseRunOutcome): string {
  if (outcome === 'failed') return 'border-red/40 text-red hover:bg-red/10';
  if (outcome === 'noop' || outcome === 'partial')
    return 'border-amber/40 text-amber hover:bg-amber/10';
  return 'border-brand/40 text-brand hover:bg-brand/10';
}

function outcomeChipLabel(p: EpicPhase, outcome: UnresolvedOutcome): string {
  if (outcome === 'failed') return 'failed';
  if (outcome === 'partial') return `ran · ${String(p.checkboxesDone)}/${String(p.checkboxesTotal)}`;
  return 'ran · no progress';
}

/** The clickable outcome chip. READ-ONLY — it stays enabled while a plan run
 * owns the docs, because a diagnosis is exactly what a user wants then. */
function RunOutcomeChip({
  phase,
  outcome,
  onOpen,
}: {
  phase: EpicPhase;
  outcome: UnresolvedOutcome;
  onOpen: () => void;
}): JSX.Element {
  const { cls, title } = OUTCOME_CHIP[outcome];
  return (
    <button
      type="button"
      data-tip={title}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
      className={`rounded border px-1.5 py-px font-mono text-[9.5px] transition-colors hover:brightness-125 ${cls}`}
    >
      {outcomeChipLabel(phase, outcome)}
    </button>
  );
}

/** The completed-outcome chip, shared by every render site. It describes the
 * LAST RUN, not the present — hence `last run: done` rather than `Run done`,
 * which read as a claim about the phase and collided with a live `11/12`
 * beside it.
 *
 * Detecting that collision without a `runCheckboxesAfter` field (the DTO exposes
 * only `runCheckboxesBefore`): `completed` is derivable ONLY when the run's
 * stamped end count reached the total (internal/phasediag/outcome.go:35), and
 * that stamped count wins over the live one (`OutcomeFromRow`). So a `completed`
 * outcome sitting next to `checkboxesDone < checkboxesTotal` is precisely — and
 * only — the case where the two counts disagree, i.e. the doc changed after the
 * run ended. The measured count is rendered as `total/total`: `after >= total`
 * is exactly what the daemon asserted. */
function RunCompletedChip({ phase }: { phase: EpicPhase }): JSX.Element {
  const total = phase.checkboxesTotal;
  const drifted = total > 0 && phase.checkboxesDone < total;
  return (
    <span
      className="rounded border border-green/40 bg-green/10 px-1.5 py-px font-mono text-[9.5px] text-green"
      data-tip={
        drifted
          ? `the run ticked every criterion (${String(total)}/${String(total)}); the doc has since changed to ${String(phase.checkboxesDone)}/${String(total)}`
          : 'the run ticked every acceptance criterion'
      }
    >
      last run: done
    </span>
  );
}

// ── Per-phase model selection ───────────────────────────────────────────────
// The picker's options ARE the closed set the daemon accepts (planning.Models,
// internal/planning/runner.go), so SC-5 — "the UI cannot send a value the API
// would reject" — is structural here rather than a second validation.
//
// `default` is NOT a model. It means SEND NO `model` KEY AT ALL, leaving the
// daemon's own SWARMERY_PHASERUN_MODEL (a hand-pinned plist value that may carry
// a `[1m]` context-window suffix the closed set does not contain) in charge. It
// is first, and it is the fallback, so a phase run started by someone who never
// touched this control behaves exactly as it did before the control existed.
//
// Scope: this is the PER-PHASE control only. The whole-plan run reads
// SWARMERY_PLANRUN_MODEL and is deliberately untouched, so it gets no picker —
// one there would silently do nothing.
//
// Since phase 3, `default` is also what lets a phase DOC's own `**Model:**`
// declaration apply: sending no key leaves the daemon's four-rung ladder in
// charge, and rung 2 of that ladder is the doc. Choosing a real model here is an
// OVERRIDE of every doc on the plan — which is why the label says so, and why
// each phase that declares one shows a `doc:` chip beside its Run button.
/** The concrete model a `default` phase run ends up on when no doc and no env
 * knob says otherwise — `planning.DefaultModel`, the last rung of
 * `phaserun.resolveModel`.
 *
 * Named here rather than left implicit because "default" used to be a lie of
 * omission: it meant NO --model flag, which is not a house default but the
 * ACCOUNT default (Fable, ~2× the Opus price). The rung nobody picked was the
 * most expensive one, and the picker said nothing about it. The daemon now pins
 * this rung, and the label says which model that is. */
const PHASE_RUN_DEFAULT_MODEL_LABEL = 'opus 5.5';

const PHASE_RUN_MODELS = [
  { value: 'default', label: `per-doc, else ${PHASE_RUN_DEFAULT_MODEL_LABEL} (no model sent)` },
  // Labels name the GENERATION each alias resolves to today and what choosing
  // it costs relative to opus 5.5, which is the rung `default` lands on. No
  // entry says "default" any more: two of them did, the picker's own default is
  // the first row, and a second thing calling itself the default is how an
  // operator ends up overriding every doc on the plan by picking what they read
  // as "leave it alone".
  { value: 'opus', label: 'opus 5.5' },
  { value: 'sonnet', label: 'sonnet 5 — faster, cheaper' },
  // ~2.5x, not ~2x: fable 5 is $10/$50 per MTok against opus 5.5's $4/$20
  // (tools/swarmery/config/pricing.json). The old figure was measured against
  // opus 5, which cost $5/$25 — opus 5.5 got cheaper and the comparison moved.
  { value: 'fable', label: 'fable 5.1 — most capable, ~2.5× cost' },
] as const;
type PhaseRunModel = (typeof PHASE_RUN_MODELS)[number]['value'];
const DEFAULT_PHASE_RUN_MODEL: PhaseRunModel = 'default';
/** Its OWN key — beside the planner's `swarmery.planning.model`, never shared:
 * the two choices are about different runs and different money. */
const PHASE_RUN_MODEL_KEY = 'swarmery.phaserun.model';

// ── Per-phase effort selection ──────────────────────────────────────────────
// The second half of "what does this run cost": which brain, and how hard it
// thinks. Its options ARE the CLI's closed set (claudeflags.ValidEfforts), so —
// like the model picker — the UI cannot send a value the API would reject.
//
// `default` means SEND NO `effort` KEY, which hands the decision to the same
// ladder the model takes: the phase DOC's own `**Effort:**` header first, then
// SWARMERY_PHASERUN_EFFORT, then the engine's pinned default.
//
// What it does NOT mean is "cheap". A `claude -p` with no --effort runs at the
// CLI's own default, xhigh — the DEEPEST setting — so before the daemon pinned
// this rung, every un-picked phase run paid maximum reasoning tokens for up to
// four hours. The labels below say which end is which for that reason.
const PHASE_RUN_EFFORTS = [
  { value: 'default', label: 'per-doc, else high (no effort sent)' },
  { value: 'low', label: 'low — mechanical work' },
  { value: 'medium', label: 'medium — scoped work' },
  { value: 'high', label: 'high — implementation' },
  { value: 'xhigh', label: 'xhigh — deepest, slowest' },
  { value: 'max', label: 'max — no ceiling' },
] as const;
type PhaseRunEffort = (typeof PHASE_RUN_EFFORTS)[number]['value'];
const DEFAULT_PHASE_RUN_EFFORT: PhaseRunEffort = 'default';
/** Its own key, beside the model's — the two are different decisions about the
 * same money and must not share storage. */
const PHASE_RUN_EFFORT_KEY = 'swarmery.phaserun.effort';

function isPhaseRunEffort(v: string | null): v is PhaseRunEffort {
  return PHASE_RUN_EFFORTS.some((e) => e.value === v);
}

/** Last-used choice; falls back to the default when storage is unavailable
 * (Safari private mode throws on access, not just on write). */
function readStoredPhaseRunEffort(): PhaseRunEffort {
  try {
    const v = localStorage.getItem(PHASE_RUN_EFFORT_KEY);
    return isPhaseRunEffort(v) ? v : DEFAULT_PHASE_RUN_EFFORT;
  } catch {
    return DEFAULT_PHASE_RUN_EFFORT;
  }
}

function isPhaseRunModel(v: string | null): v is PhaseRunModel {
  return PHASE_RUN_MODELS.some((m) => m.value === v);
}

/** Last-used choice; falls back to the default when storage is unavailable
 * (Safari private mode throws on access, not just on write). */
function readStoredPhaseRunModel(): PhaseRunModel {
  try {
    const v = localStorage.getItem(PHASE_RUN_MODEL_KEY);
    return isPhaseRunModel(v) ? v : DEFAULT_PHASE_RUN_MODEL;
  } catch {
    return DEFAULT_PHASE_RUN_MODEL;
  }
}

/** The short name behind a full model ID (`claude-opus-5-5` → `opus`). The `[1m]`
 * suffix on the operator's pinned env value is stripped for the LABEL only — the
 * full string stays in the tooltip, because that suffix is the whole difference
 * between a 200k run and a 1M one. An ID we do not know is shown verbatim. */
function phaseModelShortName(id: string): string {
  const base = id.replace(/\[[^\]]*\]$/, '');
  return MODEL_SHORT_NAMES[base] ?? id;
}
const MODEL_SHORT_NAMES: Record<string, string> = {
  'claude-opus-5-5': 'opus',
  // Kept past the 5.5 cutover: runs recorded before it still carry this id in
  // sessions.model, and a historical row must keep rendering as "opus".
  'claude-opus-5': 'opus',
  'claude-sonnet-5': 'sonnet',
  'claude-fable-5-1': 'fable',
};

/** Which model a finished run ACTUALLY used — read from the session the run
 * produced (`sessions.model`), never from what the UI asked for, so a run that
 * fell back to the daemon's default cannot be mislabelled as the operator's
 * choice. Renders nothing while the run is live: a label about a process still
 * in flight would be a claim, not a record. */
function RunModelChip({ phase }: { phase: EpicPhase }): JSX.Element | null {
  if (phase.runModel === null || phase.runState === 'running') return null;
  // A run that FELL BACK gets its own chip instead of the plain one. `runModel`
  // is the session's first model, which for these runs is a true statement about
  // the first turn and a false one about the output: an Opus 5.5 safeguard
  // refusal moves the session onto an older model and the work continues there.
  // The tooltip lists every model with its turn count, because "42 turns on opus
  // 5.5, 3 on opus 4.1" says how much of the run the fallback actually touched.
  const lastUse = phase.runModels[phase.runModels.length - 1];
  if (phase.runModelFellBack && phase.runModels.length > 1 && lastUse !== undefined) {
    const last = lastUse.model;
    return (
      <span
        className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
        data-tip={`this run changed model mid-flight: ${phase.runModels
          .map((m) => `${m.model} (${m.turns} turn${m.turns === 1 ? '' : 's'})`)
          .join(' → ')}`}
      >
        {phaseModelShortName(phase.runModel)} → fell back to {phaseModelShortName(last)}
      </span>
    );
  }
  return (
    <span
      className="rounded border border-line px-1.5 py-px font-mono text-[9.5px] text-ink-dim"
      data-tip={`the last run used ${phase.runModel} (from its session)`}
    >
      {phaseModelShortName(phase.runModel)}
    </span>
  );
}

/** The models the daemon actually knows (`planning.Models`), used to tell a
 * declaration it will accept from one it will refuse. Short names and full IDs
 * both resolve server-side, so both count as known here. */
const KNOWN_PHASE_MODELS = new Set<string>([
  ...PHASE_RUN_MODELS.filter((m) => m.value !== 'default').map((m) => m.value),
  ...Object.keys(MODEL_SHORT_NAMES),
]);

/** What the phase DOC asks for (`**Model:** opus` in its header) — rung 2 of the
 * daemon's ladder, and a different fact from `RunModelChip`'s: that one reports
 * what a past run USED, this one what the next run WILL use.
 *
 * Rendered per phase because the declaration is per phase, while the picker above
 * the column is one control for the whole plan (phase 2 made it plan-wide so the
 * row's Run, the detail panel's Retry and the diagnosis modal's Retry could not
 * disagree). Splitting it into N selects would reintroduce exactly that.
 *
 * Three states, and the distinction is the point — silently showing a declaration
 * that is not in effect is the bug this whole rung exists to avoid:
 *   - picker on `default`, value known  → IN EFFECT, normal weight;
 *   - picker overridden                 → struck through, the override named;
 *   - value the daemon does not know    → red, because Run will 409 on it. */
function DocModelChip({
  phase,
  picked,
}: {
  phase: EpicPhase;
  picked: PhaseRunModel;
}): JSX.Element | null {
  if (phase.docModel === null) return null;
  const known = KNOWN_PHASE_MODELS.has(phase.docModel);
  const overridden = picked !== 'default';
  const tip = !known
    ? `the phase doc declares **Model:** ${phase.docModel}, which is not a model this daemon knows (opus, sonnet, fable) — a run will be refused until the line is fixed`
    : overridden
      ? `the phase doc asks for ${phase.docModel}, but the picker above is set to ${picked} and overrides it for this run`
      : `the phase doc asks for ${phase.docModel} — this run will use it`;
  const cls = !known
    ? 'border-red/40 text-red'
    : overridden
      ? 'border-line text-ink-faint line-through'
      : 'border-line text-ink-dim';
  return (
    <span
      className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
      data-tip={tip}
    >
      doc: {phaseModelShortName(phase.docModel)}
    </span>
  );
}

/** The per-phase run model picker. Sits with the Run/Retry controls it governs
 * — the choice is made where the money is spent, not in a settings page. */
function PhaseRunModelPicker({
  value,
  onChange,
  disabled,
}: {
  value: PhaseRunModel;
  onChange: (m: PhaseRunModel) => void;
  disabled: boolean;
}): JSX.Element {
  return (
    <label className="flex items-center gap-1.5 font-mono text-[10px] text-ink-faint">
      phase run model
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => {
          if (isPhaseRunModel(e.target.value)) onChange(e.target.value);
        }}
        aria-label="phase run model"
        title="the model every Run phase / Retry run on this plan starts with. Leave it on the default to let each phase doc's own **Model:** line decide (and the daemon's knob where a doc declares none); choosing one here overrides every doc on the plan. The whole-plan run is not affected."
        className="rounded-lg border border-line bg-field px-2 py-1 font-mono text-[10px] text-ink-dim outline-none transition-colors hover:text-ink focus:border-brand/50 disabled:opacity-50"
      >
        {PHASE_RUN_MODELS.map((m) => (
          <option key={m.value} value={m.value}>
            {m.label}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The per-phase effort picker. Sits beside the model picker because the two
 * answer one question together — which brain, and how hard it thinks — and
 * neither is legible about cost without the other. */
function PhaseRunEffortPicker({
  value,
  onChange,
  disabled,
}: {
  value: PhaseRunEffort;
  onChange: (e: PhaseRunEffort) => void;
  disabled: boolean;
}): JSX.Element {
  return (
    <label className="flex items-center gap-1.5 font-mono text-[10px] text-ink-faint">
      effort
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => {
          if (isPhaseRunEffort(e.target.value)) onChange(e.target.value);
        }}
        aria-label="phase run effort"
        title="how hard every Run phase / Retry run on this plan thinks. Leave it on the default to let each phase doc's own **Effort:** line decide (and the daemon's knob where a doc declares none); choosing one here overrides every doc on the plan. Note that NO effort is not the cheap end — an unpinned claude run thinks at xhigh, the deepest setting. The whole-plan run is not affected."
        className="rounded-lg border border-line bg-field px-2 py-1 font-mono text-[10px] text-ink-dim outline-none transition-colors hover:text-ink focus:border-brand/50 disabled:opacity-50"
      >
        {PHASE_RUN_EFFORTS.map((e) => (
          <option key={e.value} value={e.value}>
            {e.label}
          </option>
        ))}
      </select>
    </label>
  );
}

// Plan status badge — the theme has no `yellow` token; `amber` is the app's
// semantic waiting/approval color, so paused uses it.
const STATUS_BADGE: Record<Epic['status'], string> = {
  active: 'border-brand/40 text-brand',
  paused: 'border-amber/40 text-amber',
  done: 'border-green/40 text-green',
  archived: 'border-line text-ink-faint',
};

/** Which lifecycle buttons a plan in a given status offers. */
const LIFECYCLE_ACTIONS: Record<Epic['status'], { action: EpicLifecycleAction; label: string }[]> = {
  active: [
    { action: 'pause', label: 'Pause' },
    { action: 'archive', label: 'Archive' },
  ],
  paused: [
    { action: 'resume', label: 'Resume' },
    { action: 'archive', label: 'Archive' },
  ],
  done: [{ action: 'archive', label: 'Archive' }],
  archived: [{ action: 'restore', label: 'Restore' }],
};

/** The Active/Done/Archived filter — the URL's `?status=` (plansUrl.ts). */
type EpicFilter = PlansStatus;
const FILTERS: EpicFilter[] = ['active', 'done', 'archived'];

/** Which filter tab an epic belongs to (paused plans live under Active). */
function epicFilterOf(status: Epic['status']): EpicFilter {
  return status === 'active' || status === 'paused' ? 'active' : status;
}

// PlanDetailTab (plan/spec/summary/revisions/edit) lives in plans/plansUrl.ts:
// it is part of the URL scheme now.

/** Phase panel tabs (story/criteria/runs/report/edit). Report always exists —
 * an empty note beats hiding it ("where do I read the summary?" was a dead end). */
type PhaseDetailTab = PhaseTab;

/** What the inline detail panel shows: one phase's details, or the plan's (both
 * tabbed). `null` means "no details — show the phase list".
 *
 * The phase is addressed by SEQ, not by `epic_phases.id`: a plan rescan
 * (wsingest/epics.go applyEpics) DELETEs and re-INSERTs every phase row, so the
 * surrogate id churns on each rescan — and a rescan is exactly what a checkbox
 * tick triggers. Keying by id made the open panel close, or silently jump to
 * whichever phase inherited the old id. Seq is the plan's own numbering and
 * survives. */
type DetailTarget =
  | { kind: 'phase'; seq: number; tab: PhaseDetailTab }
  | { kind: 'plan'; tab: PlanDetailTab };

/** Same selection with the phase resolved against the current epic. */
type DetailSel =
  | { kind: 'phase'; phase: EpicPhase; tab: PhaseDetailTab }
  | { kind: 'plan'; tab: PlanDetailTab };

/** Which URL-named level did not exist — named by the stale-link notice (SC-8).
 * `task` is the numeric `?task=`/`?plan=` hand-off (NaN for a non-numeric id). */
type PlansMissing =
  | { kind: 'plan'; externalId: string }
  | { kind: 'task'; taskId: number }
  | { kind: 'phase'; seq: number; planTitle: string }
  | null;

/** The one-line notice for a link whose target is gone; null when nothing is.
 * `search` supplies a non-numeric legacy id verbatim. */
function staleNotice(missing: PlansMissing, search: string): string | null {
  if (missing === null) return null;
  switch (missing.kind) {
    case 'plan':
      return `Plan ${missing.externalId} not found — showing the plan list`;
    case 'task': {
      const q = new URLSearchParams(search);
      const id = Number.isNaN(missing.taskId) ? (q.get('task') ?? q.get('plan') ?? '') : String(missing.taskId);
      return `Plan #${id} not found`;
    }
    case 'phase':
      return `Phase ${String(missing.seq)} not found in ${missing.planTitle}`;
  }
}

/** A click the page handles itself — not a new-tab / new-window / download
 * gesture, which belongs to the browser. */
function isPlainClick(e: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean }): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}

/** Plan-details tab labels, as PlanDetailPanel shows them (Revisions without its count). */
const PLAN_DETAIL_TAB_LABEL: Record<PlanDetailTab, string> = {
  plan: 'Plan',
  spec: 'Spec',
  summary: 'Summary',
  revisions: 'Revisions',
  edit: 'Edit',
};

/**
 * `document.title` of the Plans tab (SC-11):
 *   phase         `Runs · Phase 3 — <plan> · <project> — Swarmery`
 *   plan details  `Revisions — <plan> · <project> — Swarmery`
 *   plan          `<plan> · Plans · <project> — Swarmery`
 *   no plan       `Plans · <project> — Swarmery`
 * A phase the plan does not have reads as the plan (the URL is about to be
 * corrected to it).
 */
function plansTitle(epic: Epic | null, detail: DetailTarget | null, project: string): string {
  const tail = `${project} — Swarmery`;
  if (epic === null) return `Plans · ${tail}`;
  if (detail?.kind === 'phase' && epic.phases.some((p) => p.seq === detail.seq)) {
    const tab = PHASE_TABS.find((t) => t.id === detail.tab)?.label ?? detail.tab;
    return `${tab} · Phase ${String(detail.seq)} — ${epic.title} · ${tail}`;
  }
  if (detail?.kind === 'plan') return `${PLAN_DETAIL_TAB_LABEL[detail.tab]} — ${epic.title} · ${tail}`;
  return `${epic.title} · Plans · ${tail}`;
}

/**
 * The canonical target for a parsed Plans URL against the loaded epics: the
 * numeric `?task=`/`?plan=` hand-off resolved to its externalId, the first plan
 * of the filter chosen when none is named, an unknown plan / phase dropped to
 * the nearest valid level, and an unavailable tab mapped to the tab its panel
 * already falls back to (Edit on a done phase → Report; Summary on an
 * incomplete plan or Spec without a spec.md → Plan).
 *
 * Pure and deterministic in (route, epics), so replacing to it is idempotent:
 * its own output parses back to the same target. A revision id is validated
 * separately, against the revisions EpicDetail fetches.
 */
function canonicalPlansTarget(
  route: ParsedPlansRoute,
  epics: Epic[],
): { target: PlansTarget; missing: PlansMissing } {
  const listOf = (f: EpicFilter, missing: PlansMissing): { target: PlansTarget; missing: PlansMissing } => {
    const first = epics.find((e) => epicFilterOf(e.status) === f);
    return { target: first !== undefined ? { plan: first.externalId } : { plan: null, status: f }, missing };
  };

  if (route.plan === null) {
    if (route.legacy === null) return listOf(route.status ?? 'active', null);
    const { taskId, phaseSeq, wantRevisions } = route.legacy;
    const epic = epics.find((e) => e.taskId === taskId);
    if (epic === undefined) return listOf('active', { kind: 'task', taskId });
    if (wantRevisions) {
      return { target: { plan: epic.externalId, detail: { kind: 'plan', tab: 'revisions' } }, missing: null };
    }
    if (phaseSeq === null) return { target: { plan: epic.externalId }, missing: null };
    return epic.phases.some((p) => p.seq === phaseSeq)
      ? { target: { plan: epic.externalId, detail: { kind: 'phase', seq: phaseSeq, tab: 'story' } }, missing: null }
      : { target: { plan: epic.externalId }, missing: { kind: 'phase', seq: phaseSeq, planTitle: epic.title } };
  }

  const epic = epics.find((e) => e.externalId === route.plan);
  if (epic === undefined) return listOf('active', { kind: 'plan', externalId: route.plan });
  const d = route.detail;
  if (d === null) return { target: { plan: epic.externalId }, missing: null };
  const resolvedSeqs = computeResolvedSeqs(epic.phases);
  if (d.kind === 'phase') {
    const p = epic.phases.find((x) => x.seq === d.seq);
    if (p === undefined) {
      return { target: { plan: epic.externalId }, missing: { kind: 'phase', seq: d.seq, planTitle: epic.title } };
    }
    // Edit on a done phase → Report; Review on a phase that never ran → Runs
    // (the tabs each panel falls back to).
    const tab: PhaseDetailTab =
      d.tab === 'edit' && phaseStatus(p, resolvedSeqs) === 'done'
        ? 'report'
        : d.tab === 'review' && !hasReviewTab(p)
          ? 'runs'
          : d.tab;
    return { target: { plan: epic.externalId, detail: { kind: 'phase', seq: d.seq, tab } }, missing: null };
  }
  const complete = planComplete(epic, resolvedSeqs);
  const tab: PlanDetailTab =
    (d.tab === 'summary' && !complete) || (d.tab === 'spec' && !epic.hasSpec) ? 'plan' : d.tab;
  const detail: PlansDetail =
    tab === 'revisions' && d.revId !== undefined ? { kind: 'plan', tab, revId: d.revId } : { kind: 'plan', tab };
  return { target: { plan: epic.externalId, detail }, missing: null };
}

export function Plans(): JSX.Element {
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
  const filter: EpicFilter = urlEpic !== null ? epicFilterOf(urlEpic.status) : (route.status ?? 'active');
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

  // Reconcile net while any visible phase run is live: plan_updated covers the
  // run edges, this 5s poll covers a missed frame mid-run. Cleared when none run.
  const anyRunning = useMemo(
    () =>
      (epics ?? []).some(
        (e) => e.planRun?.runState === 'running' || e.phases.some((p) => p.runState === 'running'),
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
    () => (epics ?? []).filter((e) => epicFilterOf(e.status) === filter),
    [epics, filter],
  );
  const counts = useMemo(() => {
    const c: Record<EpicFilter, number> = { active: 0, done: 0, archived: 0 };
    for (const e of epics ?? []) c[epicFilterOf(e.status)] += 1;
    return c;
  }, [epics]);

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
      !window.confirm('Archive this plan? The task folder moves to the archive/ zone.')
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

  if (projLoading) return <Loading label="workspace…" />;
  if (project === null) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>unknown project — pick one from the switcher</Empty>
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
  if (epics === null) return <Loading label="epics…" />;
  if (epics.length === 0) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>
          no epics yet — a plan under this project&apos;s workspace (a{' '}
          <code className="font-mono text-[11px]">plan/</code> dir with a phase table) becomes an epic here
        </Empty>
      </div>
    );
  }

  // Resolve the selection against the CURRENT epic — a refetch can drop the
  // referenced phase (plan rescan), in which case the panel closes itself and
  // the phase list comes back.
  let detail: DetailSel | null = null;
  if (activeEpic !== null && detailTarget !== null) {
    if (detailTarget.kind === 'plan') {
      detail = { kind: 'plan', tab: detailTarget.tab };
    } else {
      const p = activeEpic.phases.find((x) => x.seq === detailTarget.seq);
      detail = p !== undefined ? { kind: 'phase', phase: p, tab: detailTarget.tab } : null;
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col px-3 py-4 desk:px-6">
      {actionError !== null && (
        <div
          role="alert"
          className="mb-3 flex items-center gap-2 rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11px] text-red"
        >
          <span className="min-w-0 flex-1">{actionError}</span>
          <button type="button" onClick={() => setActionError(null)} aria-label="dismiss" className="text-red/70">
            ×
          </button>
        </div>
      )}
      {/* A stale link's notice (SC-8): the actionError strip's shape, in amber —
          the page did land somewhere valid, nothing failed. */}
      {notice !== null && (
        <div
          role="status"
          className="mb-3 flex items-center gap-2 rounded-lg border border-amber/40 bg-amber/10 px-3 py-1.5 font-mono text-[11px] text-amber"
        >
          <span className="min-w-0 flex-1">{notice}</span>
          <button type="button" onClick={() => setNotice(null)} aria-label="dismiss notice" className="text-amber/70">
            ×
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 gap-5">
        {/* Epic list behind status filter tabs. */}
        <div className="flex w-[280px] shrink-0 flex-col">
          <div className="mb-2 flex items-center gap-1" role="tablist" aria-label="plan status filter">
            {FILTERS.map((f) => (
              <Link
                key={f}
                role="tab"
                aria-selected={filter === f}
                // A real link to the plan-less filter URL: one push, then the
                // correction effect REPLACES it with that filter's first plan
                // (or leaves an empty filter on its empty state). Re-clicking
                // the current filter is not a step — and must not re-pick its
                // first plan — so that plain click is swallowed.
                to={plansHref(slug, { plan: null, status: f }, location.search)}
                onClick={(ev) => {
                  if (f === filter && isPlainClick(ev)) ev.preventDefault();
                }}
                className={`rounded-md border px-2 py-1 font-mono text-[10.5px] capitalize transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand ${
                  filter === f
                    ? 'border-line-strong bg-surface2 text-brand'
                    : 'border-transparent text-ink-dim hover:text-ink'
                }`}
              >
                {f} ({counts[f]})
              </Link>
            ))}
          </div>
          <div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto pr-1">
            {filtered.length === 0 ? (
              <Empty>no {filter} plans</Empty>
            ) : (
              filtered.map((e) => (
                // The row is a real link to the plan (Cmd-click opens it in a new tab)
                // AND hosts CopyIdBadge's own <button>. Interactive content can't nest
                // inside an <a> (invalid HTML, and the chip's "copy id: …" would fold
                // into the link's name), so the link is the title and stretches over
                // the whole row with an ::after overlay. The chips that need their own
                // pointer (copy, the card tooltip) sit above that overlay (relative z-10).
                <div
                  key={e.taskId}
                  className={`relative rounded-lg border px-3 py-2.5 transition-colors ${
                    selected === e.taskId
                      ? 'border-line-strong bg-surface2'
                      : 'border-line bg-surface/40 hover:border-line-strong'
                  }`}
                >
                  <div className="flex items-center gap-1.5">
                    <Link
                      to={plansHref(slug, { plan: e.externalId }, location.search)}
                      // A plain click is one push. Re-clicking the selected plan keeps
                      // its open details, as before.
                      onClick={(ev) => {
                        if (selected === e.taskId && isPlainClick(ev)) ev.preventDefault();
                      }}
                      aria-current={selected === e.taskId}
                      className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink after:absolute after:inset-0 after:rounded-lg focus:outline-none focus-visible:after:outline focus-visible:after:outline-2 focus-visible:after:outline-offset-1 focus-visible:after:outline-brand"
                    >
                      {e.title}
                    </Link>
                    {/* A micro-plan: this plan IS a dispatched board card, materialized so its
                        outcome is evidence in a doc rather than a column someone dragged it to.
                        The chip is what makes the two views of one unit of work navigable. */}
                    {e.cardExternalId !== null && (
                      <span
                        data-tip={`board card ${e.cardExternalId}`}
                        className="relative z-10 shrink-0 rounded border border-line px-1 py-px font-mono text-[9px] text-ink-dim"
                      >
                        card
                      </span>
                    )}
                  </div>
                  <div className="mt-0.5 flex min-w-0 items-center gap-1.5 font-mono text-[10px] text-ink-faint">
                    {/* The chip shrinks and ellipsizes before the row scrolls — a
                        `yyyy-mm-dd-slug` plan id can run past this column's width,
                        and copying still copies the FULL id regardless of how much
                        of it is visually truncated. */}
                    <CopyIdBadge id={e.externalId} label="plan" truncate className="relative z-10 min-w-0 flex-1" />
                    {e.startedAt !== null && <span className="shrink-0">{e.startedAt.slice(0, 10)}</span>}
                    <span className="shrink-0">
                      {e.phases.length} phase{e.phases.length === 1 ? '' : 's'}
                    </span>
                  </div>
                  <ProgressBar done={e.rollup.done} total={e.rollup.total} className="mt-2" />
                </div>
              ))
            )}
          </div>
        </div>

        {/* Epic detail: phase timeline. */}
        <div className="flex min-w-0 flex-1 flex-col overflow-y-auto">
          {activeEpic === null ? (
            <>
              {/* Invisible spacer — same height as the tablist above the list column so the
                  empty states in both columns share the same vertical baseline. */}
              <div className="mb-2 flex items-center gap-1 opacity-0 pointer-events-none" aria-hidden="true">
                <span className="rounded-md border px-2 py-1 font-mono text-[10.5px]">&nbsp;</span>
              </div>
              <Empty>select an epic</Empty>
            </>
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
                  urlRevId !== null ? `Revision #${String(urlRevId)} not found` : null,
                );
              }}
              runBusy={runBusy}
              runMsg={runMsg !== null && runMsg.taskId === activeEpic.taskId ? runMsg.text : null}
              runBranches={runMsg !== null && runMsg.taskId === activeEpic.taskId ? runMsg.branches : []}
              onForceRun={forceRunFor(activeEpic.taskId)}
              onRun={(phaseId) => startRun(activeEpic.taskId, phaseId)}
              onCancelRun={(phaseId) => cancelRun(activeEpic.taskId, phaseId)}
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
      </div>

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
              ? 'a whole-plan run is executing this plan'
              : activeEpic.status !== 'active'
                ? 'plan is not active'
                : 'a phase run is already starting'
          }
          onClose={() => setOutcomeFor(null)}
          onRetry={() => startRun(activeEpic.taskId, outcomeFor)}
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
    </div>
  );
}

function EpicDetail({
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
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const complete = planComplete(epic, resolvedSeqs);
  const now = useNow(1000);
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

  return (
    <div className="pr-1">
      <div className="mb-3 flex items-baseline justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-2">
          {/* The plan title opens the plan rail — same click-to-detail idiom
              as the phase rows below. */}
          <button
            type="button"
            onClick={() => onOpenPlan('plan')}
            data-tip="open plan details"
            className="min-w-0 truncate text-left text-[15px] font-semibold text-ink transition-colors hover:text-brand"
          >
            {epic.title}
          </button>
          <span
            className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9.5px] ${STATUS_BADGE[epic.status]}`}
          >
            {epic.status}
          </span>
          <CopyIdBadge id={epic.externalId} label="plan" className="shrink-0" />
        </div>
        <span className="shrink-0 font-mono text-[11px] text-ink-dim">
          {epic.rollup.done}/{epic.rollup.total} ({Math.round(epic.rollup.pct)}%)
        </span>
      </div>
      <div className="mb-4 flex flex-wrap items-center gap-1.5">
        {/* Leaving the details is the first thing offered while they are open,
            in the same row as the plan actions. */}
        {detail !== null && <BackToPhases onBack={onCloseDetail} />}
        <button
          type="button"
          onClick={() => onOpenPlan('plan')}
          className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
        >
          ❐ open plan README
        </button>
        {LIFECYCLE_ACTIONS[epic.status].map(({ action, label }) => (
          <button
            key={action}
            type="button"
            disabled={busyLifecycle}
            onClick={() => onLifecycle(epic, action)}
            className="rounded-md border border-line-strong bg-surface2 px-2 py-1 font-mono text-[10.5px] text-ink-dim transition-colors hover:bg-surface2/70 hover:text-ink disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint"
          >
            {busyLifecycle ? '…' : label}
          </button>
        ))}
        <button
          type="button"
          disabled={reviseBusy || planRunning || phaseRunActive}
          data-tip={
            planRunning || phaseRunActive
              ? 'a run owns the plan docs — revise once it finishes'
              : 'interview against this plan and stage the changes as a reviewable diff'
          }
          onClick={() => {
            setReviseErr(null);
            setReviseOpenRevId(null);
            setReviseOpen(true);
          }}
          className="rounded-md border border-brand/40 px-2 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-brand/10 disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint"
        >
          Revise plan
        </button>
        {complete && (
          <button
            type="button"
            onClick={() => onOpenPlan('summary')}
            data-tip="what was shipped — per-phase executed work"
            className="rounded-md border border-green/40 px-2 py-1 font-mono text-[10.5px] text-green transition-colors hover:bg-green/10"
          >
            ✓ summary
          </button>
        )}
        {epic.hasSpec && epic.spec !== null && (
          <span
            data-tip="spec coverage — criteria covered by at least one phase"
            className={`rounded-md border px-2 py-1 font-mono text-[10.5px] ${
              epic.spec.covered < epic.spec.total
                ? 'border-amber/40 text-amber'
                : 'border-green/40 text-green'
            }`}
          >
            spec {epic.spec.covered}/{epic.spec.total}
          </span>
        )}
        <PlanRunControls
          epic={epic}
          complete={complete}
          now={now}
          busy={planRunBusy}
          phaseRunActive={phaseRunActive}
          onRun={onRunPlan}
          onCancel={onCancelPlanRun}
        />
      </div>

      {runMsg !== null && (
        <div className="mb-2 flex items-start gap-2 rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[10.5px] text-red">
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
              data-tip="run this phase again even though nothing has changed since it blocked"
              className="shrink-0 rounded-md border border-red/40 px-2 py-0.5 font-mono text-[10.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
            >
              Run anyway
            </button>
          )}
        </div>
      )}

      {/* Phases on the left, the sessions they produced on the right — the
          sessions column stays put while the phase area swaps between the
          timeline and a phase/plan detail, so a transcript is one click away
          from whatever is being read. */}
      <div className="flex min-w-0 items-start gap-4">
        <div className="min-w-0 flex-1">
          {/* Above the phase area, not in the plan header: the header's control is
              the WHOLE-PLAN run, which reads SWARMERY_PLANRUN_MODEL and is out of
              scope here (risk R3). Sitting here it governs exactly what it names —
              every per-phase Run/Retry below it, list or detail panel. */}
          <div className="mb-2 flex items-center justify-end gap-3">
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
          {/* A detail — a phase or the plan itself — opens INLINE, in place of the
              phase list, so it gets the whole column; "← all phases" (or Escape)
              above the panel backs out to the list. */}
          {detail?.kind === 'phase' ? (
            <PhaseDetailPanel
              epic={epic}
              phase={detail.phase}
              tab={detail.tab}
              onTab={(t) => onOpenPhase(detail.phase.seq, t)}
              tabHref={(t) => phaseHref(detail.phase.seq, t)}
              onStep={(seq) => onOpenPhase(seq, detail.tab)}
              runBusy={runBusy}
              planRunning={planRunning}
              phaseRunModel={phaseRunModel}
              onRetry={() => onRun(detail.phase.id)}
              onCancelRun={() => onCancelRun(detail.phase.id)}
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
            <PhaseList
              epic={epic}
              resolvedSeqs={resolvedSeqs}
              now={now}
              runBusy={runBusy}
              planRunning={planRunning}
              phaseRunModel={phaseRunModel}
              onOpenPhase={onOpenPhase}
              phaseHref={(seq) => phaseHref(seq, 'story')}
              onRun={onRun}
              onCancelRun={onCancelRun}
              onOpenOutcome={onOpenOutcome}
            />
          )}
        </div>
        <PlanSessions
          epic={epic}
          activePhaseId={detail !== null && detail.kind === 'phase' ? detail.phase.id : null}
        />
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

const SESSION_DOT: Record<SessionStatus, string> = {
  active: 'bg-green',
  waiting_approval: 'bg-amber',
  awaiting_reply: 'bg-amber',
  idle: 'bg-ink-faint',
  completed: 'bg-brand/60',
  killed: 'bg-red/70',
};

/** Every session the plan produced, to the RIGHT of the phase timeline: the
 * plan-run controller, each phase run, and the subagents under them — resolved
 * server-side from the stamped run branch / worktree cwd (?planTask=, see
 * internal/api/session_plan_group.go), the same rule the Sessions page groups by.
 *
 * The phase rows themselves link a session only WHILE a run is live, so on a
 * finished plan — the common case, e.g. an archived 72/72 plan — the transcripts
 * of everything that shipped were unreachable from the plan page. This column is
 * that missing link, and it is the only place a subagent transcript surfaces at
 * all: subagents never stamp a phase row. */
function PlanSessions({
  epic,
  activePhaseId,
}: {
  epic: Epic;
  /** The phase whose details are open, so its sessions read as selected. */
  activePhaseId: number | null;
}): JSX.Element {
  const sessionHref = useSessionHref();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // Run edges (start, cancel, finish) mint and end sessions. `epic` is already
  // refetched on the plan_updated WS frame, so a fingerprint of its run state is
  // the cheapest live trigger there is — no second subscription.
  const runFingerprint =
    epic.phases.map((p) => `${p.id}:${p.runState}:${p.runSessionUuid ?? ''}`).join('|') +
    `#${epic.planRun?.runState ?? ''}:${epic.planRun?.runSessionUuid ?? ''}`;

  useEffect(() => {
    let alive = true;
    setErr(null);
    fetchSessions({ planTask: epic.taskId }, { limit: 200 })
      .then((page) => {
        if (alive) setSessions(page.sessions);
      })
      .catch((e: unknown) => {
        if (alive) setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      alive = false;
    };
  }, [epic.taskId, runFingerprint]);

  // The operator's hand-picked slice lives WITH the plan it belongs to:
  // [taskId, scope]. Keying on taskId (the same idiom as `runMsg`, above) means
  // the pick cannot leak onto another plan and needs no reset effect — so the
  // refetch on `runFingerprint`, which replaces the whole session array whenever
  // a run ends, cannot knock the operator back to the automatic slice.
  const [picked, setPicked] = useState<{ taskId: number; scope: PlanSessionScope } | null>(null);
  const pickedScope = picked !== null && picked.taskId === epic.taskId ? picked.scope : null;

  // Ordering, dedup of the two sources, the phase slice and the auto-fallback
  // are one pure decision — see lib/planSessionScope.ts. This component only
  // renders what comes back.
  const view = useMemo(
    () =>
      scopePlanSessions({
        sessions: sessions ?? [],
        linked: epic.linkedSessions,
        activePhaseId,
        picked: pickedScope,
      }),
    [sessions, epic.linkedSessions, activePhaseId, pickedScope],
  );

  // The human label for the open phase — `phaseId` is a database id, `seq` is
  // what the timeline next to this column calls it.
  const activePhaseSeq = epic.phases.find((p) => p.id === activePhaseId)?.seq ?? null;

  return (
    <aside className="hidden w-[264px] shrink-0 flex-col lg:flex" aria-label="plan sessions">
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <span className="font-mono text-[10.5px] uppercase tracking-wide text-ink-dim">sessions</span>
        {sessions !== null &&
          // A plan with no sessions at all has nothing to switch between, so it
          // keeps the bare count rather than a toggle that cannot do anything.
          (view.phaseCount === null || view.totalCount === 0 ? (
            <span className="font-mono text-[10px] text-ink-faint">{view.totalCount}</span>
          ) : (
            // Both numbers stay on screen in either slice, so a narrowed column
            // never hides the FACT that it is hiding rows — and `#N (0)` is the
            // visible marker of the auto-fallback.
            //
            // The visible label is two counts, which say nothing on their own to
            // a screen reader, so the accessible name spells the state out and
            // `aria-pressed` carries the flip. Both keep the literal prefix
            // "session scope" — that is what selectors match on.
            <button
              type="button"
              aria-pressed={view.scope === 'phase'}
              aria-label={
                view.scope === 'phase'
                  ? `session scope: phase ${String(activePhaseSeq ?? '?')} only, ${String(view.phaseCount)} of ${String(view.totalCount)} sessions`
                  : `session scope: all ${String(view.totalCount)} sessions of the plan`
              }
              data-tip={
                view.scope === 'phase'
                  ? 'showing this phase only — click for every session of the plan'
                  : view.fellBack
                    ? 'this phase has no sessions of its own — showing the whole plan; click to narrow anyway'
                    : 'showing every session of the plan — click to narrow to this phase'
              }
              onClick={() =>
                setPicked({
                  taskId: epic.taskId,
                  scope: view.scope === 'phase' ? 'all' : 'phase',
                })
              }
              className="rounded font-mono text-[10px] text-ink-faint transition-colors hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
            >
              {/* Colour alone must not carry the state — the active side is also
                  underlined, which survives a monochrome or high-contrast view. */}
              <span
                className={
                  view.scope === 'phase' ? 'text-ink underline decoration-dotted underline-offset-2' : undefined
                }
              >
                #{activePhaseSeq ?? '?'} ({view.phaseCount})
              </span>
              <span className="px-1">/</span>
              <span
                className={
                  view.scope === 'all' ? 'text-ink underline decoration-dotted underline-offset-2' : undefined
                }
              >
                all ({view.totalCount})
              </span>
            </button>
          ))}
      </div>
      {err !== null ? (
        <ErrorBox message={err} />
      ) : sessions === null ? (
        <Loading />
      ) : view.totalCount === 0 ? (
        <Empty>no sessions ran this plan</Empty>
      ) : view.sessions.length === 0 && view.linked.length === 0 ? (
        // Only reachable through a deliberate pick: the automatic rule hands back
        // 'all' when the phase slice would be empty.
        <Empty>no sessions for this phase</Empty>
      ) : (
        <ol className="space-y-1">
          {view.sessions.map((s) => {
            const g = s.planGroup ?? null;
            const label = g?.role === 'phase' ? `#${String(g.phaseSeq ?? '?')}` : 'plan';
            const selected = g?.role === 'phase' && g.phaseId === activePhaseId;
            return (
              <li key={s.id}>
                <Link
                  to={sessionHref(s.sessionUuid)}
                  data-tip={g?.phaseName ?? undefined}
                  className={`block rounded-lg border px-2 py-1.5 transition-colors ${
                    selected
                      ? 'border-line-strong bg-surface2'
                      : 'border-line bg-surface/40 hover:border-line-strong'
                  }`}
                >
                  <div className="flex items-center gap-1.5">
                    <span
                      className={`h-1.5 w-1.5 shrink-0 rounded-full ${SESSION_DOT[s.status]} ${
                        s.status === 'active' ? 'animate-pulse' : ''
                      }`}
                    />
                    <span
                      className={`shrink-0 rounded border px-1 py-px font-mono text-[9px] ${
                        g?.role === 'phase' ? 'border-line text-ink-dim' : 'border-brand/40 text-brand'
                      }`}
                    >
                      {label}
                    </span>
                    <span className="truncate text-[11.5px] text-ink">
                      {s.title ?? s.why ?? s.sessionUuid.slice(0, 8)}
                    </span>
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[9.5px] text-ink-faint">
                    {fmtDateTime(s.startedAt)} · {s.status}
                    {s.costUsd != null ? ` · ${fmtCost(s.costUsd)}` : ''}
                  </div>
                </Link>
              </li>
            );
          })}
          {view.linked.map((l) => (
            <li key={l.sessionUuid}>
              <Link
                to={sessionHref(l.sessionUuid)}
                data-tip={
                  l.linkSource === 'heuristic'
                    ? 'inferred from files this session edited under plan/'
                    : 'linked to this plan by the daemon'
                }
                className="block rounded-lg border border-line border-dashed bg-surface/40 px-2 py-1.5 transition-colors hover:border-line-strong"
              >
                <div className="flex items-center gap-1.5">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-ink-faint" />
                  <span
                    className={`shrink-0 rounded border px-1 py-px font-mono text-[9px] ${
                      l.linkSource === 'heuristic'
                        ? 'border-line text-ink-faint'
                        : 'border-line text-ink-dim'
                    }`}
                  >
                    {l.linkSource === 'heuristic' ? 'inferred' : 'linked'}
                  </span>
                  <span className="truncate text-[11.5px] text-ink">{l.sessionUuid.slice(0, 8)}</span>
                </div>
                <div className="mt-0.5 truncate font-mono text-[9.5px] text-ink-faint">
                  {fmtDateTime(l.startedAt)}
                  {l.endedAt === null ? ' · live' : ''}
                  {l.costUsd != null ? ` · ${fmtCost(l.costUsd)}` : ''}
                </div>
              </Link>
            </li>
          ))}
        </ol>
      )}
    </aside>
  );
}

/** The phase timeline — seq order, depends-on badges, per-phase progress and
 * run controls. A row opens that phase's details, which replace this list. */
function PhaseList({
  epic,
  resolvedSeqs,
  now,
  runBusy,
  planRunning,
  phaseRunModel,
  onOpenPhase,
  phaseHref,
  onRun,
  onCancelRun,
  onOpenOutcome,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
  now: number;
  runBusy: number | null;
  /** A whole-plan run owns the phase docs — per-phase runs stand down. */
  planRunning: boolean;
  /** The picker's current value — read ONLY to tell whether a phase doc's own
   * declaration is in effect or overridden (DocModelChip). The resolution itself
   * stays server-side, in phaserun.resolveModel: a second ladder here would be a
   * second thing to drift. */
  phaseRunModel: PhaseRunModel;
  onOpenPhase: (seq: number, tab: PhaseDetailTab) => void;
  /** The URL a phase opens at (its panel's Story) — what its name links to. */
  phaseHref: (seq: number) => string;
  onRun: (phaseId: number) => void;
  onCancelRun: (phaseId: number) => void;
  onOpenOutcome: (phaseId: number) => void;
}): JSX.Element {
  const sessionHref = useSessionHref();
  return (
    <ol className="space-y-2">
      {epic.phases.map((p) => {
        const activated = p.activatedAt !== null;
        const status = phaseStatus(p, resolvedSeqs);
        // Process state, kept out of `phaseStatus` on purpose: only a live run
        // may say `running`, and only a live run may claim liveness.
        const running = p.runState === 'running';
        const chip = phaseChip(status, running);
        const depsUnmet = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
        const runDisabled =
          runBusy !== null || planRunning || epic.status !== 'active' || depsUnmet.length > 0;
        const runTitle = planRunning
          ? 'a whole-plan run is executing this plan'
          : epic.status !== 'active'
            ? 'plan is not active'
            : depsUnmet.length > 0
              ? `waiting on phase ${depsUnmet.join(', ')}`
              : 'run this phase headlessly in an isolated worktree';
        const openPhase = (): void => {
          onOpenPhase(p.seq, 'story');
        };
        return (
          <li
            key={p.id}
            role="button"
            tabIndex={0}
            aria-label={`open Phase ${String(p.seq)} — ${p.name} details`}
            onClick={openPhase}
            onKeyDown={(e) => {
              if (e.target !== e.currentTarget) return; // inner buttons handle their own keys
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                openPhase();
              }
            }}
            className="cursor-pointer rounded-lg border border-line bg-surface/40 px-3 py-2.5 transition-colors hover:border-line-strong focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="shrink-0 font-mono text-[10px] text-ink-faint">
                    Phase {p.seq}
                  </span>
                  {/* The row holds buttons, so the anchor is the NAME, not the row
                      (no <a> around inner buttons). Its click is the Link's push;
                      stopPropagation keeps the row from opening it a second time. */}
                  <Link
                    to={phaseHref(p.seq)}
                    onClick={(e) => e.stopPropagation()}
                    className="truncate text-[13px] font-medium text-ink"
                  >
                    {p.name}
                  </Link>
                  <span
                    className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9px] ${chip.cls}`}
                  >
                    {chip.label}
                  </span>
                  {status === 'in_progress' && (
                    <PhaseActivity docUpdatedAt={p.docUpdatedAt} running={running} />
                  )}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5">
                  {p.dependsOn.map((seq) => (
                    <span
                      key={seq}
                      className={`rounded border px-1 py-px font-mono text-[9px] ${
                        resolvedSeqs.has(seq)
                          ? 'border-green/40 text-green'
                          : 'border-line text-ink-faint'
                      }`}
                      data-tip={resolvedSeqs.has(seq) ? 'dependency resolved' : 'dependency pending'}
                    >
                      ← #{seq}
                    </span>
                  ))}
                  <span className="font-mono text-[10px] text-ink-faint">
                    {p.checkboxesDone}/{p.checkboxesTotal || 0}
                  </span>
                </div>
                <ProgressBar done={p.checkboxesDone} total={p.checkboxesTotal} className="mt-2 max-w-[220px]" />
              </div>

              <div className="flex shrink-0 flex-col items-end gap-1.5">
                {/* Legacy chip: phases activated into a board task before the plan↔board
                    decoupling still show their board column for historical context. */}
                {activated && (
                  <span
                    className="rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
                    data-tip={p.boardTaskExternalId ?? undefined}
                  >
                    activated{p.boardColumn !== null ? ` · ${p.boardColumn}` : ''}
                  </span>
                )}

                {/* Direct phase run (no board task): running shows elapsed +
                    Cancel + session link; a DONE phase retires the Run button
                    and offers ✓ summary (opens the details rail); idle/failed
                    offer Run/Retry. */}
                <ContinuationChip events={p.runEvents} />
                <SurpriseChip phase={p} onOpen={() => onOpenPhase(p.seq, 'story')} />

                {p.runState === 'running' ? (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <span
                      className="inline-flex items-center gap-1 rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
                      data-tip="headless run in progress"
                    >
                      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-brand" />
                      Running
                      {p.runStartedAt !== null ? ` · ${fmtElapsed(p.runStartedAt, now)}` : ''}
                    </span>
                    {p.runSessionUuid !== null && (
                      <Link
                        to={sessionHref(p.runSessionUuid)}
                        onClick={(e) => e.stopPropagation()}
                        className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
                      >
                        session
                      </Link>
                    )}
                    <button
                      type="button"
                      disabled={runBusy !== null}
                      onClick={(e) => {
                        e.stopPropagation();
                        onCancelRun(p.id);
                      }}
                      className="rounded-md border border-red/40 px-1.5 py-px font-mono text-[9.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      Cancel
                    </button>
                  </span>
                ) : status === 'done' ? (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <RunModelChip phase={p} />
                    <DocModelChip phase={p} picked={phaseRunModel} />
                    {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
                    {isUnresolvedOutcome(p.runOutcome) && (
                      <RunOutcomeChip
                        phase={p}
                        outcome={p.runOutcome}
                        onOpen={() => onOpenOutcome(p.id)}
                      />
                    )}
                    <button
                      type="button"
                      onClick={() => {
                        onOpenPhase(p.seq, 'report');
                      }}
                      data-tip="what was done — the executor's Completion Report and execution record"
                      className="font-mono text-[9.5px] text-green underline-offset-2 transition-colors hover:underline"
                    >
                      ✓ summary
                    </button>
                  </span>
                ) : (
                  <span className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <RunModelChip phase={p} />
                    <DocModelChip phase={p} picked={phaseRunModel} />
                    {p.runOutcome === 'completed' && <RunCompletedChip phase={p} />}
                    {isUnresolvedOutcome(p.runOutcome) && (
                      <RunOutcomeChip
                        phase={p}
                        outcome={p.runOutcome}
                        onOpen={() => onOpenOutcome(p.id)}
                      />
                    )}
                    <button
                      type="button"
                      disabled={runDisabled}
                      data-tip={runTitle}
                      onClick={(e) => {
                        e.stopPropagation();
                        onRun(p.id);
                      }}
                      className={`rounded-md border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint ${runButtonCls(p.runOutcome)}`}
                    >
                      {runBusy === p.id
                        ? '…'
                        : isUnresolvedOutcome(p.runOutcome)
                          ? 'Retry run'
                          : 'Run phase'}
                    </button>
                  </span>
                )}
                {/* A done phase is history: its doc describes work already
                    shipped, so editing it would rewrite the record rather than
                    steer the work. The ✓ summary button above is the only
                    affordance it keeps. */}
                {status !== 'done' && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation();
                      onOpenPhase(p.seq, 'edit');
                    }}
                    className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-ink hover:underline"
                  >
                    edit doc
                  </button>
                )}
              </div>
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/** One acceptance checkbox: its 0-based SOURCE line (what the PATCH endpoint
 * addresses), tick state and label. */
interface Check {
  line: number;
  text: string;
  done: boolean;
}

/** Acceptance-criteria checkbox lines of a markdown doc, in order. The line
 * index is what makes the list toggleable — togglePlanCheckbox rewrites that
 * exact `- [ ]`↔`- [x]` line. */
function extractChecks(md: string): Check[] {
  const out: Check[] = [];
  md.split('\n').forEach((raw, i) => {
    const m = /^\s*[-*]\s+\[( |x)\]\s+(.*)$/i.exec(raw);
    if (m !== null) out.push({ line: i, done: (m[1] ?? '').toLowerCase() === 'x', text: m[2] ?? '' });
  });
  return out;
}

/** The body of one `## <heading>` markdown section (up to the next `## `
 * heading), or null when the doc has no such section / it is empty. */
function extractSection(md: string, heading: string): string | null {
  const lines = md.split('\n');
  const want = `## ${heading}`.toLowerCase();
  const start = lines.findIndex((l) => l.trim().toLowerCase() === want);
  if (start === -1) return null;
  const body: string[] = [];
  for (const line of lines.slice(start + 1)) {
    if (/^##\s/.test(line)) break;
    body.push(line);
  }
  const text = body.join('\n').trim();
  return text === '' ? null : text;
}

/** Lazily fetches one plan doc; errors fold into the returned text (same
 * behaviour the old details modal had). `version` re-triggers the fetch when
 * the doc changes server-side (docUpdatedAt from a plan_updated refetch). The
 * setter lets a local write (checkbox toggle, Save) adopt the server's response
 * without waiting for the next refetch. */
function usePlanDoc(
  taskId: number,
  path: string,
  version: string | null,
): [string | null, (text: string) => void] {
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setText(null);
    fetchPlanDoc(taskId, path)
      .then((d) => {
        if (alive) setText(d.content);
      })
      .catch((e: unknown) => {
        if (alive)
          setText(`failed to load ${path}: ${e instanceof Error ? e.message : String(e)}`);
      });
    return () => {
      alive = false;
    };
  }, [taskId, path, version]);
  return [text, setText];
}

/** The back-out control. Lives in the plan's action row (first, ahead of "open
 * plan README" / lifecycle), not inside the detail panel's header where it used
 * to get lost against the panel's own identity block. Sized like its row
 * siblings, weighted like the lifecycle buttons so it still reads as the
 * primary way out. */
function BackToPhases({ onBack }: { onBack: () => void }): JSX.Element {
  return (
    <button
      type="button"
      onClick={onBack}
      data-tip="back to the phase list (Esc)"
      className="inline-flex items-center gap-1.5 rounded-md border border-line-strong bg-surface2 px-2 py-1 font-mono text-[10.5px] text-ink transition-colors hover:bg-surface2/70 hover:text-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
    >
      <span aria-hidden="true">←</span> all phases
    </button>
  );
}

/** The execution modes offered when starting a whole-plan run. The run-plan
 * skill triages its own ROUTE (sequential / parallel group / workflow) from the
 * phase DAG — the UI must not duplicate that, since it needs the graph to be
 * meaningful. What the skill cannot derive is the call an interactive session
 * always asks about: dispatch per-phase executors, or do the work inline. */
const PLAN_RUN_MODES: { id: PlanRunMode; label: string; hint: string }[] = [
  { id: 'auto', label: 'auto', hint: 'let the run-plan skill triage the route from the phase DAG' },
  { id: 'subagents', label: 'subagent-driven', hint: 'dispatch an implementer + a fresh reviewer per phase' },
  { id: 'inline', label: 'inline', hint: 'the controller implements the phases itself, no dispatch' },
];

/** Whole-plan run controls in the plan action row: a Run plan button that opens
 * an inline agent+mode picker, the live chip (elapsed + session link + Cancel)
 * while a run is in flight, and the failure chip + Retry afterwards. */
function PlanRunControls({
  epic,
  complete,
  now,
  busy,
  phaseRunActive,
  onRun,
  onCancel,
}: {
  epic: Epic;
  complete: boolean;
  now: number;
  busy: boolean;
  phaseRunActive: boolean;
  onRun: (agent: string, mode: PlanRunMode) => void;
  onCancel: () => void;
}): JSX.Element | null {
  const [open, setOpen] = useState(false);
  const sessionHref = useSessionHref();
  const run = epic.planRun;

  if (run?.runState === 'running') {
    return (
      <span className="flex items-center gap-1.5">
        <span
          className="inline-flex items-center gap-1 rounded-md border border-brand/40 bg-brand/10 px-2 py-1 font-mono text-[10.5px] text-brand"
          data-tip={`the whole plan is running${run.agent !== null ? ` — agent ${run.agent}` : ''} (mode: ${run.mode})`}
        >
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-brand" />
          Running plan
          {run.runStartedAt !== null ? ` · ${fmtElapsed(run.runStartedAt, now)}` : ''}
        </span>
        {run.runSessionUuid !== null && (
          <Link
            to={sessionHref(run.runSessionUuid)}
            className="font-mono text-[10.5px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
          >
            session
          </Link>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={onCancel}
          className="rounded-md border border-red/40 px-2 py-1 font-mono text-[10.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Cancel
        </button>
      </span>
    );
  }

  // A finished plan has nothing left to hand over; the ✓ summary button next to
  // this one is the right affordance there.
  if (complete) return null;

  const failed = run?.runState === 'failed';
  const disabled = busy || phaseRunActive || epic.status !== 'active';
  const title = phaseRunActive
    ? 'a phase run is active — cancel it before running the whole plan'
    : epic.status !== 'active'
      ? 'plan is not active'
      : 'hand the whole plan to one agent, headlessly, in an isolated worktree';

  return (
    <>
      <span className="flex items-center gap-1.5">
        {failed && (
          <span
            className="rounded-md border border-red/40 bg-red/10 px-2 py-1 font-mono text-[10.5px] text-red"
            data-tip={run?.runError ?? 'plan run failed'}
          >
            Run failed
          </span>
        )}
        <button
          type="button"
          disabled={disabled}
          data-tip={title}
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className={`rounded-md border px-2 py-1 font-mono text-[10.5px] transition-colors disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint ${
            failed ? 'border-red/40 text-red hover:bg-red/10' : 'border-brand/40 text-brand hover:bg-brand/10'
          }`}
        >
          {busy ? '…' : failed ? '▶ Retry plan' : '▶ Run plan'}
        </button>
      </span>
      {open && (
        <PlanRunDialog
          initialAgent={run?.agent ?? ''}
          initialMode={run?.mode ?? 'auto'}
          onCancel={() => setOpen(false)}
          onRun={(agent, mode) => {
            setOpen(false);
            onRun(agent, mode);
          }}
        />
      )}
    </>
  );
}

/** The inline "how should this run?" panel — agent + execution mode. Inline
 * rather than modal, matching the rest of this page. It takes the full row so
 * it reads as a step, not as a dropdown hanging off the button. */
function PlanRunDialog({
  initialAgent,
  initialMode,
  onRun,
  onCancel,
}: {
  initialAgent: string;
  initialMode: PlanRunMode;
  onRun: (agent: string, mode: PlanRunMode) => void;
  onCancel: () => void;
}): JSX.Element {
  const [agent, setAgent] = useState(initialAgent);
  const [mode, setMode] = useState<PlanRunMode>(initialMode);
  const [agents, setAgents] = useState<string[] | null>(null);

  // The picker is the reason this is a panel and not a bare button: the agent
  // list is only worth fetching once someone actually opens it.
  useEffect(() => {
    let alive = true;
    fetchSystemItems('agents')
      .then((items) => {
        if (!alive) return;
        const names = [...new Set(items.map((i) => i.name))].sort((a, b) => a.localeCompare(b));
        setAgents(names);
      })
      .catch(() => {
        if (alive) setAgents([]); // the free-text field still works
      });
    return () => {
      alive = false;
    };
  }, []);

  return (
    <div className="mt-1 w-full rounded-lg border border-line bg-surface/60 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-1.5">
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-faint">agent</span>
          <input
            list="plan-run-agents"
            value={agent}
            onChange={(e) => setAgent(e.target.value)}
            placeholder={agents === null ? 'loading…' : 'tech-lead (default)'}
            className="w-[190px] rounded-md border border-line bg-field px-2 py-1 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
          />
          <datalist id="plan-run-agents">
            {(agents ?? []).map((a) => (
              <option key={a} value={a} />
            ))}
          </datalist>
        </label>

        <div className="flex items-center gap-1.5" role="radiogroup" aria-label="execution mode">
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-faint">mode</span>
          {PLAN_RUN_MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              role="radio"
              aria-checked={mode === m.id}
              data-tip={m.hint}
              onClick={() => setMode(m.id)}
              className={`rounded-md border px-2 py-1 font-mono text-[10.5px] transition-colors ${
                mode === m.id
                  ? 'border-brand/50 bg-brand/10 text-brand'
                  : 'border-line text-ink-dim hover:border-line-strong hover:text-ink'
              }`}
            >
              {m.label}
            </button>
          ))}
        </div>

        <div className="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim transition-colors hover:text-ink"
          >
            cancel
          </button>
          <button
            type="button"
            onClick={() => onRun(agent.trim(), mode)}
            className="rounded-md border border-brand/50 bg-brand/10 px-2.5 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-brand/20"
          >
            ▶ Run
          </button>
        </div>
      </div>
      <p className="mt-2 font-mono text-[10px] leading-relaxed text-ink-faint">
        {PLAN_RUN_MODES.find((m) => m.id === mode)?.hint} · the run executes core&apos;s{' '}
        <code className="text-ink-dim">run-plan</code> skill headlessly in an isolated worktree:
        it commits per phase but never pushes, and stops with PLAN BLOCKED if a phase needs a human.
      </p>
    </div>
  );
}

/** Shell of the inline detail panel: an identity header, an optional tab bar
 * and the body — full column width (the parent column owns the scroll), so
 * plan docs and completion reports read as prose instead of a 360px rail. The
 * back control lives outside, above the panel (see BackToPhases). */
function DetailShell({
  ariaLabel,
  header,
  tabBar,
  children,
}: {
  ariaLabel: string;
  header: ReactNode;
  tabBar?: ReactNode;
  children: ReactNode;
}): JSX.Element {
  return (
    <section
      aria-label={ariaLabel}
      className="overflow-hidden rounded-xl border border-line bg-surface"
    >
      <div className="border-b border-line px-4 pt-3 pb-3">{header}</div>
      {tabBar}
      <div className="px-4 py-3 text-[13px] leading-relaxed">{children}</div>
    </section>
  );
}

/** The shared tab bar of both detail panels. With `hrefFor` each tab is a real
 * link (its click navigates, `onTab` is not called as well). */
function DetailTabs<T extends string>({
  label,
  tabs,
  active,
  onTab,
  hrefFor,
}: {
  label: string;
  tabs: { id: T; label: string }[];
  active: T;
  onTab: (tab: T) => void;
  hrefFor?: ((tab: T) => string) | undefined;
}): JSX.Element {
  return (
    <div
      className="flex shrink-0 gap-1 overflow-x-auto border-b border-line px-4"
      role="tablist"
      aria-label={label}
    >
      {tabs.map((t) => {
        const className = `-mb-px shrink-0 border-b-2 px-3 py-[8px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
          active === t.id ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
        }`;
        return hrefFor !== undefined ? (
          <Link key={t.id} to={hrefFor(t.id)} role="tab" aria-selected={active === t.id} className={className}>
            {t.label}
          </Link>
        ) : (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={active === t.id}
            onClick={() => onTab(t.id)}
            className={className}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

/** Acceptance-criteria list with tick state (✓ done / ○ open). With `onToggle`
 * the rows become buttons that flip the criterion in the doc (the affordance
 * the retired doc modal owned); without it the list is read-only. `lint`
 * renders under a toggleable row (the Criteria tab's class-marker hint). */
function ChecksList({
  checks,
  onToggle,
  busyLine,
  lint,
}: {
  checks: Check[];
  onToggle?: (c: Check) => void;
  busyLine?: number | null;
  lint?: (c: Check) => ReactNode;
}): JSX.Element {
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
              data-tip={c.done ? 'un-tick this criterion in the doc' : 'tick this criterion in the doc'}
              className="flex w-full items-start gap-2 rounded px-1 py-0.5 text-left transition-colors hover:bg-surface2/50 disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
            >
              {mark}
              <span className={c.done ? 'text-ink-dim line-through' : 'text-ink'}>{c.text}</span>
            </button>
            {lint?.(c)}
          </li>
        );
      })}
    </ul>
  );
}

/** Inline raw-markdown editor — the Edit tab of both detail panels. Loads the
 * doc itself, PUTs on Save (the daemon writes a timestamped backup), and tells
 * the page to refetch so the rollup follows. Replaces the doc modal. */
function DocEditor({
  taskId,
  path,
  version,
  onSaved,
}: {
  taskId: number;
  path: string;
  /** Bumps to re-fetch when the doc changed server-side. */
  version: string | null;
  onSaved: () => void;
}): JSX.Element {
  const [content, setContent] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setContent(null);
    setError(null);
    setNote(null);
    fetchPlanDoc(taskId, path)
      .then((d) => {
        if (!alive) return;
        setContent(d.content);
        setDraft(d.content);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      alive = false;
    };
  }, [taskId, path, version]);

  const save = (): void => {
    setSaving(true);
    setError(null);
    savePlanDoc(taskId, path, draft)
      .then((doc) => {
        setContent(doc.content);
        setDraft(doc.content);
        setNote(doc.backup !== undefined ? 'saved · backup written' : 'saved');
        onSaved();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false));
  };

  // Unsaved work cannot be lost without a prompt (SC-12). In-app navigation —
  // Back, a tab, a plan link — that would leave this location is blocked and
  // confirmed; a reload or a closed tab gets the native beforeunload prompt
  // (the pages/Memory.tsx idiom).
  const unsaved = content !== null && draft !== content;
  const shouldBlock = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) =>
      unsaved &&
      `${currentLocation.pathname}${currentLocation.search}` !== `${nextLocation.pathname}${nextLocation.search}`,
    [unsaved],
  );
  const blocker = useBlocker(shouldBlock);
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    if (window.confirm(`Discard unsaved changes to ${path}?`)) blocker.proceed();
    else blocker.reset();
  }, [blocker, path]);
  const unsavedRef = useRef(unsaved);
  unsavedRef.current = unsaved;
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent): void => {
      if (unsavedRef.current) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, []);

  if (content === null && error === null) return <Loading label="doc…" />;
  const dirty = draft !== content;

  return (
    <div className="space-y-2">
      <div className="font-mono text-[10px] text-ink-faint">{path}</div>
      {error !== null && (
        <div
          role="alert"
          className="rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[11px] text-red"
        >
          {error}
        </div>
      )}
      {note !== null && !dirty && (
        <div className="rounded-md border border-green/30 bg-green/10 px-2.5 py-1.5 font-mono text-[11px] text-green">
          {note}
        </div>
      )}
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        spellCheck={false}
        aria-label="plan doc source"
        className="min-h-[460px] w-full resize-y rounded-lg border border-line bg-field px-3.5 py-3 font-mono text-[12.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
      />
      <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          disabled={!dirty || saving}
          onClick={() => setDraft(content ?? '')}
          className="rounded-md border border-line px-3 py-1.5 font-mono text-[11px] text-ink-dim transition-colors hover:text-ink disabled:cursor-not-allowed disabled:text-ink-faint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          revert
        </button>
        <button
          type="button"
          disabled={!dirty || saving}
          onClick={save}
          className="rounded-md border border-line-strong bg-surface2 px-3 py-1.5 font-mono text-[11px] text-brand transition-colors hover:bg-surface2/70 disabled:cursor-not-allowed disabled:text-ink-faint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          {saving ? 'saving…' : 'Save'}
        </button>
      </div>
    </div>
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
  if (phase.runOutcome === 'running')
    return (
      <span
        className="inline-flex items-center gap-1 rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
        data-tip="headless run in progress"
      >
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-brand" />
        Running
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
function PhaseDetailPanel({
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
    tab === 'edit' && !editable ? 'report' : tab === 'review' && !hasReviewTab(phase) ? 'runs' : tab;
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
  const runTitle = planRunning
    ? 'a whole-plan run is executing this plan'
    : epic.status !== 'active'
      ? 'plan is not active'
      : depsUnmet.length > 0
        ? `waiting on phase ${depsUnmet.join(', ')}`
        : 'run this phase headlessly in an isolated worktree';
  // The card's ONE primary action (1d): Cancel while live, Run/Retry until done.
  const primary = running
    ? { label: 'Cancel', onClick: onCancelRun, disabled: runBusy !== null }
    : status !== 'done'
      ? {
          label:
            runBusy === phase.id ? '…' : isUnresolvedOutcome(phase.runOutcome) ? 'Retry run' : 'Run phase',
          onClick: onRetry,
          disabled: runDisabled,
          tip: runTitle,
        }
      : null;

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
        <PhaseSummary phase={phase} doc={doc} />
      ) : activeTab === 'story' ? (
        <div className="space-y-5">
          <PhaseCard
            phase={phase}
            {...(primary !== null ? { primary } : {})}
            secondaries={
              isUnresolvedOutcome(phase.runOutcome) ? [{ label: 'why it did not move', onClick: onOpenOutcome }] : []
            }
            {...(phase.completionReport !== null ? { report: <Markdown text={phase.completionReport} /> } : {})}
            terms={terms}
          />
          <ForecastStory phase={phase} />
          {phase.surprise == null && <ForecastSection phase={phase} />}
        </div>
      ) : activeTab === 'review' ? (
        // Keyed by phase: ↑/↓ step navigation keeps the tab, and a land still in
        // flight for the previous phase must not render onto this one.
        <PhaseReview key={phase.id} epic={epic} phase={phase} terms={terms} onLanded={onDocChanged} />
      ) : activeTab === 'runs' ? (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className={`rounded border px-1.5 py-px font-mono text-[9px] ${chip.cls}`}>{chip.label}</span>
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
                data-tip={resolvedSeqs.has(seq) ? 'dependency resolved' : 'dependency pending'}
              >
                ← #{seq}
              </span>
            ))}
            {primary !== null && (
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
          {running && phase.runSessionUuid !== null && (
            <Link to={sessionHref(phase.runSessionUuid)} className="mt-2 inline-block font-mono text-[10px] text-ink-dim hover:text-brand hover:underline">
              session →
            </Link>
          )}
          {appliedRevision !== undefined && (
            <div className="mt-2 font-mono text-[10px] text-ink-faint">
              this doc was changed by an applied revision{' '}
              {fmtAgo(appliedRevision.decidedAt ?? appliedRevision.createdAt)} —{' '}
              <button type="button" onClick={onOpenRevisions} className="text-brand hover:underline">
                see Revisions
              </button>
            </div>
          )}
          {phase.runState === 'failed' && (
            <div className="mt-3 rounded-md border border-red/40 bg-red/10 px-2.5 py-2 font-mono text-[10.5px] break-words text-red">
              {phase.runError ?? 'run failed'}
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
          <RailSection label="acceptance criteria">
            {checks === null ? (
              <Loading label="criteria…" />
            ) : checks.length === 0 ? (
              <div className="font-mono text-[11.5px] text-ink-faint">no checkboxes in this doc</div>
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
                    onMarked={(d) => {
                      setDoc(d.content);
                      onDocChanged();
                    }}
                  />
                )}
              />
            )}
          </RailSection>
          <RailSection label="doc">{doc === null ? <Loading label="doc…" /> : <Markdown text={doc} />}</RailSection>
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
function PhaseSummary({ phase, doc }: { phase: EpicPhase; doc: string | null }): JSX.Element {
  const checks = useMemo(() => (doc !== null ? extractChecks(doc) : []), [doc]);
  const execRecord = useMemo(() => (doc !== null ? extractSection(doc, 'Execution record') : null), [doc]);

  if (doc === null) return <Loading label="summary…" />;

  const done = checks.filter((c) => c.done).length;
  const score = (
    <div className="mb-3 font-mono text-[10.5px] text-ink-faint">
      {done}/{checks.length} acceptance criteria met · full list on the Criteria tab
    </div>
  );

  if (phase.completionReport === null && execRecord === null) {
    return (
      <>
        {checks.length > 0 && score}
        <div className="font-mono text-[11.5px] text-ink-faint">
          no summary of the work written — the executor left neither a{' '}
          <span className="text-ink-dim">## Completion Report</span> nor an{' '}
          <span className="text-ink-dim">## Execution record</span> section in this phase doc
        </div>
      </>
    );
  }

  return (
    <>
      {checks.length > 0 && score}
      {phase.completionReport !== null && (
        <RailSection label="what was done">
          <Markdown text={phase.completionReport} />
        </RailSection>
      )}
      {execRecord !== null && (
        <RailSection label="execution record">
          <Markdown text={execRecord} />
        </RailSection>
      )}
    </>
  );
}

/** Plan details panel: a Plan tab (the plan README markdown), a Spec tab
 * (rendered spec.md + per-criterion coverage; only when the plan has one), an
 * Edit tab (raw README) and — only when every phase is done — a Summary tab
 * with the per-phase executed work. Rendered inline in place of the phase
 * list. */
function PlanDetailPanel({
  epic,
  tab,
  onTab,
  tabHref,
  onDocChanged,
  revisions,
  revisionsErr,
  stagedCount,
  focusRevId = null,
  onRevisionsChanged,
}: {
  epic: Epic;
  tab: PlanDetailTab;
  onTab: (tab: PlanDetailTab) => void;
  /** Each tab's URL — makes the tabs real links. */
  tabHref?: ((tab: PlanDetailTab) => string) | undefined;
  onDocChanged: () => void;
  /** The plan's revisions, newest first (fetched by EpicDetail). */
  revisions: PlanRevision[] | null;
  revisionsErr: string | null;
  stagedCount: number;
  /** The revision a `/details/revisions/<revId>` link opened — expanded and
   * marked current on the Revisions tab. */
  focusRevId?: number | null;
  /** A revision was decided — refetch the list (and the docs, on apply). */
  onRevisionsChanged: () => void;
}): JSX.Element {
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const complete = planComplete(epic, resolvedSeqs);
  // The Summary tab exists only on complete plans, the Spec tab only on plans
  // with a spec.md — a stale selection (e.g. a rescan un-ticked a box, or
  // removed the spec) degrades to the Plan tab instead of a dead panel.
  const activeTab: PlanDetailTab =
    (tab === 'summary' && !complete) || (tab === 'spec' && !epic.hasSpec) ? 'plan' : tab;
  const tabs: { id: PlanDetailTab; label: string }[] = [
    { id: 'plan', label: 'Plan' },
    ...(epic.hasSpec ? [{ id: 'spec' as const, label: 'Spec' }] : []),
    ...(complete ? [{ id: 'summary' as const, label: 'Summary' }] : []),
    { id: 'revisions', label: stagedCount > 0 ? `Revisions (${String(stagedCount)})` : 'Revisions' },
    { id: 'edit', label: 'Edit' },
  ];

  return (
    <DetailShell
      ariaLabel={`${epic.title} — plan details`}
      header={
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-ink">{epic.title}</div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <span
              className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${STATUS_BADGE[epic.status]}`}
            >
              {epic.status}
            </span>
            <CopyIdBadge id={epic.externalId} label="plan" />
            <span className="font-mono text-[10px] text-ink-faint">
              {epic.rollup.done}/{epic.rollup.total} ({Math.round(epic.rollup.pct)}%)
            </span>
          </div>
        </div>
      }
      tabBar={
        <DetailTabs label="plan details tabs" tabs={tabs} active={activeTab} onTab={onTab} hrefFor={tabHref} />
      }
    >
      {activeTab === 'edit' ? (
        <DocEditor taskId={epic.taskId} path="README.md" version={null} onSaved={onDocChanged} />
      ) : activeTab === 'plan' ? (
        <PlanReadme epic={epic} />
      ) : activeTab === 'spec' ? (
        <PlanSpec epic={epic} />
      ) : activeTab === 'revisions' ? (
        <PlanRevisionsTab
          revisions={revisions}
          revisionsErr={revisionsErr}
          focusRevId={focusRevId}
          onChanged={onRevisionsChanged}
        />

      ) : (
        <PlanSummary epic={epic} resolvedSeqs={resolvedSeqs} />
      )}
    </DetailShell>
  );
}

const REVISION_STATUS_CHIP: Record<PlanRevision['status'], string> = {
  staged: 'border-amber/40 bg-amber/10 text-amber',
  applied: 'border-green/40 bg-green/10 text-green',
  rejected: 'border-red/40 bg-red/10 text-red',
  superseded: 'border-line text-ink-faint',
  failed: 'border-red/40 bg-red/10 text-red',
};

/** Revisions tab body: the open (staged) revision as a full diff review, and
 * below it the decided history — every row answers "was it manual or
 * automated?" with its `origin` and `decidedBy`, never only the open one. */
function PlanRevisionsTab({
  revisions,
  revisionsErr,
  focusRevId,
  onChanged,
}: {
  revisions: PlanRevision[] | null;
  revisionsErr: string | null;
  /** The linked revision (`/details/revisions/<revId>`): the staged one is
   * always shown as a full review; a decided row is expanded (reason unclamped)
   * and both are marked aria-current. */
  focusRevId: number | null;
  onChanged: () => void;
}): JSX.Element {
  if (revisionsErr !== null) return <ErrorBox message={revisionsErr} onRetry={onChanged} />;
  if (revisions === null) return <Loading label="revisions…" />;
  const staged = revisions.find((r) => r.status === 'staged');
  const decided = revisions.filter((r) => r.status !== 'staged');
  if (staged === undefined && decided.length === 0) {
    return (
      <div className="font-mono text-[11.5px] text-ink-faint">
        no revisions — &quot;Revise plan&quot; in the action row starts one
      </div>
    );
  }
  return (
    <div className="space-y-4">
      {staged !== undefined && (
        <div
          data-revision-id={staged.id}
          {...(staged.id === focusRevId ? { 'aria-current': 'true' as const } : {})}
        >
          <RailSection label="staged — awaiting your decision">
            <RevisionReview revisionId={staged.id} onDecided={onChanged} />
          </RailSection>
        </div>
      )}
      {decided.length > 0 && (
        <RailSection label="history">
          <ul className="space-y-2">
            {decided.map((r) => (
              <li
                key={r.id}
                data-revision-id={r.id}
                {...(r.id === focusRevId ? { 'aria-current': 'true' as const } : {})}
                className={`rounded-lg border bg-surface/40 px-3 py-2.5 ${
                  r.id === focusRevId ? 'border-brand/50' : 'border-line'
                }`}
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <span
                    className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${REVISION_STATUS_CHIP[r.status]}`}
                  >
                    {r.status}
                  </span>
                  <span className="rounded border border-line-strong bg-surface2 px-1.5 py-px font-mono text-[9.5px] text-ink-dim">
                    {ORIGIN_LABEL[r.origin]}
                  </span>
                  <span className="font-mono text-[10px] text-ink-faint">
                    {r.decidedAt !== undefined
                      ? `decided ${fmtDateTime(r.decidedAt)}${r.decidedBy !== undefined ? ` by ${r.decidedBy}` : ''}`
                      : `created ${fmtDateTime(r.createdAt)}`}
                  </span>
                </div>
                <div
                  className={`mt-1.5 text-[12px] leading-relaxed whitespace-pre-wrap text-ink-2 ${
                    r.id === focusRevId ? '' : 'line-clamp-3'
                  }`}
                >
                  {r.reason}
                </div>
                {r.error !== undefined && r.error !== '' && (
                  <div className="mt-1 font-mono text-[10.5px] break-words text-red">{r.error}</div>
                )}
                <div className="mt-1.5 font-mono text-[10px] text-ink-faint">
                  {r.files.length} doc{r.files.length === 1 ? '' : 's'}
                  {r.files.length > 0 && (
                    <>
                      {': '}
                      {r.files
                        .map((f) =>
                          f.action === 'rename' && f.renameFrom !== undefined
                            ? `${f.renameFrom} → ${f.docPath} (rename)`
                            : `${f.docPath} (${f.action})`,
                        )
                        .join(', ')}
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        </RailSection>
      )}
    </div>
  );
}

/** Plan tab body — the plan README markdown. */
function PlanReadme({ epic }: { epic: Epic }): JSX.Element {
  const [readme] = usePlanDoc(epic.taskId, 'README.md', null);
  if (readme === null) return <Loading label="readme…" />;
  return <Markdown text={readme} />;
}

/** Spec tab body — the rendered plan/spec.md under a coverage rail: progress
 * bar plus one row per criterion (tick glyph, cid chip, text, then the phase
 * chips whose `**Covers:**` line claims it — or an amber `uncovered`). Phase
 * `Covers` references to ids the spec never declared surface as amber notes
 * (speculation signal). The rail exists only when the scanner parsed criteria
 * rows (`epic.spec`); a spec.md without SC lines still renders as markdown. */
function PlanSpec({ epic }: { epic: Epic }): JSX.Element {
  const [doc] = usePlanDoc(epic.taskId, 'spec.md', null);
  const spec = epic.spec;
  // Tone the P{seq} chips like the phase rows do — the covering phase's own
  // derived status through `phaseChip`, so a done phase covers in green.
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const phaseBySeq = useMemo(
    () => new Map(epic.phases.map((p) => [p.seq, p])),
    [epic.phases],
  );
  return (
    <>
      {spec !== null && (
        <RailSection label="coverage">
          <ProgressBar done={spec.covered} total={spec.total} className="mb-2.5 max-w-[220px]" />
          <ul className="space-y-1.5">
            {spec.criteria.map((c) => (
              <li key={c.cid} className="flex flex-wrap items-start gap-2">
                <span
                  aria-hidden="true"
                  className={`mt-px shrink-0 font-mono text-[12px] ${
                    c.done ? 'text-green' : 'text-ink-faint'
                  }`}
                >
                  {c.done ? '✓' : '○'}
                </span>
                <span className="shrink-0 rounded border border-line bg-surface2 px-1.5 py-px font-mono text-[9.5px] text-ink-dim">
                  {c.cid}
                </span>
                <span className={`min-w-0 flex-1 ${c.done ? 'text-ink-dim' : 'text-ink'}`}>
                  {c.text}
                </span>
                {c.coveredBy.length > 0 ? (
                  c.coveredBy.map((seq) => {
                    const p = phaseBySeq.get(seq);
                    const cls =
                      p !== undefined
                        ? phaseChip(phaseStatus(p, resolvedSeqs), false).cls
                        : PHASE_CHIP.pending.cls;
                    return (
                      <span
                        key={seq}
                        data-tip={p !== undefined ? `covered by phase ${String(seq)} — ${p.name}` : undefined}
                        className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
                      >
                        P{seq}
                      </span>
                    );
                  })
                ) : (
                  <span
                    data-tip="no phase declares it covers this criterion"
                    className="shrink-0 rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
                  >
                    uncovered
                  </span>
                )}
              </li>
            ))}
          </ul>
          {spec.unknownRefs.length > 0 && (
            <div className="mt-2 space-y-0.5">
              {spec.unknownRefs.map((r) => (
                <div
                  key={`${String(r.seq)}-${r.cid}`}
                  className="font-mono text-[10.5px] text-amber"
                >
                  phase {r.seq} covers {r.cid}, which the spec never declares
                </div>
              ))}
            </div>
          )}
        </RailSection>
      )}
      {doc === null ? (
        <Loading label="spec…" />
      ) : spec !== null ? (
        <RailSection label="doc">
          <Markdown text={doc} />
        </RailSection>
      ) : (
        <Markdown text={doc} />
      )}
    </>
  );
}

/** Summary tab body (complete plans only): per-phase executed work — phase
 * title + the N/N score, and what the executor reported doing (Completion
 * Report and/or the `## Execution record` doc section). The criteria
 * themselves stay on the phase docs — a summary reports work, not contract.
 * Derived
 * client-side from the existing docs endpoint (one fetch per phase doc, plus
 * plan/SUMMARY.md when the executor wrote one) — no API extension needed. */
function PlanSummary({
  epic,
  resolvedSeqs,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
}): JSX.Element {
  const [docs, setDocs] = useState<Record<string, string> | null>(null);
  const [summaryMd, setSummaryMd] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setDocs(null);
    setSummaryMd(null);
    void Promise.all(
      epic.phases.map(async (p) => {
        try {
          const d = await fetchPlanDoc(epic.taskId, p.docRelPath);
          return [p.docRelPath, d.content] as const;
        } catch {
          return [p.docRelPath, ''] as const; // a missing doc degrades to DTO-only data
        }
      }),
    ).then((entries) => {
      if (alive) setDocs(Object.fromEntries(entries));
    });
    if (epic.hasSummary) {
      fetchPlanDoc(epic.taskId, 'SUMMARY.md')
        .then((d) => {
          if (alive) setSummaryMd(d.content);
        })
        .catch(() => undefined); // optional artifact — skip silently
    }
    return () => {
      alive = false;
    };
  }, [epic.taskId, epic.hasSummary, epic.phases]);

  if (docs === null) return <Loading label="summary…" />;

  return (
    <div className="space-y-4">
      {summaryMd !== null && (
        <RailSection label="plan summary">
          <Markdown text={summaryMd} />
        </RailSection>
      )}
      {epic.phases.map((p) => {
        const doc = docs[p.docRelPath] ?? '';
        const execRecord = extractSection(doc, 'Execution record');
        const status = phaseStatus(p, resolvedSeqs);
        return (
          <section key={p.id} className="rounded-lg border border-line bg-surface/40 px-3 py-2.5">
            <div className="flex items-center gap-2">
              <span className="shrink-0 font-mono text-[10px] text-ink-faint">Phase {p.seq}</span>
              <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">
                {p.name}
              </span>
              <span
                className={`shrink-0 font-mono text-[10px] ${
                  status === 'done' ? 'text-green' : 'text-ink-faint'
                }`}
              >
                {p.checkboxesDone}/{p.checkboxesTotal || 0}
              </span>
            </div>
            {p.completionReport !== null && (
              <div className="mt-2 border-t border-line pt-2">
                <div className="mb-1 font-mono text-[10px] uppercase tracking-wider text-ink-faint">
                  what was done
                </div>
                <Markdown text={p.completionReport} />
              </div>
            )}
            {execRecord !== null && (
              <div className="mt-2 border-t border-line pt-2">
                <div className="mb-1 font-mono text-[10px] uppercase tracking-wider text-ink-faint">
                  execution record
                </div>
                <Markdown text={execRecord} />
              </div>
            )}
            {p.completionReport === null && execRecord === null && (
              <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
                no summary written for this phase
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}

/** A thin progress bar. total===0 renders an empty track (no divide-by-zero). */
function ProgressBar({
  done,
  total,
  className = '',
}: {
  done: number;
  total: number;
  className?: string;
}): JSX.Element {
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div
      // Track must contrast with every card fill it sits on — the selected plan
      // card is itself bg-surface2, which made a bg-surface2 track invisible and
      // a partial fill read as a complete bar.
      className={`h-1.5 overflow-hidden rounded-full bg-line-strong/50 ${className}`}
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${done} of ${total} checkboxes done`}
    >
      <div className="h-full rounded-full bg-brand transition-[width]" style={{ width: `${String(pct)}%` }} />
    </div>
  );
}

/** ContinuationChip surfaces the run completion loop's decisions (`run_events`,
 * migration 0077) on the phase row.
 *
 * It renders NOTHING for a run that finished on its first turn, which is the
 * overwhelmingly common case — the chip has to earn its space. It appears
 * exactly when the harness had to intervene, because that is the fact `runState`
 * alone cannot carry: a `partial` phase nudged twice that progressed each time
 * and one that stalled on turn one read identically without it, and the
 * difference is what says whether the phase doc or the executor is at fault.
 *
 * The tooltip carries each continuation's message, so the operator can see what
 * the run was actually told rather than inferring it. */
function ContinuationChip({ events }: { events: RunEvent[] | null }): JSX.Element | null {
  // A daemon older than the [] guard sends null for an empty timeline.
  const continuations = (events ?? []).filter((e) => e.kind === 'continuation');
  if (continuations.length === 0) return null;
  const tip = continuations
    .map((e) => `#${e.attempt}: ${e.detail.split('\n')[0]}`)
    .join(' — ');
  return (
    <span
      className="inline-flex items-center gap-1 rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
      data-tip={`the run ended its turn with criteria unticked and was resumed ${continuations.length}× — ${tip}`}
      onClick={(e) => e.stopPropagation()}
    >
      ↻ {continuations.length}
    </span>
  );
}
