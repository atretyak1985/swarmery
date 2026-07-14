import { useNavigate } from 'react-router-dom';
import type { Session } from '../api/types';
import { projectColor } from '../lib/colors';
import { fmtSpan, fmtTime, projectLabel } from '../lib/format';
import { KillButton } from './KillButton';
import { ProcBadge } from './ProcBadge';
import { TaskChip } from './TaskChip';
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

/* ----- Canvas visual bucket (Canvas.dc.html §Sessions: active/done/error) —
 * the real SessionStatus keeps 5 values; the flat-row dot/chip only draw from
 * 3 tones (+ a 4th "waiting" amber kept from the existing product surface,
 * since collapsing it into "done" would hide a session mid-approval). ----- */
type CanvasTone = 'active' | 'waiting' | 'error' | 'done';

const CANVAS_TONE: Record<Session['status'], CanvasTone> = {
  active: 'active',
  waiting_approval: 'waiting',
  killed: 'error',
  idle: 'done',
  completed: 'done',
};

const CANVAS_LABEL: Record<CanvasTone, string> = {
  active: 'working',
  waiting: 'waiting',
  error: 'error',
  done: 'done',
};

const CANVAS_CHIP_STYLE: Record<CanvasTone, string> = {
  active: 'border-green/40 text-green',
  waiting: 'border-amber/40 text-amber',
  error: 'border-red/40 text-red',
  done: 'border-line-strong text-ink-dim',
};

/** Row status dot (Canvas §3a): only LIVE sessions carry a marker — a hollow
 * colour ring for active/error/waiting. done/idle render an empty span so the
 * grid column stays aligned without a resting-state dot. */
function RowDot({ tone }: { tone: CanvasTone }): JSX.Element {
  if (tone === 'active') {
    return <span className="inline-block h-2 w-2 shrink-0 animate-pulse-dot rounded-full border-2 border-green" />;
  }
  if (tone === 'error') {
    return <span className="inline-block h-2 w-2 shrink-0 rounded-full border-2 border-red" />;
  }
  if (tone === 'waiting') {
    return <span className="inline-block h-2 w-2 shrink-0 rounded-full border-2 border-amber" />;
  }
  return <span className="inline-block h-2 w-2 shrink-0" />;
}

/** Right-justified status chip (Canvas §3e): "working · 3h43m" / "error · 31s" / plain span. */
function RowChip({ tone, suffix }: { tone: CanvasTone; suffix: string }): JSX.Element {
  return (
    <span
      className={`justify-self-end rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] whitespace-nowrap ${CANVAS_CHIP_STYLE[tone]}`}
    >
      {tone === 'active' || tone === 'error' ? `${CANVAS_LABEL[tone]} · ${suffix}` : suffix}
    </span>
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
  const navigate = useNavigate();
  const liveNow = now !== null && NOW_STATUSES.has(session.status);
  const goToDetail = (): void => { navigate(`/sessions/${session.id}`); };

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
        <div className="mt-[3px] flex" onClick={(e) => e.stopPropagation()}>
          <KillButton session={session} />
        </div>
      )}
    </>
  );

  /* Navigation via div+useNavigate instead of <Link> so that KillButton's
   * stopPropagation reliably blocks navigation — <a> tags intercept clicks at
   * the browser level before React's synthetic event system can stop them. */
  if (!flat) {
    return (
      <div
        role="link"
        tabIndex={0}
        onClick={goToDetail}
        onKeyDown={(e) => { if (e.key === 'Enter') goToDetail(); }}
        className={`mb-2.5 block cursor-pointer rounded-xl border bg-surface px-3.5 py-[11px] transition-colors focus-visible:outline-2 focus-visible:outline-brand ${
          CARD_BORDERS[session.status] ?? 'border-line hover:border-ink-dim/50'
        }`}
      >
        {card}
      </div>
    );
  }

  /* Flat rows: mobile keeps the stacked card; ≥900px renders the Canvas
   * 5-column row (Canvas.dc.html §Sessions: dot / project / title+why /
   * model / status chip). Branch + start-time drop from their own columns
   * on desktop — they fold into the meta line under the title, same as the
   * stacked mobile card, so no data is lost, only re-laid-out. */
  const tone = CANVAS_TONE[session.status];
  return (
    <div
      role="link"
      tabIndex={0}
      onClick={goToDetail}
      onKeyDown={(e) => { if (e.key === 'Enter') goToDetail(); }}
      className="block cursor-pointer transition-colors hover:bg-surface focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
    >
      <div className="px-3.5 py-[11px] desk:hidden">{card}</div>
      <div className="hidden grid-cols-[15px_130px_minmax(0,1fr)_150px_90px] items-center gap-3.5 px-1 py-3 desk:grid">
        <span className="flex justify-center">
          <RowDot tone={tone} />
        </span>
        <span className="flex min-w-0 items-center gap-[7px]">
          <ProjectDot slug={session.projectSlug} />
          <span className="truncate font-mono text-[11px] text-ink-3">
            {projectLabel(session.projectName, session.projectSlug)}
          </span>
        </span>
        <span className="min-w-0">
          <span
            className={`block truncate text-[14px] font-semibold ${
              session.title === null ? 'font-normal text-ink-faint italic' : 'text-ink'
            }`}
          >
            {session.title ?? '(untitled session)'}
          </span>
          <span className="mt-0.5 block truncate text-[12px] text-[#6c7178]">
            {liveNow ? `now: ${now}` : (session.why ?? meta(session))}
          </span>
          {(session.taskExternalId != null || session.procPid != null) && (
            <span className="mt-[3px] flex min-w-0 items-center gap-1.5">
              {session.taskExternalId != null && (
                <TaskChip
                  externalId={session.taskExternalId}
                  linkSource={session.taskLinkSource}
                  confidence={session.taskConfidence}
                />
              )}
              <ProcBadge session={session} />
              {session.procPid != null && (
                <span onClick={(e) => e.stopPropagation()}>
                  <KillButton session={session} />
                </span>
              )}
            </span>
          )}
        </span>
        <span className="truncate font-mono text-[11px] text-ink-faint">
          {session.model ?? '—'}
        </span>
        <RowChip tone={tone} suffix={chipSuffix(session)} />
      </div>
    </div>
  );
}
