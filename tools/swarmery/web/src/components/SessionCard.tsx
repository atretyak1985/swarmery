import { useNavigate } from 'react-router-dom';
import type { Session } from '../api/types';
import { fmtSpan, fmtTime } from '../lib/format';
import { accountLabel } from '../lib/sessionAccount';
import { useSessionHref } from '../lib/sessionHref';
import { modelShortName, sessionModelFallback } from '../lib/sessionModelChip';
import { sessionState, useNowMs, type SessionState } from '../lib/sessionState';
import { ExplainPair } from './Explain';
import { KillButton, killSlotKind } from './KillButton';
import { OUTCOME_GLYPH } from './OutcomePicker';
import { ProjectName } from './ProjectName';
import { ProcBadge } from './ProcBadge';
import { StopButton } from './StopButton';
import { TaskChip } from './TaskChip';

/** Context-occupancy warning thresholds (tokens). Mirrors advisor R9's
 * R9ContextTokens=300k danger line; 150k is an early amber warning. */
const CONTEXT_WARN = 150_000;
const CONTEXT_DANGER = 300_000;

/** A chip showing how full the context window is — only when it's getting big.
 * A fat context is the fat-session cost driver (every turn re-reads it), so it
 * earns a visible warning: amber past 150k, red past 300k. */
function ContextBadge({ session }: { session: Session }): JSX.Element | null {
  const ctx = session.contextTokens;
  if (ctx == null || ctx < CONTEXT_WARN) return null;
  const danger = ctx >= CONTEXT_DANGER;
  const k = Math.round(ctx / 1000);
  // The native title= is gone: its text now lives in the glossary as
  // fat-session's `short` + `actions`, and two tooltips on one element (native
  // and ours, with different wording) is worse than either alone.
  //
  // ExplainPair, not a bare fragment: both layouts render this chip into a flex
  // row that already holds neighbours — the stacked card's header row puts
  // ProcBadge straight after it, the desktop grid's title row puts the outcome
  // glyph straight before it, and dev's HandoffChip lands adjacent in both. A
  // loose trigger would sit exactly as far from the chip it explains as from
  // the next one along.
  return (
    <ExplainPair id="fat-session">
      <span
        className={`shrink-0 rounded-full border px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap ${
          danger ? 'border-red/40 bg-red/10 text-red' : 'border-amber/40 bg-amber/10 text-amber'
        }`}
      >
        {k}k ctx
      </span>
    </ExplainPair>
  );
}

/** A chip flagging that the daemon generated a continuation handoff brief for
 * this fat session (migration 0039). Purple to distinguish it from the amber/red
 * context warning it sits beside — clicking through to the session opens the
 * rail's Handoff section with the full brief + a copy-paste resume command.
 *
 * `purple`, not Tailwind's `violet-*`: the palette defaults do not flip with
 * :root[data-mode='light'], where violet-400 lands at 2.73:1 on the light
 * surface. --color-purple is re-tuned per palette to clear AA (index.css).
 *
 * The native title= is gone — the explainer carries the same wording plus the
 * thresholds and the resume steps. */
function HandoffChip({ session }: { session: Session }): JSX.Element | null {
  if (session.handoff == null) return null;
  return (
    <ExplainPair id="handoff">
      <span className="shrink-0 rounded-full border border-purple/40 bg-purple/15 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-purple">
        Handoff
      </span>
    </ExplainPair>
  );
}

/** Which Claude Code subscription ran this session (migration 0047) — rendered
 * ONLY for non-default accounts (lib/sessionAccount): on a one-subscription
 * machine every row would otherwise carry an identical "default" chip.
 *
 * Neutral line/ink styling on purpose: the account is provenance, not a
 * warning, so it must not compete with the amber/red context chip or the
 * purple handoff chip it sits beside. */
function AccountBadge({ session }: { session: Session }): JSX.Element | null {
  const label = accountLabel(session);
  if (label === null) return null;
  return (
    <span
      data-tip={`ingested from the ${label} Claude Code account`}
      className="shrink-0 rounded-full border border-line-strong bg-surface2 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim"
    >
      {label}
    </span>
  );
}

/** The session's model, and — when it moved — BOTH ends of the move.
 *
 * `session.model` is the FIRST assistant model of the transcript, which for a
 * session an Opus 5.5 safeguard pushed onto an older model is a true statement
 * about the first turn and a false one about the output. The daemon has carried
 * `modelLast`/`modelChanged` for exactly this and nothing rendered them, so the
 * row kept showing the model the session started on for its whole life.
 *
 * Markup and wording are the phase-run chip's (Plans.tsx RunModelChip) on
 * purpose: a reader who has learnt "amber → fell back to" on a phase run should
 * not have to learn it again on the session that produced it. */
function SessionModelCell({ session }: { session: Session }): JSX.Element {
  const moved = sessionModelFallback(session);
  if (moved !== null) {
    // Amber and "fell back to" are reserved for a move DOWN — the thing that
    // explains a weaker answer. A move up is still worth showing, but in the
    // neutral accent and neutral wording, or the chip cries wolf on every
    // deliberate escalation.
    const cls = moved.fellBack
      ? 'border-amber/40 bg-amber/10 text-amber'
      : 'border-ink-faint/40 bg-ink-faint/10 text-ink-faint';
    return (
      <span
        className={`truncate rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
        data-tip={`this session changed model mid-flight: started on ${moved.from}, its newest turn ran on ${moved.to}`}
      >
        {modelShortName(moved.from)} {moved.fellBack ? '→ fell back to' : '→ changed to'}{' '}
        {modelShortName(moved.to)}
      </span>
    );
  }
  return (
    <span className="truncate font-mono text-[11px] text-ink-faint">
      {session.model ?? '—'}
    </span>
  );
}

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

/* ----- Canvas visual bucket — the UI speaks the tri-state (running/stuck/
 * done, lib/sessionState.ts) plus two display nuances kept from the existing
 * product surface: "waiting" (a session mid-approval must stay visible) and
 * "error" (killed rows keep their red accent). ----- */
type CanvasTone = 'active' | 'waiting' | 'stuck' | 'error' | 'done';

/** Tri-state → tone; waiting_approval and killed stay visible as nuances. */
function toneOf(s: Session, nowMs: number): CanvasTone {
  if (s.status === 'waiting_approval') return 'waiting';
  const state: SessionState = sessionState(s, nowMs);
  if (state === 'running') return 'active';
  if (state === 'stuck') return 'stuck';
  return s.status === 'killed' ? 'error' : 'done';
}

/* Live sessions get a status-tinted hairline (Redesign "Active now" card). */
const CARD_BORDERS: Partial<Record<CanvasTone, string>> = {
  active: 'border-green/25 hover:border-green/55',
  waiting: 'border-amber/35 hover:border-amber/70',
  stuck: 'border-amber/35 hover:border-amber/70',
};

const CANVAS_LABEL: Record<CanvasTone, string> = {
  active: 'working',
  waiting: 'waiting',
  stuck: 'stuck',
  error: 'killed',
  done: 'done',
};

const CANVAS_CHIP_STYLE: Record<CanvasTone, string> = {
  active: 'border-green/40 text-green',
  waiting: 'border-amber/40 text-amber',
  stuck: 'border-amber/40 text-amber',
  error: 'border-red/40 text-red',
  done: 'border-line-strong text-ink-dim',
};

/** Chip suffix: stuck shows QUIET TIME (silence since last transcript
 * activity), not session age — `working · 17 h 32 min` was the lie this
 * replaces. Everything else keeps the session span. */
function chipSuffix(session: Session, tone: CanvasTone): string {
  if (tone === 'stuck') {
    return `quiet ${fmtSpan(session.endedAt ?? session.startedAt, null)}`;
  }
  return fmtSpan(session.startedAt, session.endedAt);
}

/** Row status dot (Canvas §3a): only LIVE sessions carry a marker — a hollow
 * colour ring for active/error/waiting/stuck. done renders an empty span so
 * the grid column stays aligned without a resting-state dot. */
function RowDot({ tone }: { tone: CanvasTone }): JSX.Element {
  if (tone === 'active') {
    return <span className="inline-block h-2 w-2 shrink-0 animate-pulse-dot rounded-full border-2 border-green" />;
  }
  if (tone === 'error') {
    return <span className="inline-block h-2 w-2 shrink-0 rounded-full border-2 border-red" />;
  }
  if (tone === 'waiting' || tone === 'stuck') {
    return <span className="inline-block h-2 w-2 shrink-0 rounded-full border-2 border-amber" />;
  }
  return <span className="inline-block h-2 w-2 shrink-0" />;
}

/** Right-justified status chip (Canvas §3e): "working · 3h43m" / "stuck · quiet 42 min" / plain span. */
function RowChip({ tone, suffix }: { tone: CanvasTone; suffix: string }): JSX.Element {
  return (
    <span
      className={`justify-self-end rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] whitespace-nowrap ${CANVAS_CHIP_STYLE[tone]}`}
    >
      {tone === 'active' || tone === 'stuck' || tone === 'error'
        ? `${CANVAS_LABEL[tone]} · ${suffix}`
        : suffix}
    </span>
  );
}

/* Column templates for the ≥900px flat row. Enumerated as whole literals (not
 * assembled) so Tailwind's source scan can see every class it must emit. */
const FLAT_GRID = {
  full: 'grid-cols-[15px_130px_minmax(0,1fr)_150px_90px]',
  noProject: 'grid-cols-[15px_minmax(0,1fr)_150px_90px]',
  tagged: 'grid-cols-[15px_40px_130px_minmax(0,1fr)_150px_90px]',
  taggedNoProject: 'grid-cols-[15px_40px_minmax(0,1fr)_150px_90px]',
} as const;

function flatGrid(hideProject: boolean, tagged: boolean): string {
  if (tagged) return hideProject ? FLAT_GRID.taggedNoProject : FLAT_GRID.tagged;
  return hideProject ? FLAT_GRID.noProject : FLAT_GRID.full;
}

export function SessionCard({
  session,
  now = null,
  flat = false,
  dense = false,
  hideProject = false,
  label = null,
  roleTag = null,
  subline = null,
}: {
  session: Session;
  /** Live "now: <last action>" line, fed by event_appended WS messages. */
  now?: string | null;
  /** Row inside a grouped list card (no own border — hover fill instead). */
  flat?: boolean;
  /** Tighter padding for a fan-out row nested inside a card (PlanRunCard). */
  dense?: boolean;
  /** Drop the project cell — redundant when the list is already scoped to one
   * project (project-mode /p/:slug/sessions). */
  hideProject?: boolean;
  /** Replaces the session title as the row's headline. Plan fan-out rows pass
   * the phase name / "plan controller", because every session of one plan run
   * carries the same boilerplate prompt as its title — it tells them apart from
   * nothing. */
  label?: string | null;
  /** Mono role tag in its own leading column (`ctl`, `#5`) — plan fan-out rows. */
  roleTag?: string | null;
  /** Replaces the dim meta line (model · branch · time) with an explanatory
   * one-liner — "dispatches phases in dependency order", retry attempts, … */
  subline?: string | null;
}): JSX.Element {
  const navigate = useNavigate();
  // Preserve the mode when opening a session: in project mode (/p/:slug/…) stay
  // under the project subtree so the header/sidebar don't flip to session mode.
  const sessionHref = useSessionHref();
  const nowMs = useNowMs();
  const tone = toneOf(session, nowMs);
  const liveNow = now !== null && (tone === 'active' || tone === 'waiting');
  const goToDetail = (): void => {
    navigate(sessionHref(session.id));
  };
  const headline = label ?? session.title;

  /* Plan fan-out rows (roleTag set) have an AUTHORITATIVE sub-line. Every
   * session of a plan run carries the same boilerplate prompt as its title AND
   * as its `why` preview ("You are executing ONE phase of an approved
   * implementation plan, headlessly, in an isolated git worktree…"), so falling
   * back to either would reprint the exact text this row exists to replace.
   * Precedence: the caller's note (controller explainer / retry attempt) →
   * the live action line → nothing at all. */
  const planRow = roleTag !== null;
  const liveLine = liveNow ? `now: ${now ?? ''}` : null;
  const rowSubline: string | null = planRow
    ? (subline ?? liveLine)
    : (liveLine ?? subline ?? session.why ?? meta(session));

  /* Action slot: stuck rows with a confirmed-alive process keep the hard
   * Kill; any other live tone offers the graceful Stop (no PID needed);
   * done rows keep KillButton's existing 'exited' tag when a PID is known. */
  const action: JSX.Element | null =
    tone === 'stuck' && killSlotKind(session) === 'killable' ? (
      <KillButton session={session} />
    ) : tone === 'active' || tone === 'waiting' || tone === 'stuck' ? (
      <StopButton session={session} />
    ) : session.procPid != null ? (
      <KillButton session={session} />
    ) : null;

  /* Stacked card — standalone cards and the <900px rows inside day groups. */
  const card = (
    <>
      <div className="flex items-center gap-2">
        <RowDot tone={tone} />
        {hideProject ? (
          <span className="min-w-0 flex-1" />
        ) : (
          <ProjectName
            name={session.projectName}
            slug={session.projectSlug}
            className="min-w-0 flex-1 truncate font-mono text-[11px]"
          />
        )}
        <AccountBadge session={session} />
        <ContextBadge session={session} />
        <HandoffChip session={session} />
        <ProcBadge session={session} />
        {session.outcome != null && (
          <span
            role="img"
            aria-label={session.outcome}
            data-tip={session.outcome}
            className={`font-mono text-[11px] ${OUTCOME_GLYPH[session.outcome].className}`}
          >
            {OUTCOME_GLYPH[session.outcome].glyph}
          </span>
        )}
        <RowChip tone={tone} suffix={chipSuffix(session, tone)} />
      </div>
      <div className="mt-px mb-[3px] flex min-w-0 items-baseline gap-1.5">
        {roleTag !== null && (
          <span className="shrink-0 font-mono text-[10.5px] text-ink-faint">{roleTag}</span>
        )}
        <span className="min-w-0 truncate text-[13.5px] font-semibold">
          {headline ?? session.sessionUuid}
        </span>
      </div>
      <div className="truncate font-mono text-[11px] text-ink-dim">
        {subline ?? meta(session)}
      </div>
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
      {action !== null && (
        <div className="mt-[3px] flex" onClick={(e) => e.stopPropagation()}>
          {action}
        </div>
      )}
    </>
  );

  /* Navigation via div+useNavigate instead of <Link> so that the action
   * buttons' stopPropagation reliably blocks navigation — <a> tags intercept
   * clicks at the browser level before React's synthetic event system can
   * stop them. */
  if (!flat) {
    return (
      <div
        role="link"
        tabIndex={0}
        onClick={goToDetail}
        onKeyDown={(e) => { if (e.key === 'Enter') goToDetail(); }}
        className={`mb-2.5 block cursor-pointer rounded-xl border bg-surface px-3.5 py-[11px] transition-colors focus-visible:outline-2 focus-visible:outline-brand ${
          CARD_BORDERS[tone] ?? 'border-line hover:border-ink-dim/50'
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
  return (
    <div
      role="link"
      tabIndex={0}
      onClick={goToDetail}
      onKeyDown={(e) => { if (e.key === 'Enter') goToDetail(); }}
      className="block cursor-pointer transition-colors hover:bg-surface focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
    >
      <div className={`desk:hidden ${dense ? 'px-3 py-2' : 'px-3.5 py-[11px]'}`}>{card}</div>
      <div
        className={`hidden items-center gap-3.5 px-1 desk:grid ${dense ? 'py-2' : 'py-3'} ${flatGrid(hideProject, roleTag !== null)}`}
      >
        <span className="flex justify-center">
          <RowDot tone={tone} />
        </span>
        {roleTag !== null && (
          <span className="truncate font-mono text-[10.5px] text-ink-faint">{roleTag}</span>
        )}
        {!hideProject && (
          <span className="flex min-w-0 items-center">
            <ProjectName
              name={session.projectName}
              slug={session.projectSlug}
              className="truncate font-mono text-[11px]"
            />
          </span>
        )}
        <span className="min-w-0">
          <span className="flex min-w-0 items-baseline gap-1.5">
            <span
              className={`min-w-0 truncate text-[14px] font-semibold ${
                headline == null ? 'font-normal text-ink-faint italic' : 'text-ink'
              }`}
            >
              {headline ?? '(untitled session)'}
            </span>
            {session.outcome != null && (
              <span
                role="img"
                aria-label={session.outcome}
                data-tip={session.outcome}
                className={`shrink-0 font-mono text-[11px] ${OUTCOME_GLYPH[session.outcome].className}`}
              >
                {OUTCOME_GLYPH[session.outcome].glyph}
              </span>
            )}
            <AccountBadge session={session} />
            <ContextBadge session={session} />
            <HandoffChip session={session} />
          </span>
          {/* Non-breaking space when there is no sub-line: the row keeps its
              height, so a fan-out with mixed notes does not shear. */}
          <span className="mt-0.5 block truncate text-[12px] text-ink-dim">
            {rowSubline ?? ' '}
          </span>
          {(session.taskExternalId != null || action !== null) && (
            <span className="mt-[3px] flex min-w-0 items-center gap-1.5">
              {session.taskExternalId != null && (
                <TaskChip
                  externalId={session.taskExternalId}
                  linkSource={session.taskLinkSource}
                  confidence={session.taskConfidence}
                />
              )}
              <ProcBadge session={session} />
              {action !== null && (
                <span onClick={(e) => e.stopPropagation()}>{action}</span>
              )}
            </span>
          )}
        </span>
        <SessionModelCell session={session} />
        <RowChip tone={tone} suffix={chipSuffix(session, tone)} />
      </div>
    </div>
  );
}
