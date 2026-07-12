// Overview (Redesign parity): day-chip navigator over /api/stats/overview,
// KPI tiles with inline sparklines from the trailing series, live "active
// now" hero cards with ELAPSED/TOKENS/COST columns, day-scoped completed
// list, and the desktop right rail (errors / cost by model / projects).

import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type { Session, StatsOverview, WSMessage } from '../api/types';
import { fetchSessions, fetchStatsOverview } from '../api';
import { projectColor } from '../lib/colors';
import {
  addDays,
  fmtCost,
  fmtDayShort,
  fmtDayTitle,
  fmtSpan,
  fmtTime,
  fmtTokens,
  isoDay,
  parseDay,
  projectLabel,
} from '../lib/format';
import { liveActionText } from '../lib/payload';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { Sparkline } from '../components/Sparkline';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

const DAY_WINDOW = 7; // day chips: today-6 … today

/* ----- day-chip navigator ----- */

function DayNav({ day, onSelect }: { day: string; onSelect: (day: string) => void }): JSX.Element {
  const today = isoDay();
  const days = Array.from({ length: DAY_WINDOW }, (_, i) => addDays(today, i - (DAY_WINDOW - 1)));
  const chipBase =
    'shrink-0 rounded-md border py-1 text-center font-mono text-[10.5px] transition-colors';

  return (
    <div className="ml-auto flex items-center gap-1">
      <button
        type="button"
        aria-label="previous day"
        disabled={day <= days[0]!}
        onClick={() => onSelect(addDays(day, -1))}
        className={`${chipBase} w-6 border-line text-ink-dim enabled:hover:text-ink disabled:opacity-40`}
      >
        ‹
      </button>
      {days.map((d) => (
        <button
          key={d}
          type="button"
          aria-pressed={d === day}
          onClick={() => onSelect(d)}
          className={`${chipBase} w-[30px] ${
            d === day
              ? 'border-ink-dim bg-surface2 text-ink'
              : d === today
                ? 'border-line text-brand'
                : 'border-line text-ink-dim hover:text-ink'
          }`}
        >
          {parseDay(d).getDate()}
        </button>
      ))}
      <button
        type="button"
        aria-label="next day"
        disabled={day >= today}
        onClick={() => onSelect(addDays(day, 1))}
        className={`${chipBase} w-6 border-line text-ink-dim enabled:hover:text-ink disabled:opacity-40`}
      >
        ›
      </button>
    </div>
  );
}

/* ----- KPI tiles ----- */

function KpiTile({
  label,
  value,
  tone = '',
  frame = 'border-line',
  children,
}: {
  label: string;
  value: string;
  tone?: string;
  frame?: string;
  children?: JSX.Element | null;
}): JSX.Element {
  return (
    <div className={`rounded-xl border bg-surface px-2 py-2.5 desk:px-3.5 desk:py-3 ${frame}`}>
      <div className="truncate font-mono text-[9px] tracking-[0.08em] text-ink-dim uppercase desk:text-[10.5px]">
        {label}
      </div>
      <div className={`mt-1 font-mono text-[17px] leading-none font-bold desk:text-[24px] ${tone}`}>
        {value}
      </div>
      {children}
    </div>
  );
}

function KpiRow({ stats, isToday }: { stats: StatsOverview; isToday: boolean }): JSX.Element {
  const spark = (values: number[], tone?: 'dim' | 'amber' | 'red'): JSX.Element | null => {
    const highlight = stats.series.findIndex((p) => p.day === stats.day);
    return (
      <Sparkline
        values={values}
        highlight={highlight === -1 ? stats.series.length - 1 : highlight}
        tone={tone ?? 'dim'}
      />
    );
  };
  return (
    <div className="mt-3 grid grid-cols-5 gap-1.5 desk:gap-2.5">
      <KpiTile value={String(stats.sessions)} label="sessions">
        {spark(stats.series.map((p) => p.sessions))}
      </KpiTile>
      <KpiTile value={String(isToday ? stats.active : 0)} label="active" tone="text-green">
        <div className="mt-3 truncate font-mono text-[9px] text-ink-dim desk:text-[10.5px]">
          {isToday ? `${String(stats.waiting_approval)} waiting approval` : 'no live sessions'}
        </div>
      </KpiTile>
      <KpiTile value={fmtTokens(stats.tokens_in + stats.tokens_out)} label="tokens">
        {spark(stats.series.map((p) => p.tokens))}
      </KpiTile>
      <KpiTile value={fmtCost(stats.cost_usd)} label="cost" tone="text-brand">
        {spark(
          stats.series.map((p) => p.cost_usd ?? 0),
          'amber',
        )}
      </KpiTile>
      <KpiTile
        value={String(stats.errors)}
        label="errors"
        tone="text-red"
        frame="border-red/35"
      >
        {spark(
          stats.series.map((p) => p.errors),
          'red',
        )}
      </KpiTile>
    </div>
  );
}

/* ----- "Active now" hero card (elapsed / tokens / cost columns) ----- */

function HeroStat({ value, label, tone = '' }: { value: string; label: string; tone?: string }): JSX.Element {
  return (
    <div>
      <div className={`font-mono text-[16px] leading-none font-bold ${tone}`}>{value}</div>
      <div className="mt-1 font-mono text-[10px] tracking-[0.06em] text-ink-dim uppercase">
        {label}
      </div>
    </div>
  );
}

function HeroCard({ session, now }: { session: Session; now: string | null }): JSX.Element {
  const waiting = session.status === 'waiting_approval';
  const meta = [session.gitBranch, session.model].filter((v) => v !== null).join(' · ');
  return (
    <Link
      to={`/sessions/${String(session.id)}`}
      className={`mb-2.5 flex flex-wrap items-center gap-x-5 gap-y-3.5 rounded-xl border bg-surface px-4 py-3.5 transition-colors focus-visible:outline-2 focus-visible:outline-brand ${
        waiting ? 'border-amber/35 hover:border-amber/70' : 'border-green/25 hover:border-green/55'
      }`}
    >
      <div className="min-w-0 flex-[1_1_260px]">
        <div className="flex items-center gap-2">
          <span
            className={`h-[7px] w-[7px] shrink-0 rounded-full ${
              waiting ? 'bg-amber' : 'animate-pulse-dot bg-green'
            }`}
            aria-hidden="true"
          />
          <span className="flex min-w-0 items-center gap-1.5 rounded-md bg-surface2 px-2 py-0.5">
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full"
              style={{ background: projectColor(session.projectSlug) }}
              aria-hidden="true"
            />
            <span className="truncate font-mono text-[11px] text-ink">
              {projectLabel(session.projectName, session.projectSlug)}
            </span>
          </span>
          {meta !== '' && (
            <span className="truncate font-mono text-[11px] text-ink-dim">{meta}</span>
          )}
        </div>
        <div className="mt-[7px] mb-[3px] truncate text-[15px] font-semibold">
          {session.title ?? session.sessionUuid}
        </div>
        <div className={`truncate font-mono text-[11px] ${waiting ? 'text-amber' : 'text-green'}`}>
          {waiting ? 'waiting for approval' : now !== null ? `now: ${now}` : `started ${fmtTime(session.startedAt)}`}
        </div>
      </div>
      <div className="flex shrink-0 gap-[22px] text-right">
        <HeroStat value={fmtSpan(session.startedAt, null)} label="elapsed" />
        <HeroStat
          value={session.tokens != null ? fmtTokens(session.tokens) : '—'}
          label="tokens"
        />
        <HeroStat value={fmtCost(session.costUsd ?? null)} label="cost" tone="text-brand" />
      </div>
    </Link>
  );
}

/* ----- day-scoped completed rows (Redesign table-like list) ----- */

function CompletedRow({ session }: { session: Session }): JSX.Element {
  return (
    <Link
      to={`/sessions/${String(session.id)}`}
      className="grid grid-cols-[110px_minmax(0,1fr)_90px] items-center gap-3 px-3.5 py-2.5 transition-colors hover:bg-surface2 wide:grid-cols-[120px_minmax(0,1fr)_130px_60px_100px]"
    >
      <span className="flex min-w-0 items-center gap-[7px]">
        <span
          className="h-1.5 w-1.5 shrink-0 rounded-full"
          style={{ background: projectColor(session.projectSlug) }}
          aria-hidden="true"
        />
        <span className="truncate font-mono text-[11px] text-ink-3">
          {projectLabel(session.projectName, session.projectSlug)}
        </span>
      </span>
      <span
        className={`truncate text-[13px] font-semibold ${session.title === null ? 'font-normal text-ink-dim italic' : ''}`}
      >
        {session.title ?? '(no title)'}
      </span>
      <span className="hidden truncate font-mono text-[11px] text-ink-dim wide:block">
        {session.model ?? '—'}
      </span>
      <span className="hidden font-mono text-[11px] text-ink-dim wide:inline">
        {session.endedAt !== null ? fmtTime(session.endedAt) : '—'}
      </span>
      <span className="justify-self-end rounded-full border border-line px-2 py-0.5 font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
        {fmtSpan(session.startedAt, session.endedAt)}
      </span>
    </Link>
  );
}

/* ----- right rail (desktop ≥1280px; stacked below on mobile) ----- */

function RailCard({ children, className = '' }: { children: ReactNode; className?: string }): JSX.Element {
  return (
    <div className={`rounded-xl border border-line bg-surface ${className}`}>{children}</div>
  );
}

function Bar({ pct, className }: { pct: number; className: string }): JSX.Element {
  return (
    <div className="mt-1 h-[3px] overflow-hidden rounded-full bg-line">
      <div
        className={`h-full rounded-full ${className}`}
        style={{ width: `${String(Math.round(pct * 100))}%` }}
      />
    </div>
  );
}

function Rail({ stats, isToday }: { stats: StatsOverview; isToday: boolean }): JSX.Element {
  const errTotal = stats.errors_by_project.reduce((a, r) => a + r.errors, 0);
  const costTotal = stats.cost_by_model.reduce((a, r) => a + r.cost_usd, 0);
  const dayName = isToday ? 'today' : fmtDayShort(stats.day);
  return (
    <div className="min-w-0">
      <SectionTitle>Errors · {dayName}</SectionTitle>
      <RailCard className="px-4 py-3.5">
        <div className="flex items-baseline justify-between">
          <span className="font-mono text-[11px] text-ink-dim">
            across {stats.errors_by_project.length}{' '}
            {stats.errors_by_project.length === 1 ? 'project' : 'projects'}
          </span>
          <span className="font-mono text-[16px] font-bold text-red">{stats.errors}</span>
        </div>
        {stats.errors_by_project.map((row) => (
          <div key={row.slug} className="mt-2.5">
            <div className="flex justify-between font-mono text-[11px]">
              <span className="truncate text-ink-3">{projectLabel(row.name, row.slug)}</span>
              <span className="text-red">{row.errors}</span>
            </div>
            <Bar pct={errTotal > 0 ? row.errors / errTotal : 0} className="bg-red/65" />
          </div>
        ))}
        {stats.errors_by_project.length === 0 && (
          <div className="mt-2 font-mono text-[11px] text-ink-dim">no errors — clean day</div>
        )}
      </RailCard>

      <SectionTitle>Cost · by model</SectionTitle>
      <RailCard className="px-4 py-3.5">
        {stats.cost_by_model.map((row) => (
          <div key={row.model} className="mb-2.5 last:mb-0">
            <div className="flex justify-between font-mono text-[11px]">
              <span className="truncate text-ink-3">{row.model}</span>
              <span className="text-brand">{fmtCost(row.cost_usd)}</span>
            </div>
            <Bar pct={costTotal > 0 ? row.cost_usd / costTotal : 0} className="bg-amber/60" />
          </div>
        ))}
        {stats.cost_by_model.length === 0 && (
          <div className="font-mono text-[11px] text-ink-dim">no cost recorded</div>
        )}
      </RailCard>

      <SectionTitle>Projects</SectionTitle>
      <RailCard className="overflow-hidden">
        {stats.projects.map((row) => (
          <Link
            key={row.slug}
            to={`/sessions?project=${encodeURIComponent(row.slug)}`}
            className="flex items-center gap-2 border-b border-line-soft px-3.5 py-[9px] transition-colors last:border-b-0 hover:bg-surface2"
          >
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full"
              style={{ background: projectColor(row.slug) }}
              aria-hidden="true"
            />
            <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink">
              {projectLabel(row.name, row.slug)}
            </span>
            <span className="font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
              {row.sessions} {isToday ? 'today' : 'this day'}
            </span>
          </Link>
        ))}
        {stats.projects.length === 0 && (
          <div className="px-3.5 py-3 font-mono text-[11px] text-ink-dim">no projects touched</div>
        )}
      </RailCard>
    </div>
  );
}

/* ----- screen ----- */

const LIVE_STATUSES = new Set<Session['status']>(['active', 'waiting_approval', 'idle']);

function sessionDay(s: Session): string {
  return isoDay(new Date(s.endedAt ?? s.startedAt));
}

export function Overview(): JSX.Element {
  const navigate = useNavigate();
  const [day, setDay] = useState(isoDay());
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [stats, setStats] = useState<StatsOverview | null>(null);
  const [statsError, setStatsError] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});

  const isToday = day === isoDay();

  const loadSessions = useCallback((): void => {
    fetchSessions()
      .then((list) => {
        setSessions(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, []);

  const loadStats = useCallback((): void => {
    fetchStatsOverview(day)
      .then((s) => {
        setStats(s);
        setStatsError(false);
      })
      .catch(() => setStatsError(true));
  }, [day]);

  useEffect(loadSessions, [loadSessions]);
  useEffect(() => {
    setStats(null);
    loadStats();
  }, [loadStats]);

  const reload = useCallback((): void => {
    loadSessions();
    loadStats();
  }, [loadSessions, loadStats]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type === 'event_appended') {
      const text = liveActionText(msg.payload.event);
      if (text !== null) {
        const { sessionId } = msg.payload;
        setNowById((prev) => ({ ...prev, [sessionId]: text }));
      }
      return;
    }
    setSessions((prev) => (prev === null ? prev : applySessionMessage(prev, msg)));
  }, []);
  useLiveUpdates(onMessage, reload);

  const live = (sessions ?? [])
    .filter((s) => LIVE_STATUSES.has(s.status))
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  const completed = (sessions ?? [])
    .filter((s) => !LIVE_STATUSES.has(s.status) && sessionDay(s) === day)
    .sort((a, b) => (b.endedAt ?? b.startedAt).localeCompare(a.endedAt ?? a.startedAt))
    .slice(0, 6);

  const headSub =
    stats === null
      ? null
      : isToday
        ? `${String(stats.sessions)} sessions · ${String(stats.projects.length)} projects touched · ${String(stats.active)} running now`
        : `${String(stats.sessions)} sessions · ${String(stats.errors)} errors`;

  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2 pt-1">
        <h1 className="font-display text-[19px] leading-tight font-bold tracking-[0.01em] desk:text-[21px]">
          {fmtDayTitle(day)}
        </h1>
        {headSub !== null && <span className="font-mono text-[11px] text-ink-dim">{headSub}</span>}
        <DayNav day={day} onSelect={setDay} />
      </div>

      {stats !== null ? (
        <KpiRow stats={stats} isToday={isToday} />
      ) : statsError ? (
        <div className="mt-3 font-mono text-[11px] text-ink-dim">stats unavailable</div>
      ) : (
        <Loading label="stats…" />
      )}

      <div className="wide:grid wide:grid-cols-[minmax(0,1fr)_320px] wide:items-start wide:gap-5">
        <div className="min-w-0">
          {isToday && (
            <>
              <SectionTitle>Active now{live.length > 0 ? ` · ${String(live.length)}` : ''}</SectionTitle>
              {error !== null && <ErrorBox message={error} onRetry={loadSessions} />}
              {sessions === null && error === null && <Loading label="sessions…" />}
              {sessions !== null && live.length === 0 && (
                <Empty>
                  no live sessions — run <span className="font-mono text-ink">swarmery ingest</span>{' '}
                  or start a Claude Code session
                </Empty>
              )}
              {live.map((s) => (
                <HeroCard key={s.id} session={s} now={nowById[s.id] ?? null} />
              ))}
            </>
          )}

          <SectionTitle>
            {isToday ? 'Recently completed' : `Completed · ${fmtDayShort(day)}`}
          </SectionTitle>
          <div className="divide-y divide-line-soft overflow-hidden rounded-xl border border-line bg-surface">
            {sessions !== null && completed.length === 0 && (
              <div className="px-3.5 py-4 text-center text-[12.5px] text-ink-dim">
                no completed sessions this day
              </div>
            )}
            {completed.map((s) => (
              <CompletedRow key={s.id} session={s} />
            ))}
            <button
              type="button"
              onClick={() => void navigate('/sessions')}
              className="block w-full px-3.5 py-2.5 text-left font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
            >
              all sessions →
            </button>
          </div>
        </div>

        {stats !== null && <Rail stats={stats} isToday={isToday} />}
      </div>
    </>
  );
}
