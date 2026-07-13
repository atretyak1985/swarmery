// Overview (Redesign parity): day-chip navigator over /api/stats/overview,
// KPI tiles with inline sparklines from the trailing series, live "active
// now" hero cards with ELAPSED/TOKENS/COST columns, day-scoped completed
// list, and the desktop right rail (pending approvals when any / errors /
// cost by model / projects). The ACTIVE tile's "N waiting approval" subline
// and the rail's top-3 pending card are live (phase 2 permission_* WS).

import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type {
  PermissionRequest,
  Session,
  StatsOverview,
  TaskSummary,
  WSMessage,
} from '../api/types';
import { fetchApprovals, fetchSessions, fetchStatsOverview, fetchTasks } from '../api';
import { requestSummary } from '../lib/approvals';
import { projectColor } from '../lib/colors';
import {
  addDays,
  fmtAgo,
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
import { applyPermissionMessage, applySessionMessage, useLiveUpdates } from '../lib/ws';
import { Sparkline } from '../components/Sparkline';
import {
  COMPLETED_ROW_GRID,
  DurationPill,
  Empty,
  ErrorBox,
  Loading,
  SectionTitle,
} from '../components/ui';

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

function KpiRow({
  stats,
  isToday,
  waitingLive,
}: {
  stats: StatsOverview;
  isToday: boolean;
  /** Live pending-approvals count (WS-fed); null until loaded → stats value. */
  waitingLive: number | null;
}): JSX.Element {
  const waiting = waitingLive ?? stats.waiting_approval;
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
        <div
          className={`mt-3 truncate font-mono text-[9px] desk:text-[10.5px] ${
            isToday && waiting > 0 ? 'text-amber' : 'text-ink-dim'
          }`}
        >
          {isToday ? `${String(waiting)} waiting approval` : 'no live sessions'}
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

/* ----- day-scoped completed rows — same column system as the Sessions
 * table (project | title | model | start | duration pill) so Overview and
 * Sessions read as one table. Mobile keeps project | title | pill. ----- */

function CompletedRow({ session }: { session: Session }): JSX.Element {
  return (
    <Link
      to={`/sessions/${String(session.id)}`}
      className={`grid grid-cols-[110px_minmax(0,1fr)_max-content] items-center gap-3 px-3.5 py-2.5 transition-colors hover:bg-surface2 ${COMPLETED_ROW_GRID}`}
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
      <span className="hidden truncate font-mono text-[11px] text-ink-dim desk:block">
        {session.model ?? '—'}
      </span>
      <span className="hidden font-mono text-[11px] text-ink-3 desk:inline">
        {fmtTime(session.startedAt)}
      </span>
      <span className="justify-self-end">
        <DurationPill
          status={session.status}
          startedAt={session.startedAt}
          endedAt={session.endedAt}
        />
      </span>
    </Link>
  );
}

/* ----- phase 3.5: workspaces — "tasks · 14 days" slice. Each row is one
 * workspace card (agent-work.sh task) with the sessions it attracted and
 * the money they burned (Σ over task_sessions). Sits below "Recently
 * completed": same navy list card, same column discipline. ----- */

const OUTCOME_TONES: Record<TaskSummary['outcome'], string> = {
  active: 'text-green',
  done: 'text-ink-dim',
  archived: 'text-ink-3',
};

/* Fixed desk column widths (same discipline as COMPLETED_ROW_GRID):
 * [task 1fr] [project] [sessions] [Σ$] [outcome]. Mobile keeps task | Σ$ | outcome. */
const TASK_ROW_GRID = 'desk:grid-cols-[minmax(0,1fr)_120px_88px_70px_80px]';

function TaskRow({ task }: { task: TaskSummary }): JSX.Element {
  return (
    <div
      className={`grid grid-cols-[minmax(0,1fr)_70px_80px] items-center gap-3 px-3.5 py-2.5 ${TASK_ROW_GRID}`}
    >
      <span className="min-w-0">
        <span className="block truncate text-[13px] font-semibold">{task.title}</span>
        <span className="block truncate font-mono text-[10px] text-ink-dim">
          {task.externalId}
        </span>
      </span>
      <span className="hidden min-w-0 items-center gap-[7px] desk:flex">
        <span
          className="h-1.5 w-1.5 shrink-0 rounded-full"
          style={{ background: projectColor(task.projectSlug) }}
          aria-hidden="true"
        />
        <span className="truncate font-mono text-[11px] text-ink-3">
          {projectLabel(task.projectName, task.projectSlug)}
        </span>
      </span>
      <span className="hidden text-right font-mono text-[11px] text-ink-3 desk:block">
        {task.sessions} {task.sessions === 1 ? 'session' : 'sessions'}
      </span>
      <span className="text-right font-mono text-[12px] font-bold text-brand">
        {fmtCost(task.costUsd)}
      </span>
      <span
        className={`justify-self-end font-mono text-[10.5px] tracking-[0.06em] uppercase ${OUTCOME_TONES[task.outcome]}`}
      >
        {task.outcome}
      </span>
    </div>
  );
}

function TasksSlice({ tasks }: { tasks: TaskSummary[] | null }): JSX.Element | null {
  if (tasks === null || tasks.length === 0) return null; // no workspace repo → no section
  return (
    <>
      <SectionTitle>Tasks · 14 days</SectionTitle>
      <div className="divide-y divide-line-soft overflow-hidden rounded-xl border border-line bg-surface">
        {tasks.slice(0, 8).map((t) => (
          <TaskRow key={t.id} task={t} />
        ))}
      </div>
    </>
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

/* PENDING APPROVALS rail card (design §3.1): top-3 oldest-first + "all →"
 * link to /approvals; rendered only while something is actually pending. */
function ApprovalsRail({ pending }: { pending: PermissionRequest[] }): JSX.Element {
  const top = [...pending].sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)).slice(0, 3);
  return (
    <>
      <SectionTitle>Pending approvals · {pending.length}</SectionTitle>
      <div className="overflow-hidden rounded-xl border border-amber/35 bg-surface">
        {top.map((r) => (
          <Link
            key={r.id}
            to="/approvals"
            className="block border-b border-line-soft px-3.5 py-[9px] transition-colors last:border-b-0 hover:bg-surface2"
          >
            <div className="flex items-center gap-2">
              <span className="h-[6px] w-[6px] shrink-0 rounded-full bg-amber" aria-hidden="true" />
              <span className="font-mono text-[11px] font-semibold text-ink-2">{r.toolName}</span>
              <span className="ml-auto font-mono text-[10px] whitespace-nowrap text-amber">
                {fmtAgo(r.requestedAt)}
              </span>
            </div>
            <div className="mt-0.5 truncate font-mono text-[10.5px] text-ink-dim">
              {requestSummary(r)}
            </div>
          </Link>
        ))}
        <Link
          to="/approvals"
          className="block border-t border-line-soft px-3.5 py-2 font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
        >
          all approvals →
        </Link>
      </div>
    </>
  );
}

function Rail({
  stats,
  isToday,
  pending,
}: {
  stats: StatsOverview;
  isToday: boolean;
  pending: PermissionRequest[];
}): JSX.Element {
  const errTotal = stats.errors_by_project.reduce((a, r) => a + r.errors, 0);
  const costTotal = stats.cost_by_model.reduce((a, r) => a + r.cost_usd, 0);
  const dayName = isToday ? 'today' : fmtDayShort(stats.day);
  return (
    <div className="min-w-0">
      {isToday && pending.length > 0 && <ApprovalsRail pending={pending} />}
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
  // Live pending approvals (phase 2) — the rail card + ACTIVE tile subline.
  const [approvals, setApprovals] = useState<PermissionRequest[] | null>(null);
  // phase 3.5: workspaces — 14-day task slice (null/empty hides the section).
  const [tasks, setTasks] = useState<TaskSummary[] | null>(null);

  const isToday = day === isoDay();

  const loadSessions = useCallback((): void => {
    fetchSessions()
      .then((list) => {
        setSessions(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, []);

  const loadApprovals = useCallback((): void => {
    fetchApprovals('pending')
      .then(setApprovals)
      .catch(() => setApprovals(null)); // approvals API absent → stats fallback
  }, []);

  const loadStats = useCallback((): void => {
    fetchStatsOverview(day)
      .then((s) => {
        setStats(s);
        setStatsError(false);
      })
      .catch(() => setStatsError(true));
  }, [day]);

  const loadTasks = useCallback((): void => {
    fetchTasks()
      .then(setTasks)
      .catch(() => setTasks(null)); // older daemon / no workspace → section hidden
  }, []);

  useEffect(loadSessions, [loadSessions]);
  useEffect(loadApprovals, [loadApprovals]);
  useEffect(loadTasks, [loadTasks]);
  useEffect(() => {
    setStats(null);
    loadStats();
  }, [loadStats]);

  const reload = useCallback((): void => {
    loadSessions();
    loadStats();
    loadApprovals();
    loadTasks();
  }, [loadSessions, loadStats, loadApprovals, loadTasks]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type === 'event_appended') {
      const text = liveActionText(msg.payload.event);
      if (text !== null) {
        const { sessionId } = msg.payload;
        setNowById((prev) => ({ ...prev, [sessionId]: text }));
      }
      return;
    }
    if (msg.type === 'permission_requested' || msg.type === 'permission_resolved') {
      // Upsert then keep only pendings — this state backs the rail card and
      // the ACTIVE tile "waiting approval" subline.
      setApprovals((prev) =>
        prev === null
          ? prev
          : applyPermissionMessage(prev, msg).filter((r) => r.status === 'pending'),
      );
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

  const orphaned = live.filter((s) => s.procState === 'orphaned');
  const orphanedCostUsd = orphaned.reduce((sum, s) => sum + (s.costUsd ?? 0), 0);

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
        <KpiRow
          stats={stats}
          isToday={isToday}
          waitingLive={approvals !== null ? approvals.length : null}
        />
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
              {orphaned.length > 0 && (
                <div className="mb-3 flex items-center gap-2 rounded-xl border border-amber-500/30 bg-amber-500/10 px-4 py-2.5 text-[12.5px] text-amber-600">
                  <span className="font-semibold">
                    {orphaned.length} lost session{orphaned.length !== 1 ? 's' : ''}
                    {orphanedCostUsd > 0 ? ` · $${orphanedCostUsd.toFixed(2)} today` : ''}
                  </span>
                  <Link
                    to="/sessions?status=active"
                    className="ml-auto font-mono text-[11px] underline hover:no-underline"
                  >
                    view →
                  </Link>
                </div>
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

          <TasksSlice tasks={tasks} />
        </div>

        {stats !== null && <Rail stats={stats} isToday={isToday} pending={approvals ?? []} />}
      </div>
    </>
  );
}
