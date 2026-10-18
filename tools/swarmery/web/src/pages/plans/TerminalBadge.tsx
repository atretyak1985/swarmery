// "open in terminal" badge (mods-swarmery-link phase 7): which sessions have a
// plan — or one of its phases — open in a terminal, fed by
// GET /api/epics/{taskId}/terminal-sessions. The rows come from the
// swarmery-link mod's plan pick, so the badge says nothing about sessions that
// never reported one; with no rows it renders nothing at all.

import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchTerminalSessions } from '../../api';
import type { TerminalSession } from '../../api/types';
import { useSessionHref } from '../../lib/sessionHref';
import { useLiveUpdates } from '../../lib/ws';

const SHORT_ID_LEN = 8;

/** The session's short id — the first 8 chars of its uuid. */
export function shortSessionId(uuid: string): string {
  return uuid.slice(0, SHORT_ID_LEN);
}

/**
 * The plan's terminal sessions, fetched once per plan and refetched on a
 * `plan_updated` frame for that plan (and on a socket reconnect). A response for
 * a plan the page has since left is dropped, and so is one that lost the race to
 * a newer fetch. A failed fetch reads as "none": a badge is a hint, never an error.
 */
export function useTerminalSessions(taskId: number): TerminalSession[] {
  const [loaded, setLoaded] = useState<{ taskId: number; rows: TerminalSession[] } | null>(null);
  const latest = useRef<{ taskId: number; seq: number }>({ taskId, seq: 0 });

  const load = useCallback((): void => {
    const seq = latest.current.taskId === taskId ? latest.current.seq + 1 : 1;
    latest.current = { taskId, seq };
    fetchTerminalSessions(taskId)
      .then((rows) => {
        if (latest.current.taskId !== taskId || latest.current.seq !== seq) return;
        setLoaded({ taskId, rows });
      })
      .catch(() => {
        if (latest.current.taskId !== taskId || latest.current.seq !== seq) return;
        setLoaded({ taskId, rows: [] });
      });
  }, [taskId]);

  useEffect(() => {
    load();
  }, [load]);

  useLiveUpdates((msg) => {
    if (msg.type === 'plan_updated' && msg.payload.taskId === taskId) load();
  }, load);

  // The rows of another plan are never shown for the one now open.
  return loaded !== null && loaded.taskId === taskId ? loaded.rows : [];
}

/**
 * One badge for a set of sessions (already narrowed to a phase or to the plan).
 * Links to the newest session; several show a count and list them all in the tip.
 */
export function TerminalBadge({
  sessions,
  scope,
  className = '',
}: {
  sessions: readonly TerminalSession[];
  /** What the sessions are open on, for the accessible name. */
  scope: 'plan' | 'phase';
  className?: string;
}): JSX.Element | null {
  const { t } = useLingui();
  const sessionHref = useSessionHref();
  const newest = sessions[0];
  if (newest === undefined) return null;

  const label = t`open in terminal`;
  const liveWord = t`live`;
  const shortId = shortSessionId(newest.sessionUuid);
  const count = sessions.length;
  const listing = sessions
    .map((s) => `${shortSessionId(s.sessionUuid)}${s.live ? ` (${liveWord})` : ''}`)
    .join(', ');
  const subject = scope === 'plan' ? t`this plan` : t`this phase`;
  const liveNote = newest.live ? liveWord : t`not live`;
  const name =
    count === 1
      ? t`${label}: ${subject} is open in session ${shortId} (${liveNote})`
      : t`${label}: ${subject} is open in ${count} sessions — ${listing}`;

  return (
    <Link
      to={sessionHref(newest.sessionId)}
      // The phase row around the badge opens the phase on click; the badge is
      // its own destination.
      onClick={(e) => e.stopPropagation()}
      data-testid="terminal-badge"
      data-scope={scope}
      data-live={newest.live ? 'true' : 'false'}
      data-tip={name}
      aria-label={name}
      className={`inline-flex shrink-0 items-center gap-1 rounded border border-brand/40 px-1.5 py-px font-mono text-[9.5px] whitespace-nowrap text-brand focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand ${className}`}
    >
      {newest.live && (
        <span aria-hidden="true" className="inline-block h-1.5 w-1.5 rounded-full bg-green" />
      )}
      <span>{label}</span>
      <span aria-hidden="true" className="text-ink-dim">
        {shortId}
        {count > 1 ? ` +${String(count - 1)}` : ''}
      </span>
    </Link>
  );
}
