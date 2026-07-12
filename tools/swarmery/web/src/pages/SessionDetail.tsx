// Session detail (Redesign parity): header with status/model facts and
// TOKENS/COST/ERRORS numerals top-right, then the Timeline, Diffs, and Chat
// tabs beside a desktop right rail (agents / skills / files changed; mobile
// keeps the SummaryChips strip). Live: session_updated merges header state;
// event_appended is attributed via its sessionId and appended to the open
// timeline.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import type { SessionDetail, SessionStatus, WSMessage } from '../api/types';
import { fetchSession } from '../api';
import { fmtAgo, fmtCost, fmtDateTime, fmtSpan, fmtTokens, projectLabel } from '../lib/format';
import { useLiveUpdates } from '../lib/ws';
import { ErrorBox, Loading } from '../components/ui';
import { Timeline } from './detail/Timeline';
import { Diffs } from './detail/Diffs';
import { Chat } from './detail/Chat';
import { SummaryChips } from './detail/SummaryChips';
import { DetailRail } from './detail/DetailRail';

const STATUS_TONES: Record<SessionStatus, string> = {
  active: 'text-green',
  waiting_approval: 'text-amber',
  idle: 'text-ink-dim',
  completed: 'text-ink-dim',
  killed: 'text-red',
};

type Tab = 'timeline' | 'diffs' | 'chat';

function Kv({ label, value, tone = 'text-ink' }: { label: string; value: string; tone?: string }): JSX.Element {
  return (
    <span>
      {label} <b className={`font-medium ${tone}`}>{value}</b>
    </span>
  );
}

export function SessionDetailPage(): JSX.Element {
  const { id } = useParams<{ id: string }>();
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>('timeline');

  const load = useCallback((): void => {
    if (id === undefined) return;
    fetchSession(id)
      .then((d) => {
        setDetail(d);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [id]);

  useEffect(() => {
    setDetail(null);
    load();
  }, [load]);

  const onMessage = useCallback((msg: WSMessage): void => {
    setDetail((prev) => {
      if (prev === null) return prev;
      if (msg.type === 'session_started') return prev;
      if (msg.type === 'session_updated') {
        return msg.payload.id === prev.id ? { ...prev, ...msg.payload } : prev;
      }
      // event_appended: attributed directly via the payload's sessionId
      // (step-10 contract change).
      const { sessionId, event } = msg.payload;
      if (sessionId !== prev.id) return prev;
      if (prev.events.some((e) => e.id === event.id)) return prev;
      return { ...prev, events: [...prev.events, event] };
    });
  }, []);
  useLiveUpdates(onMessage, load);

  const facts = useMemo(() => {
    if (detail === null) return null;
    let tokens = 0;
    let cost: number | null = null;
    for (const turn of detail.turns) {
      tokens += (turn.tokensIn ?? 0) + (turn.tokensOut ?? 0);
      if (turn.costUsd !== null) cost = (cost ?? 0) + turn.costUsd;
    }
    const errors = detail.events.filter(
      (e) =>
        e.type === 'error' ||
        e.status === 'error' ||
        e.status === 'denied' ||
        e.status === 'timeout',
    ).length;
    return { tokens, cost, errors };
  }, [detail]);

  if (error !== null) {
    return (
      <>
        <BackLink />
        <ErrorBox message={error} onRetry={load} />
      </>
    );
  }
  if (detail === null || facts === null) {
    return (
      <>
        <BackLink />
        <Loading label="session…" />
      </>
    );
  }

  const lastEvent = detail.events.length > 0 ? detail.events[detail.events.length - 1] : undefined;
  const diffCount = detail.fileChanges.length;

  return (
    <>
      <div className="mb-2.5 flex items-center gap-2 pt-0.5 text-[12px] text-ink-dim">
        <Link to="/sessions" className="shrink-0 transition-colors hover:text-ink">
          ← Sessions
        </Link>
        <span aria-hidden="true">/</span>
        <span className="truncate font-mono text-[11px] text-brand">
          {projectLabel(detail.projectName, detail.projectSlug)}
          {detail.gitBranch !== null ? ` · ${detail.gitBranch}` : ''}
        </span>
      </div>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-3">
        <div className="min-w-0 flex-1">
          <h1 className="mb-2 font-display text-[21px] leading-[1.3] font-bold tracking-[0.01em]">
            {detail.title ?? detail.sessionUuid}
          </h1>
          <div className="flex flex-wrap gap-x-3.5 gap-y-1.5 font-mono text-[11.5px] text-ink-dim">
            <Kv label="status" value={detail.status} tone={STATUS_TONES[detail.status]} />
            {detail.model !== null && <Kv label="model" value={detail.model} />}
            <Kv label="started" value={fmtDateTime(detail.startedAt)} />
            <Kv
              label={detail.endedAt !== null ? 'duration' : 'running'}
              value={fmtSpan(detail.startedAt, detail.endedAt)}
            />
            {lastEvent !== undefined && detail.endedAt === null && (
              <Kv label="last event" value={fmtAgo(lastEvent.ts)} />
            )}
          </div>
        </div>
        <div className="flex shrink-0 gap-[22px] text-right">
          <HeadStat value={fmtTokens(facts.tokens)} label="tokens" />
          <HeadStat value={fmtCost(facts.cost)} label="cost" tone="text-brand" />
          <HeadStat
            value={String(facts.errors)}
            label="errors"
            tone={facts.errors > 0 ? 'text-red' : 'text-ink-dim'}
          />
        </div>
      </div>

      {/* Mobile at-a-glance strip; the desktop rail replaces it at ≥1280px. */}
      <div className="wide:hidden">
        <SummaryChips events={detail.events} />
      </div>

      <div className="mt-4 wide:grid wide:grid-cols-[minmax(0,1fr)_300px] wide:items-start wide:gap-6">
        <div className="min-w-0">
          <div className="flex gap-0.5 border-b border-line" role="tablist">
            <TabButton active={tab === 'timeline'} onClick={() => setTab('timeline')}>
              Timeline
            </TabButton>
            <TabButton active={tab === 'diffs'} onClick={() => setTab('diffs')}>
              {`Diffs${diffCount > 0 ? ` · ${diffCount}` : ''}`}
            </TabButton>
            <TabButton active={tab === 'chat'} onClick={() => setTab('chat')}>
              Chat
            </TabButton>
          </div>

          {tab === 'timeline' && <Timeline detail={detail} />}
          {tab === 'diffs' && <Diffs changes={detail.fileChanges} />}
          {tab === 'chat' && <Chat detail={detail} onShowTimeline={() => setTab('timeline')} />}
        </div>

        <div className="hidden wide:block">
          <DetailRail
            events={detail.events}
            fileChanges={detail.fileChanges}
            onShowDiffs={() => setTab('diffs')}
          />
        </div>
      </div>
    </>
  );
}

function HeadStat({
  value,
  label,
  tone = 'text-ink',
}: {
  value: string;
  label: string;
  tone?: string;
}): JSX.Element {
  return (
    <div>
      <div className={`font-mono text-[18px] leading-none font-bold ${tone}`}>{value}</div>
      <div className="mt-1 font-mono text-[10px] tracking-[0.06em] text-ink-dim uppercase">
        {label}
      </div>
    </div>
  );
}

function BackLink(): JSX.Element {
  return (
    <Link to="/sessions" className="mb-2 block pt-0.5 text-[12px] text-ink-dim hover:text-ink">
      ← Sessions
    </Link>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`-mb-px border-b-2 px-3.5 py-2 text-[12.5px] font-medium transition-colors ${
        active ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}
