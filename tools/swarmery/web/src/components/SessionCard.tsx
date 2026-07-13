import { Link } from 'react-router-dom';
import type { Session } from '../api/types';
import { projectColor } from '../lib/colors';
import { fmtSpan, fmtTime, projectLabel } from '../lib/format';
import { KillButton } from './KillButton';
import { ProcBadge } from './ProcBadge';
import { TaskChip } from './TaskChip';
import { DurationPill, LiveDot, SESSION_ROW_GRID, StatusChip } from './ui';

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

/** Small project accent dot (stable color by slug). */
function ProjectDot({ slug }: { slug: string }): JSX.Element {
  return (
    <span
      className="h-1.5 w-1.5 shrink-0 rounded-full"
      style={{ background: projectColor(slug) }}
      aria-hidden="true"
    />
  );
}

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
  const liveNow = now !== null && NOW_STATUSES.has(session.status);

  /* Stacked card — standalone cards and the <900px rows inside day groups. */
  const card = (
    <>
      <div className="flex items-center gap-2">
        <LiveDot status={session.status} />
        <span
          className={`min-w-0 flex-1 truncate font-mono text-[11px] ${flat ? 'text-ink-3' : 'text-ink'}`}
        >
          {projectLabel(session.projectName, session.projectSlug)}
        </span>
        <ProcBadge session={session} />
        <StatusChip status={session.status} suffix={chipSuffix(session)} />
      </div>
      <div className="mt-px mb-[3px] truncate text-[13.5px] font-semibold">
        {session.title ?? session.sessionUuid}
      </div>
      <div className="truncate font-mono text-[11px] text-ink-dim">{meta(session)}</div>
      {session.taskExternalId != null && (
        <div className="mt-[3px] flex min-w-0">
          <TaskChip
            externalId={session.taskExternalId}
            linkSource={session.taskLinkSource}
            confidence={session.taskConfidence}
          />
        </div>
      )}
      {liveNow && (
        <div className="mt-[3px] truncate font-mono text-[10.5px] text-green">now: {now}</div>
      )}
      {session.procPid != null && (
        <div className="mt-[3px] flex">
          <KillButton session={session} />
        </div>
      )}
    </>
  );

  if (!flat) {
    return (
      <Link
        to={`/sessions/${session.id}`}
        className={`mb-2.5 block rounded-xl border bg-surface px-3.5 py-[11px] transition-colors focus-visible:outline-2 focus-visible:outline-brand ${
          CARD_BORDERS[session.status] ?? 'border-line hover:border-ink-dim/50'
        }`}
      >
        {card}
      </Link>
    );
  }

  /* Flat rows: mobile keeps the stacked card; ≥900px renders the aligned
   * table row (Redesign sessions grid — one column template per day group). */
  return (
    <Link
      to={`/sessions/${session.id}`}
      className="block transition-colors hover:bg-surface2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
    >
      <div className="px-3.5 py-[11px] desk:hidden">{card}</div>
      <div className={`hidden items-center gap-3 px-3.5 py-[9px] desk:grid ${SESSION_ROW_GRID}`}>
        <span className="flex justify-center">
          <LiveDot status={session.status} />
        </span>
        <span className="flex min-w-0 items-center gap-[7px]">
          <ProjectDot slug={session.projectSlug} />
          <span className="truncate font-mono text-[11px] text-ink-3">
            {projectLabel(session.projectName, session.projectSlug)}
          </span>
        </span>
        <span className="min-w-0">
          <span
            className={`block truncate text-[13px] ${
              session.title === null ? 'font-normal text-ink-dim italic' : 'font-semibold text-ink'
            }`}
          >
            {session.title ?? '(no title)'}
          </span>
          {session.taskExternalId != null && (
            <span className="mt-[2px] flex min-w-0">
              <TaskChip
                externalId={session.taskExternalId}
                linkSource={session.taskLinkSource}
                confidence={session.taskConfidence}
              />
            </span>
          )}
          {liveNow && (
            <span className="block truncate font-mono text-[10.5px] text-green">now: {now}</span>
          )}
          {session.procPid != null && (
            <span className="mt-[2px] flex">
              <KillButton session={session} />
            </span>
          )}
        </span>
        <span className="truncate font-mono text-[11px] text-ink-dim">{session.model ?? '—'}</span>
        <span className="truncate font-mono text-[11px] text-ink-dim">
          {session.gitBranch ?? '—'}
        </span>
        <span className="font-mono text-[11px] text-ink-3">{fmtTime(session.startedAt)}</span>
        <span className="flex items-center justify-end gap-1.5">
          <ProcBadge session={session} />
          <DurationPill status={session.status} startedAt={session.startedAt} endedAt={session.endedAt} />
        </span>
      </div>
    </Link>
  );
}
