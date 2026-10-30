// Health (v4, handoff §3, §9): one place for "how is the agent fleet doing and
// what should change", in the frame every place shares: the seven old tabs are the
// accordion groups of the list, and the row picked there is a detail page.
//
// `?tab=` is the open group and `?item=` the row in it, `?days=` the one range for
// everything, so a view is shareable and the retired /analytics and /retro routes
// still redirect onto a group. The retro and analytics pages render embedded (their
// own range rows hidden) and lazy, because Analytics pulls in Recharts. What they
// show that the design's blocks cannot express stays there, inside the detail.

import { useLingui } from '@lingui/react/macro';
import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
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
  Recommendation,
  RetroAgentsResp,
  RetroFrictionResp,
} from '../../api/types';
import {
  PageDetail,
  PagePlace,
  type PageRow,
  Segmented,
  usePlaceParams,
} from '../../components/place/PageDetail';
import { Loading } from '../../components/ui';
import { isoDay } from '../../lib/format';
import { useHealth } from '../../lib/health';
import { PLACES, type PlaceId } from '../../lib/nav';
import { useScope } from '../../lib/scope';
import { CalibrationPanel } from '../LessonsCalibration';
import { useTriageRun } from '../inbox/useTriageRun';
import { FrictionTriageStrip } from './FrictionTriageStrip';
import { AgentDetail, DeniedDetail, ErrorDetail, FleetDetail, RecDetail, RemovableDetail } from './HealthOverview';
import { PhaseRunsTab } from './PhaseRunsTab';
import {
  DEFAULT_DAYS,
  HEALTH_PRESETS,
  HEALTH_TABS,
  type HealthPreset,
  type HealthTab,
  becauseText,
  countProposals,
  countRecs,
  daysFromParam,
  frictionRunCap,
  placeSub,
  rangeFor,
  uncoveredDenied,
  untriagedErrors,
  waitingText,
  windowCell,
} from './healthModel';
import { OPEN_PROPOSALS, OPEN_RECS, healthGroups } from './healthPlace';

const Retro = lazy(() => import('../Retro').then((m) => ({ default: m.Retro })));
const Analytics = lazy(() => import('../Analytics').then((m) => ({ default: m.Analytics })));

const RANGE_OPTIONS = HEALTH_PRESETS.map((d) => ({ value: String(d), label: `${String(d)} d` }));

/** Statuses the counts split, fetched in one call. */
const VERIFIED_RECS = ['verified'] as const;
const GATHERING_RECS = ['accepted', 'adopted'] as const;

/** What the Friction group's trigger visits: the error groups and the failing agents, nothing from the Inbox. */
const FRICTION_TRIAGE_KINDS = ['friction', 'agent'] as const;

function placeHref(id: PlaceId, slug: string | null): string {
  return PLACES.find((p) => p.id === id)?.href(slug) ?? '/';
}

/** `?days=` as the one range for every group; the default is left out of the URL. */
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
  const { t } = useLingui();
  const { slug } = useParams<{ slug: string }>();
  const projectSlug = slug ?? null;
  const { scope } = useScope();
  const { tab, item, pick } = usePlaceParams<HealthTab>(HEALTH_TABS, 'overview');
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

  // A mute lifted on the board changes the group count and the overview.
  const reloadFriction = useCallback((): void => {
    fetchRetroFriction(range)
      .then(setFriction)
      .catch(() => setFriction(null));
  }, [range]);

  // The Friction group's own triage trigger. When its run ends, the friction data
  // is refetched twice over, as the unmute path does: Health's copy (group count,
  // overview) and the embedded Retro panel's copy (bumped key).
  const [frictionReloadKey, setFrictionReloadKey] = useState(0);
  const onFrictionRunEnd = useCallback((): void => {
    reloadFriction();
    setFrictionReloadKey((k) => k + 1);
  }, [reloadFriction]);
  const triage = useTriageRun(projectSlug, onFrictionRunEnd, tab === 'friction');
  const [starting, setStarting] = useState(false);
  const [dismissedRunId, setDismissedRunId] = useState<number | null>(null);
  const [dismissedError, setDismissedError] = useState<string | null>(null);
  const lastRun = triage.lastRun;
  const runFailed = lastRun?.status === 'failed';
  const runError = triage.startError ?? (runFailed ? lastRun.error || t`The triage run failed.` : null);
  // One identity per error (this start attempt, or that failed run), as on the Inbox.
  const errorKey =
    triage.startError !== null ? `start:${triage.startError}` : runFailed ? `run:${String(lastRun.id)}` : null;
  const triageError = errorKey !== null && dismissedError === errorKey ? null : runError;
  const startFrictionRun = (): void => {
    if (starting) return;
    setStarting(true);
    setDismissedError(null);
    triage
      .start({ kinds: FRICTION_TRIAGE_KINDS, cap: frictionRunCap(friction, agents) })
      .finally(() => setStarting(false));
  };

  // Decisions are not windowed: loaded once per scope. On a project page the
  // recommendations are the project's own, as on the project Today, so the
  // counts match the project Inbox they link to.
  useEffect(() => {
    const status = [...OPEN_RECS, ...VERIFIED_RECS, ...GATHERING_RECS].join(',');
    (projectSlug === null ? fetchRecommendations(status) : fetchProjectRecommendations(projectSlug, status))
      .then((r) => setRecs(r.recommendations))
      .catch(() => setRecs(null));
    fetchProposals(OPEN_PROPOSALS.join(','))
      .then((r) => setProposals(r.proposals))
      .catch(() => setProposals(null));
  }, [projectSlug]);

  const decisions = useMemo<PageRow[]>(() => {
    const findings = recs !== null ? countRecs(recs, OPEN_RECS) : 0;
    const rewrites = proposals !== null ? countProposals(proposals, OPEN_PROPOSALS) : 0;
    return [
      {
        key: 'waiting',
        glyph: '●',
        glyphTone: findings + rewrites > 0 ? 'amber' : 'faint',
        label: t`waiting on you`,
        sub: waitingText(findings, rewrites),
        right: t`→ Inbox`,
        rightTone: 'amber',
        to: placeHref('inbox', projectSlug),
      },
      {
        key: 'because',
        glyph: '✓',
        label: t`because of you`,
        sub: becauseText(
          recs !== null ? countRecs(recs, VERIFIED_RECS) : 0,
          recs !== null ? countRecs(recs, GATHERING_RECS) : 0,
        ),
        right: t`→ Proof`,
        rightTone: 'faint',
        to: `${placeHref('learning', projectSlug)}?tab=proof`,
      },
    ];
  }, [recs, proposals, projectSlug, t]);

  const groups = healthGroups({ agents, friction, recs, proposals, days });
  const openGroup = groups.find((g) => g.key === tab) ?? groups[0];
  const selected = openGroup?.items.find((i) => i.key === item)?.key ?? openGroup?.items[0]?.key ?? null;
  const toBoard = useCallback((): void => pick('friction'), [pick]);
  const toFrictionRow = useCallback((key: string): void => pick('friction', key), [pick]);
  const toAgents = useCallback((): void => pick('agents'), [pick]);

  const windowLabel = `${String(days)} d`;
  const sectionFallback = <Loading label={t`loading…`} />;
  const retro = (section: 'agents' | 'friction' | 'estimates' | 'advisor'): JSX.Element => (
    <Suspense fallback={sectionFallback}>
      <Retro
        section={section}
        range={range}
        inboxHref={`${placeHref('inbox', projectSlug)}?tab=advisor`}
        onFrictionChanged={reloadFriction}
        frictionReloadKey={frictionReloadKey}
      />
    </Suspense>
  );

  const detail = (): JSX.Element => {
    switch (tab) {
      case 'overview':
        return selected === 'removable' ? (
          <RemovableDetail friction={friction} onOpen={toFrictionRow} onBoard={toBoard} />
        ) : (
          <FleetDetail
            agents={agents}
            error={error}
            onRetry={load}
            scopeLabel={projectSlug ?? t`all projects`}
            windowLabel={windowLabel}
            decisions={decisions}
            autoMode={autoMode}
            alertsHref={`${placeHref('inbox', projectSlug)}?tab=alerts`}
          />
        );
      case 'friction': {
        const denied = friction !== null ? uncoveredDenied(friction).find((d) => `denied:${d.tool}` === selected) : undefined;
        if (denied !== undefined) return <DeniedDetail key={denied.tool} denied={denied} onBoard={toBoard} />;
        const group = friction !== null ? untriagedErrors(friction).find((g) => `error:${g.key}` === selected) : undefined;
        if (group !== undefined) return <ErrorDetail group={group} onBoard={toBoard} />;
        return (
          <PageDetail kind={t`health · friction`} kindTone="brand" meta={windowLabel} title={t`Tools you deny and errors that repeat.`}>
            <div className="mt-3">
              <FrictionTriageStrip
                untriaged={friction !== null ? untriagedErrors(friction).length : 0}
                running={triage.run === null ? null : { done: triage.run.done, total: triage.run.total }}
                summary={
                  lastRun === null || runFailed
                    ? null
                    : { applied: lastRun.applied, suggested: lastRun.suggested, failed: lastRun.failed }
                }
                dismissed={lastRun !== null && dismissedRunId === lastRun.id}
                error={triageError}
                busy={starting}
                onStart={startFrictionRun}
                onDismiss={() => setDismissedRunId(lastRun?.id ?? null)}
                onDismissError={() => setDismissedError(errorKey)}
              />
            </div>
            {retro('friction')}
          </PageDetail>
        );
      }
      case 'estimates':
        return selected === 'honesty' ? (
          <PageDetail kind={t`health · estimates`} meta={windowLabel} title={t`Forecast honesty`}>
            <CalibrationPanel />
          </PageDetail>
        ) : (
          <PageDetail kind={t`health · estimates`} meta={windowLabel} title={t`How estimates landed, and what the runs taught.`}>
            {retro('estimates')}
          </PageDetail>
        );
      case 'phaseruns':
        return (
          <PageDetail kind={t`health · phase runs`} meta={windowLabel} title={t`How plan-phase runs ended.`}>
            <PhaseRunsTab from={range.from} to={range.to} />
          </PageDetail>
        );
      case 'advisor': {
        const rec = recs?.find((r) => `rec:${String(r.id)}` === selected);
        if (rec !== undefined) {
          return <RecDetail rec={rec} kind={t`advisor · open`} inboxHref={`${placeHref('inbox', projectSlug)}?tab=advisor`} />;
        }
        return (
          <PageDetail kind={t`advisor · open`} kindTone="blue" meta={windowLabel} title={t`What the Advisor found and what waits on you.`}>
            {retro('advisor')}
          </PageDetail>
        );
      }
      case 'cost': {
        const spent = agents !== null ? String(Math.round(windowCell(agents).costUsd)) : null;
        return (
          <PageDetail
            kind={t`health · cost`}
            kindTone="brand"
            meta={windowLabel}
            title={spent !== null ? t`$${spent} spent in this window.` : t`Cost & tokens`}
          >
            <Suspense fallback={<Loading label={t`cost…`} />}>
              <Analytics range={{ from: range.from, to: range.to }} />
            </Suspense>
          </PageDetail>
        );
      }
      case 'agents': {
        const row = agents?.agents.find((a) => `agent:${a.agent}` === selected);
        if (row !== undefined) {
          return (
            <AgentDetail key={row.agent} row={row} systemHref={placeHref('system', projectSlug)} onAll={toAgents} />
          );
        }
        return (
          <PageDetail kind={t`health · agent`} meta={windowLabel} title={t`Every agent, scored over this window.`}>
            {retro('agents')}
          </PageDetail>
        );
      }
    }
  };

  return (
    <PagePlace
      title={t`Health`}
      {...(agents !== null ? { sub: placeSub(windowCell(agents), days) } : {})}
      groups={groups}
      openGroup={tab}
      selected={selected}
      onOpenGroup={(key) => pick(key as HealthTab)}
      onSelect={(key) => {
        const first = openGroup?.items[0]?.key;
        pick(tab, key === first ? null : key);
      }}
      detail={
        <div className="relative">
          <div className="px-4 pt-3 desk:absolute desk:top-3 desk:right-7 desk:z-10 desk:p-0">
            <Segmented label={t`Range`} options={RANGE_OPTIONS} value={String(days)} onChange={setDays} />
          </div>
          {detail()}
        </div>
      }
    />
  );
}
