// Command deck (Canvas restyle): a status-sentence hero derived from live
// counts, a reliability/cost/quality tri-stat bar, a vertical "spine" of
// today's notable sessions plus any still-running or stuck ones regardless of
// start day (expandable to a lazy-fetched tool trace), and a
// sticky right rail of pending approvals + error triage. Data wiring is 100%
// the existing hooks (sessions + stats/overview + approvals). The Quality tile
// reads stats.tests_passed/failed/skipped (test_run aggregates); on days with
// no test signal those fields are absent and it degrades to a neutral dash.

import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type {
  ErrorGroup,
  Event,
  PermissionRequest,
  Session,
  SessionDetail,
  StatsOverview,
  WSMessage,
} from '../api/types';
import {
  fetchApprovals,
  fetchErrorGroups,
  fetchSession,
  fetchSessions,
  fetchStatsOverview,
} from '../api';
import { requestSummary } from '../lib/approvals';
import { projectColor } from '../lib/colors';
import {
  addDays,
  fmtAgo,
  fmtCost,
  fmtDayShort,
  fmtTime,
  fmtTokens,
  isoDay,
} from '../lib/format';
import { argSummary } from '../lib/payload';
import { usePageSearch } from '../lib/pageSearch';
import { useScope } from '../lib/scope';
import { sessionState, useNowMs } from '../lib/sessionState';
import { applyPermissionMessage, applySessionMessage, useLiveUpdates } from '../lib/ws';
import { ApproxHint, Empty, ErrorBox, Loading } from '../components/ui';
import { ProjectName } from '../components/ProjectName';

const MAX_SPINE_ROWS = 8;
function sessionDay(s: Session): string {
  return isoDay(new Date(s.startedAt));
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
  stuck,
  pending,
  errors,
}: {
  active: number;
  stuck: number;
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
  const stuckPart =
    stuck === 0 ? null : (
      <span className="text-amber">
        {' '}
        {stuck === 1 ? 'One session looks stuck.' : `${String(stuck)} sessions look stuck.`}
      </span>
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
      {stuckPart}
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

type SpineKind = 'active' | 'stuck' | 'error' | 'done';

function spineKind(s: Session, nowMs: number): SpineKind {
  if (s.status === 'killed') return 'error';
  const st = sessionState(s, nowMs);
  if (st === 'running') return 'active';
  if (st === 'stuck') return 'stuck';
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
      : kind === 'stuck'
        ? 'border-amber'
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

function statusLabel(s: Session, nowMs: number): string {
  if (s.status === 'waiting_approval') return 'waiting';
  if (s.status === 'killed') return 'error';
  const st = sessionState(s, nowMs);
  return st === 'running' ? 'working' : st === 'stuck' ? 'stuck' : 'done';
}

function SpineRow({
  session,
  nowMs,
  open,
  onToggle,
}: {
  session: Session;
  nowMs: number;
  open: boolean;
  onToggle: () => void;
}): JSX.Element {
  const navigate = useNavigate();
  const [trace, setTrace] = useState<SpineTraceRow[] | null>(null);
  const [traceError, setTraceError] = useState(false);
  const kind = spineKind(session, nowMs);

  useEffect(() => {
    if (!open || trace !== null || traceError) return;
    fetchSession(session.id)
      .then((d) => setTrace(traceOf(d)))
      .catch(() => setTraceError(true));
  }, [open, session.id, trace, traceError]);

  // Finished sessions are anchored on the spine by when they ended (that is the
  // today-relevant moment and the sort key); live ones by when they started.
  const anchor = session.endedAt ?? session.startedAt;
  const time = fmtTime(anchor);
  const rel = fmtAgo(anchor);
  const startedDay = sessionDay(session);
  const startedLabel =
    startedDay === isoDay()
      ? null
      : startedDay === addDays(isoDay(), -1)
        ? 'started yesterday'
        : `started ${fmtDayShort(startedDay)}`;
  const costTokens = [
    session.costUsd != null ? fmtCost(session.costUsd) : null,
    session.tokens != null ? fmtTokens(session.tokens) : null,
  ]
    .filter((v): v is string => v !== null)
    .join(' · ');

  const chipTone =
    kind === 'active'
      ? 'border-green/40 text-green'
      : kind === 'stuck'
        ? 'border-amber/40 text-amber'
        : kind === 'error'
          ? 'border-red/40 text-red'
          : 'border-line-strong text-ink-dim';

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
              {statusLabel(session, nowMs)}
            </span>
            {startedLabel !== null && (
              <span className="rounded-full border border-line-strong px-[9px] py-px font-mono text-[10px] whitespace-nowrap text-ink-faint">
                {startedLabel}
              </span>
            )}
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

function Spine({
  sessions,
  nowMs,
  query,
}: {
  sessions: Session[];
  nowMs: number;
  query: string;
}): JSX.Element {
  const [openId, setOpenId] = useState<number | null>(null);
  const today = isoDay();
  const matchesQuery = (s: Session): boolean =>
    query === '' ||
    [s.title, s.projectName, s.projectSlug, s.gitBranch].some(
      (v) => v != null && v.toLowerCase().includes(query),
    );
  const matched = sessions
    // Running and stuck rows always show (a stuck overnight session is exactly
    // what you want to see and kill); done rows only when they belong to today.
    .filter((s) => (sessionState(s, nowMs) !== 'done' || sessionDay(s) === today) && matchesQuery(s))
    .sort((a, b) => (b.endedAt ?? b.startedAt).localeCompare(a.endedAt ?? a.startedAt));
  // Non-done rows are the page's whole point (a stuck overnight session must
  // stay visible to be killed) — they always survive the row cap; today's done
  // rows fill whatever room is left.
  const live = matched.filter((s) => sessionState(s, nowMs) !== 'done');
  const done = matched.filter((s) => sessionState(s, nowMs) === 'done');
  const rows = [...live.slice(0, MAX_SPINE_ROWS), ...done].slice(0, MAX_SPINE_ROWS);

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
        <Empty>{query !== '' ? 'no sessions match the filter' : 'nothing notable yet today'}</Empty>
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
              nowMs={nowMs}
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

/* ----- error drill-down modal (analytics uplift) ----- */

function ErrorDrilldown({
  day,
  project,
  projectName,
  onClose,
}: {
  day: string;
  project: string | null;
  /** Display name for the header; falls back to the slug, then "all projects". */
  projectName: string | null;
  onClose: () => void;
}): JSX.Element {
  const [groups, setGroups] = useState<ErrorGroup[] | null>(null);
  const [approx, setApprox] = useState(false);
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    setGroups(null);
    setApprox(false);
    setFailed(false);
    fetchErrorGroups({ from: day, to: day, ...(project !== null ? { project } : {}) })
      .then((r) => {
        setGroups(r.groups);
        setApprox(r.approx);
      })
      .catch(() => setFailed(true));
  }, [day, project]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 p-4"
      role="dialog"
      aria-modal="true"
      aria-label="error drill-down"
      onClick={onClose}
    >
      <div
        className="mt-[8vh] max-h-[76vh] w-full max-w-[560px] overflow-y-auto rounded-[14px] border border-line-strong bg-surface p-5"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3">
          <h2 className="min-w-0 flex-1 truncate font-mono text-[11px] tracking-[0.14em] text-red uppercase">
            Errors · {projectName ?? project ?? 'all projects'} · {day}
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim hover:text-ink"
          >
            close
          </button>
        </div>

        {failed && <div className="mt-3 font-mono text-[11px] text-red">failed to load error groups</div>}
        {groups === null && !failed && <div className="mt-3 font-mono text-[11px] text-ink-dim">loading…</div>}
        {groups !== null && groups.length === 0 && (
          <div className="mt-3 font-mono text-[11px] text-ink-dim">no errors for this day</div>
        )}
        {approx && <ApproxHint />}

        {groups !== null &&
          groups.map((g) => (
            <div key={g.key} className="mt-3 rounded-xl border border-line px-3 py-2.5">
              <button
                type="button"
                onClick={() => setOpen((o) => (o === g.key ? null : g.key))}
                className="block w-full text-left"
                aria-expanded={open === g.key}
              >
                <div className="flex items-baseline gap-2 font-mono text-[11.5px]">
                  <span className="shrink-0 text-red">{g.count}×</span>
                  <span className="min-w-0 flex-1 truncate text-ink-3" title={g.example}>
                    {g.example}
                  </span>
                  <span className="shrink-0 text-ink-faint">{fmtAgo(g.last_ts)}</span>
                </div>
              </button>
              {open === g.key && (
                <div className="mt-2 flex flex-col gap-1 border-t border-line pt-2">
                  {g.samples.map((s) => (
                    <Link
                      key={s.session_id}
                      to={`/sessions/${String(s.session_id)}`}
                      className="truncate font-mono text-[11px] text-blue hover:underline"
                    >
                      #{s.session_id} · {s.title ?? 'untitled session'}
                    </Link>
                  ))}
                </div>
              )}
            </div>
          ))}
      </div>
    </div>
  );
}

function TriageRail({
  stats,
  onSelect,
}: {
  stats: StatsOverview;
  onSelect: (slug: string | null, name: string | null) => void;
}): JSX.Element {
  const rows = stats.errors_by_project;
  const total = rows.reduce((a, r) => a + r.errors, 0);
  return (
    <div className="mt-4">
      <div className="flex items-center gap-2">
        <span className="h-[7px] w-[7px] shrink-0 rounded-full bg-red" aria-hidden="true" />
        <h2 className="font-mono text-[11px] tracking-[0.14em] text-red uppercase">Needs triage</h2>
      </div>
      <div className="mt-3.5 rounded-xl border border-line bg-surface px-[15px] py-[13px]">
        <button
          type="button"
          onClick={() => onSelect(null, null)}
          className="flex w-full items-baseline justify-between text-left"
          title="show all error groups"
        >
          <span className="font-mono text-[11px] text-ink-dim">
            errors across {rows.length} {rows.length === 1 ? 'project' : 'projects'}
          </span>
          <span className="font-display text-[20px] leading-none font-semibold text-red">
            {stats.errors}
          </span>
        </button>
        {rows.length === 0 ? (
          <div className="mt-2 font-mono text-[11px] text-ink-dim">no errors — clean day</div>
        ) : (
          rows.map((row) => (
            <button
              key={row.slug}
              type="button"
              onClick={() => onSelect(row.slug, row.name)}
              className="mt-[11px] block w-full text-left"
              title={`show ${row.slug} error groups`}
            >
              <div className="flex justify-between font-mono text-[11px]">
                <ProjectName name={row.name} slug={row.slug} className="truncate" />
                <span className="text-red">{row.errors}</span>
              </div>
              <TriageBar pct={total > 0 ? row.errors / total : 0} />
            </button>
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
  const query = usePageSearch();
  const nowMs = useNowMs();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stats, setStats] = useState<StatsOverview | null>(null);
  const [prevStats, setPrevStats] = useState<StatsOverview | null>(null);
  const [statsError, setStatsError] = useState(false);
  const [approvals, setApprovals] = useState<PermissionRequest[] | null>(null);
  const [drill, setDrill] = useState<{ project: string | null; name: string | null } | null>(null);

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

  const states = (sessions ?? []).map((s) => sessionState(s, nowMs));
  const activeCount = states.filter((st) => st === 'running').length;
  const stuckCount = states.filter((st) => st === 'stuck').length;
  const pendingCount = approvals?.length ?? 0;

  return (
    <div className="wide:grid wide:grid-cols-[minmax(0,1fr)_320px] wide:items-start">
      <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
        <EyebrowClock />
        {stats !== null ? (
          <HeroHeadline
            active={activeCount}
            stuck={stuckCount}
            pending={pendingCount}
            errors={stats.errors}
          />
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
          <Spine sessions={sessions ?? []} nowMs={nowMs} query={query} />
        )}
      </div>

      <aside className="min-w-0 border-line px-4 pb-10 wide:sticky wide:top-14 wide:min-h-[calc(100vh-56px)] wide:border-l wide:px-7 wide:pt-[34px] wide:pb-10">
        {approvals !== null && approvals.length > 0 && <WaitingRail pending={approvals} />}
        {stats !== null && (
          <TriageRail
            stats={stats}
            // "all errors" under a global scope drills into that scope, so the
            // modal always matches the scoped total shown on the rail. The
            // display name comes from the clicked row, or is looked up for the
            // scope slug so the header never shows a raw slug when a name exists.
            onSelect={(slug, name) => {
              const project = slug ?? scope;
              setDrill({
                project,
                name:
                  name ??
                  (project !== null
                    ? (stats.errors_by_project.find((r) => r.slug === project)?.name ?? null)
                    : null),
              });
            }}
          />
        )}
      </aside>

      {drill !== null && (
        <ErrorDrilldown
          day={day}
          project={drill.project}
          projectName={drill.name}
          onClose={() => setDrill(null)}
        />
      )}
    </div>
  );
}
