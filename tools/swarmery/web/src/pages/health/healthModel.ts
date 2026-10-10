// Health (Canvas v3 phase 5, artboard 2c): the pure half of the place that
// replaces /analytics and /retro. Everything here is a function of the retro
// payloads — no fetching, no React — so the one-sentence summary, the strip's
// "this window" cell and the tab counts are unit-tested on plain objects.
//
// "Failed" is the retro scorecard's own grain: `error_rate` is the share of
// runs with at least one behavior-fixable error, so failed runs of an agent are
// `error_rate × runs`, and the fleet rate is their sum over the summed runs.

import { plural, t } from '@lingui/core/macro';
import type {
  AgentChangeProposal,
  FrictionTriageState,
  HealthAutoMode,
  Recommendation,
  RetroAgentRow,
  RetroAgentsResp,
  RetroDeniedTool,
  RetroErrorGroup,
  RetroFrictionResp,
} from '../../api/types';
import { addDays } from '../../lib/format';

export type HealthTab = 'overview' | 'agents' | 'friction' | 'estimates' | 'phaseruns' | 'advisor' | 'cost';

export const HEALTH_TABS: readonly HealthTab[] = [
  'overview',
  'agents',
  'friction',
  'estimates',
  'phaseruns',
  'advisor',
  'cost',
];

/** Same presets as the retired Retro/Analytics range rows. */
export const HEALTH_PRESETS = [7, 14, 30, 90] as const;
export type HealthPreset = (typeof HEALTH_PRESETS)[number];
export const DEFAULT_DAYS: HealthPreset = 14;

/** Below this many runs the one sentence refuses to generalise. */
export const MIN_RUNS_FOR_SENTENCE = 10;

/** The fallback's source text, for comparisons; oneSentence returns it in the
 * active locale. A module-level t`` would run before activate(). */
// i18n-ignore — source-locale constant, never rendered (oneSentence uses t`` below)
export const FALLBACK_SENTENCE = 'Not enough runs in this window to say how the fleet is doing.';

function fallbackSentence(): string {
  return t`Not enough runs in this window to say how the fleet is doing.`;
}

/** `?days=` → a preset; anything else reads as the default. */
export function daysFromParam(raw: string | null): HealthPreset {
  const n = Number(raw);
  return (HEALTH_PRESETS as readonly number[]).includes(n) ? (n as HealthPreset) : DEFAULT_DAYS;
}

/** A local-day window ending today, `days` long inclusive (Retro.applyPreset). */
export function rangeFor(days: number, today: string): { from: string; to: string } {
  return { from: addDays(today, -(days - 1)), to: today };
}

function failedRuns(rate: number, runs: number): number {
  return rate * runs;
}

function pct(failed: number, runs: number): number {
  return runs > 0 ? (failed / runs) * 100 : 0;
}

export interface WindowCell {
  runs: number;
  /** Failed-run share of this window, 0–100. */
  failedPct: number;
  /** The same share over the preceding equal window; null when it had no runs. */
  prevFailedPct: number | null;
  /** Subagents + the orchestrator. */
  costUsd: number;
}

export function windowCell(a: RetroAgentsResp): WindowCell {
  let runs = 0;
  let failed = 0;
  let prevRuns = 0;
  let prevFailed = 0;
  let cost = a.main.cost_usd;
  for (const row of a.agents) {
    runs += row.runs;
    failed += failedRuns(row.error_rate, row.runs);
    prevRuns += row.prev.runs;
    prevFailed += failedRuns(row.prev.error_rate, row.prev.runs);
    cost += row.cost_usd;
  }
  return {
    runs,
    failedPct: pct(failed, runs),
    prevFailedPct: prevRuns > 0 ? pct(prevFailed, prevRuns) : null,
    costUsd: cost,
  };
}

/** Agents ranked by failed runs (error_rate × runs), then by runs. */
export function topAgents(a: RetroAgentsResp, n: number): RetroAgentRow[] {
  return [...a.agents]
    .sort(
      (x, y) =>
        failedRuns(y.error_rate, y.runs) - failedRuns(x.error_rate, x.runs) || y.runs - x.runs,
    )
    .slice(0, n);
}

function windowDays(a: RetroAgentsResp): number | null {
  const from = Date.parse(`${a.from}T00:00:00`);
  const to = Date.parse(`${a.to}T00:00:00`);
  if (Number.isNaN(from) || Number.isNaN(to) || to < from) return null;
  return Math.round((to - from) / 86_400_000) + 1;
}

function trendClause(now: number, prev: number | null, days: number | null): string {
  const dayCount = String(days ?? 0);
  const window = days !== null ? t`the ${dayCount} days before` : t`the window before`;
  const nowPct = fmtPct(now);
  if (prev === null) return t`The fleet failed ${nowPct} of its runs in this window`;
  if (Math.abs(now - prev) < 2) return t`The fleet is failing about as often as ${window}`;
  const prevPct = fmtPct(prev);
  if (now < prev) {
    return now <= prev * 0.55
      ? t`The fleet is failing half as often as ${window}`
      : t`The fleet is failing less often than ${window} (${prevPct} → ${nowPct})`;
  }
  return now >= prev * 1.8
    ? t`The fleet is failing twice as often as ${window}`
    : t`The fleet is failing more often than ${window} (${prevPct} → ${nowPct})`;
}

/**
 * The headline of the Overview tab: which way the fleet's failure rate moved
 * and whose runs moved it. Honest fallback under MIN_RUNS_FOR_SENTENCE runs.
 */
export function oneSentence(a: RetroAgentsResp): string {
  const cell = windowCell(a);
  if (cell.runs < MIN_RUNS_FOR_SENTENCE) return fallbackSentence();
  const head = trendClause(cell.failedPct, cell.prevFailedPct, windowDays(a));

  // Whose failed-run count changed the most, and how much of the total change.
  const deltas = a.agents.map((row) => ({
    agent: row.agent,
    delta: failedRuns(row.error_rate, row.runs) - failedRuns(row.prev.error_rate, row.prev.runs),
  }));
  const total = deltas.reduce((s, d) => s + Math.abs(d.delta), 0);
  const lead = [...deltas].sort((x, y) => Math.abs(y.delta) - Math.abs(x.delta))[0];
  if (cell.prevFailedPct !== null && lead !== undefined && total > 0) {
    const share = Math.abs(lead.delta) / total;
    const agent = lead.agent;
    if (share >= 0.6) return t`${head}; almost all of the change is ${agent}.`;
    if (share >= 0.35) return t`${head}; the biggest part of the change is ${agent}.`;
    return t`${head}; the change is spread across several agents.`;
  }
  const worst = topAgents(a, 1)[0];
  if (worst !== undefined && failedRuns(worst.error_rate, worst.runs) > 0) {
    const agent = worst.agent;
    return t`${head}; most failures come from ${agent}.`;
  }
  return `${head}.`;
}

/** 11.4 → "11 %"; the design's thin-spaced percent. */
export function fmtPct(v: number): string {
  return `${String(Math.round(v))} %`;
}

/** "142 runs · 11 % failed" + the delta vs the previous window, split for tone. */
export function windowCellText(c: WindowCell): { value: string; delta: string | null; better: boolean } {
  const failed = fmtPct(c.failedPct);
  const runs = c.runs;
  const value = plural(runs, {
    one: `# run · ${failed} failed`,
    few: `# runs · ${failed} failed`,
    many: `# runs · ${failed} failed`,
    other: `# runs · ${failed} failed`,
  });
  if (c.prevFailedPct === null || Math.round(c.prevFailedPct) === Math.round(c.failedPct)) {
    return { value, delta: null, better: false };
  }
  const better = c.failedPct < c.prevFailedPct;
  const arrow = better ? '↓' : '↑';
  const prevPct = fmtPct(c.prevFailedPct);
  return { value, delta: t`${arrow} from ${prevPct}`, better };
}

/* ----- friction ----- */

/** Denied tools no approval rule covers yet, most denied first. */
export function uncoveredDenied(f: RetroFrictionResp): RetroDeniedTool[] {
  return f.denied_tools.filter((d) => !d.has_rule).sort((x, y) => y.denied - x.denied);
}

/**
 * Error groups that repeated inside the window, most frequent first. The
 * friction payload carries a count, not the distinct days, so "repeated" is
 * count ≥ 2.
 */
export function repeatedErrors(f: RetroFrictionResp): RetroErrorGroup[] {
  return f.error_groups.filter((g) => g.count >= 2).sort((x, y) => y.count - x.count);
}

const KNOWN_TRIAGE_STATES: readonly FrictionTriageState[] = ['untriaged', 'muted', 'tracked', 'fix_proposed'];

/**
 * A group with no triage block (older daemon), or with a state this client
 * does not know (newer daemon), counts as untriaged.
 */
export function triageStateOf(g: RetroErrorGroup): FrictionTriageState {
  // The state arrives over the wire: a newer daemon may send one this client
  // does not know. Unknown is never shown, sorted or counted as dealt with.
  const state: string | undefined = g.triage?.state;
  return KNOWN_TRIAGE_STATES.find((s) => s === state) ?? 'untriaged';
}

/** Repeated groups nobody has dealt with yet — what the tab counts and the overview offers. */
export function untriagedErrors(f: RetroFrictionResp): RetroErrorGroup[] {
  return repeatedErrors(f).filter((g) => triageStateOf(g) === 'untriaged');
}

/** Friction tab count: removable denials + repeated error groups still untriaged. */
export function frictionCount(f: RetroFrictionResp): number {
  return uncoveredDenied(f).length + untriagedErrors(f).length;
}

/** Agent rows the cap assumes before the agents payload has loaded. */
export const AGENT_ROWS_FALLBACK = 25;

/**
 * Upper bound for a friction + agent run: every untriaged group and every agent
 * row Health holds (25 when agents have not loaded), at least 1. The server's
 * agent source lists a subset of those rows, so the bound holds; the engine
 * ignores the excess.
 */
export function frictionRunCap(friction: RetroFrictionResp | null, agents: RetroAgentsResp | null): number {
  const groups = friction !== null ? untriagedErrors(friction).length : 0;
  return Math.max(1, groups + (agents?.agents.length ?? AGENT_ROWS_FALLBACK));
}

/* ----- the strip's decision cells ----- */

/** The api mock ignores `?status=`; filtering here keeps counts honest on both. */
export function countRecs(recs: readonly Recommendation[], statuses: readonly string[]): number {
  return recs.filter((r) => statuses.includes(r.status)).length;
}

export function countProposals(ps: readonly AgentChangeProposal[], statuses: readonly string[]): number {
  return ps.filter((p) => statuses.includes(p.status)).length;
}

export function waitingText(findings: number, rewrites: number): string {
  const findingsText = plural(findings, {
    one: '# Advisor finding',
    few: '# Advisor findings',
    many: '# Advisor findings',
    other: '# Advisor findings',
  });
  const rewritesText = plural(rewrites, {
    one: '# agent rewrite',
    few: '# agent rewrites',
    many: '# agent rewrites',
    other: '# agent rewrites',
  });
  return t`${findingsText} · ${rewritesText} to approve`;
}

export function becauseText(verified: number, gathering: number): string {
  const changesText = plural(verified, {
    one: '# change',
    few: '# changes',
    many: '# changes',
    other: '# changes',
  });
  const gatheringCount = String(gathering);
  return t`${changesText} verified · ${gatheringCount} gathering proof`;
}

/* ----- the auto mode permission check row ----- */

/** quiet: nothing in the last hour · seen: some, below the alert · alerting: the alert is open. */
export type AutoModeTone = 'quiet' | 'seen' | 'alerting';

export interface AutoModeRowText {
  tone: AutoModeTone;
  text: string;
}

/**
 * The Overview row about Claude Code's server-side auto mode permission check
 * (`autoModeClassifier` of GET /api/health): how many tool calls it left without
 * a verdict in the last hour, in how many sessions, and when last. `lastSeen` is
 * the already-rendered age of `lastAt` ("12 min ago"), null when there is none.
 */
export function autoModeRow(m: HealthAutoMode, lastSeen: string | null): AutoModeRowText {
  const tone: AutoModeTone = m.alerting ? 'alerting' : m.noVerdictLastHour > 0 ? 'seen' : 'quiet';
  if (m.noVerdictLastHour === 0) {
    return { tone, text: t`answering — no check went without a verdict in the last hour` };
  }
  const checks = m.noVerdictLastHour;
  const sessionCount = m.sessionsLastHour;
  const head = plural(checks, {
    one: '# check got no verdict in the last hour',
    few: '# checks got no verdict in the last hour',
    many: '# checks got no verdict in the last hour',
    other: '# checks got no verdict in the last hour',
  });
  const sessions = plural(sessionCount, {
    one: 'in # session',
    few: 'in # sessions',
    many: 'in # sessions',
    other: 'in # sessions',
  });
  const last = lastSeen !== null ? ` · ${t`last ${lastSeen}`}` : '';
  return { tone, text: `${head} · ${sessions}${last}` };
}
