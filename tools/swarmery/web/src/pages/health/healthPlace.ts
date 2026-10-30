// Health's list (handoff §3, §9): the seven old tabs as accordion groups, every
// one of them holding at least one row, so an old `?tab=` always lands on something.
// A group whose tab used to be one big board keeps that board as its first row and
// lists the single things on it below, which is what the group counts.

import { plural, t } from '@lingui/core/macro';
import type { AgentChangeProposal, Recommendation, RetroAgentRow, RetroAgentsResp, RetroFrictionResp } from '../../api/types';
import type { PageTone, PlaceGroup, PlaceItem } from '../../components/place/PageDetail';
import { fmtAgo } from '../../lib/format';
import {
  type HealthTab,
  countProposals,
  countRecs,
  fmtPct,
  frictionCount,
  topAgents,
  uncoveredDenied,
  untriagedErrors,
  windowCell,
} from './healthModel';

/** Above this failed share an agent is called out, as the design's 25 % line. */
export const HOT_FAILED_PCT = 25;

/** Below this change in points an agent reads as flat. */
const MOVED_POINTS = 2;

export const OPEN_RECS = ['proposed'] as const;
export const OPEN_PROPOSALS = ['proposed', 'needs_target'] as const;

export interface AgentMove {
  now: number;
  /** Null when the window before had no runs. */
  prev: number | null;
  /** prev − now, rounded: positive is better. */
  drop: number | null;
}

export function agentMove(row: RetroAgentRow): AgentMove {
  const now = row.error_rate * 100;
  const prev = row.prev.runs > 0 ? row.prev.error_rate * 100 : null;
  return { now, prev, drop: prev === null ? null : Math.round(prev - now) };
}

/** Agents by failed share, worst first (handoff §9); ties by runs. */
export function agentsByFailure(a: RetroAgentsResp): RetroAgentRow[] {
  return [...a.agents].sort((x, y) => y.error_rate - x.error_rate || y.runs - x.runs);
}

function agentItem(row: RetroAgentRow): PlaceItem {
  const { now, prev, drop } = agentMove(row);
  const hot = now >= HOT_FAILED_PCT;
  const down = drop !== null && drop >= MOVED_POINTS;
  const up = drop !== null && drop <= -MOVED_POINTS;
  const runs = row.runs;
  const head = plural(runs, { one: '# run', few: '# runs', many: '# runs', other: '# runs' });
  const failed = fmtPct(now);
  const was = prev !== null ? fmtPct(prev) : '';
  const arrow = down ? '↓' : up ? '↑' : '→';
  const rightTone: PageTone = down ? 'green' : up ? 'red' : 'faint';
  return {
    key: `agent:${row.agent}`,
    glyph: hot ? '●' : '○',
    glyphSmall: true,
    glyphTone: hot ? 'brand' : down ? 'green' : 'faint',
    title: row.agent,
    summary: down ? t`${head} · ${failed} failed · was ${was}` : t`${head} · ${failed} failed`,
    ...(drop !== null ? { right: `${arrow}${String(Math.abs(drop))}`, rightTone } : {}),
  };
}

export interface HealthPlaceInput {
  agents: RetroAgentsResp | null;
  friction: RetroFrictionResp | null;
  recs: readonly Recommendation[] | null;
  proposals: readonly AgentChangeProposal[] | null;
  days: number;
}

function fleetSummary(agents: RetroAgentsResp): string {
  const failed = fmtPct(windowCell(agents).failedPct);
  const agent = topAgents(agents, 1)[0]?.agent;
  return agent === undefined ? t`${failed} failed` : t`${failed} failed · ${agent} moved it most`;
}

function removableSummary(friction: RetroFrictionResp): string | undefined {
  const denied = uncoveredDenied(friction)[0];
  const repeated = untriagedErrors(friction)[0];
  const parts: string[] = [];
  if (denied !== undefined) {
    const tool = denied.tool;
    const count = String(denied.denied);
    parts.push(t`${tool} denied ${count}×`);
  }
  if (repeated !== undefined) {
    const count = String(repeated.count);
    parts.push(t`same error ${count}×`);
  }
  return parts.length > 0 ? parts.join(' · ') : undefined;
}

export function advisorOpenCount(
  recs: readonly Recommendation[] | null,
  proposals: readonly AgentChangeProposal[] | null,
): number {
  return (
    (recs !== null ? countRecs(recs, OPEN_RECS) : 0) + (proposals !== null ? countProposals(proposals, OPEN_PROPOSALS) : 0)
  );
}

export function healthGroups({ agents, friction, recs, proposals, days }: HealthPlaceInput): PlaceGroup[] {
  const window = `${String(days)} d`;
  const removable = friction !== null ? frictionCount(friction) : 0;
  const removableText = friction !== null ? removableSummary(friction) : undefined;
  const deniedTools = friction !== null ? uncoveredDenied(friction) : [];
  const errors = friction !== null ? untriagedErrors(friction) : [];
  const openRecs = (recs ?? []).filter((r) => (OPEN_RECS as readonly string[]).includes(r.status));
  const advisorOpen = advisorOpenCount(recs, proposals);
  const spent = agents !== null ? String(Math.round(windowCell(agents).costUsd)) : null;
  const agentCount = agents?.agents.length ?? 0;

  return [
    {
      key: 'overview' satisfies HealthTab,
      label: t`Overview`,
      items: [
        {
          key: 'fleet',
          glyph: '♡',
          glyphTone: 'brand',
          title: t`The one sentence`,
          ...(agents !== null ? { summary: fleetSummary(agents) } : {}),
          age: window,
        },
        {
          key: 'removable',
          glyph: '◇',
          title: t`Friction you can remove today`,
          ...(removableText !== undefined ? { summary: removableText } : {}),
          ...(removable > 0 ? { right: String(removable), rightTone: 'brand' as const } : {}),
        },
      ],
    },
    {
      key: 'friction' satisfies HealthTab,
      label: t`Friction`,
      tone: 'amber',
      count: removable,
      items: [
        {
          key: 'board',
          glyph: '▤',
          title: t`Friction board`,
          summary: t`denied tools · repeating errors · approvals`,
        },
        ...deniedTools.map((d): PlaceItem => {
          const tool = d.tool;
          const denied = String(d.denied);
          const calls = String(d.calls);
          return {
            key: `denied:${tool}`,
            glyph: '▲',
            glyphTone: 'brand',
            title: t`${tool} denied ${denied} of ${calls} times`,
            summary: t`no rule covers it · + always allow`,
          };
        }),
        ...errors.map((g): PlaceItem => {
          const count = String(g.count);
          const sessions = String(g.sessions.length);
          return {
            key: `error:${g.key}`,
            glyph: '✗',
            glyphTone: 'red',
            title: g.example,
            summary: t`same error ${count}× in ${sessions} sessions`,
            age: fmtAgo(g.last_ts),
          };
        }),
      ],
    },
    {
      key: 'estimates' satisfies HealthTab,
      label: t`Estimates`,
      items: [
        { key: 'accuracy', glyph: '≈', title: t`Estimation accuracy`, summary: t`tasks against their estimate · retro lessons` },
        { key: 'honesty', glyph: '≈', title: t`Forecast honesty`, summary: t`how close forecasts land` },
      ],
    },
    {
      key: 'phaseruns' satisfies HealthTab,
      label: t`Phase runs`,
      items: [{ key: 'outcomes', glyph: '▤', title: t`Outcomes`, summary: t`how plan-phase runs ended`, age: window }],
    },
    {
      key: 'advisor' satisfies HealthTab,
      label: t`Advisor`,
      tone: 'blue',
      count: advisorOpen,
      items: [
        {
          key: 'all',
          glyph: '▤',
          title: t`Findings and rewrites`,
          summary: t`the review digest · recommendations · agent rewrites`,
        },
        ...openRecs.map((rec): PlaceItem => {
          const rule = rec.rule;
          const target = rec.target;
          return {
            key: `rec:${String(rec.id)}`,
            glyph: '●',
            glyphSmall: true,
            glyphTone: 'blue',
            title: rec.title,
            summary: t`${rule} · ${target} · also in Inbox`,
            age: fmtAgo(rec.created_at),
          };
        }),
      ],
    },
    {
      key: 'cost' satisfies HealthTab,
      label: t`Cost & tokens`,
      items: [
        {
          key: 'spend',
          glyph: '$',
          title: t`Spend · ${window}`,
          ...(spent !== null ? { summary: t`$${spent} in this window` } : {}),
          age: window,
        },
      ],
    },
    {
      key: 'agents' satisfies HealthTab,
      label: t`Agents`,
      count: agentCount,
      items: [
        { key: 'all', glyph: '☷', title: t`All agents`, summary: t`scorecards · judged sessions` },
        ...(agents !== null ? agentsByFailure(agents).map(agentItem) : []),
      ],
    },
  ];
}
