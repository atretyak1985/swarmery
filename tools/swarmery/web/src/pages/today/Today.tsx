// Today (handoff §3) — the home of both scopes: `/` (All projects) and `/p/:slug`
// (one project). The list column is the event feed, what happened (EventFeed); the
// detail is the day: the loop, what waits on you, what is live, the week's spend.
// The previous home, the fleet command deck or the project overview, stays one
// click down under "Today in detail", so nothing it did is lost. It mounts on open:
// it fetches a page's worth of its own, and the home should not pay for that.
//
// The deck arrives as the `detail` prop: main.tsx lazy-loads ProjectOverview,
// and a static import here would pull it into the fleet's initial bundle.
//
// Data is existing fetchers only (Canvas v3 D3 — no new endpoint): epics,
// sessions, decisions, recommendations, the needs-you queue and the Inbox's own
// aggregation (useInboxItems).

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import { fetchEpics, fetchProjectRecommendations, fetchRecommendations, fetchSessions } from '../../api';
import { fetchDecisions } from '../../api/decisions';
import type { Epic, Recommendation, Session, WSMessage } from '../../api/types';
import { PlaceLayout } from '../../components/place/PlaceLayout';
import { fmtEyebrowClock } from '../../lib/format';
import { loadLastProject } from '../../lib/lastProject';
import { useSessionHref } from '../../lib/sessionHref';
import { gatesBySession, rowState } from '../../lib/sessionsView';
import { useNowMs } from '../../lib/sessionState';
import { useNeedsYou } from '../../lib/useNeedsYou';
import { applySessionMessage, useLiveUpdates } from '../../lib/ws';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import type { InboxItem, InboxKind } from '../inbox/inboxModel';
import { useInboxItems } from '../inbox/useInboxItems';
import { buildFeed, feedLinks } from './feedModel';
import { EventFeed } from './EventFeed';
import { LiveNow } from './LiveNow';
import { LoopMap } from './LoopMap';
import { buildLoop, epicCounts } from './loopModel';
import { SpendWeek } from './SpendWeek';
import { WaitingOnYou } from './WaitingOnYou';

const SESSION_PAGE = 100;

/** Clock line, as Overview's EyebrowClock, plus the scope. */
function EyebrowClock({ scope }: { scope: string }): JSX.Element {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 30_000);
    return () => window.clearInterval(id);
  }, []);
  const text = fmtEyebrowClock(now);
  return (
    <div className="font-mono text-[10.5px] leading-[normal] tracking-[0.14em] text-ink-faint uppercase">
      {text} · {scope}
    </div>
  );
}

function countKind(items: readonly InboxItem[], kind: InboxKind): number {
  return items.filter((i) => i.kind === kind).length;
}

function countStatus(recs: readonly Recommendation[], statuses: readonly string[]): number {
  return recs.filter((r) => statuses.includes(r.status)).length;
}

export function Today({ detail }: { detail: ReactNode }): JSX.Element {
  const { t } = useLingui();
  const { slug: routeSlug } = useParams<{ slug?: string }>();
  const slug = routeSlug ?? null;
  const { project, projectId } = useProjectWorkspace();
  const now = useNowMs();
  const inbox = useInboxItems(slug);
  const needs = useNeedsYou(slug);
  const sessionHref = useSessionHref();

  const [epics, setEpics] = useState<Epic[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(true);
  const [classifier, setClassifier] = useState({ calls: 0, checked: 0 });
  const [recs, setRecs] = useState<Recommendation[]>([]);
  const [deckOpen, setDeckOpen] = useState(false);

  // A project scope waits for its numeric id; the fleet asks for every epic.
  const epicScopeReady = slug === null || projectId !== null;
  useEffect(() => {
    if (!epicScopeReady) return;
    let live = true;
    fetchEpics(projectId ?? undefined)
      .then((e) => {
        if (live) setEpics(e);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [epicScopeReady, projectId]);

  const loadSessions = useCallback((): void => {
    fetchSessions(slug === null ? {} : { project: slug }, { limit: SESSION_PAGE })
      .then((r) => setSessions(r.sessions))
      .catch(() => undefined)
      .finally(() => setSessionsLoading(false));
  }, [slug]);
  useEffect(loadSessions, [loadSessions]);

  const projectSlug = project?.slug ?? null;
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type !== 'session_started' && msg.type !== 'session_updated') return;
      // Under a project, a frame from another project is not ours.
      if (slug !== null && msg.payload.projectSlug !== projectSlug) return;
      setSessions((prev) => applySessionMessage(prev, msg));
    },
    [slug, projectSlug],
  );
  useLiveUpdates(onMessage, loadSessions);

  useEffect(() => {
    let live = true;
    fetchDecisions()
      .then((d) => {
        if (!live) return;
        setClassifier({
          calls: d.questions.reduce((n, q) => n + q.calls, 0),
          checked: d.questions.reduce((n, q) => n + q.withTruth, 0),
        });
      })
      .catch(() => undefined);
    const status = 'verified,accepted,adopted';
    (slug === null ? fetchRecommendations(status) : fetchProjectRecommendations(slug, status))
      .then((r) => {
        if (live) setRecs(r.recommendations);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [slug]);

  const gates = useMemo(() => gatesBySession(needs.items), [needs.items]);
  const running = useMemo(
    () => sessions.filter((s) => rowState(s, now, gates.get(s.id)) === 'working'),
    [sessions, now, gates],
  );

  const stages = useMemo(() => {
    const counts = epicCounts(epics, now);
    return buildLoop(
      {
        epics,
        liveSessions: running.length,
        askingSessions: gates.size,
        phaseDecisions: needs.items.filter((i) => i.kind === 'phase_decision').length,
        stoppedPhases: counts.stoppedPhases,
        scoredRuns: counts.scoredRuns,
        farOffPlan: counts.farOffPlan,
        classifierCalls: classifier.calls,
        classifierChecked: classifier.checked,
        lessonCandidates: countKind(inbox.items, 'lesson'),
        advisorFindings: countKind(inbox.items, 'advisor'),
        retirements: countKind(inbox.items, 'retire'),
        verifiedChanges: countStatus(recs, ['verified']),
        gatheringProof: countStatus(recs, ['accepted', 'adopted']),
      },
      slug,
      loadLastProject(),
    );
  }, [epics, now, running.length, gates, needs.items, inbox.items, classifier, recs, slug]);

  const links = useMemo(() => feedLinks(slug, sessionHref), [slug, sessionHref]);
  const feed = useMemo(
    () => buildFeed({ sessions, needs: needs.items, epics, inbox: inbox.items, links, nowMs: now }),
    [sessions, needs.items, epics, inbox.items, links, now],
  );

  const scopeLabel = slug === null ? t`all projects` : (project?.name ?? slug);
  const liveCount = String(running.length);
  const headline =
    needs.count === 0
      ? t`Nothing waits on you · ${liveCount} running`
      : plural(needs.count, {
          one: `# thing waits on you · ${liveCount} running`,
          few: `# things wait on you · ${liveCount} running`,
          many: `# things wait on you · ${liveCount} running`,
          other: `# things wait on you · ${liveCount} running`,
        });

  return (
    <PlaceLayout
      list={<EventFeed events={feed} now={now} loading={sessionsLoading} />}
      detail={
        <div data-testid="today">
          <div className="max-w-[1040px] px-4 pt-[22px] pb-6 desk:px-7">
            <EyebrowClock scope={scopeLabel} />
            <h2 className="m-0 mt-2 text-[22px] leading-[normal] font-semibold tracking-[-0.015em] text-ink">
              {headline}
            </h2>
            <p className="m-0 mt-1.5 max-w-[72ch] text-[13px] leading-[1.55] text-ink-dim">
              <Trans>
                Every stage feeds the next. Numbers are the last 7 days; amber means something there is waiting on you.
              </Trans>
            </p>
            <LoopMap stages={stages} />
            <div className="mt-[22px] grid grid-cols-1 gap-[22px] desk:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
              <WaitingOnYou
                items={needs.items}
                count={needs.count}
                loading={needs.loading}
                links={links}
                now={now}
                onResolved={needs.reload}
              />
              <div className="min-w-0">
                <LiveNow
                  sessions={running}
                  epics={epics}
                  loading={sessionsLoading}
                  now={now}
                  sessionsHref={slug === null ? '/sessions' : `/p/${slug}/sessions`}
                />
                <SpendWeek slug={slug} />
              </div>
            </div>
            <button
              type="button"
              aria-expanded={deckOpen}
              onClick={() => setDeckOpen((open) => !open)}
              className="mt-6 flex w-full cursor-pointer items-center gap-2 border-none bg-transparent p-0 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase hover:text-ink-dim"
            >
              <Trans>Today in detail</Trans>
              <span aria-hidden="true" className="h-px flex-1 bg-line" />
              <span aria-hidden="true">{deckOpen ? '▾' : '▸'}</span>
            </button>
          </div>
          {deckOpen && <div data-testid="today-detail">{detail}</div>}
        </div>
      }
    />
  );
}
