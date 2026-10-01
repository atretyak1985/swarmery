// Health (Canvas v3 phase 5, artboard 2c): one place for "how is the agent
// fleet doing and what should change", replacing /analytics and /retro.
//
// A sticky StatusStrip answers the three questions (this window · waiting on
// you · because of you) over ONE date range, then tabs: Overview · Agents ·
// Friction · Estimates · Advisor · Cost & tokens. The range and the tab both
// live in the URL (`?days=30&tab=agents`), so a view is shareable and the old
// routes can redirect onto a specific tab. The retro and analytics pages render
// embedded (their own range rows hidden) and are lazy — Analytics pulls in
// Recharts, so it loads only when the Cost tab is opened.

import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  type AnalyticsRange,
  fetchProjectRecommendations,
  fetchProposals,
  fetchRecommendations,
  fetchRetroAgents,
  fetchRetroFriction,
} from '../../api';
import type {
  AgentChangeProposal,
  HealthAutoMode,
  Recommendation,
  RetroAgentsResp,
  RetroFrictionResp,
} from '../../api/types';
import { StatusStrip, type StatusCell } from '../../components/StatusStrip';
import { type TabItem, Tabs, useTabParam } from '../../components/Tabs';
import { Loading } from '../../components/ui';
import { fmtAgo, isoDay } from '../../lib/format';
import { useHealth } from '../../lib/health';
import { PLACES, type PlaceId } from '../../lib/nav';
import { useScope } from '../../lib/scope';
import { HealthOverview } from './HealthOverview';
import {
  type AutoModeTone,
  DEFAULT_DAYS,
  HEALTH_PRESETS,
  HEALTH_TABS,
  type HealthPreset,
  type HealthTab,
  autoModeRow,
  becauseText,
  countProposals,
  countRecs,
  daysFromParam,
  frictionCount,
  rangeFor,
  waitingText,
  windowCell,
  windowCellText,
} from './healthModel';

const Retro = lazy(() => import('../Retro').then((m) => ({ default: m.Retro })));
const Analytics = lazy(() => import('../Analytics').then((m) => ({ default: m.Analytics })));

const RANGE_OPTIONS = HEALTH_PRESETS.map((d) => ({ value: String(d), label: `${String(d)} d` }));

/** Statuses the strip counts, fetched in one call and split client-side. */
const OPEN_RECS = ['proposed'] as const;
const VERIFIED_RECS = ['verified'] as const;
const GATHERING_RECS = ['accepted', 'adopted'] as const;
const OPEN_PROPOSALS = ['proposed', 'needs_target'] as const;

function placeHref(id: PlaceId, slug: string | null): string {
  return PLACES.find((p) => p.id === id)?.href(slug) ?? '/';
}

const AUTO_MODE_TONE: Record<AutoModeTone, string> = {
  quiet: 'text-ink-faint',
  seen: 'text-amber',
  alerting: 'text-red',
};

/**
 * Whether Claude Code's server-side auto mode permission check is answering —
 * a fact about the machine, not about the range or the project, so it sits
 * above the Overview rather than inside a windowed section. The state is
 * carried by the words, the colour only repeats it.
 */
function AutoModeRow({ mode, inboxHref }: { mode: HealthAutoMode; inboxHref: string }): JSX.Element {
  const row = autoModeRow(mode, mode.lastAt !== null ? fmtAgo(mode.lastAt) : null);
  return (
    <div
      role="status"
      aria-label="Auto mode permission check"
      className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-line px-4 py-2.5 desk:px-7"
    >
      <span className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
        Auto mode permission check
      </span>
      <span className={`font-mono text-[11px] ${AUTO_MODE_TONE[row.tone]}`}>{row.text}</span>
      {mode.alerting && (
        <Link to={inboxHref} className="font-mono text-[11px] font-semibold text-red hover:underline">
          alerting → Inbox
        </Link>
      )}
    </div>
  );
}

/** `?days=` as the one range for every tab; the default is left out of the URL. */
function useDaysParam(): [HealthPreset, (days: string) => void] {
  const [params, setParams] = useSearchParams();
  const days = daysFromParam(params.get('days'));
  const setDays = useCallback(
    (raw: string): void => {
      const d = daysFromParam(raw);
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (d === DEFAULT_DAYS) next.delete('days');
          else next.set('days', String(d));
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  return [days, setDays];
}

export function Health(): JSX.Element {
  const { slug } = useParams<{ slug: string }>();
  const projectSlug = slug ?? null;
  const { scope } = useScope();
  const [tab, setTab] = useTabParam<HealthTab>('tab', HEALTH_TABS, 'overview');
  const [days, setDays] = useDaysParam();
  const today = isoDay();

  const range = useMemo<AnalyticsRange & { from: string; to: string }>(
    () => ({ ...rangeFor(days, today), ...(scope !== null ? { project: scope } : {}) }),
    [days, today, scope],
  );

  const [agents, setAgents] = useState<RetroAgentsResp | null>(null);
  const [friction, setFriction] = useState<RetroFrictionResp | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [recs, setRecs] = useState<Recommendation[] | null>(null);
  const [proposals, setProposals] = useState<AgentChangeProposal[] | null>(null);
  // Absent while the daemon is unreachable or older than the field: no row.
  const autoMode = useHealth().health?.autoModeClassifier;

  const load = useCallback((): void => {
    setError(null);
    setAgents(null);
    fetchRetroAgents(range)
      .then(setAgents)
      .catch((e: unknown) => setError(String(e)));
    fetchRetroFriction(range)
      .then(setFriction)
      .catch(() => setFriction(null));
  }, [range]);
  useEffect(load, [load]);

  // Decisions are not windowed: loaded once per scope. On a project page the
  // recommendations are the project's own, as on the project Today, so the
  // strip counts match the project Inbox it links to.
  useEffect(() => {
    const status = [...OPEN_RECS, ...VERIFIED_RECS, ...GATHERING_RECS].join(',');
    (projectSlug === null ? fetchRecommendations(status) : fetchProjectRecommendations(projectSlug, status))
      .then((r) => setRecs(r.recommendations))
      .catch(() => setRecs(null));
    fetchProposals(OPEN_PROPOSALS.join(','))
      .then((r) => setProposals(r.proposals))
      .catch(() => setProposals(null));
  }, [projectSlug]);

  const cells = useMemo<StatusCell[]>(() => {
    const out: StatusCell[] = [];
    if (agents !== null) {
      const c = windowCell(agents);
      const t = windowCellText(c);
      out.push({
        label: 'this window',
        value: t.value,
        delta: (
          <>
            {t.delta !== null && <span className={t.better ? 'text-green' : 'text-red'}>{t.delta}</span>}
            {` · $${String(Math.round(c.costUsd))} spent`}
          </>
        ),
      });
    } else {
      out.push({ label: 'this window', value: '…' });
    }
    const findings = recs !== null ? countRecs(recs, OPEN_RECS) : 0;
    const rewrites = proposals !== null ? countProposals(proposals, OPEN_PROPOSALS) : 0;
    out.push({
      label: 'waiting on you',
      value: waitingText(findings, rewrites),
      delta: <span className="text-amber">→ Inbox</span>,
      href: placeHref('inbox', projectSlug),
      tone: findings + rewrites > 0 ? 'amber' : 'neutral',
    });
    out.push({
      label: 'because of you',
      value: becauseText(
        recs !== null ? countRecs(recs, VERIFIED_RECS) : 0,
        recs !== null ? countRecs(recs, GATHERING_RECS) : 0,
      ),
      delta: <span className="text-ink-faint">→ Proof</span>,
      href: `${placeHref('learning', projectSlug)}?tab=proof`,
    });
    return out;
  }, [agents, recs, proposals, projectSlug]);

  const advisorOpen =
    recs !== null || proposals !== null
      ? (recs !== null ? countRecs(recs, OPEN_RECS) : 0) +
        (proposals !== null ? countProposals(proposals, OPEN_PROPOSALS) : 0)
      : null;

  const tabs: TabItem<HealthTab>[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'agents', label: 'Agents', ...(agents !== null ? { count: agents.agents.length } : {}) },
    {
      id: 'friction',
      label: 'Friction',
      ...(friction !== null && frictionCount(friction) > 0 ? { count: frictionCount(friction) } : {}),
    },
    { id: 'estimates', label: 'Estimates' },
    {
      id: 'advisor',
      label: 'Advisor',
      ...(advisorOpen !== null && advisorOpen > 0 ? { count: `${String(advisorOpen)} open` } : {}),
    },
    { id: 'cost', label: 'Cost & tokens' },
  ];

  return (
    <div>
      <StatusStrip
        title="Health"
        subtitle={
          projectSlug === null
            ? 'how the agent fleet is doing and what to change'
            : "how this project's agents are doing and what to change"
        }
        range={{ value: String(days), options: RANGE_OPTIONS, onChange: setDays }}
        cells={cells}
        tabs={<Tabs tabs={tabs} value={tab} onChange={setTab} ariaLabel="Health" />}
      />
      {tab === 'overview' && autoMode !== undefined && (
        <AutoModeRow mode={autoMode} inboxHref={`${placeHref('inbox', projectSlug)}?tab=alerts`} />
      )}
      <div role="tabpanel" aria-label={tabs.find((t) => t.id === tab)?.label}>
        {tab === 'overview' ? (
          <HealthOverview
            agents={agents}
            friction={friction}
            error={error}
            onRetry={load}
            onTab={setTab}
          />
        ) : tab === 'cost' ? (
          <Suspense fallback={<Loading label="cost…" />}>
            <Analytics range={{ from: range.from, to: range.to }} />
          </Suspense>
        ) : (
          <Suspense fallback={<Loading label={`${tab}…`} />}>
            <Retro section={tab} range={range} />
          </Suspense>
        )}
      </div>
    </div>
  );
}
