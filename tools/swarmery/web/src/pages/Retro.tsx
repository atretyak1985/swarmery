// Retro (retro improvement loop, phase 1): per-agent health scorecards over a
// local-day range with a previous-window comparison, a system health strip
// (orchestrator cost + total runs/errors), and a friction board — denied
// tools (with a one-click auto-approve rule), top error groups, and
// approval-wait stats. Data comes from /api/retro/{agents,friction}; range
// presets and project scope mirror Analytics.tsx.
//
// Phase 3 adds the advisor recommendations rail at the top: evidenced
// R1–R6 rule-engine proposals with Accept/Dismiss, lifecycle status chips
// (accepted → adopted → verified), an "Analyze now" trigger, and a lazily
// fetched Verified history section.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  AgentChangeProposal,
  Recommendation,
  RecommendationTargetKind,
  RetroAgentRow,
  RetroAgentsResp,
  RetroErrorGroup,
  RetroFrictionResp,
  RetroLesson,
  RetroLessonGroup,
  RetroTaskRow,
  Session,
} from '../api/types';
import {
  applyProposal,
  createApprovalRule,
  fetchFirstPassRates,
  fetchProposals,
  fetchRecommendations,
  fetchRetroAgents,
  fetchRetroFriction,
  fetchRetroLessonGroups,
  fetchRetroLessons,
  fetchRetroTasks,
  fetchSessions,
  fetchTrajectoryJudgments,
  type TrajectoryJudgment,
  patchProposal,
  patchRecommendation,
  retryProposal,
  runAdvise,
} from '../api';
import {
  addDays,
  fmtAgo,
  fmtCost,
  fmtDayShort,
  fmtDurationMs,
  fmtTokens,
  isoDay,
} from '../lib/format';
import { ScopeChip } from '../components/ScopeChip';
import { useScope } from '../lib/scope';
import { Explain } from '../components/Explain';
import { RetroImproveCard } from '../components/RetroImproveCard';
import { HowItWorks } from '../components/HowItWorks';
import { ApproxHint, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { ImproveModal } from '../components/ImproveModal';

const PRESETS = [7, 14, 30, 90] as const;

/* ----- range controls (Analytics preset row, without metric/pivot) ----- */

function RangeControls({
  preset,
  from,
  to,
  onPreset,
  onFrom,
  onTo,
}: {
  preset: number | null;
  from: string;
  to: string;
  onPreset: (n: number) => void;
  onFrom: (d: string) => void;
  onTo: (d: string) => void;
}): JSX.Element {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {/* Project scope leads the controls row (this page has no search box) —
          Retro's rollups are scoped by useScope().scope. */}
      <ScopeChip />
      <span className="mx-1 h-4 w-px bg-line" aria-hidden="true" />
      {PRESETS.map((n) => (
        <button
          key={n}
          type="button"
          onClick={() => onPreset(n)}
          className={`rounded-[7px] border px-[9px] py-[5px] font-mono text-[11px] transition-colors ${
            preset === n
              ? 'border-brand/40 bg-brand/10 text-brand'
              : 'border-line-strong text-ink-dim hover:text-ink'
          }`}
        >
          {n}d
        </button>
      ))}
      <span className="mx-1 h-4 w-px bg-line" aria-hidden="true" />
      <input
        type="date"
        value={from}
        max={to}
        onChange={(e) => onFrom(e.target.value)}
        className="rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-ink-dim"
      />
      <span className="font-mono text-[11px] text-ink-faint">→</span>
      <input
        type="date"
        value={to}
        min={from}
        max={isoDay()}
        onChange={(e) => onTo(e.target.value)}
        className="rounded-md border border-line bg-surface px-2 py-1 font-mono text-[11px] text-ink-dim"
      />
    </div>
  );
}

/* ----- recommendations rail (retro phase 3) ----- */

/** Distinct chip hue per rule, drawn from the existing palette. */
const RULE_HUES: Record<string, string> = {
  R1: 'border-blue/40 text-blue',
  R2: 'border-red/40 text-red',
  R3: 'border-amber/40 text-amber',
  R4: 'border-purple/40 text-purple',
  R5: 'border-green/40 text-green',
  R6: 'border-line-strong text-ink-2',
};

function ruleHue(rule: string): string {
  return RULE_HUES[rule] ?? 'border-line-strong text-ink-dim';
}

/** Display-only mirrors of the advisor engine's verification thresholds —
 * twin: internal/advisor/advisor.go VerifyAfterDays / VerifyImprovement,
 * keep in lockstep. */
const VERIFY_AFTER_DAYS = 7;
const VERIFY_IMPROVEMENT = 0.2;

/** Whole days until the verification window opens (≤0 = already open). */
function daysUntilVerify(anchor: string): number {
  const elapsedDays = (Date.now() - new Date(anchor).getTime()) / 86_400_000;
  return Math.ceil(VERIFY_AFTER_DAYS - elapsedDays);
}

/** Post-verify observations the advisor folds into the evidence JSON. */
function verifyObservation(ev: unknown): { note: string | null; postValue: number | null } {
  if (typeof ev !== 'object' || ev === null) return { note: null, postValue: null };
  const o = ev as Record<string, unknown>;
  const note = typeof o.note === 'string' ? o.note : null;
  const post = o.post_adoption;
  const v =
    typeof post === 'object' && post !== null
      ? (post as Record<string, unknown>).value
      : undefined;
  return { note, postValue: typeof v === 'number' ? v : null };
}

/** Compact metric value: 2 significant digits (rates like 0.43, 0.071). */
function fmtMetric(v: number): string {
  return String(Number(v.toPrecision(2)));
}

/** Metric-vs-baseline progress line for a rec whose verification clock is
 * running: baseline value → latest observed value (when a verify pass has
 * recorded one) and the ≥20%-better target the engine checks against. Every
 * rule metric improves downward except R6's cache hit rate. */
function VerifyProgress({ rec }: { rec: Recommendation }): JSX.Element | null {
  const b = rec.baseline;
  if (b === null || b.value === 0) return null;
  const { note, postValue } = verifyObservation(rec.evidence);
  const target =
    rec.rule === 'R6' ? b.value * (1 + VERIFY_IMPROVEMENT) : b.value * (1 - VERIFY_IMPROVEMENT);
  return (
    <span
      className="font-mono text-[10px] text-ink-faint"
      data-tip={`verified when ${b.metric} is ≥${String(VERIFY_IMPROVEMENT * 100)}% better than the baseline snapshot`}
    >
      {b.metric} {fmtMetric(b.value)}
      {postValue !== null ? ` → ${fmtMetric(postValue)}` : ''}
      {` (target ${rec.rule === 'R6' ? '≥' : '≤'}${fmtMetric(target)})`}
      {note === 'insufficient post-adoption traffic' ? ' · insufficient traffic so far' : ''}
    </span>
  );
}

/** Target kinds internal/advisor/advisor.go verify() reads from the `adopted`
 * status — these have a detectable adoption signal to wait for. Keep in
 * lockstep with that query's first `target_kind IN (…)` list. */
const ADOPTABLE_KINDS: readonly RecommendationTargetKind[] = ['agent', 'tool', 'process'];

/** Target kinds verify() reads straight from `accepted` — no adoption hop, so
 * the countdown runs from accepted_at. Lockstep with that query's second
 * `target_kind IN (…)` list. */
const ACCEPTED_VERIFY_KINDS: readonly RecommendationTargetKind[] = [
  'agent',
  'error_group',
  'config',
];

/** Lifecycle chip for in-flight statuses (accepted/adopted). The accepted copy
 * is per target kind, and the three cases are disjoint:
 *
 *  - ADOPTABLE_KINDS — verify() reads them from `adopted`, so the chip waits for
 *    the adoption signal;
 *  - ACCEPTED_VERIFY_KINDS minus those — verify() reads them straight from
 *    `accepted`, so the chip counts down to the verification check instead;
 *  - everything else (memory/project/session/skill) — verify() selects NEITHER
 *    status for these, so there is no terminal state to count down to and the
 *    chip says only `accepted`. R11's `skill` recs are deliberately in this
 *    bucket: absence of retrospectives is not evidence a lesson was absorbed
 *    (see internal/advisor/rules_lesson.go), so they close by an operator's
 *    accepted → dismissed and nothing else. Rendering a `verify check in 14d`
 *    over them would promise exactly the step that never happens.
 *
 * Adopted recs count down from the detected change. */
function RecStatusChip({ rec }: { rec: Recommendation }): JSX.Element | null {
  const countdown = (anchor: string): string => {
    const d = daysUntilVerify(anchor);
    return d > 0
      ? `verify check in ${String(d)}d`
      : `awaiting ≥${String(VERIFY_IMPROVEMENT * 100)}% improvement`;
  };
  if (rec.status === 'accepted') {
    const kind = rec.target_kind;
    const adoptable = ADOPTABLE_KINDS.includes(kind);
    const anchor = rec.baseline?.accepted_at;
    const showCountdown = !adoptable && ACCEPTED_VERIFY_KINDS.includes(kind) && anchor !== undefined;
    return (
      <>
        <span className="rounded-[7px] border border-amber/40 bg-amber/10 px-1.5 py-[2px] font-mono text-[10px] text-amber">
          {adoptable
            ? 'accepted — waiting for adoption'
            : showCountdown
              ? `accepted — ${countdown(anchor)}`
              : 'accepted'}
        </span>
        {showCountdown && <VerifyProgress rec={rec} />}
      </>
    );
  }
  if (rec.status === 'adopted') {
    const anchor = rec.baseline?.adopted_at;
    return (
      <>
        <span className="rounded-[7px] border border-blue/40 bg-blue/10 px-1.5 py-[2px] font-mono text-[10px] text-blue">
          {anchor !== undefined
            ? `change detected — ${countdown(anchor)}`
            : 'change detected — verifying'}
        </span>
        {anchor !== undefined && <VerifyProgress rec={rec} />}
      </>
    );
  }
  return null;
}

function RecCard({
  rec,
  busy,
  onAction,
}: {
  rec: Recommendation;
  busy: boolean;
  onAction: (id: number, status: 'accepted' | 'dismissed') => void;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span
          className={`rounded-[7px] border px-1.5 py-[2px] font-mono text-[10px] font-medium ${ruleHue(rec.rule)}`}
        >
          {rec.rule}
        </span>
        <span className="min-w-0 flex-1 font-mono text-[12.5px] font-medium text-ink">
          {rec.title}
        </span>
        <span className="font-mono text-[10px] text-ink-faint">{fmtAgo(rec.updated_at)}</span>
      </div>
      <p className="mt-1.5 font-mono text-[10.5px] leading-relaxed text-ink-3">{rec.detail}</p>
      <div className="mt-2 flex flex-wrap items-center gap-1.5">
        <RecStatusChip rec={rec} />
        {rec.status === 'proposed' && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onAction(rec.id, 'accepted')}
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-green/40 hover:text-green disabled:opacity-50"
          >
            {busy ? '…' : 'Accept'}
          </button>
        )}
        {(rec.status === 'proposed' || rec.status === 'accepted') && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onAction(rec.id, 'dismissed')}
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-red/40 hover:text-red disabled:opacity-50"
          >
            {busy ? '…' : 'Dismiss'}
          </button>
        )}
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          className="ml-auto font-mono text-[10px] text-ink-faint transition-colors hover:text-ink"
        >
          {open ? '▾ evidence' : '▸ evidence'}
        </button>
      </div>
      {open && (
        <pre className="mt-2 overflow-x-auto rounded-[10px] border border-line bg-field px-3 py-2 font-mono text-[10px] whitespace-pre-wrap text-ink-dim">
          {JSON.stringify(rec.evidence, null, 2)}
        </pre>
      )}
    </div>
  );
}

function RecommendationsRail(): JSX.Element | null {
  const [recs, setRecs] = useState<Recommendation[] | null>(null);
  const [verified, setVerified] = useState<Recommendation[] | null>(null);
  const [verifiedOpen, setVerifiedOpen] = useState(false);
  const [analyzing, setAnalyzing] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  // In-flight rec ids: the ref is the synchronous double-submit guard, the
  // state mirror drives rendering (the friction board's +rule Set pattern).
  const inflight = useRef<Set<number>>(new Set());
  const [busy, setBusy] = useState<ReadonlySet<number>>(new Set());

  const load = useCallback((): void => {
    fetchRecommendations()
      .then((r) => setRecs(r.recommendations))
      .catch(() => setRecs(null)); // endpoint unavailable → hide the rail
  }, []);
  useEffect(load, [load]);

  // Both terminal endings, in one place. `verified` is an improvement measured
  // against a baseline; `resolved` is the advisor closing a row because its
  // condition stopped reproducing. Showing only the first left the second
  // invisible — a finding that got FIXED simply vanished from the page, which is
  // how a screen full of open recommendations came to look like nothing had been
  // done.
  const loadVerified = useCallback((): void => {
    fetchRecommendations('verified,resolved')
      .then((r) => setVerified(r.recommendations))
      .catch(() => setVerified([]));
  }, []);

  const onAction = useCallback(
    (id: number, status: 'accepted' | 'dismissed'): void => {
      if (inflight.current.has(id)) return;
      inflight.current.add(id);
      setBusy(new Set(inflight.current));
      setFailed(null);
      patchRecommendation(id, status)
        .then((updated) => {
          setRecs((prev) => {
            if (prev === null) return prev;
            if (updated.status === 'dismissed') return prev.filter((r) => r.id !== id);
            return prev.map((r) => (r.id === id ? updated : r));
          });
        })
        .catch((e: unknown) => {
          setFailed(String(e));
        })
        .finally(() => {
          inflight.current.delete(id);
          setBusy(new Set(inflight.current));
        });
    },
    [],
  );

  const analyze = useCallback((): void => {
    setAnalyzing(true);
    setFailed(null);
    runAdvise()
      .then(() => {
        load();
        if (verifiedOpen) loadVerified();
      })
      .catch((e: unknown) => {
        setFailed(String(e));
      })
      .finally(() => setAnalyzing(false));
  }, [load, loadVerified, verifiedOpen]);

  if (recs === null) return null;

  return (
    <section className="mt-[18px]">
      <div className="flex items-baseline gap-2">
        <div className="font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
          Recommendations
        </div>
        <Explain id="retro-recommendations" />
        <span className="ml-auto flex items-baseline gap-1.5">
          <Explain id="retro-analyze-button" />
          <button
            type="button"
            disabled={analyzing}
            onClick={analyze}
            data-tip="deterministic: re-runs the local rule engine, calls no model"
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50"
          >
            {analyzing ? 'analyzing…' : 'Analyze now'}
          </button>
        </span>
      </div>
      <div className="mt-2 flex flex-col gap-2.5">
        {recs.length === 0 ? (
          <Empty>no open recommendations — the advisor found nothing to flag</Empty>
        ) : (
          recs.map((rec) => (
            <RecCard key={rec.id} rec={rec} busy={busy.has(rec.id)} onAction={onAction} />
          ))
        )}
        {failed !== null && <div className="font-mono text-[10.5px] text-red">{failed}</div>}
        <div>
          <button
            type="button"
            aria-expanded={verifiedOpen}
            onClick={() => {
              setVerifiedOpen((o) => !o);
              if (verified === null) loadVerified();
            }}
            className="font-mono text-[10.5px] text-ink-faint transition-colors hover:text-ink"
          >
            {verifiedOpen ? '▾' : '▸'} Closed
            {verified !== null ? ` (${String(verified.length)})` : ''}
          </button>
          {verifiedOpen && verified !== null && (
            <div className="mt-2 flex flex-col gap-1.5">
              {verified.length === 0 ? (
                <Empty>nothing closed yet</Empty>
              ) : (
                verified.map((rec) => (
                  <div
                    key={rec.id}
                    className="flex items-baseline gap-2 rounded-[10px] border border-line px-3.5 py-2 font-mono text-[11px]"
                  >
                    <span
                      className={`rounded-[7px] border px-1.5 py-[2px] text-[10px] ${ruleHue(rec.rule)}`}
                    >
                      {rec.rule}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-ink-3">{rec.title}</span>
                    <span className="text-green">✓ verified</span>
                    <span className="text-ink-faint">{fmtAgo(rec.updated_at)}</span>
                  </div>
                ))
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

/* ----- agent change proposals (self-improvement phase 4) ----- */

const PROPOSAL_STATUS_HUE: Record<AgentChangeProposal['status'], string> = {
  proposed: 'border-line-strong text-ink-dim',
  approved: 'border-amber/40 text-amber',
  applied: 'border-green/40 text-green',
  rejected: 'border-line-strong text-ink-faint',
  failed: 'border-red/40 text-red',
  needs_target: 'border-amber/40 text-amber',
};

/**
 * The file a proposal edits. An agent proposal carries it in agent_path, a
 * skill proposal in target_path, and a routed skill lesson that resolved to no
 * SKILL.md carries neither — which is the state the operator has to resolve, so
 * it says so rather than rendering an empty cell.
 *
 * The copy offers only what the row can actually do. There is no target picker
 * in this phase: PATCH accepts approved|rejected, and legalProposalTransition
 * refuses approve on a needs_target row, so Dismiss is the single available
 * transition — the same thing docs/retro.md states ("Dismissing is the only
 * transition it has"). Offering "pick a skill" pointed at a control that does
 * not exist.
 */
function proposalTarget(p: AgentChangeProposal): string {
  const path = p.target_kind === 'skill' ? p.target_path : p.agent_path;
  if (path !== '') return path;
  return 'no target file — dismissing is the only transition';
}

function ProposalStatusChip({ status }: { status: AgentChangeProposal['status'] }): JSX.Element {
  return (
    <span
      className={`rounded-[7px] border px-1.5 py-[2px] font-mono text-[10px] font-medium ${PROPOSAL_STATUS_HUE[status]}`}
    >
      {status}
    </span>
  );
}

/** Colorized unified diff — a lightweight +/- <pre>, no external dep. */
function DiffView({ diff }: { diff: string }): JSX.Element {
  const lines = diff.replace(/\n$/, '').split('\n');
  return (
    <pre className="mt-2 max-h-96 overflow-auto rounded-[10px] border border-line bg-field px-3 py-2 font-mono text-[10px] leading-relaxed">
      {lines.map((line, i) => {
        let cls = 'text-ink-dim';
        if (line.startsWith('+') && !line.startsWith('+++')) cls = 'text-green';
        else if (line.startsWith('-') && !line.startsWith('---')) cls = 'text-red';
        else if (line.startsWith('@@')) cls = 'text-amber';
        else if (line.startsWith('diff ') || line.startsWith('+++') || line.startsWith('---'))
          cls = 'text-ink-faint';
        return (
          <div key={i} className={`whitespace-pre-wrap ${cls}`}>
            {line || ' '}
          </div>
        );
      })}
    </pre>
  );
}

const GUARDRAIL_TEXT =
  'Approving applies this diff on a fresh branch behind hard guardrails ' +
  '(neutrality scan clean, agent frontmatter present, ≤120 changed lines) ' +
  'and opens a PR via gh. It never auto-merges. Continue?';

function ProposalDetail({
  p,
  busy,
  onDecide,
  onRetry,
  onReapply,
}: {
  p: AgentChangeProposal;
  busy: boolean;
  onDecide: (id: number, status: 'approved' | 'rejected') => void;
  onRetry: (id: number) => void;
  onReapply: (id: number) => void;
}): JSX.Element {
  return (
    <div className="mt-2 border-t border-line pt-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[10px] text-ink-faint">
        <span className="truncate" data-tip-mono data-tip={proposalTarget(p)}>
          {proposalTarget(p)}
        </span>
        {p.recommendation_id !== null && (
          <span data-tip="source recommendation">from recommendation #{p.recommendation_id}</span>
        )}
        <span>created {fmtAgo(p.created_at)}</span>
      </div>

      {p.rationale !== '' && (
        <p className="mt-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-3">
          {p.rationale}
        </p>
      )}

      {p.diff !== '' && <DiffView diff={p.diff} />}

      {p.error !== null && (
        <p className="mt-2 font-mono text-[10.5px] leading-relaxed text-red">{p.error}</p>
      )}

      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        {p.status === 'proposed' && (
          <>
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                if (window.confirm(GUARDRAIL_TEXT)) onDecide(p.id, 'approved');
              }}
              className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-green/40 hover:text-green disabled:opacity-50"
            >
              {busy ? '…' : 'Approve'}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => onDecide(p.id, 'rejected')}
              className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-red/40 hover:text-red disabled:opacity-50"
            >
              {busy ? '…' : 'Reject'}
            </button>
          </>
        )}
        {p.status === 'needs_target' && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onDecide(p.id, 'rejected')}
            data-tip="no SKILL.md was resolved from the lesson; dismissing frees the slot so the rule can re-propose"
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-red/40 hover:text-red disabled:opacity-50"
          >
            {busy ? '…' : 'Dismiss'}
          </button>
        )}
        {p.status === 'failed' && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onRetry(p.id)}
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-ink/40 hover:text-ink disabled:opacity-50"
          >
            {busy ? '…' : 'Retry'}
          </button>
        )}
        {p.status === 'approved' && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onReapply(p.id)}
            data-tip="re-run the apply/PR pipeline (e.g. after a gh outage)"
            className="rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-amber/40 hover:text-amber disabled:opacity-50"
          >
            {busy ? '…' : 'Re-run apply'}
          </button>
        )}
        {p.pr_url !== null && (
          <a
            href={p.pr_url}
            target="_blank"
            rel="noreferrer"
            className="ml-auto font-mono text-[10.5px] text-green transition-colors hover:underline"
          >
            open PR ↗
          </a>
        )}
      </div>
    </div>
  );
}

function ProposalCard({
  p,
  busy,
  onDecide,
  onRetry,
  onReapply,
}: {
  p: AgentChangeProposal;
  busy: boolean;
  onDecide: (id: number, status: 'approved' | 'rejected') => void;
  onRetry: (id: number) => void;
  onReapply: (id: number) => void;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="min-w-0 flex-1 font-mono text-[12.5px] font-medium text-ink">
          {p.agent}
        </span>
        {p.target_kind === 'skill' && (
          <span
            data-tip="a SKILL.md procedure edit, routed from a recurring retrospective lesson"
            className="rounded-[7px] border border-line-strong px-1.5 py-[2px] font-mono text-[10px] font-medium text-ink-dim"
          >
            skill
          </span>
        )}
        <ProposalStatusChip status={p.status} />
        <span className="font-mono text-[10px] text-ink-faint">{fmtAgo(p.created_at)}</span>
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          className="font-mono text-[10px] text-ink-faint transition-colors hover:text-ink"
        >
          {open ? '▾ diff' : '▸ diff'}
        </button>
      </div>
      {open && (
        <ProposalDetail
          p={p}
          busy={busy}
          onDecide={onDecide}
          onRetry={onRetry}
          onReapply={onReapply}
        />
      )}
    </div>
  );
}

/** Proposals section: list of agent-rewrite proposals with the human gate. */
function ProposalsRail({ reloadKey }: { reloadKey: number }): JSX.Element | null {
  const [proposals, setProposals] = useState<AgentChangeProposal[] | null>(null);
  const [busy, setBusy] = useState<ReadonlySet<number>>(new Set());
  const [failed, setFailed] = useState<string | null>(null);

  const load = useCallback((): void => {
    fetchProposals()
      .then((r) => setProposals(r.proposals))
      .catch(() => setProposals(null));
  }, []);
  useEffect(load, [load, reloadKey]);

  const withBusy = useCallback(
    (id: number, run: () => Promise<void>): void => {
      setBusy((s) => new Set(s).add(id));
      setFailed(null);
      run()
        .then(() => {
          // Give the async apply/generate a beat to land, then refetch.
          setTimeout(load, 500);
        })
        .catch((e: unknown) => setFailed(String(e)))
        .finally(() =>
          setBusy((s) => {
            const next = new Set(s);
            next.delete(id);
            return next;
          }),
        );
    },
    [load],
  );

  const onDecide = useCallback(
    (id: number, status: 'approved' | 'rejected') =>
      withBusy(id, () => patchProposal(id, status)),
    [withBusy],
  );
  const onRetry = useCallback((id: number) => withBusy(id, () => retryProposal(id)), [withBusy]);
  const onReapply = useCallback((id: number) => withBusy(id, () => applyProposal(id)), [withBusy]);

  if (proposals !== null && proposals.length === 0) return null;

  return (
    <section className="mt-[18px]">
      <div className="mb-2 flex items-center gap-2">
        <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
          Agent proposals
        </span>
        <Explain id="retro-proposals" />
        {failed !== null && <span className="font-mono text-[10px] text-red">{failed}</span>}
      </div>
      {proposals === null ? (
        <Empty>no proposals</Empty>
      ) : (
        <div className="grid gap-3.5 sm:grid-cols-2">
          {proposals.map((p) => (
            <ProposalCard
              key={p.id}
              p={p}
              busy={busy.has(p.id)}
              onDecide={onDecide}
              onRetry={onRetry}
              onReapply={onReapply}
            />
          ))}
        </div>
      )}
    </section>
  );
}

/* ----- LLM-judge trajectory panels (verification contour phase 2) ----- */

/** Score bar: n/5 filled segments. Higher = better for all dims. */
function ScoreBar({ value }: { value: number }): JSX.Element {
  return (
    <span className="inline-flex gap-[2px]" aria-hidden="true">
      {[1, 2, 3, 4, 5].map((i) => (
        <span
          key={i}
          className={`inline-block h-1.5 w-2.5 rounded-[2px] ${
            i <= value ? 'bg-brand' : 'bg-line-strong'
          }`}
        />
      ))}
    </span>
  );
}

const JUDGMENT_DIMS: [keyof Pick<TrajectoryJudgment, 'endResult' | 'instructionCompliance' | 'pitfalls' | 'toolCalls'>, string][] = [
  ['endResult', 'End result'],
  ['instructionCompliance', 'Instructions'],
  ['pitfalls', 'Pitfalls'],
  ['toolCalls', 'Tool use'],
];

/** Expanded judgment detail: score bars + review text for pre-fetched rows. */
function JudgmentPanel({ judgments }: { judgments: TrajectoryJudgment[] }): JSX.Element {
  return (
    <div className="mt-2.5 border-t border-line pt-2.5 flex flex-col gap-2.5">
      {judgments.map((j) => (
        <div key={`${j.agent}:${j.model}`}>
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 font-mono text-[10px] text-ink-faint">
            <span className="font-medium text-ink-3">{j.agent}</span>
            <span>·</span>
            <span>judge {j.model}</span>
            <span>·</span>
            <span className="text-brand font-medium">{j.overall.toFixed(1)}/5</span>
          </div>
          <div className="mt-1.5 grid grid-cols-2 gap-x-4 gap-y-1.5">
            {JUDGMENT_DIMS.map(([key, label]) => (
              <div key={key} className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
                <span className="w-[88px] shrink-0">{label}</span>
                <ScoreBar value={j[key]} />
                <span className="text-ink-faint">{j[key]}/5</span>
              </div>
            ))}
          </div>
          {j.review !== '' && (
            <p className="mt-1.5 font-mono text-[10.5px] leading-relaxed text-ink-3">{j.review}</p>
          )}
        </div>
      ))}
    </div>
  );
}

/** Row of a judged session in the Retro judgments section: chip in the
 * header, expands into the full panel on demand. Judgments are pre-fetched
 * by the parent section, so the row is purely presentational. */
function JudgedSessionRow({
  session,
  judgments,
}: {
  session: Session;
  judgments: TrajectoryJudgment[];
}): JSX.Element {
  const [open, setOpen] = useState(false);

  const overall = judgments.reduce((s, j) => s + j.overall, 0) / judgments.length;

  return (
    <div className="rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink-3">
          {session.title ?? session.sessionUuid.slice(0, 16)}
        </span>
        <span
          data-tip="LLM-judge trajectory score"
          className="rounded-[7px] border border-brand/40 bg-brand/10 px-1.5 py-[2px] font-mono text-[10px] text-brand"
        >
          judged · {overall.toFixed(1)}
        </span>
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          className="font-mono text-[10px] text-ink-faint transition-colors hover:text-ink"
        >
          {open ? '▾ judgment' : '▸ judgment'}
        </button>
      </div>
      <div className="mt-0.5 font-mono text-[10px] text-ink-faint">
        {session.projectName ?? session.projectSlug} · {session.startedAt.slice(0, 10)}
      </div>
      {open && <JudgmentPanel judgments={judgments} />}
    </div>
  );
}

/** Section that surfaces recent sessions with LLM-judge verdicts.
 * Fetches the last 20 completed sessions, then their judgments, and keeps
 * only judged sessions — the whole section (heading included) renders null
 * until at least one verdict exists. Advisory — no loading spinners or
 * error banners. */
function JudgmentsSection({ project }: { project?: string }): JSX.Element | null {
  const [judged, setJudged] = useState<{ session: Session; judgments: TrajectoryJudgment[] }[]>([]);

  useEffect(() => {
    let live = true;
    setJudged([]); // drop the previous scope's rows while the refetch is in flight
    fetchSessions(
      { status: 'completed', ...(project !== undefined ? { project } : {}) },
      { limit: 20 },
    )
      .then(async (r) => {
        const rows = await Promise.all(
          r.sessions.map(async (session) => ({
            session,
            judgments: await fetchTrajectoryJudgments(session.id).catch(
              () => [] as TrajectoryJudgment[],
            ),
          })),
        );
        if (live) setJudged(rows.filter((row) => row.judgments.length > 0));
      })
      .catch(() => {});
    return () => { live = false; };
  }, [project]);

  if (judged.length === 0) return null;

  return (
    <section className="mt-[18px]">
      <div className="mb-2 flex items-baseline gap-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
        <span>Trajectory judgments</span>
        <Explain id="retro-judgments" />
      </div>
      <div className="flex flex-col gap-2.5">
        {judged.map(({ session, judgments }) => (
          <JudgedSessionRow key={session.id} session={session} judgments={judgments} />
        ))}
      </div>
    </section>
  );
}

/* ----- health strip ----- */

/** vs-prev arrow: `up` colors follow "up is costly" unless `goodUp`.
 * `fmt` renders the prev value in the tooltip (defaults to a plain count;
 * pass `fmtCost` for dollar values). */
function DeltaArrow({
  cur,
  prev,
  goodUp = false,
  fmt = String,
}: {
  cur: number;
  prev: number;
  goodUp?: boolean;
  fmt?: (n: number) => string;
}): JSX.Element | null {
  if (prev === cur) return null;
  const up = cur > prev;
  const cls = up ? (goodUp ? 'text-green' : 'text-red') : goodUp ? 'text-ink-dim' : 'text-green';
  return (
    <span className={`font-mono text-[12px] ${cls}`} data-tip={`prev window: ${fmt(prev)}`}>
      {up ? '↑' : '↓'}
    </span>
  );
}

function StatCard({
  label,
  value,
  sub,
  arrow,
}: {
  label: string;
  value: string;
  sub?: string;
  arrow?: JSX.Element | null;
}): JSX.Element {
  return (
    <div className="rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">{label}</div>
      <div className="mt-1 flex items-baseline gap-1.5 font-display text-[18px] font-semibold text-ink">
        {value}
        {arrow}
      </div>
      {sub !== undefined && <div className="mt-0.5 font-mono text-[10.5px] text-ink-dim">{sub}</div>}
    </div>
  );
}

/** Shared KPI derivations extracted from the old HealthStrip — consumed by RetroLeadCard. */
function computeRetroKpis(data: RetroAgentsResp) {
  const totalRuns = data.agents.reduce((a, r) => a + r.runs, 0);
  const totalErrors = data.main.errors + data.agents.reduce((a, r) => a + r.errors, 0);
  // The contract carries no prev for main, so vs-prev totals cover subagents.
  const prevRuns = data.agents.reduce((a, r) => a + r.prev.runs, 0);
  const prevErrors = data.agents.reduce((a, r) => a + r.prev.errors, 0);
  const prevCost = data.agents.reduce((a, r) => a + r.prev.cost_usd, 0);
  const agentCost = data.agents.reduce((a, r) => a + r.cost_usd, 0);
  return { totalRuns, totalErrors, prevRuns, prevErrors, prevCost, agentCost };
}

/** Editorial lead card — Fraunces headline + sub on the left, KPI cluster on the right. */
function RetroLeadCard({ data }: { data: RetroAgentsResp }): JSX.Element {
  const { totalRuns, totalErrors, prevRuns, prevErrors, prevCost, agentCost } =
    computeRetroKpis(data);

  // Synthesize headline copy from the data.
  //
  // The two numbers are DIFFERENT UNITS and must never be phrased as a ratio.
  // `totalRuns` counts subagent runs; `totalErrors` counts error EVENTS across
  // the orchestrator and every agent, and one run can raise a dozen. The old
  // copy read "shipped 15 runs — 179 needed a human rescue", which is not just
  // wrong but impossible on its face, and it was the first line on the page.
  //
  // "Rescue" was wrong too: an error event is a tool call that failed, which the
  // agent usually retries on its own. Nobody was necessarily rescued.
  const headline =
    totalErrors > 0
      ? `${String(totalRuns)} agent ${totalRuns === 1 ? 'run' : 'runs'} in this window, and ${String(totalErrors)} error ${totalErrors === 1 ? 'event' : 'events'} across them and the orchestrator.`
      : `${String(totalRuns)} agent ${totalRuns === 1 ? 'run' : 'runs'} in this window, with no errors logged.`;
  const sub = `Orchestrator ${fmtCost(data.main.cost_usd)} · ${fmtTokens(data.main.tokens_out)} tokens out · agents ${fmtCost(agentCost)}`;

  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-7 gap-y-4 rounded-[14px] border border-line bg-surface px-5 py-4">
      {/* left — editorial copy */}
      <div className="min-w-0 flex-1 basis-80">
        <p className="font-display text-[20px] font-medium leading-[1.3] tracking-[-0.01em] text-ink text-balance">
          {headline} <Explain id="retro-kpis" />
        </p>
        <p className="mt-1.5 font-mono text-[10.5px] text-ink-3">{sub}</p>
      </div>

      {/* right — KPI cluster */}
      <div className="flex flex-wrap gap-[22px]">
        {/* Cost */}
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            Agent cost
          </div>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="font-display text-[18px] font-semibold text-ink">
              {fmtCost(agentCost)}
            </span>
            <DeltaArrow cur={agentCost} prev={prevCost} fmt={fmtCost} />
          </div>
          <div className="mt-0.5 font-mono text-[9.5px] text-ink-faint">
            prev {fmtCost(prevCost)}
          </div>
        </div>

        {/* Runs */}
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            Agent runs
          </div>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="font-display text-[18px] font-semibold text-ink">
              {String(totalRuns)}
            </span>
            <DeltaArrow cur={totalRuns} prev={prevRuns} goodUp />
          </div>
          <div className="mt-0.5 font-mono text-[9.5px] text-ink-faint">
            prev {String(prevRuns)}
          </div>
        </div>

        {/* Errors */}
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            Errors
          </div>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="font-display text-[18px] font-semibold text-ink">
              {String(totalErrors)}
            </span>
            <DeltaArrow cur={totalErrors} prev={prevErrors} />
          </div>
          <div className="mt-0.5 font-mono text-[9.5px] text-ink-faint">
            prev {String(prevErrors)}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ----- scorecard grid ----- */

function errRateClass(rate: number): string {
  if (rate > 0.2) return 'text-red';
  if (rate > 0.1) return 'text-amber';
  return 'text-ink-dim';
}

/** Fold errors_by_class into the three rendered buckets (absent → zeroes). */
function errClassSplit(byClass: Record<string, number> | undefined): {
  behavior: number;
  harness: number;
  infra: number;
} {
  return {
    behavior: byClass?.['behavior_fixable'] ?? 0,
    harness: byClass?.['harness_recoverable'] ?? 0,
    infra: byClass?.['infra_noise'] ?? 0,
  };
}

function runsDelta(row: RetroAgentRow): string {
  const d = row.runs - row.prev.runs;
  if (d === 0) return '';
  return d > 0 ? ` +${String(d)}` : ` ${String(d)}`;
}

function Scorecard({
  row,
  trajectoryKinds,
  onImprove,
}: {
  row: RetroAgentRow;
  trajectoryKinds: string[];
  onImprove: (row: RetroAgentRow) => void;
}): JSX.Element {
  const split = errClassSplit(row.errors_by_class);
  return (
    <div className="rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex items-baseline gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] font-medium text-ink">
          {row.agent}
        </span>
        {row.improvable === true && (
          <button
            type="button"
            onClick={() => onImprove(row)}
            data-tip="preview the evidence, then generate a minimal diff to THIS agent’s definition file only"
            className="rounded-[7px] border border-line-strong px-1.5 py-[2px] font-mono text-[10px] text-ink-dim transition-colors hover:border-green/40 hover:text-green"
          >
            Improve
          </button>
        )}
        <span
          className={`font-mono text-[11px] ${errRateClass(row.error_rate)}`}
          data-tip={`share of runs with ≥1 behavior-fixable error (${String(row.errors)} error events total)`}
        >
          {(row.error_rate * 100).toFixed(1)}% err
        </span>
      </div>
      {row.errors > 0 && row.errors_by_class && (
        <div
          className="mt-1 font-mono text-[10px] text-ink-faint"
          data-tip="error events by class — behavior: prompt-fixable agent behavior · harness: harness rule hit, self-recovered · infra: network/API noise (not the agent's fault)"
        >
          <span className={split.behavior > 0 ? 'text-amber' : ''}>behavior {split.behavior}</span>
          {' · '}
          <span>harness {split.harness}</span>
          {' · '}
          <span>infra {split.infra}</span>
        </div>
      )}
      <div className="mt-2 flex items-baseline gap-1.5">
        <span className="font-display text-[20px] font-semibold text-ink">{row.runs}</span>
        <span className="font-mono text-[10.5px] text-ink-dim">
          runs{runsDelta(row)}
          {row.runs !== row.prev.runs && (
            <span className="text-ink-faint"> vs prev {String(row.prev.runs)}</span>
          )}
        </span>
      </div>
      <div className="mt-2.5 grid grid-cols-2 gap-x-3 gap-y-1 font-mono text-[10.5px] text-ink-dim">
        <span>
          success{' '}
          <b className="font-medium text-ink-2">
            {row.success_rate !== null ? `${String(Math.round(row.success_rate * 100))}%` : '—'}
          </b>
        </span>
        <span>
          cost <b className="font-medium text-ink-2">{fmtCost(row.cost_usd)}</b>
        </span>
        <span>
          p95 <b className="font-medium text-ink-2">{row.p95_ms !== null ? fmtDurationMs(row.p95_ms) : '—'}</b>
        </span>
        <span>
          sessions <b className="font-medium text-ink-2">{row.sessions}</b>
        </span>
      </div>
      {(row.re_dispatch_rate !== null || row.eval !== null || trajectoryKinds.length > 0) && (
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          {row.re_dispatch_rate !== null && (
            <span
              data-tip="redispatch-classified ledger rows / total delegations in range"
              className={`rounded-[7px] border px-1.5 py-[2px] font-mono text-[10px] ${
                row.re_dispatch_rate > 0.25
                  ? 'border-red/40 text-red'
                  : 'border-line-strong text-ink-dim'
              }`}
            >
              re-dispatch {String(Math.round(row.re_dispatch_rate * 100))}%
            </span>
          )}
          {row.eval !== null && (
            <span
              data-tip={`latest eval run, finished ${row.eval.finished_at}`}
              className="rounded-[7px] border border-line-strong px-1.5 py-[2px] font-mono text-[10px] text-ink-dim"
            >
              evals {String(row.eval.passed)}/{String(row.eval.passed + row.eval.failed)}
            </span>
          )}
          {trajectoryKinds.map((kind) => (
            <span
              key={kind}
              data-tip={`trajectory anti-pattern detected: ${kind}`}
              className="rounded-[7px] border border-amber/40 px-1.5 py-[2px] font-mono text-[10px] text-amber"
            >
              {kind}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

/* ----- lessons feed (retro phase 2) ----- */

/** Both feeds are `null` while in flight and `*Failed` when their fetch threw —
 * kept apart because conflating them renders a failure as "nothing here", which
 * is the one reading an operator cannot act on. */
function LessonsFeed({
  lessons,
  groups,
  lessonsFailed,
  groupsFailed,
}: {
  lessons: RetroLesson[] | null;
  groups: RetroLessonGroup[] | null;
  lessonsFailed: boolean;
  groupsFailed: boolean;
}): JSX.Element {
  const [filter, setFilter] = useState('');
  // Grouping is a view of the same window, not a different dataset. It stays off
  // by default: the flat feed is the one that reads chronologically, and the
  // grouped one is what you switch to when you suspect a repeat.
  const [grouped, setGrouped] = useState(false);
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (lessons === null) return [];
    if (q === '') return lessons;
    return lessons.filter((l) =>
      [l.title, l.action ?? '', l.body ?? '', l.task_external_id, l.task_title]
        .join('\n')
        .toLowerCase()
        .includes(q),
    );
  }, [lessons, filter]);
  const visibleGroups = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (groups === null) return [];
    if (q === '') return groups;
    return groups.filter((g) =>
      [g.title, g.norm_title, g.latest_action ?? '', ...g.tasks]
        .join('\n')
        .toLowerCase()
        .includes(q),
    );
  }, [groups, filter]);

  const toggle = (
    <label className="flex items-center gap-1.5 font-mono text-[11px] text-ink-dim">
      {/* Never disabled. `disabled={groups === null}` locked the toggle in BOTH
          directions, so a grouped fetch that failed after the operator had
          switched the view on stranded them in it: an error rendered as
          emptiness, over a flat feed that had data and no way back to it. */}
      <input
        type="checkbox"
        checked={grouped}
        onChange={(e) => setGrouped(e.target.checked)}
        aria-label="group by lesson"
        className="accent-brand"
      />
      Group by lesson
    </label>
  );

  const controls = (
    <div className="flex flex-wrap items-center gap-3">
      <input
        type="search"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        placeholder={grouped ? 'filter lesson groups…' : 'filter lessons…'}
        aria-label="filter lessons"
        className="w-full max-w-xs rounded-md border border-line bg-surface px-2.5 py-1.5 font-mono text-[11px] text-ink placeholder:text-ink-faint"
      />
      {toggle}
    </div>
  );

  if (grouped) {
    return (
      <div className="flex flex-col gap-2.5">
        {controls}
        {groupsFailed ? (
          <ErrorBox message="lesson groups failed to load for this range — untick “Group by lesson” for the flat feed" />
        ) : groups === null ? (
          <Loading label="lesson groups…" />
        ) : groups.length === 0 ? (
          <Empty>no retrospective lessons in this range</Empty>
        ) : visibleGroups.length === 0 ? (
          <Empty>no lessons match “{filter}”</Empty>
        ) : (
          visibleGroups.map((g) => (
            <div key={g.norm_title} className="rounded-[10px] border border-line px-3.5 py-2.5">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                <span className="font-mono text-[12px] font-medium text-ink">{g.title}</span>
                <span
                  className={`rounded-[7px] border px-1.5 py-[2px] font-mono text-[10px] ${
                    g.count >= 3
                      ? 'border-amber/40 bg-amber/10 text-amber'
                      : 'border-line bg-surface text-ink-dim'
                  }`}
                  title={
                    g.count >= 3
                      ? 'recurring: the advisor raises this as an R11 recommendation'
                      : undefined
                  }
                >
                  {g.count} {g.count === 1 ? 'task' : 'tasks'}
                </span>
                {g.latest_action !== null && (
                  <span className="rounded-[7px] border border-brand/40 bg-brand/10 px-1.5 py-[2px] font-mono text-[10px] text-brand">
                    action: {g.latest_action}
                  </span>
                )}
              </div>
              <div className="mt-1.5 font-mono text-[10px] text-ink-faint">
                {g.tasks.join(' · ')}
              </div>
            </div>
          ))
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      {controls}
      {lessonsFailed ? (
        <ErrorBox message="lessons failed to load for this range" />
      ) : lessons === null ? (
        <Loading label="lessons…" />
      ) : lessons.length === 0 ? (
        <Empty>no retrospective lessons in this range</Empty>
      ) : visible.length === 0 ? (
        <Empty>no lessons match “{filter}”</Empty>
      ) : (
        visible.map((l) => (
          <div
            key={`${l.task_external_id}-${String(l.seq)}`}
            className="rounded-[10px] border border-line px-3.5 py-2.5"
          >
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="font-mono text-[12px] font-medium text-ink">{l.title}</span>
              {l.action !== null && (
                <span className="rounded-[7px] border border-brand/40 bg-brand/10 px-1.5 py-[2px] font-mono text-[10px] text-brand">
                  action: {l.action}
                </span>
              )}
            </div>
            {l.body !== null && (
              <p className="mt-1 font-mono text-[10.5px] whitespace-pre-wrap text-ink-dim">{l.body}</p>
            )}
            <div className="mt-1.5 font-mono text-[10px] text-ink-faint">
              {l.task_external_id} · {l.date}
            </div>
          </div>
        ))
      )}
    </div>
  );
}

/* ----- estimation accuracy table (retro phase 2) ----- */

function VarianceBadge({ pct }: { pct: number | null }): JSX.Element {
  if (pct === null) {
    return <span className="font-mono text-[11px] text-ink-faint">—</span>;
  }
  const abs = Math.abs(pct);
  const cls = abs <= 20 ? 'text-green' : abs <= 50 ? 'text-amber' : 'text-red';
  return (
    <span className={`font-mono text-[11px] ${cls}`}>
      {pct > 0 ? '+' : ''}
      {String(Math.round(pct))}%
    </span>
  );
}

function fmtHours(h: number | null): string {
  return h === null ? '—' : `${String(h)}h`;
}

function EstimationTable({ tasks }: { tasks: RetroTaskRow[] }): JSX.Element {
  if (tasks.length === 0) {
    return <Empty>no tasks with retro artifacts in this range</Empty>;
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline gap-2 font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
        <span className="min-w-0 flex-1">task</span>
        <span className="w-20 text-right">est / act</span>
        <span className="w-14 text-right">variance</span>
        <span className="w-12 text-right">loops</span>
        <span className="w-24 text-right">verdicts</span>
      </div>
      {tasks.map((t) => (
        <div key={t.external_id} className="flex items-baseline gap-2 font-mono text-[11.5px]">
          <span className="min-w-0 flex-1 truncate text-ink-3" data-tip-mono data-tip={t.external_id}>
            {t.title}
          </span>
          <span className="w-20 text-right text-ink-dim">
            {fmtHours(t.estimated_hours)} / {fmtHours(t.actual_hours)}
          </span>
          <span className="w-14 text-right">
            <VarianceBadge pct={t.variance_pct} />
          </span>
          <span className="w-12 text-right text-ink-dim">{t.loops}</span>
          <span
            className="w-24 text-right"
            data-tip={`${String(t.delegations)} delegations: ${String(t.verdicts.ok)} ok, ${String(t.verdicts.redispatch)} re-dispatched`}
          >
            <span className="text-green">{t.verdicts.ok} ok</span>
            {t.verdicts.redispatch > 0 && (
              <span className="text-red"> · {t.verdicts.redispatch} re</span>
            )}
          </span>
        </div>
      ))}
    </div>
  );
}

/* ----- friction board ----- */

function DeniedToolsPanel({ data }: { data: RetroFrictionResp }): JSX.Element {
  // Rows whose rule was just created from this board flip to "covered"
  // without a refetch.
  const [added, setAdded] = useState<ReadonlySet<string>>(new Set());
  // In-flight tools: the ref is the synchronous double-submit guard (state
  // updates lag rapid clicks), the state mirror drives rendering.
  const inflight = useRef<Set<string>>(new Set());
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const [failed, setFailed] = useState<string | null>(null);

  const addRule = useCallback((tool: string): void => {
    if (inflight.current.has(tool)) return;
    inflight.current.add(tool);
    setBusy(new Set(inflight.current));
    setFailed(null);
    createApprovalRule({
      projectId: null,
      toolPattern: tool,
      note: 'created from Retro friction board',
    })
      .then(() => {
        setAdded((prev) => new Set(prev).add(tool));
      })
      .catch((e: unknown) => {
        setFailed(String(e));
      })
      .finally(() => {
        inflight.current.delete(tool);
        setBusy(new Set(inflight.current));
      });
  }, []);

  if (data.denied_tools.length === 0) {
    return <Empty>no denied tool calls in this range</Empty>;
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline gap-2 font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
        <span className="min-w-0 flex-1">tool</span>
        <span className="w-14 text-right">denied</span>
        <span className="w-14 text-right">calls</span>
        <span className="w-20 text-right">rule</span>
      </div>
      {data.denied_tools.map((d) => {
        const covered = d.has_rule || added.has(d.tool);
        return (
          <div key={d.tool} className="flex items-baseline gap-2 font-mono text-[11.5px]">
            <span className="min-w-0 flex-1 truncate text-ink-3">{d.tool}</span>
            <span className="w-14 text-right text-brand">{d.denied}</span>
            <span className="w-14 text-right text-ink-dim">{d.calls}</span>
            <span className="w-20 text-right">
              {covered ? (
                <span className="text-green" data-tip="an enabled auto-approve rule covers this tool">
                  ✓ rule
                </span>
              ) : (
                <button
                  type="button"
                  disabled={busy.has(d.tool)}
                  onClick={() => addRule(d.tool)}
                  data-tip={`auto-approve every ${d.tool} request`}
                  className="rounded-[7px] border border-line-strong px-2 py-[3px] text-[10.5px] text-ink-dim transition-colors hover:border-green/40 hover:text-green disabled:opacity-50"
                >
                  {busy.has(d.tool) ? '…' : '+ rule'}
                </button>
              )}
            </span>
          </div>
        );
      })}
      {failed !== null && <div className="font-mono text-[10.5px] text-red">{failed}</div>}
      <p className="mt-1 font-mono text-[10px] text-ink-faint">
        + rule creates an all-projects auto-approve rule for the bare tool — narrow it in Approvals if needed.
      </p>
    </div>
  );
}

function ErrorGroupsPanel({ groups }: { groups: RetroErrorGroup[] }): JSX.Element {
  const [open, setOpen] = useState<string | null>(null);
  if (groups.length === 0) {
    return <Empty>no errors in this range</Empty>;
  }
  return (
    <div className="flex flex-col gap-2">
      {groups.map((g) => (
        <div key={g.key}>
          <button
            type="button"
            onClick={() => setOpen((o) => (o === g.key ? null : g.key))}
            aria-expanded={open === g.key}
            className="flex w-full items-baseline gap-2 text-left font-mono text-[11.5px]"
          >
            <span className="min-w-0 flex-1 truncate text-ink-3">
              {open === g.key ? '▾ ' : '▸ '}
              {g.example}
            </span>
            <span className="w-10 text-right text-red">{g.count}×</span>
            <span className="w-16 text-right text-ink-faint">{fmtAgo(g.last_ts)}</span>
          </button>
          {open === g.key && (
            <div className="mt-1.5 mb-1 ml-4 flex flex-col gap-1 border-l border-line pl-3 font-mono text-[10.5px] text-ink-dim">
              <div className="break-all text-ink-2">{g.example}</div>
              <div className="text-ink-faint">group key: {g.key}</div>
              {g.sessions.length > 0 && (
                <div>
                  sessions:{' '}
                  {g.sessions.map((u) => (
                    <span key={u} className="mr-2 text-ink-2">
                      {u.slice(0, 8)}
                    </span>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function fmtSec(s: number | null): string {
  return s === null ? '—' : fmtDurationMs(Math.round(s * 1000));
}

/* ----- screen ----- */

export function Retro(): JSX.Element {
  const today = isoDay();
  const [preset, setPreset] = useState<number | null>(14);
  const [from, setFrom] = useState<string>(addDays(today, -13));
  const [to, setTo] = useState<string>(today);
  const { scope } = useScope();

  const [agents, setAgents] = useState<RetroAgentsResp | null>(null);
  const [friction, setFriction] = useState<RetroFrictionResp | null>(null);
  const [lessons, setLessons] = useState<RetroLesson[] | null>(null);
  const [lessonsFailed, setLessonsFailed] = useState(false);
  const [lessonGroups, setLessonGroups] = useState<RetroLessonGroup[] | null>(null);
  const [lessonGroupsFailed, setLessonGroupsFailed] = useState(false);
  const [taskRows, setTaskRows] = useState<RetroTaskRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  /** Map of folded agent name → distinct trajectory anti-pattern kinds. */
  const [trajectoryKindsMap, setTrajectoryKindsMap] = useState<Record<string, string[]>>({});

  // Proposals gate: bumping proposalsKey refetches the Proposals rail after a
  // successful generation. The Improve button now opens a preview modal that
  // owns the in-flight state; `improveRow` is the agent whose modal is open.
  const [proposalsKey, setProposalsKey] = useState(0);
  const [improveRow, setImproveRow] = useState<RetroAgentRow | null>(null);

  const onImprove = useCallback((row: RetroAgentRow): void => {
    setImproveRow(row);
  }, []);

  const onGenerated = useCallback((): void => {
    setTimeout(() => setProposalsKey((k) => k + 1), 500);
  }, []);

  const applyPreset = useCallback(
    (n: number): void => {
      setPreset(n);
      setFrom(addDays(today, -(n - 1)));
      setTo(today);
    },
    [today],
  );

  // One range object for the page and for the improver card: a fresh literal
  // on every render would re-trigger the card's effects.
  const range = useMemo(
    () => ({ from, to, ...(scope !== null ? { project: scope } : {}) }),
    [from, to, scope],
  );

  const load = useCallback((): void => {
    setError(null);
    fetchRetroAgents(range)
      .then(setAgents)
      .catch((e: unknown) => setError(String(e)));
    fetchRetroFriction(range)
      .then(setFriction)
      .catch(() => setFriction(null));
    // Cleared before the refetch, not just on failure: both feeds describe a
    // WINDOW, so keeping the previous range's rows on screen while the new ones
    // are in flight is the cross-window disagreement the eager grouped fetch
    // below exists to prevent — only inside one range instead of across two.
    setLessons(null);
    setLessonsFailed(false);
    setLessonGroups(null);
    setLessonGroupsFailed(false);
    fetchRetroLessons(range)
      .then((r) => setLessons(r.lessons))
      .catch(() => setLessonsFailed(true));
    // Fetched alongside the flat feed rather than on first toggle: both views
    // must describe the SAME window, and a lazy second fetch would let them
    // straddle an ingest tick and disagree about what the range contained.
    fetchRetroLessonGroups(range)
      .then((r) => setLessonGroups(r.groups))
      .catch(() => setLessonGroupsFailed(true));
    fetchRetroTasks(range)
      .then((r) => setTaskRows(r.tasks))
      .catch(() => setTaskRows(null));
    // Trajectory anti-pattern kinds per agent (best-effort).
    fetchFirstPassRates()
      .then((rows) => {
        const m: Record<string, string[]> = {};
        for (const r of rows) {
          if (r.kinds.length > 0) m[r.agent] = r.kinds;
        }
        setTrajectoryKindsMap(m);
      })
      .catch(() => setTrajectoryKindsMap({}));
  }, [range]);

  useEffect(load, [load]);

  const rangeLabel = `${fmtDayShort(from)} → ${fmtDayShort(to)}`;

  return (
    <div className="px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h1 className="font-display text-[26px] leading-none font-medium tracking-[-0.01em] desk:text-[30px]">
          Retro
        </h1>
        <span className="font-mono text-[11px] text-ink-faint">{rangeLabel}</span>
      </div>

      <div className="mt-[18px]">
        <RangeControls
          preset={preset}
          from={from}
          to={to}
          onPreset={applyPreset}
          onFrom={(d) => {
            setPreset(null);
            setFrom(d);
          }}
          onTo={(d) => {
            setPreset(null);
            setTo(d);
          }}
        />
        <HowItWorks id="retro-page" className="mt-5 max-w-[80ch]" />
      </div>

      <RetroImproveCard range={range} />

      {agents !== null && <RetroLeadCard data={agents} />}

      <RecommendationsRail />

      <ProposalsRail reloadKey={proposalsKey} />

      {improveRow !== null && (
        <ImproveModal
          row={improveRow}
          onClose={() => setImproveRow(null)}
          onGenerated={onGenerated}
        />
      )}

      {error !== null && <ErrorBox message={error} onRetry={load} />}

      {agents === null && error === null ? (
        <Loading label="retro…" />
      ) : agents !== null ? (
        <>
          {agents.approx && <ApproxHint />}

          <SectionTitle>
            Agent scorecards <Explain id="retro-scorecard" /> <Explain id="retro-agent-improve" />
          </SectionTitle>
          {agents.agents.length === 0 ? (
            <Empty>no subagent activity in this range</Empty>
          ) : (
            <div className="grid gap-3.5 sm:grid-cols-2 wide:grid-cols-3">
              {agents.agents.map((row) => (
                <Scorecard
                  key={row.agent}
                  row={row}
                  trajectoryKinds={trajectoryKindsMap[row.agent.toLowerCase()] ?? []}
                  onImprove={onImprove}
                />
              ))}
            </div>
          )}
        </>
      ) : null}

      {scope !== null ? <JudgmentsSection project={scope} /> : <JudgmentsSection />}

      {/* Rendered unconditionally. Gating on `lessons !== null` unmounted the
          feed on every refetch, which silently threw away the operator's group
          toggle and filter text; LessonsFeed owns the loading and error copy
          instead. */}
      <SectionTitle>
        Lessons learned <Explain id="retro-lessons" />
      </SectionTitle>
      <LessonsFeed
        lessons={lessons}
        groups={lessonGroups}
        lessonsFailed={lessonsFailed}
        groupsFailed={lessonGroupsFailed}
      />

      {taskRows !== null && (
        <>
          <SectionTitle>
            Estimation accuracy <Explain id="retro-estimation" />
          </SectionTitle>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            <EstimationTable tasks={taskRows} />
          </div>
        </>
      )}

      {friction !== null && (
        <>
          <SectionTitle>
            Friction board <Explain id="retro-friction" />
          </SectionTitle>
          <div className="grid items-start gap-[22px] wide:grid-cols-2">
            <section>
              <div className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
                Denied tools
              </div>
              <div className="rounded-[14px] border border-line px-3.5 py-3.5">
                <DeniedToolsPanel data={friction} />
              </div>
            </section>
            <section>
              <div className="mb-2 font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
                Top error groups
              </div>
              <div className="rounded-[14px] border border-line px-3.5 py-3.5">
                <ErrorGroupsPanel groups={friction.error_groups} />
              </div>
            </section>
          </div>

          <div className="mt-3.5 grid gap-3.5 sm:grid-cols-4">
            <StatCard label="Approvals resolved" value={String(friction.approvals.resolved)} />
            <StatCard label="Avg resolve" value={fmtSec(friction.approvals.avg_resolve_sec)} />
            <StatCard
              label="Total wait"
              value={`${friction.approvals.wait_total_min.toFixed(1)} min`}
            />
            <StatCard label="Pending now" value={String(friction.approvals.pending)} />
          </div>
        </>
      )}
    </div>
  );
}
