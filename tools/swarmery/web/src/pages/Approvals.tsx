// Approvals screen (design §3.2, phase 2): PENDING cards — tool name, the
// collapsed tool_input essential (expandable to the full hook stdin JSON),
// session attribution (lazy /api/sessions join), live "hangs Ns" age and
// expiry countdown against the 120 s window — with Approve / Deny (inline
// optional reason) / Open session actions. HISTORY below: terminal-status
// chips, resolved_via, relative time. Fed by GET /api/approvals?status= +
// WS permission_requested/permission_resolved (upsert by id — the client's
// own decision comes back over WS too; refetch reconciles races/409s).

import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type { PermissionRequest, PermissionRequestStatus, Session, WSMessage } from '../api/types';
import { fetchApprovals, fetchSessions, resolveApproval, type ApprovalAction } from '../api';
import { fmtClock, requestJsonPretty, requestSummary } from '../lib/approvals';
import { projectColor } from '../lib/colors';
import { fmtAgo, projectLabel } from '../lib/format';
import { applyPermissionMessage, useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

const HISTORY_LIMIT = 50;

/* ----- terminal-status chips (approved sage / denied danger / rest dim) ----- */

const APPROVAL_CHIP: Record<PermissionRequestStatus, string> = {
  pending: 'border-amber/40 text-amber',
  approved: 'border-green/40 text-green',
  denied: 'border-red/40 text-red',
  expired: 'border-line text-ink-dim',
  resolved_elsewhere: 'border-line text-ink-dim',
};

const APPROVAL_LABEL: Record<PermissionRequestStatus, string> = {
  pending: 'pending',
  approved: 'approved',
  denied: 'denied',
  expired: 'expired',
  resolved_elsewhere: 'elsewhere',
};

/* ----- session attribution (project + title when resolvable) ----- */

function sessionLabel(sessionId: number, session: Session | null): string {
  if (session === null) return `session #${String(sessionId)}`;
  const project = projectLabel(session.projectName, session.projectSlug);
  return session.title !== null ? `${project} · ${session.title}` : project;
}

/* ----- one pending card ----- */

const ACTION_BTN =
  'flex-1 rounded-lg border px-3.5 py-1.5 text-center font-mono text-[11.5px] font-semibold transition-colors disabled:opacity-50 desk:flex-none';

function PendingCard({
  request,
  session,
  nowMs,
  busy,
  onResolve,
}: {
  request: PermissionRequest;
  session: Session | null;
  /** Shared 1 s page ticker — one interval for all cards, not per-card timers. */
  nowMs: number;
  busy: boolean;
  onResolve: (action: ApprovalAction, reason?: string) => void;
}): JSX.Element {
  const [expanded, setExpanded] = useState(false);
  const [denying, setDenying] = useState(false);
  const [reason, setReason] = useState('');

  const hangSec = (nowMs - new Date(request.requestedAt).getTime()) / 1000;
  const expireSec = (new Date(request.expiresAt).getTime() - nowMs) / 1000;
  const sessionTo = `/sessions/${String(request.sessionId)}`;

  const submitDeny = (): void => {
    const trimmed = reason.trim();
    onResolve('deny', trimmed === '' ? undefined : trimmed);
  };

  return (
    <div className="mb-2.5 rounded-xl border border-amber/35 bg-surface px-3.5 py-3">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-amber" aria-hidden="true" />
        <span className="font-mono text-[12.5px] font-semibold text-ink">{request.toolName}</span>
        <span className="font-mono text-[10.5px] text-amber">hangs {fmtClock(hangSec)}</span>
        <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
          {expireSec > 0 ? `expires in ${fmtClock(expireSec)}` : 'expiring…'}
        </span>
      </div>

      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-label={expanded ? 'collapse request JSON' : 'expand request JSON'}
        className="mt-2 flex w-full items-start gap-1.5 rounded-md bg-surface2 px-2.5 py-1.5 text-left transition-colors hover:bg-surface2/70"
      >
        <span aria-hidden="true" className="mt-px shrink-0 font-mono text-[10px] text-ink-dim">
          {expanded ? '▾' : '▸'}
        </span>
        <code
          className={`min-w-0 flex-1 font-mono text-[11.5px] text-ink-2 ${
            expanded ? 'break-all whitespace-pre-wrap' : 'block truncate'
          }`}
        >
          {requestSummary(request)}
        </code>
      </button>
      {expanded && (
        <pre className="mt-1.5 max-h-72 overflow-y-auto rounded-md bg-surface2 px-2.5 py-2 font-mono text-[10.5px] leading-relaxed break-all whitespace-pre-wrap text-ink-3">
          {requestJsonPretty(request)}
        </pre>
      )}

      <Link
        to={sessionTo}
        className="mt-2 flex items-center gap-[7px] font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
      >
        {session !== null && (
          <span
            className="h-1.5 w-1.5 shrink-0 rounded-full"
            style={{ background: projectColor(session.projectSlug) }}
            aria-hidden="true"
          />
        )}
        <span className="truncate">{sessionLabel(request.sessionId, session)}</span>
      </Link>

      <div className="mt-2.5 flex flex-wrap items-center gap-2">
        <button
          type="button"
          disabled={busy}
          onClick={() => onResolve('approve')}
          className={`${ACTION_BTN} border-green/40 bg-green/10 text-green hover:bg-green/20`}
        >
          approve
        </button>
        <button
          type="button"
          disabled={busy}
          aria-expanded={denying}
          onClick={() => setDenying((v) => !v)}
          className={`${ACTION_BTN} border-red/40 bg-red/10 text-red hover:bg-red/20`}
        >
          deny{denying ? ' ▴' : ''}
        </button>
        <Link
          to={sessionTo}
          className={`${ACTION_BTN} border-line font-normal text-ink-2 hover:bg-surface2`}
        >
          open session →
        </Link>
      </div>

      {denying && (
        <form
          className="mt-2 flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            submitDeny();
          }}
        >
          <input
            type="text"
            autoFocus
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="reason (optional) — delivered to Claude verbatim"
            aria-label="deny reason"
            className="min-w-0 flex-1 basis-[200px] rounded-lg border border-line bg-surface2 px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-dim focus:border-red/40"
          />
          <button
            type="submit"
            disabled={busy}
            className="rounded-lg border border-red/40 bg-red/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20 disabled:opacity-50"
          >
            confirm deny
          </button>
        </form>
      )}
    </div>
  );
}

/* ----- one history row ----- */

function HistoryRow({
  request,
  session,
}: {
  request: PermissionRequest;
  session: Session | null;
}): JSX.Element {
  return (
    <Link
      to={`/sessions/${String(request.sessionId)}`}
      className="block px-3.5 py-2.5 transition-colors hover:bg-surface2"
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span
          className={`rounded-full border px-2 py-0.5 font-mono text-[10.5px] whitespace-nowrap ${APPROVAL_CHIP[request.status]}`}
        >
          {APPROVAL_LABEL[request.status]}
        </span>
        <span className="font-mono text-[11.5px] font-semibold text-ink-2">{request.toolName}</span>
        <code className="min-w-0 flex-1 basis-[160px] truncate font-mono text-[11px] text-ink-dim">
          {requestSummary(request)}
        </code>
        {request.resolvedVia !== null && (
          <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim">
            via {request.resolvedVia}
          </span>
        )}
        <span className="font-mono text-[10.5px] whitespace-nowrap text-ink-3">
          {fmtAgo(request.resolvedAt ?? request.requestedAt)}
        </span>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 font-mono text-[10.5px] text-ink-dim">
        <span className="truncate">{sessionLabel(request.sessionId, session)}</span>
        {request.reason !== null && (
          <span className="min-w-0 truncate">reason: “{request.reason}”</span>
        )}
      </div>
    </Link>
  );
}

/* ----- screen ----- */

export function Approvals(): JSX.Element {
  const [requests, setRequests] = useState<PermissionRequest[] | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [nowMs, setNowMs] = useState(() => Date.now());

  const load = useCallback((): void => {
    // Pending + history in one page state; `resolved` is the terminal-status
    // meta-filter (limit 50 server-side — see web/CONTRACT-REQUESTS.md).
    Promise.all([fetchApprovals('pending'), fetchApprovals('resolved')])
      .then(([pending, resolved]) => {
        // De-dupe by id in case a request raced between the two fetches.
        const byId = new Map<number, PermissionRequest>();
        for (const r of [...pending, ...resolved]) byId.set(r.id, r);
        setRequests([...byId.values()]);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, []);

  useEffect(load, [load]);

  // Session attribution — fetched lazily, once, only when there is something
  // to attribute (plain "session #N" fallback until/unless it resolves).
  useEffect(() => {
    if (requests === null || requests.length === 0 || sessions !== null) return;
    fetchSessions()
      .then(setSessions)
      .catch(() => setSessions([]));
  }, [requests, sessions]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type !== 'permission_requested' && msg.type !== 'permission_resolved') return;
    setRequests((prev) => (prev === null ? prev : applyPermissionMessage(prev, msg)));
  }, []);
  useLiveUpdates(onMessage, load);

  const pending = (requests ?? [])
    .filter((r) => r.status === 'pending')
    .sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)); // oldest (most urgent) first
  const history = (requests ?? [])
    .filter((r) => r.status !== 'pending')
    .sort((a, b) =>
      (b.resolvedAt ?? b.requestedAt).localeCompare(a.resolvedAt ?? a.requestedAt),
    )
    .slice(0, HISTORY_LIMIT); // newest first

  // One shared 1 s ticker for every live age/expiry clock on the page.
  const hasPending = pending.length > 0;
  useEffect(() => {
    if (!hasPending) return undefined;
    const t = setInterval(() => setNowMs(Date.now()), 1_000);
    return () => clearInterval(t);
  }, [hasPending]);

  const sessionOf = (id: number): Session | null =>
    sessions?.find((s) => s.id === id) ?? null;

  const resolve = (request: PermissionRequest, action: ApprovalAction, reason?: string): void => {
    setBusyId(request.id);
    // Optimistic transfer to history; the WS permission_resolved for our own
    // decision (and the 200 body) upsert the authoritative row by id.
    const optimistic: PermissionRequest = {
      ...request,
      status: action === 'approve' ? 'approved' : 'denied',
      resolvedAt: new Date().toISOString(),
      resolvedVia: 'dashboard',
      reason: reason ?? null,
    };
    setRequests((prev) =>
      prev === null ? prev : prev.map((r) => (r.id === request.id ? optimistic : r)),
    );
    resolveApproval(request.id, action, reason)
      .then((updated) => {
        setRequests((prev) =>
          prev === null ? prev : prev.map((r) => (r.id === updated.id ? updated : r)),
        );
      })
      .catch(() => {
        // 409 (resolved elsewhere / expired first) or transport failure —
        // silent refetch; the server list is the truth.
        load();
      })
      .finally(() => setBusyId(null));
  };

  return (
    <>
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-2 pt-1">
        <h1 className="font-display text-[19px] leading-tight font-bold tracking-[0.01em] desk:text-[21px]">
          Approvals
        </h1>
        {requests !== null && (
          <span className="font-mono text-[11px] text-ink-dim">
            {pending.length} pending · {history.length} resolved
          </span>
        )}
      </div>

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {requests === null && error === null && <Loading label="approvals…" />}

      {requests !== null && (
        <>
          <SectionTitle>
            Pending{pending.length > 0 ? ` · ${String(pending.length)}` : ''}
          </SectionTitle>
          {pending.length === 0 && (
            <Empty>
              no pending approvals — agents are running unattended.{' '}
              <span className="font-mono text-ink">permission_requested</span> pushes new ones here
              live
            </Empty>
          )}
          {pending.map((r) => (
            <PendingCard
              key={r.id}
              request={r}
              session={sessionOf(r.sessionId)}
              nowMs={nowMs}
              busy={busyId === r.id}
              onResolve={(action, reason) => resolve(r, action, reason)}
            />
          ))}

          <SectionTitle>History</SectionTitle>
          {history.length === 0 ? (
            <Empty>no decisions yet — resolved requests land here with their audit trail</Empty>
          ) : (
            <div className="divide-y divide-line-soft overflow-hidden rounded-xl border border-line bg-surface">
              {history.map((r) => (
                <HistoryRow key={r.id} request={r} session={sessionOf(r.sessionId)} />
              ))}
            </div>
          )}
        </>
      )}
    </>
  );
}
