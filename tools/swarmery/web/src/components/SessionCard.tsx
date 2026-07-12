import { Link } from 'react-router-dom';
import type { Session } from '../api/types';
import { fmtSpan, fmtTime } from '../lib/format';
import { LiveDot, StatusChip } from './ui';

function meta(session: Session): string {
  const parts: string[] = [];
  if (session.model !== null) parts.push(session.model);
  if (session.gitBranch !== null) parts.push(session.gitBranch);
  parts.push(
    session.endedAt !== null
      ? `ended ${fmtTime(session.endedAt)}`
      : `started ${fmtTime(session.startedAt)}`,
  );
  return parts.join(' · ');
}

function chipSuffix(session: Session): string {
  return fmtSpan(session.startedAt, session.endedAt);
}

const NOW_STATUSES = new Set<Session['status']>(['active', 'waiting_approval', 'idle']);

/* Live sessions get a status-tinted hairline (Redesign "Active now" card). */
const CARD_BORDERS: Partial<Record<Session['status'], string>> = {
  active: 'border-green/25 hover:border-green/55',
  waiting_approval: 'border-amber/35 hover:border-amber/70',
};

export function SessionCard({
  session,
  now = null,
  flat = false,
}: {
  session: Session;
  /** Live "now: <last action>" line, fed by event_appended WS messages. */
  now?: string | null;
  /** Row inside a grouped list card (no own border — hover fill instead). */
  flat?: boolean;
}): JSX.Element {
  const shell = flat
    ? 'block px-3.5 py-[11px] transition-colors hover:bg-surface2'
    : `mb-2.5 block rounded-xl border bg-surface px-3.5 py-[11px] transition-colors ${
        CARD_BORDERS[session.status] ?? 'border-line hover:border-ink-dim/50'
      }`;
  return (
    <Link
      to={`/sessions/${session.id}`}
      className={`${shell} focus-visible:outline-2 focus-visible:outline-brand`}
    >
      <div className="flex items-center gap-2">
        <LiveDot status={session.status} />
        <span
          className={`min-w-0 flex-1 truncate font-mono text-[11px] ${flat ? 'text-ink-3' : 'text-ink'}`}
        >
          {session.projectSlug}
        </span>
        <StatusChip status={session.status} suffix={chipSuffix(session)} />
      </div>
      <div className="mt-px mb-[3px] truncate text-[13.5px] font-semibold">
        {session.title ?? session.sessionUuid}
      </div>
      <div className="truncate font-mono text-[11px] text-ink-dim">{meta(session)}</div>
      {now !== null && NOW_STATUSES.has(session.status) && (
        <div className="mt-[3px] truncate font-mono text-[10.5px] text-green">now: {now}</div>
      )}
    </Link>
  );
}
