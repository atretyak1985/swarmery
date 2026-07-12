// Overview (design §3.1, MVP scope — no approvals block): today's counters
// from /api/stats/today, live active sessions over WS, recent completed.

import { useCallback, useEffect, useState } from 'react';
import type { Session, StatsToday, WSMessage } from '../api/types';
import { fetchSessions, fetchStatsToday } from '../api';
import { fmtCost, fmtTodayHeader, fmtTokens } from '../lib/format';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { SessionCard } from '../components/SessionCard';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

function Stat({
  value,
  label,
  tone = '',
}: {
  value: string;
  label: string;
  tone?: string;
}): JSX.Element {
  return (
    <div className="rounded-[10px] border border-line bg-surface px-1 py-2.5 text-center">
      <div className={`font-mono text-[16px] font-bold ${tone}`}>{value}</div>
      <div className="mt-0.5 text-[10px] tracking-[0.04em] text-ink-dim">{label}</div>
    </div>
  );
}

function StatsRow({ stats }: { stats: StatsToday }): JSX.Element {
  return (
    <div className="grid grid-cols-5 gap-1.5">
      <Stat value={String(stats.sessions)} label="sessions" />
      <Stat value={String(stats.active)} label="active" tone="text-green" />
      <Stat value={fmtTokens(stats.tokens_in + stats.tokens_out)} label="tokens" />
      <Stat value={fmtCost(stats.cost_usd)} label="cost" tone="text-amber" />
      <Stat
        value={String(stats.errors)}
        label="errors"
        tone={stats.errors > 0 ? 'text-red' : ''}
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
      <SectionTitle>Today · {fmtTodayHeader()}</SectionTitle>
      {stats !== null ? (
        <StatsRow stats={stats} />
      ) : statsError ? (
        <div className="font-mono text-[11px] text-ink-dim">stats unavailable</div>
      ) : (
        <Loading label="stats…" />
      )}

      <SectionTitle>Active sessions</SectionTitle>
      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {sessions === null && error === null && <Loading label="sessions…" />}
      {sessions !== null && live.length === 0 && (
        <Empty>
          no live sessions — run <span className="font-mono text-ink">swarmery ingest</span> or
          start a Claude Code session
        </Empty>
      )}
      {live.map((s) => (
        <SessionCard key={s.id} session={s} />
      ))}

      <SectionTitle>Recently completed</SectionTitle>
      {sessions !== null && completed.length === 0 && <Empty>nothing completed yet</Empty>}
      {completed.map((s) => (
        <SessionCard key={s.id} session={s} />
      ))}
    </>
  );
}
