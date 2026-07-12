// Overview (design §3.1, MVP scope — no approvals block): today's counters
// from /api/stats/today, live active sessions over WS, recent completed.
// Redesign layout: display-serif day title, KPI tiles (mono label over a
// Fraunces numeral), live cards, completed sessions grouped in one list card.

import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type { Session, StatsToday, WSMessage } from '../api/types';
import { fetchSessions, fetchStatsToday } from '../api';
import { fmtCost, fmtTodayHeader, fmtTokens } from '../lib/format';
import { liveActionText } from '../lib/payload';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { SessionCard } from '../components/SessionCard';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

function Stat({
  value,
  label,
  tone = '',
  frame = 'border-line',
}: {
  value: string;
  label: string;
  tone?: string;
  frame?: string;
}): JSX.Element {
  return (
    <div className={`rounded-[14px] border bg-surface px-2 py-2.5 desk:px-3.5 desk:py-3 ${frame}`}>
      <div className="truncate font-mono text-[9px] tracking-[0.08em] text-ink-dim uppercase desk:text-[10.5px]">
        {label}
      </div>
      <div
        className={`mt-1 font-display text-[17px] leading-none font-semibold tracking-[-0.02em] desk:text-[24px] ${tone}`}
      >
        {value}
      </div>
    </div>
  );
}

function StatsRow({ stats }: { stats: StatsToday }): JSX.Element {
  return (
    <div className="mt-3 grid grid-cols-5 gap-1.5 desk:gap-2.5">
      <Stat value={String(stats.sessions)} label="sessions" />
      <Stat value={String(stats.active)} label="active" tone="text-green" />
      <Stat value={fmtTokens(stats.tokens_in + stats.tokens_out)} label="tokens" />
      <Stat value={fmtCost(stats.cost_usd)} label="cost" tone="text-brand" />
      <Stat
        value={String(stats.errors)}
        label="errors"
        tone={stats.errors > 0 ? 'text-red' : ''}
        frame={stats.errors > 0 ? 'border-red/35' : 'border-line'}
      />
    </div>
  );
}

const LIVE_STATUSES = new Set<Session['status']>(['active', 'waiting_approval', 'idle']);

export function Overview(): JSX.Element {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [stats, setStats] = useState<StatsToday | null>(null);
  const [statsError, setStatsError] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});

  const load = useCallback((): void => {
    fetchSessions()
      .then((list) => {
        setSessions(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
    fetchStatsToday()
      .then((s) => {
        setStats(s);
        setStatsError(false);
      })
      .catch(() => setStatsError(true));
  }, []);

  useEffect(load, [load]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type === 'event_appended') {
      // step-10 contract: the payload carries sessionId → live "now" line.
      const text = liveActionText(msg.payload.event);
      if (text !== null) {
        const { sessionId } = msg.payload;
        setNowById((prev) => ({ ...prev, [sessionId]: text }));
      }
      return;
    }
    setSessions((prev) => (prev === null ? prev : applySessionMessage(prev, msg)));
  }, []);
  useLiveUpdates(onMessage, load);

  const live = (sessions ?? [])
    .filter((s) => LIVE_STATUSES.has(s.status))
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  const completed = (sessions ?? [])
    .filter((s) => !LIVE_STATUSES.has(s.status))
    .sort((a, b) => (b.endedAt ?? b.startedAt).localeCompare(a.endedAt ?? a.startedAt))
    .slice(0, 5);

  return (
    <>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 pt-1">
        <h1 className="font-display text-[22px] leading-tight font-semibold tracking-[-0.02em] desk:text-[26px]">
          Today · {fmtTodayHeader()}
        </h1>
        {stats !== null && (
          <span className="font-mono text-[11px] text-ink-dim">
            {stats.sessions} sessions · {stats.active} active
          </span>
        )}
      </div>
      {stats !== null ? (
        <StatsRow stats={stats} />
      ) : statsError ? (
        <div className="mt-3 font-mono text-[11px] text-ink-dim">stats unavailable</div>
      ) : (
        <Loading label="stats…" />
      )}

      <SectionTitle>Active now{live.length > 0 ? ` · ${live.length}` : ''}</SectionTitle>
      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {sessions === null && error === null && <Loading label="sessions…" />}
      {sessions !== null && live.length === 0 && (
        <Empty>
          no live sessions — run <span className="font-mono text-ink">swarmery ingest</span> or
          start a Claude Code session
        </Empty>
      )}
      {live.map((s) => (
        <SessionCard key={s.id} session={s} now={nowById[s.id] ?? null} />
      ))}

      <SectionTitle>Recently completed</SectionTitle>
      {sessions !== null && completed.length === 0 && <Empty>nothing completed yet</Empty>}
      {completed.length > 0 && (
        <div className="divide-y divide-line-soft overflow-hidden rounded-[14px] border border-line bg-surface">
          {completed.map((s) => (
            <SessionCard key={s.id} session={s} flat />
          ))}
          <Link
            to="/sessions"
            className="block px-3.5 py-2.5 font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
          >
            all sessions →
          </Link>
        </div>
      )}
    </>
  );
}
