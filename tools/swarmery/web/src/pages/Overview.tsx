// Command deck (Canvas v2 restyle): the home reframed around HUMAN WAIT TIME.
// A wait-narrative hero ("Agents waited X on you today · N tools caused Y% of
// it") over a three-cell ledger (waited / auto-approved / still-blocked), then
// "The day" — per-project timeline lanes rasterised into working / waiting /
// idle segments — then "Where your time went", today's approvals grouped by
// tool with an inline "stop asking" that writes an auto-approve rule. Below
// that the vertical "spine" of today's notable sessions, and a sticky right
// rail of what's blocked on you plus error triage.
//
// Data wiring is 100% existing endpoints: sessions + stats/overview +
// approvals (pending AND resolved) + approval-rules. Wait time is derived from
// each PermissionRequest (resolvedAt − requestedAt; live age for pendings);
// auto-approved rows are the ones the daemon resolved via 'rule'. No backend
// change. On days with no approval signal the wait sections degrade to a
// neutral "nothing waited on you" line.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type {
  ApprovalRule,
  ErrorGroup,
  Event,
  PermissionRequest,
  Session,
  SessionDetail,
  StatsOverview,
  WSMessage,
} from '../api/types';
import {
  MOCK,
  createApprovalRule,
  fetchApprovalRules,
  fetchApprovals,
  fetchErrorGroups,
  fetchSession,
  isPendingSession,
  fetchSessions,
  fetchStatsOverview,
} from '../api';
import { questionsOf, requestSummary, suggestRulePattern } from '../lib/approvals';
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
import { PageSearchInput } from '../components/PageSearchInput';
import { ScopeChip } from '../components/ScopeChip';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { ProjectName } from '../components/ProjectName';

const MAX_SPINE_ROWS = 8;
function sessionDay(s: Session): string {
  return isoDay(new Date(s.startedAt));
}

/* ----- wait-time helpers (shared by hero, ledger, day, interrupts) ----- */

/** Compact human wait, e.g. "38s", "47m", "1h 12m". Never shows seconds past a minute. */
function fmtWait(ms: number): string {
  if (ms < 1000) return '0s';
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m === 0 ? `${h}h` : `${h}h ${m}m`;
}

/** Wait a request cost the human: resolved → its span; still pending → its live age. */
function waitMs(r: PermissionRequest, nowMs: number): number {
  const start = new Date(r.requestedAt).getTime();
  const end = r.resolvedAt !== null ? new Date(r.resolvedAt).getTime() : nowMs;
  return Math.max(0, end - start);
}

/** Auto-approved rows are the ones the daemon resolved via a rule — no human stop. */
function wasAutoApproved(r: PermissionRequest): boolean {
  return r.resolvedVia === 'rule';
}

function isToday(iso: string): boolean {
  return isoDay(new Date(iso)) === isoDay();
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

/* ----- hero: how long agents waited on you ----- */

interface ToolWait {
  /** Rule-pattern key, e.g. "Bash(git *)" or "Edit" — the "stop asking" target. */
  key: string;
  count: number;
  waitedMs: number;
  covered: boolean;
}

function WaitHero({ waitedMs, tools }: { waitedMs: number; tools: ToolWait[] }): JSX.Element {
  if (waitedMs < 1000 || tools.length === 0) {
    return (
      <h1 className="mt-3.5 max-w-[22ch] text-balance font-display text-[28px] leading-[1.16] font-medium tracking-[-0.015em] desk:text-[38px]">
        Nothing waited on you today. <span className="text-ink-dim">Agents ran clean.</span>
      </h1>
    );
  }
  // Smallest set of tools whose cumulative wait covers the majority (≥60%) — the
  // honest reading of "N tools caused Y% of it".
  const sorted = [...tools].sort((a, b) => b.waitedMs - a.waitedMs);
  let acc = 0;
  let topCount = 0;
  for (const t of sorted) {
    acc += t.waitedMs;
    topCount += 1;
    if (acc >= waitedMs * 0.6) break;
  }
  const topShare = Math.round((acc / waitedMs) * 100);
  return (
    <h1 className="mt-3.5 max-w-[22ch] text-balance font-display text-[28px] leading-[1.16] font-medium tracking-[-0.015em] desk:text-[38px]">
      Agents waited <em className="not-italic text-red">{fmtWait(waitedMs)}</em> on you today.{' '}
      <span className="text-ink-dim">
        {topCount} tool{topCount === 1 ? '' : 's'} caused {topShare}% of it.
      </span>
    </h1>
  );
}

/* ----- ledger: waited / auto-approved / still-blocked ----- */

interface LedgerCell {
  label: string;
  value: string;
  sub: string;
  tone: string;
  big: boolean;
}

function Ledger({ cells }: { cells: LedgerCell[] }): JSX.Element {
  return (
    <div className="mt-6 grid grid-cols-[repeat(auto-fit,minmax(190px,1fr))] gap-x-10 gap-y-[22px] border-t border-line pt-5">
      {cells.map((c) => (
        <div key={c.label} className="min-w-0">
          <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
            {c.label}
          </div>
          <div
            className={`mt-[7px] font-display leading-none font-semibold ${c.tone} ${
              c.big ? 'text-[32px]' : 'text-[26px]'
            }`}
          >
            {c.value}
          </div>
          <div className="mt-[5px] font-mono text-[10.5px] text-ink-dim">{c.sub}</div>
        </div>
      ))}
    </div>
  );
}

/* ----- "the day": per-project timeline lanes ----- */

interface DaySeg {
  /** flex-grow weight (bucket count). */
  weight: number;
  tone: 'work' | 'wait' | 'error' | 'idle';
}
interface DayLane {
  slug: string;
  name: string;
  task: string;
  color: string;
  segs: DaySeg[];
  waitedMs: number;
  approvals: number;
  latestSessionId: number;
}

const DAY_BUCKETS = 64;
const SEG_BG: Record<DaySeg['tone'], string> = {
  work: 'var(--color-green)',
  wait: 'var(--color-amber)',
  error: 'var(--color-red)',
  idle: 'var(--color-line-soft)',
};

/** Rasterise a project's sessions + approval waits over [start,end] into segments. */
function buildLane(
  slug: string,
  name: string,
  sessions: Session[],
  approvals: PermissionRequest[],
  startMs: number,
  endMs: number,
  nowMs: number,
): DayLane {
  const span = Math.max(1, endMs - startMs);
  // Interval sources with a priority so overlaps resolve deterministically:
  // error > wait > work. Idle is the absence of any interval.
  interface Ival {
    s: number;
    e: number;
    tone: DaySeg['tone'];
    prio: number;
  }
  const ivals: Ival[] = [];
  for (const s of sessions) {
    const ss = new Date(s.startedAt).getTime();
    const se = s.endedAt !== null ? new Date(s.endedAt).getTime() : nowMs;
    const st = sessionState(s, nowMs);
    const tone: DaySeg['tone'] = s.status === 'killed' ? 'error' : st === 'stuck' ? 'wait' : 'work';
    ivals.push({ s: ss, e: se, tone, prio: tone === 'error' ? 3 : tone === 'wait' ? 2 : 1 });
  }
  let waitedMs = 0;
  for (const a of approvals) {
    const ws = new Date(a.requestedAt).getTime();
    const we = a.resolvedAt !== null ? new Date(a.resolvedAt).getTime() : nowMs;
    waitedMs += Math.max(0, we - ws);
    ivals.push({ s: ws, e: we, tone: 'wait', prio: 2 });
  }
  // Sample each bucket midpoint; keep the highest-priority interval covering it.
  const tones: DaySeg['tone'][] = [];
  for (let i = 0; i < DAY_BUCKETS; i++) {
    const mid = startMs + ((i + 0.5) / DAY_BUCKETS) * span;
    let best: Ival | null = null;
    for (const v of ivals) {
      if (mid >= v.s && mid <= v.e && (best === null || v.prio > best.prio)) best = v;
    }
    tones.push(best?.tone ?? 'idle');
  }
  // Coalesce adjacent same-tone buckets into weighted segments.
  const segs: DaySeg[] = [];
  for (const tone of tones) {
    const last = segs[segs.length - 1];
    if (last !== undefined && last.tone === tone) last.weight += 1;
    else segs.push({ tone, weight: 1 });
  }
  const latest = [...sessions].sort((a, b) =>
    (b.endedAt ?? b.startedAt).localeCompare(a.endedAt ?? a.startedAt),
  )[0];
  const task = latest?.title ?? latest?.gitBranch ?? 'session';
  return {
    slug,
    name,
    task,
    color: projectColor(slug),
    segs,
    waitedMs,
    approvals: approvals.length,
    latestSessionId: latest?.id ?? 0,
  };
}

function DayTimeline({
  lanes,
  startMs,
  endMs,
}: {
  lanes: DayLane[];
  startMs: number;
  endMs: number;
}): JSX.Element {
  const navigate = useNavigate();
  const ticks = [startMs, startMs + (endMs - startMs) / 2, endMs].map((t) =>
    fmtTime(new Date(t).toISOString()),
  );
  const legend: { label: string; tone: DaySeg['tone'] }[] = [
    { label: 'working', tone: 'work' },
    { label: 'waiting', tone: 'wait' },
    { label: 'idle', tone: 'idle' },
  ];
  return (
    <>
      <div className="mt-9 flex flex-wrap items-center gap-3">
        <h2 className="font-mono text-[11px] tracking-[0.16em] text-ink-dim uppercase">The day</h2>
        <span className="font-mono text-[10px] text-ink-faint">
          {fmtTime(new Date(startMs).toISOString())} → {fmtTime(new Date(endMs).toISOString())}
        </span>
        <span className="h-px flex-1 bg-line" aria-hidden="true" />
        <span className="flex flex-wrap gap-[11px] font-mono text-[10px] text-ink-faint">
          {legend.map((g) => (
            <span key={g.label} className="inline-flex items-center gap-[5px]">
              <span
                className="h-[8px] w-[8px] rounded-[2px]"
                style={{ background: SEG_BG[g.tone] }}
                aria-hidden="true"
              />
              {g.label}
            </span>
          ))}
        </span>
      </div>
      <div className="mt-3">
        {lanes.map((ln) => (
          <div
            key={ln.slug}
            className="grid grid-cols-[132px_minmax(0,1fr)] items-center gap-3.5 border-b border-line-soft py-[9px]"
          >
            <div className="min-w-0">
              <div className="flex items-center gap-1.5">
                <span
                  className="h-[6px] w-[6px] shrink-0 rounded-full"
                  style={{ background: ln.color }}
                  aria-hidden="true"
                />
                <span className="min-w-0 truncate font-mono text-[10.5px] text-ink-2">{ln.name}</span>
              </div>
              <div className="mt-0.5 truncate font-mono text-[9.5px] text-ink-faint">{ln.task}</div>
            </div>
            <div className="min-w-0">
              <button
                type="button"
                onClick={() =>
                  ln.latestSessionId > 0
                    ? void navigate(`/sessions/${String(ln.latestSessionId)}`)
                    : void navigate('/sessions')
                }
                className="flex h-[19px] w-full overflow-hidden rounded focus-visible:outline-2 focus-visible:outline-brand"
                data-tip={`open ${ln.name}`}
              >
                {ln.segs.map((sg, i) => (
                  <span
                    key={i}
                    style={{ flexGrow: sg.weight, flexBasis: 0, background: SEG_BG[sg.tone] }}
                    aria-hidden="true"
                  />
                ))}
              </button>
              <div
                className={`mt-1 font-mono text-[9.5px] ${ln.waitedMs > 0 ? 'text-amber' : 'text-ink-faint'}`}
              >
                {ln.waitedMs > 0
                  ? `waited ${fmtWait(ln.waitedMs)} · ${ln.approvals} approval${ln.approvals === 1 ? '' : 's'}`
                  : 'no waiting'}
              </div>
            </div>
          </div>
        ))}
        <div className="mt-[7px] grid grid-cols-[132px_minmax(0,1fr)] gap-3.5">
          <span />
          <div className="flex justify-between font-mono text-[9.5px] text-ink-faint">
            {ticks.map((t, i) => (
              <span key={i} className="whitespace-nowrap">
                {t}
              </span>
            ))}
          </div>
        </div>
      </div>
    </>
  );
}

/* ----- "where your time went": interrupts by tool + stop-asking ----- */

function Interrupts({
  tools,
  maxWait,
  onStopAsking,
}: {
  tools: ToolWait[];
  maxWait: number;
  onStopAsking: (key: string) => void;
}): JSX.Element {
  const [pending, setPending] = useState<Set<string>>(new Set());
  return (
    <>
      <div className="mt-[38px] flex items-center gap-3">
        <h2 className="font-mono text-[11px] tracking-[0.16em] text-ink-dim uppercase">
          Where your time went
        </h2>
        <span className="h-px flex-1 bg-line" aria-hidden="true" />
        <Link to="/approvals" className="font-mono text-[10.5px] text-ink-faint hover:text-brand">
          all approvals →
        </Link>
      </div>
      <div className="mt-1.5">
        {tools.map((t) => {
          const busy = pending.has(t.key);
          const covered = t.covered || busy;
          return (
            // The four trailing columns are fixed-width and already sum past a
            // 390px viewport, so below desk the tool key takes its own line
            // instead of pushing the row into horizontal overflow.
            <div
              key={t.key}
              className="flex flex-wrap items-center gap-3 border-b border-line-soft py-[11px] desk:flex-nowrap"
            >
              <span className="w-full min-w-0 truncate font-mono text-[12px] text-ink desk:w-[148px] desk:shrink-0">
                {t.key}
              </span>
              <span className="w-[92px] shrink-0 font-mono text-[11px] whitespace-nowrap text-ink-dim">
                {t.count} stop{t.count === 1 ? '' : 's'}
              </span>
              <span className="h-[5px] min-w-[24px] flex-1 overflow-hidden rounded-full bg-line-soft">
                <span
                  className="block h-full rounded-full bg-red/70"
                  style={{ width: `${String(maxWait > 0 ? Math.round((t.waitedMs / maxWait) * 100) : 0)}%` }}
                />
              </span>
              <span className="w-[58px] shrink-0 text-right font-display text-[15px] font-semibold text-red">
                {fmtWait(t.waitedMs)}
              </span>
              <span className="w-[108px] shrink-0 text-right">
                {covered ? (
                  <span className="font-mono text-[10px] text-green">✓ auto-approved</span>
                ) : (
                  <button
                    type="button"
                    data-tip="auto-approve this tool from now on"
                    onClick={() => {
                      setPending((p) => new Set(p).add(t.key));
                      onStopAsking(t.key);
                    }}
                    className="rounded-[7px] border border-line-strong px-2.5 py-[3px] font-mono text-[10px] text-ink-dim transition-colors hover:border-green/50 hover:text-green focus-visible:outline-2 focus-visible:outline-brand"
                  >
                    stop asking
                  </button>
                )}
              </span>
            </div>
          );
        })}
      </div>
      <p className="mt-3 max-w-[70ch] font-mono text-[10.5px] leading-[1.6] text-ink-faint">
        Every rule you add here removes a stop from tomorrow. Rules stay per-tool and per-project —
        narrow or revoke them in Approvals.
      </p>
    </>
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
      .then((d) => {
        // An integer id never answers 202, but the union says so — fail closed.
        if (isPendingSession(d)) setTraceError(true);
        else setTrace(traceOf(d));
      })
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

/* ----- right rail: blocked on you ----- */

/**
 * One glanceable line of context per card. AskUserQuestion never dumps its raw
 * `{questions:[…]}` payload — it reads as the first question plus a "+N more"
 * tail; the full form lives one click away under review →. Everything else is
 * the normal request summary. The card clamps this to two lines, so every card
 * in the rail is the same height regardless of tool.
 */
function blockedContext(request: PermissionRequest): string {
  if (request.toolName === 'AskUserQuestion') {
    const qs = questionsOf(request);
    if (qs !== null && qs.length > 0) {
      const first = qs[0]?.question ?? 'answer required';
      return qs.length > 1 ? `${first} · +${String(qs.length - 1)} more` : first;
    }
  }
  return requestSummary(request);
}

function BlockedCard({ request, nowMs }: { request: PermissionRequest; nowMs: number }): JSX.Element {
  const okLabel = request.toolName === 'AskUserQuestion' ? 'answer' : 'approve';
  const age = fmtWait(waitMs(request, nowMs));
  return (
    <div className="mt-3.5 rounded-xl border border-amber/28 bg-amber/5 px-3.5 py-3">
      <div className="flex items-center gap-2">
        <span className="font-mono text-[12px] font-bold text-ink">{request.toolName}</span>
        <span className="ml-auto font-mono text-[10px] text-amber">idle {age}</span>
      </div>
      <div className="mt-1.5 line-clamp-2 text-[12.5px] leading-[1.45] break-words text-ink-3 [text-wrap:pretty]">
        {blockedContext(request)}
      </div>
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

function BlockedRail({ pending, nowMs }: { pending: PermissionRequest[]; nowMs: number }): JSX.Element | null {
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
          Blocked on you · {pending.length}
        </h2>
      </div>
      <div className="mt-[7px] font-mono text-[10.5px] text-ink-faint">
        {pending.length === 1 ? 'one agent is' : `${String(pending.length)} agents are`} idle until
        you answer
      </div>
      {top.map((r) => (
        <BlockedCard key={r.id} request={r} nowMs={nowMs} />
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
        {approx && (
          <div className="mt-3 font-mono text-[10.5px] text-ink-faint">approximate — sampled</div>
        )}

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
                  <span className="min-w-0 flex-1 truncate text-ink-3" data-tip={g.example}>
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
    <div className="mt-[30px]">
      <div className="flex items-center gap-2">
        <span className="h-[7px] w-[7px] shrink-0 rounded-full bg-red" aria-hidden="true" />
        <h2 className="font-mono text-[11px] tracking-[0.14em] text-red uppercase">Needs triage</h2>
      </div>
      <div className="mt-3.5 rounded-xl border border-line bg-surface px-[15px] py-[13px]">
        <button
          type="button"
          onClick={() => onSelect(null, null)}
          className="flex w-full items-baseline justify-between text-left"
          data-tip="show all error groups"
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
              data-tip={`show ${row.slug} error groups`}
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

/** 09:00 local today in ms — the day-timeline window start (or the earliest
 *  session start, whichever is earlier). */
function nineAmMs(): number {
  const d = new Date();
  d.setHours(9, 0, 0, 0);
  return d.getTime();
}

export function Overview(): JSX.Element {
  const day = isoDay();
  const { scope, scopeProject } = useScope();
  const query = usePageSearch();
  const nowMs = useNowMs();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stats, setStats] = useState<StatsOverview | null>(null);
  const [statsError, setStatsError] = useState(false);
  const [approvals, setApprovals] = useState<PermissionRequest[] | null>(null);
  const [resolved, setResolved] = useState<PermissionRequest[]>([]);
  const [rules, setRules] = useState<ApprovalRule[]>([]);
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
    // Resolved history powers the wait ledger + interrupts; failure degrades to
    // an empty set (the sections just read "nothing waited on you").
    fetchApprovals('resolved')
      .then((rs) => setResolved(rs.filter((r) => isToday(r.requestedAt))))
      .catch(() => setResolved([]));
  }, []);

  const loadRules = useCallback((): void => {
    fetchApprovalRules()
      .then(setRules)
      .catch(() => setRules([]));
  }, []);

  const loadStats = useCallback((): void => {
    fetchStatsOverview(day, scope ?? undefined)
      .then((s) => {
        setStats(s);
        setStatsError(false);
      })
      .catch(() => setStatsError(true));
  }, [day, scope]);

  useEffect(loadSessions, [loadSessions]);
  useEffect(loadApprovals, [loadApprovals]);
  useEffect(loadRules, [loadRules]);
  useEffect(loadStats, [loadStats]);

  const reload = useCallback((): void => {
    loadSessions();
    loadStats();
    loadApprovals();
    loadRules();
  }, [loadSessions, loadStats, loadApprovals, loadRules]);

  // Mirrors Sessions.tsx: loadSessions is server-scoped, so WS-applied
  // sessions must pass the same scope filter or an out-of-scope
  // session_created/session_updated pollutes the list until reload.
  const matchesProject = useCallback(
    (s: Session): boolean => scope === null || s.projectSlug === (scopeProject?.slug ?? scope),
    [scope, scopeProject],
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
        // A resolution moves a row into today's history → refresh the ledger.
        if (msg.type === 'permission_resolved') loadApprovals();
        return;
      }
      setSessions((prev) =>
        prev === null ? prev : applySessionMessage(prev, msg).filter(matchesProject),
      );
    },
    [matchesProject, loadApprovals],
  );
  useLiveUpdates(onMessage, reload);

  const pending = approvals ?? [];

  // Rule-covered tool patterns (enabled rules only) — drives "✓ auto-approved".
  const coveredPatterns = useMemo(
    () => new Set(rules.filter((r) => r.enabled).map((r) => r.toolPattern)),
    [rules],
  );

  // Per-tool wait, grouped by rule pattern across today's approvals (pending +
  // resolved). Auto-approved (resolvedVia 'rule') rows cost no human wait but
  // still show as covered.
  const toolWaits = useMemo<ToolWait[]>(() => {
    const all = [...pending, ...resolved];
    const map = new Map<string, ToolWait>();
    for (const r of all) {
      const key = suggestRulePattern(r);
      const w = wasAutoApproved(r) ? 0 : waitMs(r, nowMs);
      const cur = map.get(key) ?? { key, count: 0, waitedMs: 0, covered: coveredPatterns.has(key) };
      cur.count += wasAutoApproved(r) ? 0 : 1;
      cur.waitedMs += w;
      map.set(key, cur);
    }
    return [...map.values()].filter((t) => t.count > 0).sort((a, b) => b.waitedMs - a.waitedMs);
  }, [pending, resolved, nowMs, coveredPatterns]);

  const waitedTotal = useMemo(() => toolWaits.reduce((a, t) => a + t.waitedMs, 0), [toolWaits]);
  const autoApprovedCount = useMemo(
    () => resolved.filter(wasAutoApproved).length,
    [resolved],
  );
  const requestTotal = pending.length + resolved.length;

  const ledger: LedgerCell[] = [
    {
      label: 'waited today',
      value: waitedTotal > 0 ? fmtWait(waitedTotal) : '0m',
      sub: `across ${String(toolWaits.reduce((a, t) => a + t.count, 0))} stop${
        toolWaits.reduce((a, t) => a + t.count, 0) === 1 ? '' : 's'
      }`,
      tone: waitedTotal > 0 ? 'text-red' : 'text-ink-dim',
      big: true,
    },
    {
      label: 'auto-approved',
      value: String(autoApprovedCount),
      sub:
        requestTotal > 0
          ? `${String(Math.round((autoApprovedCount / requestTotal) * 100))}% of requests · no stop`
          : 'no requests yet',
      tone: 'text-green',
      big: false,
    },
    {
      label: 'still blocked',
      value: String(pending.length),
      sub: pending.length === 0 ? 'nothing idle' : 'agents idle now',
      tone: pending.length > 0 ? 'text-amber' : 'text-ink-dim',
      big: false,
    },
  ];

  // The day: window is 09:00 → now (widened if a session started earlier), lanes
  // one per project that has sessions today.
  const dayWindow = useMemo(() => {
    const todaySessions = (sessions ?? []).filter((s) => sessionDay(s) === day);
    const starts = todaySessions.map((s) => new Date(s.startedAt).getTime());
    const start = Math.min(nineAmMs(), ...(starts.length > 0 ? starts : [nineAmMs()]));
    return { start, end: nowMs, sessions: todaySessions };
  }, [sessions, day, nowMs]);

  const dayLanes = useMemo<DayLane[]>(() => {
    const bySlug = new Map<string, Session[]>();
    for (const s of dayWindow.sessions) {
      const slug = s.projectSlug ?? 'unknown';
      const arr = bySlug.get(slug) ?? [];
      arr.push(s);
      bySlug.set(slug, arr);
    }
    const lanes = [...bySlug.entries()].map(([slug, ss]) => {
      const name = ss[0]?.projectName ?? slug;
      const projApprovals = [...pending, ...resolved].filter((r) =>
        ss.some((s) => s.id === r.sessionId),
      );
      return buildLane(slug, name, ss, projApprovals, dayWindow.start, dayWindow.end, nowMs);
    });
    return lanes.sort((a, b) => b.waitedMs - a.waitedMs).slice(0, 6);
  }, [dayWindow, pending, resolved, nowMs]);

  const onStopAsking = useCallback(
    (toolPattern: string): void => {
      // Optimistic: mark covered locally so the row flips to "✓ auto-approved"
      // immediately; a MOCK build skips the network write (no rules endpoint).
      setRules((prev) => [
        ...prev,
        {
          id: -Date.now(),
          projectId: null,
          projectSlug: null,
          toolPattern,
          action: 'approve',
          enabled: true,
          note: 'stop asking (command deck)',
          createdAt: new Date().toISOString(),
          source: 'manual',
        },
      ]);
      if (MOCK) return;
      createApprovalRule({ projectId: null, toolPattern, note: 'stop asking (command deck)' })
        .then(() => loadRules())
        .catch(() => loadRules()); // reconcile on failure — drops the optimistic row
    },
    [loadRules],
  );

  return (
    <div className="wide:grid wide:grid-cols-[minmax(0,1fr)_320px] wide:items-start">
      <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
        <EyebrowClock />
        {approvals !== null ? (
          <WaitHero waitedMs={waitedTotal} tools={toolWaits} />
        ) : (
          <h1 className="mt-3.5 max-w-[22ch] font-display text-[28px] leading-[1.16] font-medium tracking-[-0.015em] text-ink-dim desk:text-[38px]">
            Reading today's activity…
          </h1>
        )}

        <Ledger cells={ledger} />

        {dayLanes.length > 0 && (
          <DayTimeline lanes={dayLanes} startMs={dayWindow.start} endMs={dayWindow.end} />
        )}

        {toolWaits.length > 0 && (
          <Interrupts
            tools={toolWaits}
            maxWait={toolWaits[0]?.waitedMs ?? 0}
            onStopAsking={onStopAsking}
          />
        )}

        {/* In-page session-title filter (moved out of the header) + the project
            scope chip, in the Sessions filter-row order: search, then scope.
            The chip is this page's only way back to "all projects" now that the
            sidebar switcher is gone. */}
        <div className="mt-9 flex flex-wrap items-center gap-2">
          <PageSearchInput />
          <ScopeChip />
        </div>
        {error !== null && <ErrorBox message={error} onRetry={loadSessions} />}
        {sessions === null && error === null ? (
          <Loading label="sessions…" />
        ) : (
          <Spine sessions={sessions ?? []} nowMs={nowMs} query={query} />
        )}
      </div>

      <aside className="min-w-0 border-line px-4 pb-10 wide:sticky wide:top-14 wide:min-h-[calc(100vh-56px)] wide:border-l wide:px-7 wide:pt-[34px] wide:pb-10">
        {pending.length > 0 && <BlockedRail pending={pending} nowMs={nowMs} />}
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
                    ? (stats.errors_by_project.find((r) => r.slug === project)?.name ??
                      scopeProject?.name ??
                      null)
                    : null),
              });
            }}
          />
        )}
        {stats === null && statsError && (
          <div className="mt-4 font-mono text-[11px] text-ink-dim">triage unavailable</div>
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
