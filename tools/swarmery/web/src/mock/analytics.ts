// Offline analytics fixtures for VITE_MOCK=1 — deterministic pseudo-random
// series so the Analytics page renders without the Go daemon. Shapes mirror
// the frozen contract in ../api/types.ts. Values are seeded by (key, day) so a
// reload is stable and totals look plausible, not the real attribution logic.

import type {
  AnalyticsDimension,
  AnalyticsMetric,
  BreakdownResp,
  BreakdownRow,
  MatrixResp,
  TimeseriesResp,
} from '../api/types';
import { addDays, isoDay } from '../lib/format';
import { mockProjects } from './data';

interface Range {
  from?: string;
  to?: string;
}

const AGENTS = [
  'tech-lead',
  'debugger',
  'security-auditor',
  'test-writer',
  'react-specialist',
  'api-designer',
];
const SKILLS = ['code-review', 'brainstorming', 'commit', 'frontend-design', 'testing'];
const MODELS = ['claude-opus-4-8', 'claude-sonnet-4-6', 'claude-haiku-4-5'];

/** Stable [0,1) hash of an arbitrary seed string. */
function rand(seed: string): number {
  let h = 2166136261;
  for (let i = 0; i < seed.length; i += 1) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return (h >>> 8) / 0xffffff;
}

function resolveDays(range: Range): string[] {
  const to = range.to ?? isoDay();
  const from = range.from ?? addDays(to, -13);
  const days: string[] = [];
  for (let d = from; d <= to; d = addDays(d, 1)) {
    days.push(d);
    if (days.length > 400) break;
  }
  return days;
}

function membersOf(dim: AnalyticsDimension): { key: string; name: string }[] {
  switch (dim) {
    case 'project':
      return mockProjects.map((p) => ({ key: p.slug, name: p.name ?? p.slug }));
    case 'model':
      return MODELS.map((m) => ({ key: m, name: m }));
    case 'agent':
      return AGENTS.map((a) => ({ key: a, name: a }));
    case 'skill':
      return SKILLS.map((s) => ({ key: s, name: s }));
  }
}

/** Per-member per-day magnitude for a metric (0 on "quiet" days). */
function magnitude(metric: AnalyticsMetric, key: string, day: string): number {
  const active = rand(`${key}|${day}|on`);
  if (active < 0.35) return 0; // quiet day
  const base = rand(`${key}|${day}|v`);
  if (metric === 'cache') return Number((0.55 + base * 0.42).toFixed(3));
  if (metric === 'cost') return Number((base * 3.2).toFixed(2));
  if (metric === 'tokens') return Math.round(base * 380_000);
  return Math.round(base * 6); // runs
}

export function mockTimeseries(
  metric: AnalyticsMetric,
  group: AnalyticsDimension,
  range: Range,
): TimeseriesResp {
  const buckets = resolveDays(range);
  const series = membersOf(group)
    .map(({ key, name }) => {
      const values = buckets.map((day) => magnitude(metric, key, day));
      const total =
        metric === 'cache'
          ? Number(
              (
                values.reduce((a, v) => a + v, 0) /
                Math.max(1, values.filter((v) => v > 0).length)
              ).toFixed(3),
            )
          : Number(values.reduce((a, v) => a + v, 0).toFixed(2));
      return { key, name, total, values };
    })
    .filter((s) => s.total > 0)
    .sort((a, b) => b.total - a.total || a.key.localeCompare(b.key));

  return {
    from: buckets[0] ?? isoDay(),
    to: buckets[buckets.length - 1] ?? isoDay(),
    metric,
    group,
    buckets,
    series,
    approx: false,
    ...(metric === 'cache'
      ? {
          cache: {
            hit_rate: 0.872,
            cache_read_tokens: 41_200_000,
            input_tokens: 6_050_000,
            saved_usd: 312.4,
          },
        }
      : {}),
  };
}

export function mockBreakdown(by: AnalyticsDimension, range: Range): BreakdownResp {
  const days = resolveDays(range);
  // project/model & agent carry cost/tokens (agent = phase 2 exact $); agent &
  // skill also carry run counts; skill has no $.
  const hasCost = by === 'project' || by === 'model' || by === 'agent';
  const hasRuns = by === 'agent' || by === 'skill';

  const rows: BreakdownRow[] = membersOf(by).map(({ key, name }) => {
    const cost = hasCost
      ? Number(days.reduce((a, d) => a + magnitude('cost', key, d), 0).toFixed(2))
      : 0;
    const tokens = hasCost ? days.reduce((a, d) => a + magnitude('tokens', key, d), 0) : 0;
    const tokensIn = Math.round(tokens * 0.78);
    const runs = hasRuns ? days.reduce((a, d) => a + magnitude('runs', key, d), 0) : 0;
    return {
      key,
      name,
      cost_usd: hasCost && cost > 0 ? cost : null,
      tokens_in: hasCost ? tokensIn : null,
      tokens_out: hasCost ? tokens - tokensIn : null,
      tokens_cache_read: hasCost ? Math.round(tokens * 6.4) : null,
      cache_hit_rate: hasCost ? Number((0.6 + rand(`${key}|hr`) * 0.35).toFixed(3)) : null,
      runs: hasRuns ? runs : null,
      sessions: hasRuns ? Math.max(1, Math.round(runs * 0.5)) : Math.round(rand(`${key}|sess`) * 12) + 1,
      last_used: hasRuns && runs > 0 ? `${days[days.length - 1] ?? isoDay()}T14:20:00.000Z` : null,
    };
  });

  // Rank by the primary measure: cost when present, else runs.
  const primary = (r: BreakdownRow): number => (hasCost ? (r.cost_usd ?? 0) : (r.runs ?? 0));
  return rows.filter((r) => primary(r) > 0 || (r.runs ?? 0) > 0).sort((a, b) => primary(b) - primary(a));
}

export function mockMatrix(
  rowsDim: 'agent' | 'skill',
  metric: 'runs' | 'cost',
  range: Range,
): MatrixResp {
  const days = resolveDays(range);
  // cost is an agent-only metric (phase 2); skills fall back to runs.
  const effMetric = rowsDim === 'skill' ? 'runs' : metric;
  const rowMembers = membersOf(rowsDim);
  const colMembers = mockProjects.map((p) => ({ key: p.slug, name: p.name ?? p.slug }));

  const cells: MatrixResp['cells'] = [];
  const rowTotals = new Map<string, number>();
  const colTotals = new Map<string, number>();
  for (const rmem of rowMembers) {
    for (const cmem of colMembers) {
      const v = days.reduce((a, d) => a + magnitude(effMetric, `${rmem.key}|${cmem.key}`, d), 0);
      if (v === 0) continue;
      cells.push(
        effMetric === 'cost'
          ? { row: rmem.key, col: cmem.key, runs: 0, cost: Number(v.toFixed(2)) }
          : { row: rmem.key, col: cmem.key, runs: v },
      );
      rowTotals.set(rmem.key, (rowTotals.get(rmem.key) ?? 0) + v);
      colTotals.set(cmem.key, (colTotals.get(cmem.key) ?? 0) + v);
    }
  }
  const rank =
    (totals: Map<string, number>) =>
    (a: { key: string }, b: { key: string }): number =>
      (totals.get(b.key) ?? 0) - (totals.get(a.key) ?? 0) || a.key.localeCompare(b.key);

  return {
    metric: effMetric,
    rows: rowMembers.filter((m) => (rowTotals.get(m.key) ?? 0) > 0).sort(rank(rowTotals)),
    cols: colMembers.filter((m) => (colTotals.get(m.key) ?? 0) > 0).sort(rank(colTotals)),
    cells,
  };
}
