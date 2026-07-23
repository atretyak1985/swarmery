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
  Recommendation,
  RetroAgentRow,
  RetroAgentsResp,
  RetroErrorGroup,
  RetroFrictionResp,
  RetroLesson,
  RetroTaskRow,
} from '../api/types';
import {
  createApprovalRule,
  fetchRecommendations,
  fetchRetroAgents,
  fetchRetroFriction,
  fetchRetroLessons,
  fetchRetroTasks,
  patchRecommendation,
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
import { useScope } from '../lib/scope';
import { ApproxHint, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

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
    <div className="flex items-center gap-1.5">
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
      title={`verified when ${b.metric} is ≥${String(VERIFY_IMPROVEMENT * 100)}% better than the baseline snapshot`}
    >
      {b.metric} {fmtMetric(b.value)}
      {postValue !== null ? ` → ${fmtMetric(postValue)}` : ''}
      {` (target ${rec.rule === 'R6' ? '≥' : '≤'}${fmtMetric(target)})`}
      {note === 'insufficient post-adoption traffic' ? ' · insufficient traffic so far' : ''}
    </span>
  );
}

/** Lifecycle chip for in-flight statuses (accepted/adopted). The accepted
 * copy is per target kind: agent/tool/process recs have a detectable adoption
 * signal to wait for; error_group/config verify straight from accepted, so
 * their chip counts down to the verification check instead ("waiting for
 * adoption" would promise a step that never happens). Adopted recs count down
 * from the detected change. */
function RecStatusChip({ rec }: { rec: Recommendation }): JSX.Element | null {
  const countdown = (anchor: string): string => {
    const d = daysUntilVerify(anchor);
    return d > 0
      ? `verify check in ${String(d)}d`
      : `awaiting ≥${String(VERIFY_IMPROVEMENT * 100)}% improvement`;
  };
  if (rec.status === 'accepted') {
    const kind = rec.target_kind;
    const adoptable = kind === 'agent' || kind === 'tool' || kind === 'process';
    const anchor = rec.baseline?.accepted_at;
    const showCountdown = !adoptable && anchor !== undefined;
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

  const loadVerified = useCallback((): void => {
    fetchRecommendations('verified')
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
        <button
          type="button"
          disabled={analyzing}
          onClick={analyze}
          title="run the advisor rule engine now"
          className="ml-auto rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50"
        >
          {analyzing ? 'analyzing…' : 'Analyze now'}
        </button>
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
            {verifiedOpen ? '▾' : '▸'} Verified
            {verified !== null ? ` (${String(verified.length)})` : ''}
          </button>
          {verifiedOpen && verified !== null && (
            <div className="mt-2 flex flex-col gap-1.5">
              {verified.length === 0 ? (
                <Empty>nothing verified yet</Empty>
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
    <span className={`font-mono text-[12px] ${cls}`} title={`prev window: ${fmt(prev)}`}>
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

function HealthStrip({ data }: { data: RetroAgentsResp }): JSX.Element {
  const totalRuns = data.agents.reduce((a, r) => a + r.runs, 0);
  const totalErrors = data.main.errors + data.agents.reduce((a, r) => a + r.errors, 0);
  // The contract carries no prev for main, so vs-prev totals cover subagents.
  const prevRuns = data.agents.reduce((a, r) => a + r.prev.runs, 0);
  const prevErrors = data.agents.reduce((a, r) => a + r.prev.errors, 0);
  const prevCost = data.agents.reduce((a, r) => a + r.prev.cost_usd, 0);
  const agentCost = data.agents.reduce((a, r) => a + r.cost_usd, 0);
  return (
    <div className="mt-[18px] grid gap-3.5 sm:grid-cols-3">
      <StatCard
        label="Orchestrator cost"
        value={fmtCost(data.main.cost_usd)}
        sub={`${fmtTokens(data.main.tokens_out)} tokens out · agents ${fmtCost(agentCost)}`}
        arrow={<DeltaArrow cur={agentCost} prev={prevCost} fmt={fmtCost} />}
      />
      <StatCard
        label="Agent runs"
        value={String(totalRuns)}
        sub={`prev window ${String(prevRuns)}`}
        arrow={<DeltaArrow cur={totalRuns} prev={prevRuns} goodUp />}
      />
      <StatCard
        label="Errors"
        value={String(totalErrors)}
        sub={`prev window ${String(prevErrors)} (subagents)`}
        arrow={<DeltaArrow cur={totalErrors} prev={prevErrors} />}
      />
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

function Scorecard({ row }: { row: RetroAgentRow }): JSX.Element {
  const split = errClassSplit(row.errors_by_class);
  return (
    <div className="rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex items-baseline gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] font-medium text-ink">
          {row.agent}
        </span>
        <span
          className={`font-mono text-[11px] ${errRateClass(row.error_rate)}`}
          title={`share of runs with ≥1 behavior-fixable error (${String(row.errors)} error events total)`}
        >
          {(row.error_rate * 100).toFixed(1)}% err
        </span>
      </div>
      {row.errors > 0 && row.errors_by_class && (
        <div
          className="mt-1 font-mono text-[10px] text-ink-faint"
          title="error events by class — behavior: prompt-fixable agent behavior · harness: harness rule hit, self-recovered · infra: network/API noise (not the agent's fault)"
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
      {(row.re_dispatch_rate !== null || row.eval !== null) && (
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          {row.re_dispatch_rate !== null && (
            <span
              title="redispatch-classified ledger rows / total delegations in range"
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
              title={`latest eval run, finished ${row.eval.finished_at}`}
              className="rounded-[7px] border border-line-strong px-1.5 py-[2px] font-mono text-[10px] text-ink-dim"
            >
              evals {String(row.eval.passed)}/{String(row.eval.passed + row.eval.failed)}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

/* ----- lessons feed (retro phase 2) ----- */

function LessonsFeed({ lessons }: { lessons: RetroLesson[] }): JSX.Element {
  const [filter, setFilter] = useState('');
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (q === '') return lessons;
    return lessons.filter((l) =>
      [l.title, l.action ?? '', l.body ?? '', l.task_external_id, l.task_title]
        .join('\n')
        .toLowerCase()
        .includes(q),
    );
  }, [lessons, filter]);

  return (
    <div className="flex flex-col gap-2.5">
      <input
        type="search"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        placeholder="filter lessons…"
        aria-label="filter lessons"
        className="w-full max-w-xs rounded-md border border-line bg-surface px-2.5 py-1.5 font-mono text-[11px] text-ink placeholder:text-ink-faint"
      />
      {lessons.length === 0 ? (
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
          <span className="min-w-0 flex-1 truncate text-ink-3" title={t.external_id}>
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
            title={`${String(t.delegations)} delegations: ${String(t.verdicts.ok)} ok, ${String(t.verdicts.redispatch)} re-dispatched`}
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
                <span className="text-green" title="an enabled auto-approve rule covers this tool">
                  ✓ rule
                </span>
              ) : (
                <button
                  type="button"
                  disabled={busy.has(d.tool)}
                  onClick={() => addRule(d.tool)}
                  title={`auto-approve every ${d.tool} request`}
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
  const [taskRows, setTaskRows] = useState<RetroTaskRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const applyPreset = useCallback(
    (n: number): void => {
      setPreset(n);
      setFrom(addDays(today, -(n - 1)));
      setTo(today);
    },
    [today],
  );

  const load = useCallback((): void => {
    const range = { from, to, ...(scope !== null ? { project: scope } : {}) };
    setError(null);
    fetchRetroAgents(range)
      .then(setAgents)
      .catch((e: unknown) => setError(String(e)));
    fetchRetroFriction(range)
      .then(setFriction)
      .catch(() => setFriction(null));
    fetchRetroLessons(range)
      .then((r) => setLessons(r.lessons))
      .catch(() => setLessons(null));
    fetchRetroTasks(range)
      .then((r) => setTaskRows(r.tasks))
      .catch(() => setTaskRows(null));
  }, [from, to, scope]);

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
      </div>

      <RecommendationsRail />

      {error !== null && <ErrorBox message={error} onRetry={load} />}

      {agents === null && error === null ? (
        <Loading label="retro…" />
      ) : agents !== null ? (
        <>
          <HealthStrip data={agents} />
          {agents.approx && <ApproxHint />}

          <SectionTitle>Agent scorecards</SectionTitle>
          {agents.agents.length === 0 ? (
            <Empty>no subagent activity in this range</Empty>
          ) : (
            <div className="grid gap-3.5 sm:grid-cols-2 wide:grid-cols-3">
              {agents.agents.map((row) => (
                <Scorecard key={row.agent} row={row} />
              ))}
            </div>
          )}
        </>
      ) : null}

      {lessons !== null && (
        <>
          <SectionTitle>Lessons learned</SectionTitle>
          <LessonsFeed lessons={lessons} />
        </>
      )}

      {taskRows !== null && (
        <>
          <SectionTitle>Estimation accuracy</SectionTitle>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            <EstimationTable tasks={taskRows} />
          </div>
        </>
      )}

      {friction !== null && (
        <>
          <SectionTitle>Friction board</SectionTitle>
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
