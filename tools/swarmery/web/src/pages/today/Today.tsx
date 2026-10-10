// Today (Canvas v3 phase 4, artboard 1a) — the home of both scopes: `/`
// (All projects) and `/p/:slug` (one project). It answers "what do I do now":
// a clock line, "The loop, this week" (LoopMap), then "Waiting on you" (top
// Inbox items) beside "Live now" (running sessions). The previous home — the
// fleet command deck or the project overview — follows under "Today in
// detail", unchanged, so nothing the old home did is lost.
//
// The deck arrives as the `detail` prop: main.tsx lazy-loads ProjectOverview,
// and a static import here would pull it into the fleet's initial bundle.
//
// Data is existing fetchers only (Canvas v3 D3 — no new endpoint): epics,
// sessions, decisions, recommendations, plus the Inbox's own aggregation
// (useInboxItems), which feeds both the loop's counts and the waiting rows.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import { fetchEpics, fetchProjectRecommendations, fetchRecommendations, fetchSessions } from '../../api';
import { fetchDecisions } from '../../api/decisions';
import type { Epic, Recommendation, Session, WSMessage } from '../../api/types';
import { loadLastProject } from '../../lib/lastProject';
import { sessionState, useNowMs } from '../../lib/sessionState';
import { applySessionMessage, useLiveUpdates } from '../../lib/ws';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import type { InboxItem, InboxKind } from '../inbox/inboxModel';
import { useInboxItems } from '../inbox/useInboxItems';
import { LiveNow } from './LiveNow';
import { LoopMap } from './LoopMap';
import { buildLoop, epicCounts } from './loopModel';
import { SectionHead, WaitingOnYou } from './WaitingOnYou';

const SESSION_PAGE = 100;

/** Clock line, copied from Overview's EyebrowClock (not imported) plus the scope. */
function EyebrowClock({ scope }: { scope: string }): JSX.Element {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 30_000);
    return () => window.clearInterval(id);
  }, []);
  const text = now
    .toLocaleString([], {
      weekday: 'long',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
    .replace(/,/g, ' ·');
  return (
    <div className="font-mono text-[11px] tracking-[0.16em] text-ink-faint uppercase">
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

  const [epics, setEpics] = useState<Epic[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(true);
  const [classifier, setClassifier] = useState({ calls: 0, checked: 0 });
  const [recs, setRecs] = useState<Recommendation[]>([]);

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

  const running = useMemo(
    () => sessions.filter((s) => sessionState(s, now) === 'running'),
    [sessions, now],
  );

  const stages = useMemo(() => {
    const counts = epicCounts(epics, now);
    return buildLoop(
      {
        epics,
        liveSessions: running.length,
        pendingApprovals: countKind(inbox.items, 'approval'),
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
  }, [epics, now, running.length, inbox.items, classifier, recs, slug]);

  const scopeLabel = slug === null ? t`all projects` : (project?.name ?? slug);

  return (
    <div data-testid="today">
      <div className="px-4 pt-6 desk:px-9 desk:pt-[30px]">
        <EyebrowClock scope={scopeLabel} />
        <h1 className="m-0 mt-1.5 font-display text-[30px] leading-[1.15] font-medium tracking-[-0.01em] text-ink">
          <Trans>The loop, this week</Trans>
        </h1>
        <p className="m-0 mt-1.5 text-[13px] text-ink-dim">
          <Trans>
            Every stage feeds the next. Numbers are the last 7 days; amber means something there is waiting on you.
          </Trans>
        </p>
        <LoopMap stages={stages} />
        <div className="mt-7 grid grid-cols-1 gap-5 desk:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
          <WaitingOnYou
            items={inbox.items}
            count={inbox.count}
            loading={inbox.loading}
            slug={slug}
            onResolved={inbox.reload}
          />
          <LiveNow sessions={running} epics={epics} loading={sessionsLoading} now={now} />
        </div>
        <div className="mt-10">
          <SectionHead label={t`Today in detail`} />
        </div>
      </div>
      <div data-testid="today-detail">{detail}</div>
    </div>
  );
}
