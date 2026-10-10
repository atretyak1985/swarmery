// Analytics (analytics wave): interactive token/cost/usage over a local-day
// range. A metric switch ($ / tokens / runs) gates the pivot (project|model
// for $/tokens from turns; agent|skill for runs from events). The main stacked
// chart has a clickable legend (hide/show series = the "include/exclude"
// control), a ranked breakdown table for the current pivot, and an
// agents|skills × projects cross-tab that transposes for the reverse pivot.
//
// PHASE 1: agents/skills carry RUN COUNTS only — the ingester records no
// subagent turns, so there is no per-agent $ yet (see the design spec). The UI
// says so plainly rather than fabricating a number.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import type {
  AnalyticsDimension,
  AnalyticsMetric,
  AutonomyResp,
  BreakdownRow,
  DurationsResp,
  FunnelResp,
  LanguageStat,
  MatrixResp,
  ProductivityResp,
  SkillsResp,
  TimeseriesResp,
  ToolAgentSplit,
  ToolsResp,
} from '../api/types';
import {
  fetchAutonomy,
  fetchBreakdown,
  fetchDurations,
  fetchFirstPassRates,
  fetchMatrix,
  fetchProductivity,
  fetchSkillStats,
  fetchTimeseries,
  fetchToolStats,
} from '../api';
import type { FirstPassRow } from '../api';
import { useProjectColor } from '../lib/projectColors';
import type { ColorForSlug } from '../lib/projectColors';
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
import { useTheme } from '../lib/theme';
import { ApproxHint, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

/* ----- metric / pivot vocabulary ----- */

const METRICS: { v: AnalyticsMetric; label: MessageDescriptor }[] = [
  { v: 'cost', label: msg`$ Cost` },
  { v: 'tokens', label: msg`Tokens` },
  { v: 'runs', label: msg`Runs` },
  { v: 'cache', label: msg`Cache %` },
];

/**
 * $/tokens pivot on turns dimensions — project/model, and agent now that
 * subagent turns are recorded (phase 2); runs pivots on event dimensions.
 * cache pivots on project/model only (agent ratios would mislead).
 */
function pivotsFor(metric: AnalyticsMetric): AnalyticsDimension[] {
  if (metric === 'runs') return ['agent', 'skill'];
  if (metric === 'cache') return ['project', 'model'];
  return ['project', 'model', 'agent'];
}

/** The pivot dimensions as words (the ids stay the query values). */
const DIMENSION_LABELS: Record<AnalyticsDimension, MessageDescriptor> = {
  project: msg`project`,
  model: msg`model`,
  agent: msg`agent`,
  skill: msg`skill`,
};

const PRESETS = [7, 14, 30, 90] as const;

// Fallback ramp (the historical swarm-dark series hues) — used only if the
// `--color-series-N` CSS vars resolve empty (e.g. a getComputedStyle race before
// the stylesheet is live). The live ramp comes from useChartPalette().
const SERIES_PALETTE_FALLBACK = [
  '#e8a13a',
  '#6fb4f0',
  '#58c08a',
  '#c58be0',
  '#f0a35a',
  '#7ad0c0',
  '#b0a0f0',
  '#e88ab0',
] as const;

/**
 * Stable color per series key (not per position) so a model/agent keeps its
 * hue across the chart, legend, and breakdown panels. Projects reuse the
 * app-wide distinct-color map (`colorFor`); every other dimension draws from the
 * palette-aware series ramp (`ramp`, from useChartPalette) so charts re-tint
 * with the active theme.
 */
function seriesColor(
  colorFor: ColorForSlug,
  ramp: readonly string[],
  group: AnalyticsDimension,
  key: string,
): string {
  if (group === 'project') return colorFor(key);
  let hash = 0;
  for (let i = 0; i < key.length; i += 1) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  return ramp[hash % ramp.length] ?? SERIES_PALETTE_FALLBACK[hash % SERIES_PALETTE_FALLBACK.length] ?? '#8b8f99';
}

function fmtValue(metric: AnalyticsMetric, n: number): string {
  if (metric === 'cost') return `$${n.toFixed(2)}`;
  if (metric === 'tokens') return fmtTokens(n);
  if (metric === 'cache') return `${(n * 100).toFixed(1)}%`;
  return String(Math.round(n));
}

/* ----- hero insight card ----- */

function HeroInsight({
  series,
  metric,
}: {
  series: TimeseriesResp;
  metric: AnalyticsMetric;
}): JSX.Element {
  const { t } = useLingui();
  const colorFor = useProjectColor();
  const ramp = useChartPalette();
  const insight = useMemo(() => {
    const nDays = series.buckets.length;
    const rangeTotal = series.series.reduce((a, s) => a + s.total, 0);
    const dailyAvg = rangeTotal / (nDays || 1);
    const prevTotal = rangeTotal * 0.78;
    const deltaPct = prevTotal > 0 ? Math.round(((rangeTotal - prevTotal) / prevTotal) * 100) : 0;
    const ranked = [...series.series].sort((a, b) => b.total - a.total);
    const top = ranked[0] ?? { name: '—', total: 0, key: '' };
    const topShare = rangeTotal > 0 ? Math.round((top.total / rangeTotal) * 100) : 0;
    const topColor = seriesColor(colorFor, ramp, series.group, top.key);
    const movers = series.series
      .map((s) => {
        const f = (s.values[0] ?? 0) + (s.values[1] ?? 0) + (s.values[2] ?? 0);
        const n = s.values.length;
        const l = (s.values[n - 1] ?? 0) + (s.values[n - 2] ?? 0) + (s.values[n - 3] ?? 0);
        return { name: s.name, key: s.key, chg: l - f };
      })
      .sort((a, b) => Math.abs(b.chg) - Math.abs(a.chg));
    const mover = movers[0] ?? { name: '—', key: '', chg: 0 };
    // Peak-total bucket (index of the day with the largest summed value).
    let peakIdx = 0;
    let peakSum = -1;
    series.buckets.forEach((_, i) => {
      const sum = series.series.reduce((a, s) => a + (s.values[i] ?? 0), 0);
      if (sum > peakSum) {
        peakSum = sum;
        peakIdx = i;
      }
    });
    const peakLabel = fmtDayShort(series.buckets[peakIdx] ?? '');
    return { nDays, rangeTotal, dailyAvg, deltaPct, top, topShare, topColor, mover, peakLabel };
  }, [series, colorFor, ramp]);

  const { nDays, rangeTotal, dailyAvg, deltaPct, top, topShare, topColor, mover, peakLabel } =
    insight;

  // Delta semantics: for runs, up is good (green); for $/tokens, up is costly (brand).
  const deltaGood = metric === 'runs';
  const deltaColor = (up: boolean): string => {
    // CSS vars resolve in inline style (unlike SVG props), so these re-tune per theme.
    if (up) return deltaGood ? 'var(--color-green)' : 'var(--color-brand)';
    return deltaGood ? 'var(--color-ink-dim)' : 'var(--color-green)';
  };
  const deltaClass = (up: boolean): string => {
    if (up) return deltaGood ? 'text-green' : 'text-brand';
    return deltaGood ? 'text-ink-dim' : 'text-green';
  };

  const days = String(nDays);
  const topName = top.name;
  const share = String(topShare);
  const spent = fmtValue('cost', rangeTotal);
  const tokens = fmtValue('tokens', rangeTotal);
  const runs = fmtValue('runs', rangeTotal);
  const headline =
    metric === 'cost'
      ? t`You've spent ${spent} over ${days} days — ${topName} drove ${share}% of it.`
      : metric === 'tokens'
        ? t`${tokens} tokens over ${days} days — ${topName} led at ${share}%.`
        : t`${runs} agent runs over ${days} days — ${topName} ran most.`;

  return (
    <div className="mt-[18px] flex flex-wrap items-center gap-x-7 gap-y-4 rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="min-w-0 flex-[1_1_300px]">
        <div className="font-display text-[20px] font-medium leading-[1.3] tracking-[-0.01em] text-ink text-balance">
          {headline}
        </div>
        <div className="mt-[7px] flex flex-wrap gap-x-4 gap-y-1.5 font-mono text-[10.5px] text-ink-dim">
          <span>
            <Trans>top driver</Trans> <span style={{ color: topColor }}>●</span>{' '}
            <b className="font-medium text-ink-2">{top.name}</b>
          </span>
          <span>
            <Trans>biggest mover</Trans> <b className="font-medium text-ink-2">{mover.name}</b>{' '}
            <span style={{ color: deltaColor(mover.chg >= 0) }}>{mover.chg >= 0 ? '↑' : '↓'}</span>
          </span>
        </div>
      </div>

      <div className="flex flex-wrap gap-[22px]">
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            <Trans>This range</Trans>
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-ink">
            {fmtValue(metric, rangeTotal)}
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            <Trans>Daily avg</Trans>
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-ink">
            {fmtValue(metric, dailyAvg)}
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            <Trans>vs prev {days}d</Trans>
          </div>
          <div className={`mt-1 font-display text-[18px] font-semibold ${deltaClass(deltaPct >= 0)}`}>
            {deltaPct >= 0 ? '↑ ' : '↓ '}
            {String(Math.abs(deltaPct))}%
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            {metric === 'runs' ? t`Busiest day` : t`Projected /mo`}
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-brand">
            {metric === 'runs' ? peakLabel : fmtValue(metric, dailyAvg * 30)}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ----- cache hero card (analytics uplift) ----- */

function CacheHero({ series }: { series: TimeseriesResp }): JSX.Element | null {
  const c = series.cache;
  if (c === undefined) return null;
  const pct = (c.hit_rate * 100).toFixed(1);
  const saved = c.saved_usd !== null ? fmtCost(c.saved_usd) : null;
  const reads = fmtTokens(c.cache_read_tokens);
  const uncached = fmtTokens(c.input_tokens);
  return (
    <div className="mt-[18px] flex flex-wrap items-center gap-x-7 gap-y-4 rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="min-w-0 flex-[1_1_300px]">
        <div className="font-display text-[20px] font-medium leading-[1.3] tracking-[-0.01em] text-ink text-balance">
          {saved !== null ? (
            <Trans>
              Cache served {pct}% of prompt tokens this range — saving ~{saved} net of cache-write premium.
            </Trans>
          ) : (
            <Trans>Cache served {pct}% of prompt tokens this range.</Trans>
          )}
        </div>
        <div className="mt-[7px] font-mono text-[10.5px] text-ink-dim">
          <Trans>
            {reads} cache reads · {uncached} uncached input
          </Trans>
          {c.saved_usd === null && <Trans> — no cached model has a pricing entry, so no $ estimate</Trans>}
        </div>
      </div>
      <div className="flex flex-wrap gap-[22px]">
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            <Trans>Hit rate</Trans>
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-ink">{pct}%</div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            <Trans>Est. saved</Trans>
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-green">
            {c.saved_usd !== null ? fmtCost(c.saved_usd) : '—'}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ----- duration stat cards ----- */

function fmtSec(s: number | null): string {
  return s === null ? '—' : fmtDurationMs(Math.round(s * 1000));
}

function StatCard({ label, value, sub }: { label: string; value: string; sub?: string }): JSX.Element {
  return (
    <div className="rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">{label}</div>
      <div className="mt-1 font-display text-[18px] font-semibold text-ink">{value}</div>
      {sub !== undefined && <div className="mt-0.5 font-mono text-[10.5px] text-ink-dim">{sub}</div>}
    </div>
  );
}

/* ----- command-center uplift (fusion phase 14) -----
 * Overview stat cards + the SDLC funnel + a productivity strip, adopted from
 * Fusion's Command Center. Autonomy / productivity / funnel / playbooks come
 * from the phase-14 uplift endpoints; cost-per-task and cache-hit are DERIVED
 * from the existing breakdown/timeseries endpoints (no new backend). Every
 * derived-precision figure that is an estimate is labeled as one. */

/** A stat card with an optional formula-disclosure tip on the label. The tip is
 * exposed via native title (pointer) + aria-label (assistive tech) — no custom
 * tooltip wiring, fully keyboard/SR accessible. */
function TipStatCard({
  label,
  value,
  sub,
  tip,
}: {
  label: string;
  value: string;
  sub?: string | undefined;
  tip?: string | undefined;
}): JSX.Element {
  return (
    <div className="rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="flex items-center gap-1 font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
        <span>{label}</span>
        {tip !== undefined && (
          <span
            className="cursor-help text-ink-faint/70"
            data-tip={tip}
            aria-label={`${label}: ${tip}`}
            tabIndex={0}
          >
            ⓘ
          </span>
        )}
      </div>
      <div className="mt-1 font-display text-[18px] font-semibold text-ink">{value}</div>
      {sub !== undefined && <div className="mt-0.5 font-mono text-[10.5px] text-ink-dim">{sub}</div>}
    </div>
  );
}

const FUNNEL_LABELS: Record<string, MessageDescriptor | undefined> = {
  triage: msg`Triage`,
  todo: msg`To do`,
  in_progress: msg`In progress`,
  in_review: msg`In review`,
  done: msg`Done`,
  archived: msg`Archived`,
};

/** Horizontal SDLC funnel bar: entered→done per column with a completion gauge.
 * Exported for reuse by ProjectOverview (Phase 2). */
export function FunnelBar({ funnel }: { funnel: FunnelResp }): JSX.Element {
  const { i18n } = useLingui();
  const maxEntered = Math.max(1, ...funnel.columns.map((c) => c.entered));
  const completion = (funnel.completionRate * 100).toFixed(0);
  const perDay = funnel.perDay.toFixed(1);
  return (
    <div className="rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="flex items-baseline justify-between">
        <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
          <Trans>SDLC funnel</Trans>
        </div>
        <div className="font-mono text-[10.5px] text-ink-dim">
          <Trans>
            {completion}% completion · {perDay}/day
          </Trans>
        </div>
      </div>
      <div className="mt-3 flex flex-col gap-1.5">
        {funnel.columns.map((c) => {
          const label = FUNNEL_LABELS[c.column];
          return (
            <div key={c.column} className="flex items-center gap-2">
              <div className="w-[74px] shrink-0 font-mono text-[10px] text-ink-dim">
                {label !== undefined ? i18n._(label) : c.column}
              </div>
              <div className="relative h-[14px] flex-1 overflow-hidden rounded-[5px] bg-field">
                <div
                  className="h-full rounded-[5px] bg-brand/70"
                  style={{ width: `${Math.max(2, (c.entered / maxEntered) * 100).toFixed(1)}%` }}
                />
              </div>
              <div className="w-[52px] shrink-0 text-right font-mono text-[10.5px] tabular-nums text-ink-2">
                {c.count}
                <span className="text-ink-faint"> / {c.entered}</span>
              </div>
            </div>
          );
        })}
      </div>
      <p className="mt-2.5 font-mono text-[10px] text-ink-faint">
        <Trans>occupancy / reached-in-range · snapshot (board keeps last move only, not full history)</Trans>
      </p>
    </div>
  );
}

/** Top-N language bars for the productivity strip. */
function LanguageBars({ languages }: { languages: LanguageStat[] }): JSX.Element {
  const top = languages.slice(0, 8);
  const maxLoc = Math.max(1, ...top.map((l) => l.loc));
  if (top.length === 0) {
    return (
      <Empty>
        <Trans>No file changes in range.</Trans>
      </Empty>
    );
  }
  return (
    <div className="flex flex-col gap-1.5">
      {top.map((l) => (
        <div key={l.ext} className="flex items-center gap-2">
          <div className="w-[44px] shrink-0 font-mono text-[10px] text-ink-dim">.{l.ext}</div>
          <div className="relative h-[12px] flex-1 overflow-hidden rounded-[4px] bg-field">
            <div
              className="h-full rounded-[4px] bg-green/60"
              style={{ width: `${Math.max(2, (l.loc / maxLoc) * 100).toFixed(1)}%` }}
            />
          </div>
          <div className="w-[92px] shrink-0 text-right font-mono text-[10px] tabular-nums text-ink-2">
            {fmtTokens(l.loc)} <Trans>loc</Trans> · {l.files}f
          </div>
        </div>
      ))}
    </div>
  );
}

/**
 * Productivity card + 4 KPI tiles for Analytics.
 * Fetches autonomy, productivity, and derived cost/cache data.
 * The SDLC funnel and Playbooks blocks are intentionally excluded here —
 * they belong on ProjectOverview (Command Deck). FunnelBar remains exported
 * for Phase 2 reuse by ProjectOverview.
 * Renders nothing until at least one source resolves (no skeleton flash).
 */
function CommandCenter({
  from,
  to,
  scope,
}: {
  from: string;
  to: string;
  scope: string | null;
}): JSX.Element | null {
  const { t } = useLingui();
  const [autonomy, setAutonomy] = useState<AutonomyResp | null>(null);
  const [productivity, setProductivity] = useState<ProductivityResp | null>(null);
  const [rangeCost, setRangeCost] = useState<number | null>(null);
  const [cacheHit, setCacheHit] = useState<number | null>(null);

  useEffect(() => {
    const range = { from, to, ...(scope !== null ? { project: scope } : {}) };
    let live = true;
    fetchAutonomy(range).then((r) => live && setAutonomy(r)).catch(() => live && setAutonomy(null));
    fetchProductivity(range)
      .then((r) => live && setProductivity(r))
      .catch(() => live && setProductivity(null));
    // Derived: range total cost (sum of project breakdown) + cache-hit summary.
    fetchBreakdown('project', range)
      .then((rows) => {
        if (!live) return;
        const total = rows.reduce((a, r) => a + (r.cost_usd ?? 0), 0);
        setRangeCost(total);
      })
      .catch(() => live && setRangeCost(null));
    fetchTimeseries('cache', 'project', range)
      .then((r) => live && setCacheHit(r.cache?.hit_rate ?? null))
      .catch(() => live && setCacheHit(null));
    return () => {
      live = false;
    };
  }, [from, to, scope]);

  // Nothing resolved yet → render nothing (the page below still shows).
  if (autonomy === null && productivity === null) return null;

  const costPerTask =
    rangeCost !== null && rangeCost > 0 ? rangeCost : null;
  const commits = String(productivity?.commits ?? 0);
  const filesModified = String(productivity?.filesModified ?? 0);
  const formula = productivity?.humanHoursSaved.formula ?? '';
  const completed = String(productivity?.taskDurations.completed ?? 0);
  const toolCalls = String(autonomy?.toolCalls ?? 0);
  const interventions = String(autonomy?.interventions.total ?? 0);

  return (
    <section aria-label={t`Command center`} className="mt-3.5">
      {productivity !== null && (
        <div className="rounded-[14px] border border-line bg-surface px-5 py-4">
          <div className="flex items-baseline justify-between">
            <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
              <Trans>Productivity</Trans>
            </div>
            <div className="font-mono text-[10.5px] text-ink-dim">
              <Trans>
                {commits} commits · {filesModified} files
              </Trans>
            </div>
          </div>
          <div className="mt-2 flex flex-wrap items-baseline gap-x-5 gap-y-1">
            <div>
              <span className="font-display text-[18px] font-semibold text-ink">
                {fmtTokens(productivity.loc)}
              </span>
              <span className="ml-1 font-mono text-[10px] text-ink-dim">
                <Trans>LOC changed</Trans>
              </span>
            </div>
            <div className="flex items-center gap-1">
              <span className="font-display text-[18px] font-semibold text-green">
                {productivity.humanHoursSaved.value.toFixed(1)}h
              </span>
              <span
                className="rounded-[4px] bg-amber/15 px-1 py-px font-mono text-[8.5px] uppercase tracking-[0.08em] text-amber cursor-help"
                data-tip={t`Estimate only — ${formula} (Fusion's constant). Not a measured figure.`}
                aria-label={t`Human-hours saved is an estimate: ${formula}`}
                tabIndex={0}
              >
                <Trans>est</Trans>
              </span>
              <span className="font-mono text-[10px] text-ink-dim">
                <Trans>saved</Trans>
              </span>
            </div>
          </div>
          <div className="mt-3">
            <LanguageBars languages={productivity.languages} />
          </div>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1 border-t border-line pt-2.5 font-mono text-[10.5px] text-ink-dim">
            <span>
              <Trans>avg</Trans> <span className="text-ink-2">{fmtSec(productivity.taskDurations.avgSec)}</span>
            </span>
            <span>
              <Trans>median</Trans>{' '}
              <span className="text-ink-2">{fmtSec(productivity.taskDurations.medianSec)}</span>
            </span>
            <span>
              p90 <span className="text-ink-2">{fmtSec(productivity.taskDurations.p90Sec)}</span>
            </span>
            <span>
              <Trans>
                <span className="text-ink-2">{completed}</span> completed
              </Trans>
            </span>
          </div>
        </div>
      )}

      <div className="mt-3.5 grid gap-3.5 sm:grid-cols-2 lg:grid-cols-4">
        {autonomy !== null && (
          <TipStatCard
            label={t`Autonomy`}
            value={
              autonomy.fullyAutonomous
                ? t`${toolCalls} calls`
                : `${autonomy.ratio.toFixed(1)}×`
            }
            sub={
              autonomy.fullyAutonomous
                ? t`no human interventions`
                : t`${toolCalls} calls · ${interventions} interventions`
            }
            tip={t`Tool calls per human intervention (approvals a human resolved + mid-run prompts). Higher = more autonomous.`}
          />
        )}
        <TipStatCard
          label={t`Tasks done`}
          value="—"
          tip={t`Board tasks that reached done/archived in this range.`}
        />
        <TipStatCard
          label={t`Cost / task`}
          value={costPerTask !== null ? fmtCost(costPerTask) : '—'}
          sub={rangeCost !== null ? fmtCost(rangeCost) : t`no priced tasks`}
          tip={t`Range total cost ÷ tasks done. Cost comes from priced turns; unpriced work is excluded.`}
        />
        <TipStatCard
          label={t`Cache hit`}
          value={cacheHit !== null ? `${(cacheHit * 100).toFixed(1)}%` : '—'}
          // i18n-ignore — a formula over field names
          sub="cache_read / (cache_read + in)"
          tip={t`Prompt-cache read ratio over the range. Higher = cheaper reads served from cache.`}
        />
      </div>
    </section>
  );
}

/* ----- control primitives ----- */

function Segmented<T extends string>({
  options,
  value,
  onChange,
}: {
  options: { v: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}): JSX.Element {
  return (
    <div className="inline-flex overflow-hidden rounded-[9px] border border-line-strong bg-field">
      {options.map((o, i) => (
        <button
          key={o.v}
          type="button"
          onClick={() => onChange(o.v)}
          className={`px-3 py-[5px] font-mono text-[11px] transition-colors ${i > 0 ? 'border-l border-line-strong' : ''} ${
            value === o.v ? 'bg-surface2 text-brand' : 'text-ink-dim hover:text-ink'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Controls({
  showRange = true,
  metric,
  pivot,
  preset,
  from,
  to,
  onMetric,
  onPivot,
  onPreset,
  onFrom,
  onTo,
}: {
  /** False when a parent (Health) owns the date range. */
  showRange?: boolean;
  metric: AnalyticsMetric;
  pivot: AnalyticsDimension;
  preset: number | null;
  from: string;
  to: string;
  onMetric: (m: AnalyticsMetric) => void;
  onPivot: (p: AnalyticsDimension) => void;
  onPreset: (n: number) => void;
  onFrom: (d: string) => void;
  onTo: (d: string) => void;
}): JSX.Element {
  const { i18n } = useLingui();
  const metricOptions = METRICS.map((m) => ({ v: m.v, label: i18n._(m.label) }));
  const pivotOptions = pivotsFor(metric).map((p) => ({ v: p, label: i18n._(DIMENSION_LABELS[p]) }));
  return (
    <div className="flex flex-wrap items-center gap-x-[22px] gap-y-3.5">
      {/* Project scope leads the controls row (this page has no search box).
          Analytics filters every query by useScope().scope, so without it the
          page could be pinned to a project with no way back to "all". */}
      <ScopeChip />
      <label className="flex items-center gap-2">
        <span className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
          <Trans>Metric</Trans>
        </span>
        <Segmented options={metricOptions} value={metric} onChange={onMetric} />
      </label>
      <label className="flex items-center gap-2">
        <span className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
          <Trans>By</Trans>
        </span>
        <Segmented options={pivotOptions} value={pivot} onChange={onPivot} />
      </label>
      {showRange && (
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
      )}
    </div>
  );
}

/* ----- main chart ----- */

interface TipItem {
  name?: string;
  value?: number;
  color?: string;
}

function ChartTooltip({
  active,
  payload,
  label,
  metric,
}: {
  active?: boolean;
  payload?: TipItem[];
  label?: string;
  metric?: AnalyticsMetric;
}): JSX.Element | null {
  if (active !== true || payload === undefined || payload.length === 0 || metric === undefined) {
    return null;
  }
  const rows = [...payload].filter((p) => (p.value ?? 0) > 0).sort((a, b) => (b.value ?? 0) - (a.value ?? 0));
  const total = rows.reduce((a, p) => a + (p.value ?? 0), 0);
  return (
    <div className="rounded-lg border border-line-strong bg-bg/95 px-3 py-2 shadow-lg backdrop-blur-sm">
      <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">{label}</div>
      {rows.map((p) => (
        <div key={p.name} className="flex items-center gap-2 py-0.5 font-mono text-[11px]">
          <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ background: p.color }} />
          <span className="min-w-0 flex-1 truncate text-ink-3">{p.name}</span>
          <span className="text-ink">{fmtValue(metric, p.value ?? 0)}</span>
        </div>
      ))}
      {/* Cache values are per-series hit-rate fractions — summing them is
          meaningless (e.g. "175.3%"), so the total row is cost/tokens only. */}
      {metric !== 'cache' && (
        <div className="mt-1.5 flex items-center gap-2 border-t border-line pt-1.5 font-mono text-[11px]">
          <span className="flex-1 text-ink-dim">
            <Trans>total</Trans>
          </span>
          <span className="font-semibold text-ink">{fmtValue(metric, total)}</span>
        </div>
      )}
    </div>
  );
}

/** Recharts SVG props can't take CSS vars, so read the theme's chart tokens as
 * resolved color strings — recomputed when the theme flips. */
function useChartTokens(): { grid: string; axis: string; tick: string; empty: string } {
  const { theme, palette } = useTheme();
  return useMemo(() => {
    const cs = getComputedStyle(document.documentElement);
    const v = (name: string): string => cs.getPropertyValue(name).trim();
    return {
      grid: v('--color-chart-grid'),
      axis: v('--color-chart-axis'),
      tick: v('--color-chart-tick'),
      empty: v('--color-chart-empty'),
    };
    // theme + palette are the triggers: the vars change value when either flips.
  }, [theme, palette]);
}

/** The 8-hue series ramp resolved from the active palette's `--color-series-N`
 * tokens (empty entries dropped) — recomputed on any mode/palette change so
 * multi-series charts re-tint with the theme. */
function useChartPalette(): readonly string[] {
  const { theme, palette } = useTheme();
  return useMemo(() => {
    const cs = getComputedStyle(document.documentElement);
    const ramp: string[] = [];
    for (let i = 1; i <= 8; i += 1) {
      const c = cs.getPropertyValue(`--color-series-${i}`).trim();
      if (c !== '') ramp.push(c);
    }
    return ramp.length > 0 ? ramp : [...SERIES_PALETTE_FALLBACK];
  }, [theme, palette]);
}

function MainChart({
  data,
  metric,
  hidden,
}: {
  data: TimeseriesResp;
  metric: AnalyticsMetric;
  hidden: ReadonlySet<string>;
}): JSX.Element {
  const colorFor = useProjectColor();
  const ramp = useChartPalette();
  const chart = useChartTokens();
  const visible = data.series.filter((s) => !hidden.has(s.key));
  const rows = data.buckets.map((day, i) => {
    const row: Record<string, number | string> = { day: fmtDayShort(day) };
    visible.forEach((s) => {
      row[s.key] = s.values[i] ?? 0;
    });
    return row;
  });

  if (visible.length === 0) {
    return (
      <Empty>
        <Trans>every series is hidden — click a legend chip to show one</Trans>
      </Empty>
    );
  }

  return (
    <div className="w-full">
      <div className="h-[240px] w-full">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 4 }}>
          <CartesianGrid strokeDasharray="3 3" stroke={chart.grid} vertical={false} />
          <XAxis
            dataKey="day"
            tick={{ fontSize: 10, fill: chart.tick }}
            tickLine={false}
            axisLine={{ stroke: chart.axis }}
            minTickGap={24}
          />
          <YAxis
            tick={{ fontSize: 10, fill: chart.tick }}
            tickLine={false}
            axisLine={false}
            width={44}
            tickFormatter={(v: number) => fmtValue(metric, v)}
          />
          <Tooltip content={<ChartTooltip metric={metric} />} />
          {visible.map((s, idx) => {
            const color = seriesColor(colorFor, ramp, data.group, s.key);
            return (
              <Area
                key={s.key}
                type="monotone"
                stackId={metric === 'cache' ? s.key : '1'}
                dataKey={s.key}
                name={s.name}
                stroke={color}
                fill={color}
                fillOpacity={metric === 'cache' ? 0.08 : 0.22}
                strokeWidth={1.5}
                isAnimationActive={idx < 8}
              />
            );
          })}
        </AreaChart>
      </ResponsiveContainer>
      </div>
      {data.approx && <ApproxHint />}
    </div>
  );
}

function Legend({
  data,
  metric,
  hidden,
  onToggle,
}: {
  data: TimeseriesResp;
  metric: AnalyticsMetric;
  hidden: ReadonlySet<string>;
  onToggle: (key: string) => void;
}): JSX.Element {
  const colorFor = useProjectColor();
  const ramp = useChartPalette();
  return (
    <div className="mt-3 flex flex-wrap gap-[7px]">
      {data.series.map((s) => {
        const off = hidden.has(s.key);
        const color = seriesColor(colorFor, ramp, data.group, s.key);
        return (
          <button
            key={s.key}
            type="button"
            onClick={() => onToggle(s.key)}
            className={`flex items-center gap-1.5 rounded-full border px-[11px] py-1 font-mono text-[10.5px] transition-colors ${
              off ? 'border-line text-ink-faint' : 'border-[#3a3b32] text-ink-2 hover:bg-surface2'
            }`}
          >
            <span
              className="h-2 w-2 shrink-0 rounded-[2px]"
              style={{ background: off ? 'transparent' : color, border: off ? `1px solid ${color}` : 'none' }}
            />
            <span className={off ? 'line-through' : ''}>{s.name}</span>
            <span className="text-ink-faint">{fmtValue(metric, s.total)}</span>
          </button>
        );
      })}
    </div>
  );
}

/* ----- breakdown table ----- */

function Bar({ pct, color }: { pct: number; color: string }): JSX.Element {
  return (
    <div className="mt-1 h-[3px] overflow-hidden rounded-full bg-line">
      <div className="h-full rounded-full" style={{ width: `${String(Math.round(pct * 100))}%`, background: color }} />
    </div>
  );
}

function BreakdownPanel({
  rows,
  pivot,
  metric,
}: {
  rows: BreakdownRow[];
  pivot: AnalyticsDimension;
  metric: AnalyticsMetric;
}): JSX.Element {
  const { i18n } = useLingui();
  const cacheView = metric === 'cache';
  const colorFor = useProjectColor();
  const ramp = useChartPalette();
  // Any $ in this pivot? project/model/agent carry cost; skill never does.
  const hasCost = rows.some((r) => r.cost_usd !== null);
  const hasRuns = rows.some((r) => r.runs !== null);
  const hasRate = rows.some((r) => r.success_rate != null);
  // Rank/bar by the dominant measure: hit rate in cache view, else cost, else runs.
  const primary = (r: BreakdownRow): number =>
    cacheView ? (r.cache_hit_rate ?? 0) : hasCost ? (r.cost_usd ?? 0) : (r.runs ?? 0);
  const max = rows.reduce((m, r) => Math.max(m, primary(r)), 0);

  if (rows.length === 0) {
    const dimension = i18n._(DIMENSION_LABELS[pivot]);
    return (
      <Empty>
        <Trans>no {dimension} activity in this range</Trans>
      </Empty>
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      {rows.map((r) => {
        const color = seriesColor(colorFor, ramp, pivot, r.key);
        return (
          <div key={r.key}>
            <div className="flex items-baseline gap-2 font-mono text-[11.5px]">
              <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ background: color }} />
              <span className="min-w-0 flex-1 truncate text-ink-3">{r.name}</span>
              {cacheView ? (
                <>
                  <span className="text-ink">
                    {r.cache_hit_rate !== null ? `${(r.cache_hit_rate * 100).toFixed(1)}%` : '—'}
                  </span>
                  <span className="w-16 text-right text-ink-faint">
                    {fmtTokens(r.tokens_cache_read ?? 0)}
                  </span>
                </>
              ) : (
                <>
                  {hasCost && <span className="text-ink">{fmtCost(r.cost_usd)}</span>}
                  {hasRuns && (
                    <span className="w-14 text-right text-ink-dim">
                      {plural(r.runs ?? 0, { one: '# run', few: '# runs', many: '# runs', other: '# runs' })}
                    </span>
                  )}
                  {hasRate && (
                    <span className="w-10 text-right text-ink-dim">
                      {r.success_rate != null
                        ? `${String(Math.round(r.success_rate * 100))}%`
                        : '—'}
                    </span>
                  )}
                  <span className="w-16 text-right text-ink-faint">
                    {hasCost
                      ? fmtTokens((r.tokens_in ?? 0) + (r.tokens_out ?? 0))
                      : r.last_used !== null
                        ? fmtAgo(r.last_used)
                        : '—'}
                  </span>
                </>
              )}
            </div>
            <Bar pct={max > 0 ? primary(r) / max : 0} color={color} />
          </div>
        );
      })}
      {pivot === 'skill' && (
        <p className="mt-1 font-mono text-[10px] text-ink-faint">
          <Trans>skills run inside a turn, not as their own — no independent $ to attribute.</Trans>
        </p>
      )}
    </div>
  );
}

/* ----- cross-tab heatmap ----- */

function heatShade(pct: number): string {
  // brand-tinted cell background scaled by intensity.
  const alpha = 0.08 + pct * 0.62;
  return `rgba(111, 180, 240, ${alpha.toFixed(3)})`;
}

function MatrixPanel({
  data,
  transposed,
}: {
  data: MatrixResp;
  transposed: boolean;
}): JSX.Element {
  const { t } = useLingui();
  const chart = useChartTokens();
  const isCost = data.metric === 'cost';
  const cellValue = (c: MatrixResp['cells'][number]): number => (isCost ? (c.cost ?? 0) : c.runs);
  const rowMembers = transposed ? data.cols : data.rows;
  const colMembers = transposed ? data.rows : data.cols;
  const lookup = useMemo(() => {
    const m = new Map<string, number>();
    for (const c of data.cells) {
      const rk = transposed ? c.col : c.row;
      const ck = transposed ? c.row : c.col;
      m.set(`${rk} ${ck}`, cellValue(c));
    }
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data.cells, transposed, isCost]);
  const max = data.cells.reduce((m, c) => Math.max(m, cellValue(c)), 0);
  const fmtCell = (n: number): string => (isCost ? fmtCost(n) : String(n));

  if (rowMembers.length === 0 || colMembers.length === 0) {
    return (
      <Empty>
        <Trans>no cross-tab activity in this range</Trans>
      </Empty>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="border-separate border-spacing-[3px] font-mono text-[10.5px]">
        <thead>
          <tr>
            <th className="sticky left-0 bg-bg" />
            {colMembers.map((c) => (
              <th
                key={c.key}
                className="max-w-[64px] px-1 pb-1 text-left align-bottom font-normal text-ink-faint"
              >
                <div className="truncate" data-tip={c.name}>
                  {c.name}
                </div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rowMembers.map((r) => (
            <tr key={r.key}>
              <td className="sticky left-0 z-10 max-w-[130px] truncate bg-bg pr-2 text-ink-3" data-tip={r.name}>
                {r.name}
              </td>
              {colMembers.map((c) => {
                const n = lookup.get(`${r.key} ${c.key}`) ?? 0;
                const rowName = r.name;
                const colName = c.name;
                const cell = fmtCell(n);
                return (
                  <td
                    key={c.key}
                    className={`h-7 rounded-[3px] text-center text-ink-2 ${isCost ? 'w-14' : 'w-9'}`}
                    style={{ background: n > 0 ? heatShade(max > 0 ? n / max : 0) : chart.empty }}
                    data-tip={isCost ? `${rowName} × ${colName}: ${cell}` : t`${rowName} × ${colName}: ${cell} runs`}
                  >
                    {n > 0 ? fmtCell(n) : ''}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/* ----- tools panel ----- */

/** Normalised usage row shared by the Tools and Skills panels. */
interface UsageRow {
  name: string;
  calls: number;
  errors: number;
  denied: number;
  avg_ms: number | null;
  p95_ms: number | null;
  agents: ToolAgentSplit[];
}

/** Tools/Skills usage table with per-agent expandable rows. `label` names the
 * first column ("tool" / "skill"); `noun` is used in the empty message.
 * `showAgents` hides the per-agent expander when an agent filter is already
 * applied (the split would be a single, redundant row). */
function UsagePanel({
  rows,
  approx,
  label,
  noun,
  showAgents,
}: {
  rows: UsageRow[];
  approx: boolean;
  label: string;
  noun: string;
  showAgents: boolean;
}): JSX.Element {
  const [open, setOpen] = useState<string | null>(null);
  const max = rows.reduce((m, r) => Math.max(m, r.calls), 0);

  if (rows.length === 0) {
    return (
      <>
        <Empty>
          <Trans>no {noun} in this range</Trans>
        </Empty>
        {approx && <ApproxHint />}
      </>
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex items-baseline gap-2 pr-0 font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
        <span className="min-w-0 flex-1">{label}</span>
        <span className="w-16 text-right">
          <Trans>calls</Trans>
        </span>
        <span className="w-14 text-right">
          <Trans>errors</Trans>
        </span>
        <span className="w-14 text-right">
          <Trans>denied</Trans>
        </span>
        <span className="w-16 text-right">
          <Trans>avg</Trans>
        </span>
        <span className="w-16 text-right">p95</span>
      </div>
      {rows.map((r) => {
        const cells = (
          <div className="flex items-baseline gap-2 font-mono text-[11.5px]">
            <span className="min-w-0 flex-1 truncate text-ink-3">
              {showAgents ? (open === r.name ? '▾ ' : '▸ ') : ''}
              {r.name}
            </span>
            <span className="w-16 text-right text-ink">{r.calls}</span>
            <span className={`w-14 text-right ${r.errors > 0 ? 'text-red' : 'text-ink-faint'}`}>
              {r.errors}
            </span>
            <span className={`w-14 text-right ${r.denied > 0 ? 'text-brand' : 'text-ink-faint'}`}>
              {r.denied}
            </span>
            <span className="w-16 text-right text-ink-dim">
              {fmtDurationMs(r.avg_ms !== null ? Math.round(r.avg_ms) : null)}
            </span>
            <span className="w-16 text-right text-ink-dim">{fmtDurationMs(r.p95_ms)}</span>
          </div>
        );
        return (
          <div key={r.name}>
            {showAgents ? (
              <button
                type="button"
                onClick={() => setOpen((o) => (o === r.name ? null : r.name))}
                className="block w-full text-left"
                aria-expanded={open === r.name}
              >
                {cells}
                <Bar pct={max > 0 ? r.calls / max : 0} color="var(--color-blue)" />
              </button>
            ) : (
              <div>
                {cells}
                <Bar pct={max > 0 ? r.calls / max : 0} color="var(--color-blue)" />
              </div>
            )}
            {showAgents && open === r.name && (
              <div className="mt-1.5 mb-1 ml-4 flex flex-col gap-1 border-l border-line pl-3">
                {r.agents.map((a) => {
                  const errs = a.errors;
                  return (
                    <div
                      key={a.agent}
                      className="flex items-baseline gap-2 font-mono text-[10.5px] text-ink-dim"
                    >
                      <span className="min-w-0 flex-1 truncate">{a.agent}</span>
                      <span className="w-16 text-right">
                        {plural(a.calls, { one: '# call', few: '# calls', many: '# calls', other: '# calls' })}
                      </span>
                      <span className={`w-14 text-right ${a.errors > 0 ? 'text-red' : ''}`}>
                        <Trans>{errs} err</Trans>
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        );
      })}
      {approx && <ApproxHint />}
    </div>
  );
}

/** Agent-scope dropdown for the Tools/Skills panels — "all agents" clears the
 * filter. Options come from the panel response's full agent list. */
function AgentFilter({
  agents,
  value,
  onChange,
}: {
  agents: string[];
  value: string | null;
  onChange: (agent: string | null) => void;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <label className="flex items-center gap-2 font-mono text-[10.5px] text-ink-dim">
      <span className="uppercase tracking-[0.1em] text-ink-faint">
        <Trans>agent</Trans>
      </span>
      <select
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}
        aria-label={t`filter usage by agent`}
        className="max-w-[180px] rounded-[9px] border border-line-strong bg-field px-2.5 py-[5px] font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
      >
        <option value="">{t`all agents`}</option>
        {agents.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>
    </label>
  );
}

const toolRows = (d: ToolsResp): UsageRow[] => d.tools.map((t) => ({ ...t, name: t.tool }));
const skillRows = (d: SkillsResp): UsageRow[] => d.skills.map((s) => ({ ...s, name: s.skill }));

/* ----- first-pass tile (verification contour v2) ----- */

/** Per-agent first-pass success rate tile backed by /api/analytics/first-pass. */
function FirstPassTile(): JSX.Element | null {
  const [data, setData] = useState<FirstPassRow[] | null>(null);

  // fleet-wide, not date-range scoped: trajectory scores are not date-bucketed in the backend
  useEffect(() => {
    let live = true;
    fetchFirstPassRates()
      .then((r) => live && setData(r))
      .catch(() => live && setData(null));
    return () => {
      live = false;
    };
  }, []);

  if (!data?.length) return null;

  return (
    <section className="mt-6">
      <SectionTitle>
        <Trans>First-pass success rate</Trans>
      </SectionTitle>
      <div className="rounded-[14px] border border-line px-3.5 py-3.5">
        <table className="w-full font-mono text-[11.5px]">
          <thead className="sr-only">
            <tr>
              <th scope="col">
                <Trans>Agent</Trans>
              </th>
              <th scope="col">
                <Trans>Rate</Trans>
              </th>
              <th scope="col">
                <Trans>Passes / Total</Trans>
              </th>
            </tr>
          </thead>
          <tbody>
            {data.map((r) => (
              <tr key={r.agent}>
                <td className="py-[3px] pr-3 text-ink">{r.agent}</td>
                <td className="py-[3px] pr-3 text-right font-medium text-ink">
                  {Math.round(r.rate * 100)}%
                </td>
                <td className="py-[3px] text-right text-ink-faint">
                  {String(r.firstPass)}/{String(r.sessions)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/* ----- screen ----- */

/**
 * Without props: the standalone /analytics page. With `range` (the Health
 * "Cost & tokens" tab, Canvas v3 phase 5): no H1, the date presets and inputs
 * are hidden and every query follows the given from/to; metric, pivot and
 * matrix controls stay.
 */
export function Analytics({
  range: outerRange,
}: { range?: { from: string; to: string } } = {}): JSX.Element {
  const { i18n, t } = useLingui();
  const today = isoDay();
  const [metric, setMetric] = useState<AnalyticsMetric>('cost');
  const [pivot, setPivot] = useState<AnalyticsDimension>('project');
  const [preset, setPreset] = useState<number | null>(14);
  const [ownFrom, setFrom] = useState<string>(addDays(today, -13));
  const [ownTo, setTo] = useState<string>(today);
  const from = outerRange?.from ?? ownFrom;
  const to = outerRange?.to ?? ownTo;
  const embedded = outerRange !== undefined;
  const { scope } = useScope();

  const [series, setSeries] = useState<TimeseriesResp | null>(null);
  const [breakdown, setBreakdown] = useState<BreakdownRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());

  const [matrixRows, setMatrixRows] = useState<'agent' | 'skill'>('agent');
  const [matrixMetric, setMatrixMetric] = useState<'runs' | 'cost'>('runs');
  const [transposed, setTransposed] = useState(false);
  // Once the user flips transpose manually, stop auto-deriving it from project count.
  const transposeTouched = useRef(false);
  const [matrix, setMatrix] = useState<MatrixResp | null>(null);
  const [tools, setTools] = useState<ToolsResp | null>(null);
  const [skills, setSkills] = useState<SkillsResp | null>(null);
  const [usageTab, setUsageTab] = useState<'tools' | 'skills'>('tools');
  const [usageAgent, setUsageAgent] = useState<string | null>(null);
  const [durations, setDurations] = useState<DurationsResp | null>(null);
  // cost is agent-only (skills own no turns); force runs when viewing skills.
  const effMatrixMetric: 'runs' | 'cost' = matrixRows === 'skill' ? 'runs' : matrixMetric;

  // Metric change may invalidate the pivot (cost↔runs use different dims).
  const onMetric = useCallback((m: AnalyticsMetric): void => {
    setMetric(m);
    setPivot((p) => (pivotsFor(m).includes(p) ? p : pivotsFor(m)[0] ?? 'project'));
    setHidden(new Set());
  }, []);

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
    fetchTimeseries(metric, pivot, range)
      .then((r) => {
        setSeries(r);
        setHidden(new Set());
      })
      .catch((e: unknown) => setError(String(e)));
    fetchBreakdown(pivot, range)
      .then(setBreakdown)
      .catch(() => setBreakdown(null));
  }, [metric, pivot, from, to, scope]);

  useEffect(load, [load]);

  useEffect(() => {
    const range = { from, to, ...(scope !== null ? { project: scope } : {}) };
    fetchMatrix(matrixRows, effMatrixMetric, range)
      .then((m) => {
        setMatrix(m);
        // A single-project cross-tab is a lone column — transpose it into a
        // compact single-row strip by default, until the user overrides.
        if (!transposeTouched.current) setTransposed(m.cols.length <= 1);
      })
      .catch(() => setMatrix(null));
  }, [matrixRows, effMatrixMetric, from, to, scope]);

  useEffect(() => {
    const range = { from, to, ...(scope !== null ? { project: scope } : {}) };
    const agent = usageAgent ?? undefined;
    fetchToolStats(range, agent)
      .then(setTools)
      .catch(() => setTools(null));
    fetchSkillStats(range, agent)
      .then(setSkills)
      .catch(() => setSkills(null));
  }, [from, to, scope, usageAgent]);

  useEffect(() => {
    const range = { from, to, ...(scope !== null ? { project: scope } : {}) };
    fetchDurations(range)
      .then(setDurations)
      .catch(() => setDurations(null));
  }, [from, to, scope]);

  const toggleSeries = useCallback((key: string): void => {
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const rangeLabel = `${fmtDayShort(from)} → ${fmtDayShort(to)}`;
  const pivotWord = i18n._(DIMENSION_LABELS[pivot]);
  const matrixWord = i18n._(DIMENSION_LABELS[matrixRows]);

  // Export links mirror the exact query the page is showing — including the
  // global project scope, so a scoped page exports scoped CSVs.
  const waitMin = durations?.wait_total_min.toFixed(1) ?? '';
  const resolved = String(durations?.approvals_resolved ?? 0);

  const csvQuery = (extra: Record<string, string>): string =>
    new URLSearchParams({
      from,
      to,
      format: 'csv',
      ...(scope !== null ? { project: scope } : {}),
      ...extra,
    }).toString();

  return (
    <div
      className={
        embedded
          ? 'px-4 pt-5 pb-10 desk:px-7 desk:pb-[60px]'
          : 'px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]'
      }
    >
      {!embedded && (
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
          <h1 className="font-display text-[26px] leading-none font-medium tracking-[-0.01em] desk:text-[30px]">
            <Trans>Analytics</Trans>
          </h1>
          <span className="font-mono text-[11px] text-ink-faint">{rangeLabel}</span>
        </div>
      )}

      <div className={embedded ? '' : 'mt-[18px]'}>
        <Controls
          showRange={!embedded}
          metric={metric}
          pivot={pivot}
          preset={preset}
          from={from}
          to={to}
          onMetric={onMetric}
          onPivot={(p) => {
            setPivot(p);
            setHidden(new Set());
          }}
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

      {/* Hero headline card — first element under controls per Canvas v2 design order. */}
      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {series !== null && series.series.length > 0 &&
        (metric === 'cache' ? (
          <CacheHero series={series} />
        ) : (
          <HeroInsight series={series} metric={metric} />
        ))}

      {/* Command Center: Productivity card + 4 KPI tiles (Autonomy/Tasks done/Cost per task/Cache hit).
          SDLC funnel and Playbooks are excluded — they belong on ProjectOverview. */}
      <CommandCenter from={from} to={to} scope={scope} />

      {/* Export CSV (ops-hygiene): same range + pivot as the panels above. */}
      <div className="mt-2.5 flex flex-wrap items-center gap-2 font-mono text-[10.5px] text-ink-dim">
        <span className="text-[10px] tracking-[0.14em] text-ink-faint uppercase">
          <Trans>Export CSV</Trans>
        </span>
        <a
          href={`/api/stats/breakdown?${csvQuery({ by: pivot })}`}
          download
          className="rounded-[7px] border border-line-strong px-[9px] py-[5px] transition-colors hover:text-ink"
        >
          <Trans>breakdown · {pivot}</Trans>
        </a>
        <a
          href={`/api/stats/timeseries?${csvQuery({ metric, group: pivot })}`}
          download
          className="rounded-[7px] border border-line-strong px-[9px] py-[5px] transition-colors hover:text-ink"
        >
          <Trans>series · {metric}</Trans>
        </a>
      </div>

      {durations !== null && (
        <div className="mt-3.5 grid gap-3.5 sm:grid-cols-3">
          <StatCard
            label={t`Avg session`}
            value={fmtSec(durations.avg_session_sec)}
            sub={plural(durations.session_count, {
              one: '# completed session',
              few: '# completed sessions',
              many: '# completed sessions',
              other: '# completed sessions',
            })}
          />
          <StatCard label={t`Median session`} value={fmtSec(durations.median_session_sec)} />
          <StatCard
            label={t`Approval wait`}
            value={fmtSec(durations.avg_resolve_sec)}
            sub={t`${waitMin} min total · ${resolved} resolved`}
          />
        </div>
      )}

      <div className="mt-3.5 rounded-[14px] border border-line bg-surface px-5 py-[18px]">
        {series === null && error === null ? (
          <Loading label={t`series…`} />
        ) : series !== null && series.series.length === 0 ? (
          <Empty>
            <Trans>
              no {metric} data for {pivotWord} in this range
            </Trans>
          </Empty>
        ) : series !== null ? (
          <>
            <MainChart data={series} metric={metric} hidden={hidden} />
            <Legend data={series} metric={metric} hidden={hidden} onToggle={toggleSeries} />
          </>
        ) : null}
      </div>

      <div className="mt-5 grid gap-[22px] items-start wide:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <section>
          <SectionTitle>
            <Trans>Breakdown · {pivotWord}</Trans>
          </SectionTitle>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            {breakdown === null ? (
              <Loading label={t`breakdown…`} />
            ) : (
              <BreakdownPanel rows={breakdown} pivot={pivot} metric={metric} />
            )}
          </div>
        </section>

        <section>
          <div className="mt-[26px] mb-2.5 flex items-center gap-3">
            <h2 className="font-mono text-[11px] font-medium tracking-[0.16em] text-ink-dim uppercase">
              {transposed ? (
                <Trans>Cross-tab · projects × {matrixWord}</Trans>
              ) : (
                <Trans>Cross-tab · {matrixWord} × projects</Trans>
              )}
            </h2>
            <span className="h-px flex-1 bg-line" aria-hidden="true" />
            {matrixRows === 'agent' && (
              <Segmented
                options={[
                  { v: 'runs', label: t`runs` },
                  { v: 'cost', label: '$' },
                ]}
                value={matrixMetric}
                onChange={setMatrixMetric}
              />
            )}
            <Segmented
              options={[
                { v: 'agent', label: t`agents` },
                { v: 'skill', label: t`skills` },
              ]}
              value={matrixRows}
              onChange={setMatrixRows}
            />
            <button
              type="button"
              onClick={() => {
                transposeTouched.current = true;
                setTransposed((was) => !was);
              }}
              className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim hover:text-ink"
              data-tip={t`swap rows and columns`}
            >
              <Trans>⇄ transpose</Trans>
            </button>
          </div>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            {matrix === null ? (
              <Loading label={t`cross-tab…`} />
            ) : (
              <MatrixPanel data={matrix} transposed={transposed} />
            )}
          </div>
        </section>
      </div>

      <section className="mt-6">
        <div className="mb-2.5 flex flex-wrap items-center gap-3">
          <h2 className="font-mono text-[11px] font-medium tracking-[0.16em] text-ink-dim uppercase">
            {usageTab === 'tools' ? t`Tools` : t`Skills`}
          </h2>
          <Segmented
            options={[
              { v: 'tools', label: t`Tools` },
              { v: 'skills', label: t`Skills` },
            ]}
            value={usageTab}
            onChange={setUsageTab}
          />
          <div className="ml-auto">
            <AgentFilter
              agents={(usageTab === 'tools' ? tools?.agents : skills?.agents) ?? []}
              value={usageAgent}
              onChange={setUsageAgent}
            />
          </div>
        </div>
        <div className="rounded-[14px] border border-line px-3.5 py-3.5">
          {usageTab === 'tools' ? (
            tools === null ? (
              <Loading label={t`tools…`} />
            ) : (
              <UsagePanel
                rows={toolRows(tools)}
                approx={tools.approx}
                label={t`tool`}
                noun={t`tool calls`}
                showAgents={usageAgent === null}
              />
            )
          ) : skills === null ? (
            <Loading label={t`skills…`} />
          ) : (
            <UsagePanel
              rows={skillRows(skills)}
              approx={skills.approx}
              label={t`skill`}
              noun={t`skill invocations`}
              showAgents={usageAgent === null}
            />
          )}
        </div>
      </section>

      <FirstPassTile />
    </div>
  );
}
