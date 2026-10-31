// What the session rail says, derived from data the session detail already holds
// (Swarmery v4). Nothing here is fetched and nothing is invented: a block with no
// data gets an empty state from the component, never a placeholder row.

import { t } from '@lingui/core/macro';
import type { Event, FileChange, SessionDetail, UsageAccount, UsageProvider, UsageWindow } from '../api/types';
import type { GlyphState } from '../components/place/StateGlyph';
import { plansTaskHref } from '../pages/plans/plansUrl';
import { fmtDurationMs } from './format';
import { subagentDescription, subagentName } from './payload';
import { DEFAULT_ACCOUNT } from './sessionAccount';
import { compactSpan, type RowState } from './sessionsView';

const MAX_SUBAGENTS = 5;
/** How many areas the rail lists; the data keeps them all. */
export const MAX_TOUCHED_AREAS = 4;
/** The design's line for a session worth a second look at what it spends. */
const HEAVY_COST_USD = 20;
/** Past these a handoff is urgent: the numbers strip's red and advisor R9's line. */
const HOT_CONTEXT_PCT = 55;
const HOT_CONTEXT_TOKENS = 300_000;

export interface SwarmRow {
  key: string;
  name: string;
  state: GlyphState;
  note: string;
}

export interface SwarmBlock {
  rows: SwarmRow[];
  /** Every agent, the main model included; `rows` may show fewer. */
  total: number;
  /** Earlier subagents left out of `rows`. */
  hidden: number;
}

function subagentState(stop: Event | undefined, live: boolean): GlyphState {
  if (stop === undefined) return live ? 'working' : 'stopped';
  return stop.status === 'error' || stop.status === 'denied' || stop.status === 'timeout' ? 'failed' : 'done';
}

function subagentRow(event: Event, events: readonly Event[], live: boolean): SwarmRow {
  const stop = events.find((e) => e.type === 'subagent_stop' && e.parentEventId === event.id);
  const ms = stop?.durationMs ?? null;
  return {
    key: String(event.id),
    name: subagentName(event),
    state: subagentState(stop, live),
    note: [subagentDescription(event), ms === null ? null : fmtDurationMs(ms)]
      .filter((part) => part !== null)
      .join(' · '),
  };
}

/** The main model, then the subagents as they started, the latest few only: a long
 * session runs dozens, and the Steps folds keep the full list. */
export function swarmRows(detail: SessionDetail, state: RowState, nowMs: number): SwarmBlock {
  const steps = String(detail.events.length);
  const end = detail.endedAt === null ? nowMs : new Date(detail.endedAt).getTime();
  const span = compactSpan(end - new Date(detail.startedAt).getTime());
  const note =
    state === 'needs'
      ? t`paused on your decision`
      : state === 'working'
        ? t`step ${steps}`
        : state === 'failed'
          ? t`failed after ${span}`
          : t`finished in ${span}`;
  const model = detail.modelLast ?? detail.model ?? t`model`;
  const main: SwarmRow = { key: 'main', name: t`main · ${model}`, state, note };
  const live = state === 'working' || state === 'needs';
  const started = detail.events.filter((e) => e.type === 'subagent_start');
  const shown = started.slice(-MAX_SUBAGENTS);
  return {
    rows: [main, ...shown.map((e) => subagentRow(e, detail.events, live))],
    total: started.length + 1,
    hidden: started.length - shown.length,
  };
}

export interface LinkedRow {
  key: string;
  text: string;
  sub: string;
  to?: string | undefined;
}

/** What this session belongs to. No PR row: a session carries no such field (D-5). */
export function linkedRows(detail: SessionDetail, slug: string): LinkedRow[] {
  const rows: LinkedRow[] = [];
  const plan = detail.planGroup;
  if (plan != null) {
    const seq = plan.role === 'phase' ? plan.phaseSeq : null;
    rows.push({
      key: 'plan',
      text: plan.title,
      sub: seq === null ? t`plan run` : t`plan · phase ${String(seq)}`,
      to: plansTaskHref(slug, plan.taskId, seq === null ? {} : { phase: seq }),
    });
  }
  if (detail.taskExternalId != null) {
    rows.push({ key: 'task', text: detail.taskExternalId, sub: t`tracker task` });
  }
  return rows;
}

/** How much care an area's changes ask for, worst first: the design's order. */
export type AreaRisk = 'deletes' | 'sensitive' | 'new' | 'edits';

export interface TouchedArea {
  area: string;
  files: number;
  risk: AreaRisk;
}

export interface TouchedBlock {
  /** Every area, the busiest first. */
  areas: TouchedArea[];
  /** Distinct files, across every area and not only the ones shown. */
  total: number;
}

function repoRelative(path: string, cwd: string | null): string {
  if (cwd === null) return path;
  const root = cwd.endsWith('/') ? cwd : `${cwd}/`;
  return path.startsWith(root) ? path.slice(root.length) : path;
}

const SENSITIVE_AREA = /migration|billing|payment/;

function areaRisk(area: string, types: ReadonlySet<FileChange['changeType']>): AreaRisk {
  if (types.has('delete')) return 'deletes';
  if (SENSITIVE_AREA.test(area)) return 'sensitive';
  return types.has('create') ? 'new' : 'edits';
}

function areaOf(path: string): string {
  return path
    .split('/')
    .filter((segment) => segment !== '')
    .slice(0, 2)
    .join('/');
}

/** Files grouped by their first two path segments below the session's cwd. */
export function touchedAreas(changes: readonly FileChange[], cwd: string | null): TouchedBlock {
  const byArea = new Map<string, { paths: Set<string>; types: Set<FileChange['changeType']> }>();
  const all = new Set<string>();
  for (const change of changes) {
    const path = repoRelative(change.filePath, cwd);
    const area = areaOf(path);
    const seen = byArea.get(area) ?? { paths: new Set(), types: new Set() };
    seen.paths.add(path);
    seen.types.add(change.changeType);
    byArea.set(area, seen);
    all.add(path);
  }
  return {
    // A stable sort: areas with the same count keep the order they were first touched.
    areas: [...byArea]
      .map(([area, { paths, types }]) => ({ area, files: paths.size, risk: areaRisk(area, types) }))
      .sort((a, b) => b.files - a.files),
    total: all.size,
  };
}

/** `run` over `items`, at most `limit` at a time, the results in `items` order. The
 * first rejection rejects the whole. */
export async function mapLimited<T, R>(items: readonly T[], limit: number, run: (item: T) => Promise<R>): Promise<R[]> {
  const results = new Array<R>(items.length);
  let next = 0;
  const worker = async (): Promise<void> => {
    for (let at = next; at < items.length; at = next) {
      next += 1;
      results[at] = await run(items[at] as T);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

/** The plan window of a set of usage cards: the first healthy card's session window,
 * else its first. One rule for the sidebar footer and the rail's account row. */
export function planWindow(providers: readonly UsageProvider[]): UsageWindow | null {
  const healthy = providers.find((p) => p.status === 'ok');
  if (healthy === undefined) return null;
  return healthy.windows.find((w) => w.key.startsWith('session')) ?? healthy.windows[0] ?? null;
}

/** The plan window of one subscription, and nothing rather than another account's. */
export function accountWindow(
  accounts: readonly UsageAccount[],
  defaultProviders: readonly UsageProvider[],
  key: string,
): UsageWindow | null {
  const own = accounts.find((a) => a.account === key)?.providers;
  return planWindow(own ?? (key === DEFAULT_ACCOUNT ? defaultProviders : []));
}

export function isHeavy(costUsd: number | null): boolean {
  return costUsd !== null && costUsd > HEAVY_COST_USD;
}

/** A handoff turns red once the context it escapes from is fat. */
export function isHotHandoff(detail: SessionDetail): boolean {
  return (
    (detail.link?.contextPct ?? 0) >= HOT_CONTEXT_PCT ||
    (detail.handoff?.contextTokens ?? 0) >= HOT_CONTEXT_TOKENS
  );
}
