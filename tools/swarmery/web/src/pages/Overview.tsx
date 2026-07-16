// Command deck (Canvas restyle): a status-sentence hero derived from live
// counts, a reliability/cost/quality tri-stat bar, a vertical "spine" of
// today's notable sessions (expandable to a lazy-fetched tool trace), and a
// sticky right rail of pending approvals + error triage. Data wiring is 100%
// the existing hooks (sessions + stats/overview + approvals). The Quality tile
// reads stats.tests_passed/failed/skipped (test_run aggregates); on days with
// no test signal those fields are absent and it degrades to a neutral dash.

import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type {
  Event,
  PermissionRequest,
  Session,
  SessionDetail,
  StatsOverview,
  WSMessage,
} from '../api/types';
import { fetchApprovals, fetchSession, fetchSessions, fetchStatsOverview } from '../api';
import { requestSummary } from '../lib/approvals';
import { projectColor } from '../lib/colors';
import {
  fmtAgo,
  fmtCost,
  fmtDayShort,
  fmtTime,
  fmtTokens,
  isoDay,
} from '../lib/format';
import { argSummary } from '../lib/payload';
import { useScope } from '../lib/scope';
import { applyPermissionMessage, applySessionMessage, useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { ProjectName } from '../components/ProjectName';

const LIVE_STATUSES = new Set<Session['status']>(['active', 'waiting_approval', 'idle']);
const MAX_SPINE_ROWS = 8;

function sessionDay(s: Session): string {
  return isoDay(new Date(s.endedAt ?? s.startedAt));
}

/* ----- eyebrow date/time ----- */

function EyebrowClock(): JSX.Element {
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
    <div className="font-mono text-[11px] tracking-[0.18em] text-ink-faint uppercase">{text}</div>
  );
}

/* ----- hero status sentence ----- */

function HeroHeadline({
  active,
  pending,
  errors,
}: {
  active: number;
  pending: number;
  errors: number;
}): JSX.Element {
  const activePart =
    active === 0 ? (
      'Nothing is running.'
    ) : active === 1 ? (
      <>
        One agent is <em className="not-italic text-green">still working</em>.
      </>
    ) : (
      <>
        {active} agents are <em className="not-italic text-green">still working</em>.
      </>
    );
  const pendingPart =
    pending === 0 ? null : (
      <span className="text-ink-dim">
        {' '}
        {pending === 1 ? 'One thing waits' : `${String(pending)} things wait`} on you —
      </span>
    );
  const errorsPart =
    errors === 0 ? (
      ' nothing is on fire.'
    ) : (
      <span className="text-red"> {errors} error{errors === 1 ? '' : 's'} today.</span>
    );

  return (
    <h1 className="mt-3.5 max-w-[20ch] text-balance font-display text-[28px] leading-[1.16] font-medium tracking-[-0.015em] desk:text-[38px]">
      {activePart}
      {pendingPart}
      {errorsPart}
    </h1>
  );
}

/* ----- KPI tri-stat bar ----- */

function TriStatCell({
  label,
  children,
  border = true,
}: {
  label: string;
  children: ReactNode;
  border?: boolean;
}): JSX.Element {
  return (
    <div className={`flex-1 min-w-[150px] px-[18px] py-3.5 ${border ? 'border-r border-line' : ''}`}>
      <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
        {label}
      </div>
      <div className="mt-1.5 flex items-baseline gap-2">{children}</div>
    </div>
  );
}

function TriStat({ stats, prevErrors }: { stats: StatsOverview; prevErrors: number | null }): JSX.Element {
  const trend =
    prevErrors !== null && prevErrors > 0
      ? stats.errors / prevErrors
      : prevErrors === 0 && stats.errors > 0
        ? null
        : 1;
  const prevIdx = stats.series.findIndex((p) => p.day === stats.day) - 1;
  const prevDayLabel =
    prevIdx >= 0 && stats.series[prevIdx] !== undefined ? fmtDayShort(stats.series[prevIdx].day) : null;

  return (
    <div className="mt-[22px] flex flex-wrap overflow-hidden rounded-[14px] border border-line bg-surface">
      <TriStatCell label="Reliability">
        <span className="font-display text-[22px] leading-none font-semibold text-red desk:text-[26px]">
          {stats.errors}
        </span>
        <span className="font-mono text-[11px] text-ink-dim">
          errors
          {trend !== 1 && prevDayLabel !== null && (
            <>
              {' · '}
              <span className="text-red">
                {trend === null ? 'new' : `↑ ${trend.toFixed(1)}×`} vs {prevDayLabel}
              </span>
            </>
          )}
        </span>
      </TriStatCell>
      <TriStatCell label="Cost">
        <span className="font-display text-[22px] leading-none font-semibold text-brand desk:text-[26px]">
          {fmtCost(stats.cost_usd)}
        </span>
        <span className="font-mono text-[11px] text-ink-dim">
          {fmtTokens(stats.tokens_in + stats.tokens_out)} tokens
        </span>
      </TriStatCell>
      <TriStatCell label="Quality" border={false}>
        {stats.tests_passed != null ? (
          <>
            <span className="font-display text-[22px] leading-none font-semibold text-green desk:text-[26px]">
              {stats.tests_passed}
            </span>
            <span className="font-mono text-[11px] text-ink-dim">
              tests green
              {stats.tests_failed != null && stats.tests_failed > 0 && (
                <>
                  {' · '}
                  <span className="text-red">{stats.tests_failed} failed</span>
                </>
              )}
              {' · '}
              {stats.tests_skipped ?? 0} skipped
            </span>
          </>
        ) : (
          <>
            <span className="font-display text-[22px] leading-none font-semibold text-ink-dim desk:text-[26px]">
              —
            </span>
            <span className="font-mono text-[11px] text-ink-dim">no test data yet</span>
          </>
        )}
      </TriStatCell>
    </div>
  );
}

/* ----- the spine ----- */

interface SpineTraceRow {
  time: string;
  tool: string;
  detail: string;
  tone: string;
}

function traceOf(detail: SessionDetail): SpineTraceRow[] {
  return detail.events
    .filter((e: Event) => e.toolName !== null || e.type === 'commit' || e.type === 'error')
    .slice(-4)
    .map((e) => ({
      time: fmtTime(e.ts),
      tool: e.toolName ?? (e.type === 'commit' ? 'Commit' : e.type),
      detail: argSummary(e) ?? e.status ?? '',
      tone: e.status === 'error' ? 'text-red' : e.type === 'subagent_start' ? 'text-blue' : 'text-ink',
    }));
}

type SpineKind = 'active' | 'error' | 'done';

function spineKind(s: Session): SpineKind {
  if (s.status === 'killed') return 'error';
  if (LIVE_STATUSES.has(s.status)) return 'active';
  return 'done';
}

function NodeDot({ kind }: { kind: SpineKind }): JSX.Element {
  // Hollow colour ring centred ON the spine line. The line sits at 52px/66px,
  // the content column starts at 56px/70px → the line is -4px from the column
  // edge on both breakpoints, so a single -left-[9px] on a 10px ring (centre at
  // -4px) lands the ring dead-centre on the line. bg-bg masks the line inside.
  const cls =
    kind === 'active'
      ? 'border-green animate-pulse-dot'
      : kind === 'error'
        ? 'border-red'
        : 'border-ink-dim';
  return (
    <span
      className={`absolute -left-[9px] top-[16px] h-[10px] w-[10px] shrink-0 rounded-full border-2 bg-bg ${cls}`}
      aria-hidden="true"
    />
  );
}

function statusLabel(s: Session): string {
  const span =
    s.status === 'active'
      ? 'working'
      : s.status === 'waiting_approval'
        ? 'waiting'
        : s.status === 'killed'
          ? 'error'
          : 'done';
  return span;
}

function SpineRow({
  session,
  open,
  onToggle,
}: {
  session: Session;
  open: boolean;
  onToggle: () => void;
}): JSX.Element {
  const navigate = useNavigate();
  const [trace, setTrace] = useState<SpineTraceRow[] | null>(null);
  const [traceError, setTraceError] = useState(false);
  const kind = spineKind(session);

  useEffect(() => {
    if (!open || trace !== null || traceError) return;
    fetchSession(session.id)
      .then((d) => setTrace(traceOf(d)))
      .catch(() => setTraceError(true));
  }, [open, session.id, trace, traceError]);

  const time = fmtTime(session.startedAt);
  const rel = fmtAgo(session.startedAt);
  const costTokens = [
    session.costUsd != null ? fmtCost(session.costUsd) : null,
    session.tokens != null ? fmtTokens(session.tokens) : null,
  ]
    .filter((v): v is string => v !== null)
    .join(' · ');

  const chipTone =
    kind === 'active' ? 'border-green/40 text-green' : kind === 'error' ? 'border-red/40 text-red' : 'border-line-strong text-ink-dim';

  return (
    <div className="relative grid grid-cols-[56px_1fr] desk:grid-cols-[70px_1fr]">
      <div className="pt-3 pr-3 text-right desk:pr-5">
        <div className="font-mono text-[11px] text-ink-dim">{time}</div>
        <div className="font-mono text-[9.5px] text-ink-faint">{rel}</div>
      </div>
      <div className="relative border-b border-line-soft pt-3 pb-3.5 pl-5 desk:pl-[26px]">
        <NodeDot kind={kind} />
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="block w-full min-w-0 rounded-md text-left focus-visible:outline-2 focus-visible:outline-brand"
        >
          <div className="flex flex-wrap items-center gap-[9px]">
            <ProjectName
              name={session.projectName}
              slug={session.projectSlug}
              className="font-mono text-[10.5px]"
            />
            <span
              className={`rounded-full border px-[9px] py-px font-mono text-[10px] whitespace-nowrap ${chipTone}`}
            >
              {statusLabel(session)}
            </span>
            {costTokens !== '' && (
              <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-ink-faint">
                {costTokens}
              </span>
            )}
          </div>
          <div
            className={`mt-[5px] text-[15.5px] font-semibold tracking-[-0.01em] ${
              session.title === null ? 'font-normal text-ink-faint italic' : ''
            }`}
          >
            {session.title ?? '(untitled session)'}
          </div>
          {session.why != null && session.why !== '' ? (
            <div className="mt-[3px] max-w-[64ch] text-[13px] leading-[1.5] text-ink-3 [text-wrap:pretty]">
              <span className="text-ink-faint">→ </span>
              {session.why}
            </div>
          ) : (
            session.gitBranch !== null && (
              <div className="mt-[3px] max-w-[64ch] text-[13px] leading-[1.5] text-ink-3">
                <span className="text-ink-faint">→ </span>
                {session.gitBranch}
                {session.model !== null ? ` · ${session.model}` : ''}
              </div>
            )
          )}
        </button>
        {open && (
          <div className="mt-2.5 flex flex-col gap-[9px] border-l border-line-strong pl-4">
            {trace === null && !traceError && (
              <div className="font-mono text-[11px] text-ink-dim">loading trace…</div>
            )}
            {traceError && (
              <div className="font-mono text-[11px] text-ink-dim">trace unavailable</div>
            )}
            {trace !== null && trace.length === 0 && (
              <div className="font-mono text-[11px] text-ink-dim">no tool activity recorded</div>
            )}
            {trace?.map((t, i) => (
              <div key={i} className="flex items-baseline gap-2.5">
                <span className="min-w-[38px] font-mono text-[10px] text-ink-faint">{t.time}</span>
                <span className={`min-w-[64px] font-mono text-[11px] font-medium ${t.tone}`}>
                  {t.tool}
                </span>
                <span className="min-w-0 text-[12.5px] leading-[1.45] text-ink-3">{t.detail}</span>
              </div>
            ))}
            <button
              type="button"
              onClick={() => void navigate(`/sessions/${String(session.id)}`)}
              className="w-fit font-mono text-[10.5px] text-brand hover:underline focus-visible:outline-2 focus-visible:outline-brand"
            >
              open session →
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function Spine({ sessions }: { sessions: Session[] }): JSX.Element {
  const [openId, setOpenId] = useState<number | null>(null);
  const today = isoDay();
  const rows = sessions
    .filter((s) => LIVE_STATUSES.has(s.status) || sessionDay(s) === today)
    .sort((a, b) => (b.endedAt ?? b.startedAt).localeCompare(a.endedAt ?? a.startedAt))
    .slice(0, MAX_SPINE_ROWS);

  return (
    <>
      <div className="mt-[34px] mb-2.5 flex items-center gap-3">
        <h2 className="font-mono text-[11px] tracking-[0.16em] text-ink-dim uppercase">
          The spine · today
        </h2>
        <span className="h-px flex-1 bg-line" aria-hidden="true" />
        <Link to="/sessions" className="font-mono text-[10.5px] text-ink-faint hover:text-brand">
          all sessions →
        </Link>
      </div>
      {rows.length === 0 ? (
        <Empty>nothing notable yet today</Empty>
      ) : (
        <div className="relative">
          <div
            className="absolute top-3.5 bottom-2 left-[52px] w-px bg-[linear-gradient(180deg,#2a2e37,#2a2e37_82%,transparent)] desk:left-[66px]"
            aria-hidden="true"
          />
          {rows.map((s) => (
            <SpineRow
              key={s.id}
              session={s}
              open={openId === s.id}
              onToggle={() => setOpenId((prev) => (prev === s.id ? null : s.id))}
            />
          ))}
        </div>
      )}
    </>
  );
}

/* ----- right rail: waiting on you ----- */

function WaitingCard({ request }: { request: PermissionRequest }): JSX.Element {
  const okLabel = request.toolName === 'AskUserQuestion' ? 'answer' : 'approve';
  return (
    <div className="mt-3.5 rounded-xl border border-amber/28 bg-amber/5 px-3.5 py-3">
      <div className="flex items-center gap-2">
        <span className="font-mono text-[12px] font-bold text-ink">{request.toolName}</span>
        <span className="ml-auto font-mono text-[10px] text-amber">{fmtAgo(request.requestedAt)}</span>
      </div>
      <div className="mt-1.5 text-[12.5px] leading-[1.45] text-ink-3 [text-wrap:pretty]">{requestSummary(request)}</div>
      <div className="mt-2 flex items-center gap-[7px]">
        <span
          className="h-[5px] w-[5px] shrink-0 rounded-full"
          style={{ background: projectColor(String(request.sessionId)) }}
          aria-hidden="true"
        />
        <span className="truncate font-mono text-[10px] text-ink-faint">
          session #{request.sessionId}
        </span>
      </div>
      <div className="mt-2.5 flex gap-1.5">
        <Link
          to="/approvals"
          className="flex-1 rounded-lg border border-green/40 bg-green/10 py-1.5 text-center font-mono text-[11px] font-semibold text-green transition-colors hover:bg-green/20 focus-visible:outline-2 focus-visible:outline-brand"
        >
          {okLabel}
        </Link>
        <Link
          to="/approvals"
          className="flex-1 rounded-lg border border-line-strong py-1.5 text-center font-mono text-[11px] text-ink-3 transition-colors hover:bg-surface2 focus-visible:outline-2 focus-visible:outline-brand"
        >
          review →
        </Link>
      </div>
    </div>
  );
}

function WaitingRail({ pending }: { pending: PermissionRequest[] }): JSX.Element | null {
  if (pending.length === 0) return null;
  const top = [...pending].sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)).slice(0, 3);
  return (
    <div>
      <div className="flex items-center gap-2">
        <span
          className="h-[7px] w-[7px] shrink-0 animate-blink-dot rounded-full bg-amber"
          aria-hidden="true"
        />
        <h2 className="font-mono text-[11px] tracking-[0.14em] text-amber uppercase">
          Waiting on you · {pending.length}
        </h2>
      </div>
      {top.map((r) => (
        <WaitingCard key={r.id} request={r} />
      ))}
    </div>
  );
}

/* ----- right rail: needs triage ----- */

function TriageBar({ pct }: { pct: number }): JSX.Element {
  return (
    <div className="mt-1 h-[3px] overflow-hidden rounded-full bg-line">
      <div
        className="h-full rounded-full bg-red/70"
        style={{ width: `${String(Math.round(pct * 100))}%` }}
      />
    </div>
  );
}

function TriageRail({ stats }: { stats: StatsOverview }): JSX.Element {
  const rows = stats.errors_by_project;
  const total = rows.reduce((a, r) => a + r.errors, 0);
  return (
    <div className="mt-4">
      <div className="flex items-center gap-2">
        <span className="h-[7px] w-[7px] shrink-0 rounded-full bg-red" aria-hidden="true" />
        <h2 className="font-mono text-[11px] tracking-[0.14em] text-red uppercase">Needs triage</h2>
      </div>
      <div className="mt-3.5 rounded-xl border border-line bg-surface px-[15px] py-[13px]">
        <div className="flex items-baseline justify-between">
          <span className="font-mono text-[11px] text-ink-dim">
            errors across {rows.length} {rows.length === 1 ? 'project' : 'projects'}
          </span>
          <span className="font-display text-[20px] leading-none font-semibold text-red">
            {stats.errors}
          </span>
        </div>
        {rows.length === 0 ? (
          <div className="mt-2 font-mono text-[11px] text-ink-dim">no errors — clean day</div>
        ) : (
          rows.map((row) => (
            <div key={row.slug} className="mt-[11px]">
              <div className="flex justify-between font-mono text-[11px]">
                <ProjectName name={row.name} slug={row.slug} className="truncate" />
                <span className="text-red">{row.errors}</span>
              </div>
              <TriageBar pct={total > 0 ? row.errors / total : 0} />
            </div>
          ))
        )}
      </div>
    </div>
  );
}

/* ----- screen ----- */

export function Overview(): JSX.Element {
  const day = isoDay();
  const { scope } = useScope();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stats, setStats] = useState<StatsOverview | null>(null);
  const [prevStats, setPrevStats] = useState<StatsOverview | null>(null);
  const [statsError, setStatsError] = useState(false);
  const [approvals, setApprovals] = useState<PermissionRequest[] | null>(null);

  const loadSessions = useCallback((): void => {
    fetchSessions(scope !== null ? { project: scope } : {})
      .then((page) => {
        setSessions(page.sessions);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [scope]);

  const loadApprovals = useCallback((): void => {
    // Deliberately unscoped: a pending approval must never be invisible.
    fetchApprovals('pending')
      .then(setApprovals)
      .catch(() => setApprovals(null)); // approvals API absent → rail card hidden
  }, []);

  const loadStats = useCallback((): void => {
    fetchStatsOverview(day, scope ?? undefined)
      .then((s) => {
        setStats(s);
        setStatsError(false);
        const prevIdx = s.series.findIndex((p) => p.day === s.day) - 1;
        setPrevStats(prevIdx >= 0 ? { ...s, errors: s.series[prevIdx]?.errors ?? 0 } : null);
      })
      .catch(() => setStatsError(true));
  }, [day, scope]);

  useEffect(loadSessions, [loadSessions]);
  useEffect(loadApprovals, [loadApprovals]);
  useEffect(loadStats, [loadStats]);

  const reload = useCallback((): void => {
    loadSessions();
    loadStats();
    loadApprovals();
  }, [loadSessions, loadStats, loadApprovals]);

  // Mirrors Sessions.tsx: loadSessions is server-scoped, so WS-applied
  // sessions must pass the same scope filter or an out-of-scope
  // session_created/session_updated pollutes the list until reload.
  const matchesProject = useCallback(
    (s: Session): boolean => scope === null || s.projectSlug === scope,
    [scope],
  );

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'event_appended') return; // spine reads live counts, not per-event text
      if (msg.type === 'permission_requested' || msg.type === 'permission_resolved') {
        setApprovals((prev) =>
          prev === null
            ? prev
            : applyPermissionMessage(prev, msg).filter((r) => r.status === 'pending'),
        );
        return;
      }
      setSessions((prev) =>
        prev === null ? prev : applySessionMessage(prev, msg).filter(matchesProject),
      );
    },
    [matchesProject],
  );
  useLiveUpdates(onMessage, reload);

  const activeCount = (sessions ?? []).filter((s) => LIVE_STATUSES.has(s.status)).length;
  const pendingCount = approvals?.length ?? 0;

  return (
    <div className="wide:grid wide:grid-cols-[minmax(0,1fr)_320px] wide:items-start">
      <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
        <EyebrowClock />
        {stats !== null ? (
          <HeroHeadline active={activeCount} pending={pendingCount} errors={stats.errors} />
        ) : (
          <h1 className="mt-3.5 max-w-[20ch] font-display text-[28px] leading-[1.16] font-medium tracking-[-0.015em] text-ink-dim desk:text-[38px]">
            Reading today's activity…
          </h1>
        )}

        {stats !== null ? (
          <TriStat stats={stats} prevErrors={prevStats?.errors ?? null} />
        ) : statsError ? (
          <div className="mt-5 font-mono text-[11px] text-ink-dim">stats unavailable</div>
        ) : (
          <Loading label="stats…" />
        )}

        {error !== null && <ErrorBox message={error} onRetry={loadSessions} />}
        {sessions === null && error === null ? (
          <Loading label="sessions…" />
        ) : (
          <Spine sessions={sessions ?? []} />
        )}
      </div>

      <aside className="min-w-0 border-line px-4 pb-10 wide:sticky wide:top-14 wide:min-h-[calc(100vh-56px)] wide:border-l wide:px-7 wide:pt-[34px] wide:pb-10">
        {approvals !== null && approvals.length > 0 && <WaitingRail pending={approvals} />}
        {stats !== null && <TriageRail stats={stats} />}
      </aside>
    </div>
  );
}
