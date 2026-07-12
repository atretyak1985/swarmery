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

export function SessionCard({ session }: { session: Session }): JSX.Element {
  return (
    <Link
      to={`/sessions/${session.id}`}
      className="mb-2.5 block rounded-[10px] border border-line bg-surface px-3.5 py-[11px] transition-colors hover:border-ink-dim/50 focus-visible:outline-2 focus-visible:outline-amber"
    >
      <div className="flex items-center gap-2">
        <LiveDot status={session.status} />
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-amber">
          {session.projectSlug}
        </span>
        <StatusChip status={session.status} suffix={chipSuffix(session)} />
      </div>
      <div className="mt-px mb-[3px] truncate text-[13.5px] font-semibold">
        {session.title ?? session.sessionUuid}
      </div>
      <div className="truncate font-mono text-[11px] text-ink-dim">{meta(session)}</div>
    </Link>
  );
}
