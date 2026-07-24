// Offline analytics fixtures for VITE_MOCK=1 — deterministic pseudo-random
// series so the Analytics page renders without the Go daemon. Shapes mirror
// the frozen contract in ../api/types.ts. Values are seeded by (key, day) so a
// reload is stable and totals look plausible, not the real attribution logic.

import type {
  AnalyticsDimension,
  AnalyticsMetric,
  AutonomyResp,
  BreakdownResp,
  BreakdownRow,
  DurationsResp,
  ErrorsResp,
  FunnelResp,
  MatrixResp,
  PlaybookRollup,
  ProductivityResp,
  SkillsResp,
  TimeseriesResp,
  ToolsResp,
  UsageResp,
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

const TOOLS = ['Bash', 'Read', 'Edit', 'Grep', 'Write', 'Agent', 'Skill', 'WebFetch'];

const USAGE_AGENTS = ['main', ...AGENTS.slice(0, 3)];

/** Narrow a row to one agent's split (calls/errors from the split; denied
 * unknown per-agent so 0), mirroring the backend ?agent= behaviour. */
function narrowToAgent<T extends { calls: number; errors: number; denied: number; agents: { agent: string; calls: number; errors: number }[] }>(
  row: T,
  agent: string,
): T | null {
  const split = row.agents.find((a) => a.agent === agent);
  if (split === undefined) return null;
  return { ...row, calls: split.calls, errors: split.errors, denied: 0, agents: [split] };
}

export function mockToolStats(range: Range, agent?: string): ToolsResp {
  const days = resolveDays(range);
  let tools = TOOLS.map((tool) => {
    const calls = days.reduce((a, d) => a + Math.round(rand(`${tool}|${d}|c`) * 40), 0);
    const errors = Math.round(calls * rand(`${tool}|err`) * 0.06);
    const denied = tool === 'Bash' ? Math.round(calls * 0.01) : 0;
    const avg = 120 + rand(`${tool}|avg`) * 3000;
    const agents = USAGE_AGENTS.map((a, i) => ({
      agent: a,
      calls: Math.max(1, Math.round(calls * (i === 0 ? 0.6 : 0.13))),
      errors: i === 1 ? errors : 0,
    }));
    return { tool, calls, errors, denied, avg_ms: Math.round(avg), p95_ms: Math.round(avg * 3.2), agents };
  }).filter((t) => t.calls > 0);
  if (agent !== undefined) {
    tools = tools.map((t) => narrowToAgent(t, agent)).filter((t): t is (typeof tools)[number] => t !== null);
  }
  tools.sort((a, b) => b.calls - a.calls);
  return {
    from: days[0] ?? isoDay(),
    to: days[days.length - 1] ?? isoDay(),
    tools,
    agents: USAGE_AGENTS,
    approx: false,
  };
}

export function mockSkillStats(range: Range, agent?: string): SkillsResp {
  const days = resolveDays(range);
  let skills = SKILLS.map((skill) => {
    const calls = days.reduce((a, d) => a + Math.round(rand(`${skill}|${d}|c`) * 8), 0);
    const errors = Math.round(calls * rand(`${skill}|err`) * 0.05);
    const avg = 800 + rand(`${skill}|avg`) * 9000;
    const agents = USAGE_AGENTS.map((a, i) => ({
      agent: a,
      calls: Math.max(1, Math.round(calls * (i === 0 ? 0.55 : 0.15))),
      errors: i === 1 ? errors : 0,
    }));
    return { skill, calls, errors, denied: 0, avg_ms: Math.round(avg), p95_ms: Math.round(avg * 2.6), agents };
  }).filter((s) => s.calls > 0);
  if (agent !== undefined) {
    skills = skills.map((s) => narrowToAgent(s, agent)).filter((s): s is (typeof skills)[number] => s !== null);
  }
  skills.sort((a, b) => b.calls - a.calls);
  return {
    from: days[0] ?? isoDay(),
    to: days[days.length - 1] ?? isoDay(),
    skills,
    agents: USAGE_AGENTS,
    approx: false,
  };
}

export function mockErrorGroups(range: Range): ErrorsResp {
  const days = resolveDays(range);
  const to = days[days.length - 1] ?? isoDay();
  return {
    from: days[0] ?? isoDay(),
    to,
    approx: false,
    groups: [
      {
        key: 'api error # overloaded (request id #)',
        example: 'API Error 529 overloaded (request id req_011abc)',
        count: 7,
        last_ts: `${to}T16:42:00.000Z`,
        samples: [
          { session_id: 1, title: 'Fix flaky auth e2e' },
          { session_id: 3, title: null },
        ],
      },
      {
        key: "error: enoent: no such file or directory, open '/tmp/build-#/out.log'",
        example: "Error: ENOENT: no such file or directory, open '/tmp/build-4821/out.log'",
        count: 2,
        last_ts: `${to}T11:03:00.000Z`,
        samples: [{ session_id: 2, title: 'Projects dashboard polish' }],
      },
    ],
  };
}

export function mockDurations(range: Range): DurationsResp {
  const days = resolveDays(range);
  const resolved = days.length * 2;
  return {
    from: days[0] ?? isoDay(),
    to: days[days.length - 1] ?? isoDay(),
    session_count: days.length * 3,
    avg_session_sec: 1860,
    median_session_sec: 1240,
    approvals_resolved: resolved,
    avg_resolve_sec: 47,
    wait_total_min: Number(((resolved * 47) / 60).toFixed(1)),
  };
}

// --- Analytics uplift fixtures (fusion phase 14) -----------------------------

function rangeBounds(range: Range): { from: string; to: string; days: number } {
  const days = resolveDays(range);
  return { from: days[0] ?? isoDay(), to: days[days.length - 1] ?? isoDay(), days: days.length };
}

export function mockAutonomy(range: Range): AutonomyResp {
  const b = rangeBounds(range);
  const toolCalls = 120 + b.days * 18;
  const approvals = 6 + b.days;
  const userPrompts = 4 + Math.round(b.days / 2);
  const total = approvals + userPrompts;
  return {
    from: b.from,
    to: b.to,
    toolCalls,
    interventions: { approvals, userPrompts, total },
    ratio: Number((toolCalls / Math.max(1, total)).toFixed(2)),
    fullyAutonomous: false,
  };
}

export function mockProductivity(range: Range): ProductivityResp {
  const b = rangeBounds(range);
  const languages = [
    { ext: 'ts', files: 42, loc: 3820 },
    { ext: 'tsx', files: 28, loc: 2610 },
    { ext: 'go', files: 19, loc: 2140 },
    { ext: 'css', files: 7, loc: 540 },
    { ext: 'md', files: 11, loc: 430 },
    { ext: 'other', files: 5, loc: 120 },
  ];
  const loc = languages.reduce((a, l) => a + l.loc, 0);
  const filesModified = languages.reduce((a, l) => a + l.files, 0);
  return {
    from: b.from,
    to: b.to,
    commits: 34 + b.days,
    filesModified,
    loc,
    languages,
    taskDurations: {
      completed: 12 + b.days,
      avgSec: 742,
      medianSec: 610,
      p90Sec: 1580,
      totalActiveMs: (12 + b.days) * 742 * 1000,
    },
    humanHoursSaved: { value: Number((loc / 15).toFixed(1)), formula: 'loc/15', estimate: true },
  };
}

export function mockFunnel(range: Range): FunnelResp {
  const b = rangeBounds(range);
  const columns = [
    { column: 'triage', count: 3, entered: 3 },
    { column: 'todo', count: 5, entered: 5 },
    { column: 'in_progress', count: 4, entered: 4 },
    { column: 'in_review', count: 2, entered: 2 },
    { column: 'done', count: 18, entered: 9 + b.days },
    { column: 'archived', count: 6, entered: 2 },
  ];
  const doneInRange = (9 + b.days) + 2;
  const enteredInRange = doneInRange + 7;
  return {
    from: b.from,
    to: b.to,
    columns,
    enteredInRange,
    doneInRange,
    completionRate: Number((doneInRange / Math.max(1, enteredInRange)).toFixed(2)),
    perDay: Number((doneInRange / Math.max(1, b.days)).toFixed(2)),
    snapshot: true,
  };
}

export function mockPlaybookStats(range: Range): PlaybookRollup[] {
  // Non-empty so the card demos; pre-Phase-13 the real endpoint returns [].
  void range;
  return [
    { playbook: 'standard', tasksDone: 14, inProgress: 3, costUsd: 4.82, tokens: 1_240_000 },
    { playbook: 'bugfix', tasksDone: 8, inProgress: 1, costUsd: 2.15, tokens: 560_000 },
    { playbook: 'research', tasksDone: 3, inProgress: 0, costUsd: 0.94, tokens: 210_000 },
  ];
}

export function mockUsage(): UsageResp {
  const now = Date.now();
  const hr = 3_600_000;
  return {
    configured: true,
    source: 'estimate',
    generatedAt: new Date(now).toISOString(),
    windows: [
      {
        key: 'session5h',
        label: '5-hour session',
        used: 32_500_000,
        limit: 50_000_000,
        usedPct: 0.65,
        // Over pace (Fusion's canonical red state): 65% used at ~55% elapsed.
        pace: 0.65 / 0.55 - 1,
        resetsAt: new Date(now + 2.2 * hr).toISOString(),
        source: 'estimate',
      },
      {
        key: 'weekly',
        label: 'Weekly',
        used: 118_000_000,
        limit: 300_000_000,
        usedPct: 118 / 300,
        // Under pace: ~39% used at ~50% elapsed.
        pace: (118 / 300) / 0.5 - 1,
        resetsAt: new Date(now + 84 * hr).toISOString(),
        source: 'estimate',
      },
    ],
  };
}
