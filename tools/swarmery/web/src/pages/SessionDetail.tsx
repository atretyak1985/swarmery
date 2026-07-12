// Session detail (design §3.3, MVP scope): header with status/model/token/cost
// facts, then ONLY the Timeline and Diffs tabs (Context and Tree are later
// phases). Live: session_updated merges header state; event_appended is
// attributed via its sessionId and appended to the open timeline.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import type { SessionDetail, SessionStatus, WSMessage } from '../api/types';
import { fetchSession } from '../api';
import { fmtAgo, fmtCost, fmtDateTime, fmtSpan, fmtTokens } from '../lib/format';
import { useLiveUpdates } from '../lib/ws';
import { ErrorBox, Loading } from '../components/ui';
import { Timeline } from './detail/Timeline';
import { Diffs } from './detail/Diffs';
import { SummaryChips } from './detail/SummaryChips';

const STATUS_TONES: Record<SessionStatus, string> = {
  active: 'text-green',
  waiting_approval: 'text-amber',
  idle: 'text-ink-dim',
  completed: 'text-ink-dim',
  killed: 'text-red',
};

type Tab = 'timeline' | 'diffs';

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
    return { tokens, cost };
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
          {detail.projectSlug}
          {detail.gitBranch !== null ? ` · ${detail.gitBranch}` : ''}
        </span>
      </div>
      <h1 className="mb-2 font-display text-[21px] leading-[1.3] font-semibold tracking-[-0.02em] desk:text-[24px]">
        {detail.title ?? detail.sessionUuid}
      </h1>
      <div className="flex flex-wrap gap-x-3.5 gap-y-1.5 font-mono text-[11px] text-ink-dim">
        <Kv label="status" value={detail.status} tone={STATUS_TONES[detail.status]} />
        {detail.model !== null && <Kv label="model" value={detail.model} />}
        <Kv label="tokens" value={fmtTokens(facts.tokens)} />
        <Kv label="cost" value={fmtCost(facts.cost)} tone="text-brand" />
        <Kv label="started" value={fmtDateTime(detail.startedAt)} />
        <Kv
          label={detail.endedAt !== null ? 'duration' : 'running'}
          value={fmtSpan(detail.startedAt, detail.endedAt)}
        />
        {lastEvent !== undefined && detail.endedAt === null && (
          <Kv label="last event" value={fmtAgo(lastEvent.ts)} />
        )}
      </div>

      <SummaryChips events={detail.events} />

      <div className="mt-4 flex gap-0.5 border-b border-line" role="tablist">
        <TabButton active={tab === 'timeline'} onClick={() => setTab('timeline')}>
          Timeline
        </TabButton>
        <TabButton active={tab === 'diffs'} onClick={() => setTab('diffs')}>
          {`Diffs${diffCount > 0 ? ` · ${diffCount}` : ''}`}
        </TabButton>
      </div>

      {tab === 'timeline' ? <Timeline detail={detail} /> : <Diffs changes={detail.fileChanges} />}
    </>
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
