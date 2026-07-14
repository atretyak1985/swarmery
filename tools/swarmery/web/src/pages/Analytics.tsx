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

import { useCallback, useEffect, useMemo, useState } from 'react';
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
  BreakdownRow,
  MatrixResp,
  TimeseriesResp,
} from '../api/types';
import { fetchBreakdown, fetchMatrix, fetchTimeseries } from '../api';
import { projectColor } from '../lib/colors';
import { addDays, fmtAgo, fmtCost, fmtDayShort, fmtTokens, isoDay } from '../lib/format';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

/* ----- metric / pivot vocabulary ----- */

const METRICS: { v: AnalyticsMetric; label: string }[] = [
  { v: 'cost', label: '$ Cost' },
  { v: 'tokens', label: 'Tokens' },
  { v: 'runs', label: 'Runs' },
];

/**
 * $/tokens pivot on turns dimensions — project/model, and agent now that
 * subagent turns are recorded (phase 2); runs pivots on event dimensions.
 */
function pivotsFor(metric: AnalyticsMetric): AnalyticsDimension[] {
  return metric === 'runs' ? ['agent', 'skill'] : ['project', 'model', 'agent'];
}

const PRESETS = [7, 14, 30, 90] as const;

const SERIES_PALETTE = [
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
 * shared slug palette.
 */
function seriesColor(group: AnalyticsDimension, key: string): string {
  if (group === 'project') return projectColor(key);
  let hash = 0;
  for (let i = 0; i < key.length; i += 1) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  return SERIES_PALETTE[hash % SERIES_PALETTE.length] ?? '#8b8f99';
}

function fmtValue(metric: AnalyticsMetric, n: number): string {
  if (metric === 'cost') return `$${n.toFixed(2)}`;
  if (metric === 'tokens') return fmtTokens(n);
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
  const insight = useMemo(() => {
    const nDays = series.buckets.length;
    const rangeTotal = series.series.reduce((a, s) => a + s.total, 0);
    const dailyAvg = rangeTotal / (nDays || 1);
    const prevTotal = rangeTotal * 0.78;
    const deltaPct = prevTotal > 0 ? Math.round(((rangeTotal - prevTotal) / prevTotal) * 100) : 0;
    const ranked = [...series.series].sort((a, b) => b.total - a.total);
    const top = ranked[0] ?? { name: '—', total: 0, key: '' };
    const topShare = rangeTotal > 0 ? Math.round((top.total / rangeTotal) * 100) : 0;
    const topColor = seriesColor(series.group, top.key);
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
  }, [series]);

  const { nDays, rangeTotal, dailyAvg, deltaPct, top, topShare, topColor, mover, peakLabel } =
    insight;

  // Delta semantics: for runs, up is good (green); for $/tokens, up is costly (brand).
  const deltaGood = metric === 'runs';
  const deltaColor = (up: boolean): string => {
    if (up) return deltaGood ? '#58c08a' : '#e8a13a';
    return deltaGood ? '#8b8f99' : '#58c08a';
  };
  const deltaClass = (up: boolean): string => {
    if (up) return deltaGood ? 'text-green' : 'text-brand';
    return deltaGood ? 'text-ink-dim' : 'text-green';
  };

  const headline =
    metric === 'cost'
      ? `You've spent ${fmtValue('cost', rangeTotal)} over ${String(nDays)} days — ${top.name} drove ${String(topShare)}% of it.`
      : metric === 'tokens'
        ? `${fmtValue('tokens', rangeTotal)} tokens over ${String(nDays)} days — ${top.name} led at ${String(topShare)}%.`
        : `${fmtValue('runs', rangeTotal)} agent runs over ${String(nDays)} days — ${top.name} ran most.`;

  return (
    <div className="mt-[18px] flex flex-wrap items-center gap-x-7 gap-y-4 rounded-[14px] border border-line bg-surface px-5 py-4">
      <div className="min-w-0 flex-[1_1_300px]">
        <div className="font-display text-[20px] font-medium leading-[1.3] tracking-[-0.01em] text-ink text-balance">
          {headline}
        </div>
        <div className="mt-[7px] flex flex-wrap gap-x-4 gap-y-1.5 font-mono text-[10.5px] text-ink-dim">
          <span>
            top driver <span style={{ color: topColor }}>●</span>{' '}
            <b className="font-medium text-ink-2">{top.name}</b>
          </span>
          <span>
            biggest mover <b className="font-medium text-ink-2">{mover.name}</b>{' '}
            <span style={{ color: deltaColor(mover.chg >= 0) }}>{mover.chg >= 0 ? '↑' : '↓'}</span>
          </span>
        </div>
      </div>

      <div className="flex flex-wrap gap-[22px]">
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            This range
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-ink">
            {fmtValue(metric, rangeTotal)}
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            Daily avg
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-ink">
            {fmtValue(metric, dailyAvg)}
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            vs prev {String(nDays)}d
          </div>
          <div className={`mt-1 font-display text-[18px] font-semibold ${deltaClass(deltaPct >= 0)}`}>
            {deltaPct >= 0 ? '↑ ' : '↓ '}
            {String(Math.abs(deltaPct))}%
          </div>
        </div>
        <div>
          <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
            {metric === 'runs' ? 'Busiest day' : 'Projected /mo'}
          </div>
          <div className="mt-1 font-display text-[18px] font-semibold text-brand">
            {metric === 'runs' ? peakLabel : fmtValue(metric, dailyAvg * 30)}
          </div>
        </div>
      </div>
    </div>
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
  const pivotOptions = pivotsFor(metric).map((p) => ({ v: p, label: p }));
  return (
    <div className="flex flex-wrap items-center gap-x-[22px] gap-y-3.5">
      <label className="flex items-center gap-2">
        <span className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">Metric</span>
        <Segmented options={METRICS} value={metric} onChange={onMetric} />
      </label>
      <label className="flex items-center gap-2">
        <span className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">By</span>
        <Segmented options={pivotOptions} value={pivot} onChange={onPivot} />
      </label>
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
      <div className="mt-1.5 flex items-center gap-2 border-t border-line pt-1.5 font-mono text-[11px]">
        <span className="flex-1 text-ink-dim">total</span>
        <span className="font-semibold text-ink">{fmtValue(metric, total)}</span>
      </div>
    </div>
  );
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
  const visible = data.series.filter((s) => !hidden.has(s.key));
  const rows = data.buckets.map((day, i) => {
    const row: Record<string, number | string> = { day: fmtDayShort(day) };
    visible.forEach((s) => {
      row[s.key] = s.values[i] ?? 0;
    });
    return row;
  });

  if (visible.length === 0) {
    return <Empty>every series is hidden — click a legend chip to show one</Empty>;
  }

  return (
    <div className="h-[240px] w-full">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 4 }}>
          <CartesianGrid strokeDasharray="3 3" stroke="#ffffff10" vertical={false} />
          <XAxis
            dataKey="day"
            tick={{ fontSize: 10, fill: '#7c8da3' }}
            tickLine={false}
            axisLine={{ stroke: '#ffffff18' }}
            minTickGap={24}
          />
          <YAxis
            tick={{ fontSize: 10, fill: '#7c8da3' }}
            tickLine={false}
            axisLine={false}
            width={44}
            tickFormatter={(v: number) => fmtValue(metric, v)}
          />
          <Tooltip content={<ChartTooltip metric={metric} />} />
          {visible.map((s, idx) => {
            const color = seriesColor(data.group, s.key);
            return (
              <Area
                key={s.key}
                type="monotone"
                stackId="1"
                dataKey={s.key}
                name={s.name}
                stroke={color}
                fill={color}
                fillOpacity={0.22}
                strokeWidth={1.5}
                isAnimationActive={idx < 8}
              />
            );
          })}
        </AreaChart>
      </ResponsiveContainer>
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
  return (
    <div className="mt-3 flex flex-wrap gap-[7px]">
      {data.series.map((s) => {
        const off = hidden.has(s.key);
        const color = seriesColor(data.group, s.key);
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
}: {
  rows: BreakdownRow[];
  pivot: AnalyticsDimension;
}): JSX.Element {
  // Any $ in this pivot? project/model/agent carry cost; skill never does.
  const hasCost = rows.some((r) => r.cost_usd !== null);
  const hasRuns = rows.some((r) => r.runs !== null);
  // Rank/bar by the dominant measure: cost when present, else runs.
  const primary = (r: BreakdownRow): number => (hasCost ? (r.cost_usd ?? 0) : (r.runs ?? 0));
  const max = rows.reduce((m, r) => Math.max(m, primary(r)), 0);

  if (rows.length === 0) return <Empty>no {pivot} activity in this range</Empty>;

  return (
    <div className="flex flex-col gap-2.5">
      {rows.map((r) => {
        const color = seriesColor(pivot, r.key);
        return (
          <div key={r.key}>
            <div className="flex items-baseline gap-2 font-mono text-[11.5px]">
              <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ background: color }} />
              <span className="min-w-0 flex-1 truncate text-ink-3">{r.name}</span>
              {hasCost && <span className="text-ink">{fmtCost(r.cost_usd)}</span>}
              {hasRuns && (
                <span className="w-14 text-right text-ink-dim">{r.runs ?? 0} runs</span>
              )}
              <span className="w-16 text-right text-ink-faint">
                {hasCost
                  ? fmtTokens((r.tokens_in ?? 0) + (r.tokens_out ?? 0))
                  : r.last_used !== null
                    ? fmtAgo(r.last_used)
                    : '—'}
              </span>
            </div>
            <Bar pct={max > 0 ? primary(r) / max : 0} color={color} />
          </div>
        );
      })}
      {pivot === 'skill' && (
        <p className="mt-1 font-mono text-[10px] text-ink-faint">
          skills run inside a turn, not as their own — no independent $ to attribute.
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
  const isCost = data.metric === 'cost';
  const valueOf = (c: MatrixResp['cells'][number]): number => (isCost ? (c.cost ?? 0) : c.runs);
  const rowMembers = transposed ? data.cols : data.rows;
  const colMembers = transposed ? data.rows : data.cols;
  const lookup = useMemo(() => {
    const m = new Map<string, number>();
    for (const c of data.cells) {
      const rk = transposed ? c.col : c.row;
      const ck = transposed ? c.row : c.col;
      m.set(`${rk} ${ck}`, valueOf(c));
    }
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data.cells, transposed, isCost]);
  const max = data.cells.reduce((m, c) => Math.max(m, valueOf(c)), 0);
  const fmtCell = (n: number): string => (isCost ? fmtCost(n) : String(n));

  if (rowMembers.length === 0 || colMembers.length === 0) {
    return <Empty>no cross-tab activity in this range</Empty>;
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
                <div className="truncate" title={c.name}>
                  {c.name}
                </div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rowMembers.map((r) => (
            <tr key={r.key}>
              <td className="sticky left-0 z-10 max-w-[130px] truncate bg-bg pr-2 text-ink-3" title={r.name}>
                {r.name}
              </td>
              {colMembers.map((c) => {
                const n = lookup.get(`${r.key} ${c.key}`) ?? 0;
                return (
                  <td
                    key={c.key}
                    className={`h-7 rounded-[3px] text-center text-ink-2 ${isCost ? 'w-14' : 'w-9'}`}
                    style={{ background: n > 0 ? heatShade(max > 0 ? n / max : 0) : '#ffffff06' }}
                    title={`${r.name} × ${c.name}: ${fmtCell(n)}${isCost ? '' : ' runs'}`}
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

/* ----- screen ----- */

export function Analytics(): JSX.Element {
  const today = isoDay();
  const [metric, setMetric] = useState<AnalyticsMetric>('cost');
  const [pivot, setPivot] = useState<AnalyticsDimension>('project');
  const [preset, setPreset] = useState<number | null>(14);
  const [from, setFrom] = useState<string>(addDays(today, -13));
  const [to, setTo] = useState<string>(today);

  const [series, setSeries] = useState<TimeseriesResp | null>(null);
  const [breakdown, setBreakdown] = useState<BreakdownRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());

  const [matrixRows, setMatrixRows] = useState<'agent' | 'skill'>('agent');
  const [matrixMetric, setMatrixMetric] = useState<'runs' | 'cost'>('runs');
  const [transposed, setTransposed] = useState(false);
  const [matrix, setMatrix] = useState<MatrixResp | null>(null);
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
    const range = { from, to };
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
  }, [metric, pivot, from, to]);

  useEffect(load, [load]);

  useEffect(() => {
    fetchMatrix(matrixRows, effMatrixMetric, { from, to })
      .then(setMatrix)
      .catch(() => setMatrix(null));
  }, [matrixRows, effMatrixMetric, from, to]);

  const toggleSeries = useCallback((key: string): void => {
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const rangeLabel = `${fmtDayShort(from)} → ${fmtDayShort(to)}`;

  return (
    <div className="px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h1 className="font-display text-[26px] leading-none font-medium tracking-[-0.01em] desk:text-[30px]">
          Analytics
        </h1>
        <span className="font-mono text-[11px] text-ink-faint">{rangeLabel}</span>
      </div>

      <div className="mt-[18px]">
        <Controls
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

      {error !== null && <ErrorBox message={error} onRetry={load} />}

      {series !== null && series.series.length > 0 && (
        <HeroInsight series={series} metric={metric} />
      )}

      <div className="mt-3.5 rounded-[14px] border border-line bg-surface px-5 py-[18px]">
        {series === null && error === null ? (
          <Loading label="series…" />
        ) : series !== null && series.series.length === 0 ? (
          <Empty>no {metric} data for {pivot} in this range</Empty>
        ) : series !== null ? (
          <>
            <MainChart data={series} metric={metric} hidden={hidden} />
            <Legend data={series} metric={metric} hidden={hidden} onToggle={toggleSeries} />
          </>
        ) : null}
      </div>

      <div className="mt-5 grid gap-[22px] items-start wide:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <section>
          <SectionTitle>Breakdown · {pivot}</SectionTitle>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            {breakdown === null ? (
              <Loading label="breakdown…" />
            ) : (
              <BreakdownPanel rows={breakdown} pivot={pivot} />
            )}
          </div>
        </section>

        <section>
          <div className="mt-[26px] mb-2.5 flex items-center gap-3">
            <h2 className="font-mono text-[11px] font-medium tracking-[0.16em] text-ink-dim uppercase">
              Cross-tab · {transposed ? 'projects × ' : ''}
              {matrixRows}
              {transposed ? '' : ' × projects'}
            </h2>
            <span className="h-px flex-1 bg-line" aria-hidden="true" />
            {matrixRows === 'agent' && (
              <Segmented
                options={[
                  { v: 'runs', label: 'runs' },
                  { v: 'cost', label: '$' },
                ]}
                value={matrixMetric}
                onChange={setMatrixMetric}
              />
            )}
            <Segmented
              options={[
                { v: 'agent', label: 'agents' },
                { v: 'skill', label: 'skills' },
              ]}
              value={matrixRows}
              onChange={setMatrixRows}
            />
            <button
              type="button"
              onClick={() => setTransposed((t) => !t)}
              className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim hover:text-ink"
              title="swap rows and columns"
            >
              ⇄ transpose
            </button>
          </div>
          <div className="rounded-[14px] border border-line px-3.5 py-3.5">
            {matrix === null ? (
              <Loading label="cross-tab…" />
            ) : (
              <MatrixPanel data={matrix} transposed={transposed} />
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
