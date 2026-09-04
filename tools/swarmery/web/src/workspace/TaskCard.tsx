// Board task card (fusion phase 4): the unit on the board.
//
// Four rows above the lane's action block (board redesign v2 phase 1), answering
// "what / where from / does it want me" without opening the modal:
//   1. title, with a priority dot only when the priority is urgent or high
//   2. where the card came from + its age, linked, quote on hover (SourceRow)
//   3. the ONE thing it wants — failed verdict, dispatch error, blocking
//      dependency, awaited review — or nothing (SignalRow)
//   4. chips for NON-DEFAULT values only: id, model, playbook ≠ standard, agent
//      when set, a pass/inconclusive verdict, labels, paused, session link
// A card the sweeper is about to retire is dimmed and captions its own archive
// date in row 2. Everything a card no longer shows is in the TaskModal, which is
// what clicking the body opens.
//
// Clipped in this phase: the ⟲ origin badge (row 2 says which session, not just
// "from session") and the bare ⚠ dispatch-error glyph (row 3 says what broke).
// The verdict chip survives only for pass/inconclusive — a FAIL is row 3.
//
// Drag&drop is GONE (board redesign phase 4). It used to be the dispatch
// mechanism — HTML5 draggable on the card, a drop handler per column — and it
// made the one irreversible thing on this board (accepting a card into the
// dispatch queue) the easiest thing to do by accident, while being invisible to
// a keyboard and unusable on touch. Movement is verbs now: each lane renders the
// exits that make sense for a card in it (CardActions below), and the "move
// to →" ColumnMenu stays as the escape hatch covering every remaining legal
// transition.

import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type { BoardColumn, BoardTask, TaskPriority } from '../api/types';
import { useSessionHref } from '../lib/sessionHref';
import type { AttentionSignal, AttentionTone, SourceLine } from './boardModel';
import {
  ageLabel,
  attentionSignal,
  BOARD_COLUMNS,
  BOARD_LANES,
  COLUMN_LABELS,
  isStale,
  laneOf,
  LANE_TITLES,
  labelColor,
  sourceLine,
  staleLabel,
  visibleLabels,
} from './boardModel';
import { DEFAULT_PLAYBOOK } from './PlaybookPicker';

/**
 * Priorities that earn a dot. `normal` is the default every card carries and
 * `low` is quieter than the default — a mark for either is a mark on every card,
 * which is no mark at all (phase 1: badges only for non-default values).
 *
 * Partial on purpose: under `noUncheckedIndexedAccess` the lookup is
 * `string | undefined`, so the renderer cannot forget the guard.
 */
const PRIORITY_DOT: Partial<Record<TaskPriority, string>> = {
  urgent: 'bg-red',
  high: 'bg-amber',
};

const VERDICT_STYLE: Record<string, string> = {
  pass: 'border-green/40 bg-green/10 text-green',
  fail: 'border-red/40 bg-red/10 text-red',
  inconclusive: 'border-amber/40 bg-amber/10 text-amber',
};

function VerdictBadge({ verdict }: { verdict: string }): JSX.Element {
  const style = VERDICT_STYLE[verdict] ?? 'border-line text-ink-faint';
  return (
    <span className={`rounded-full border px-1.5 py-[1px] font-mono text-[9px] uppercase ${style}`}>
      {verdict}
    </span>
  );
}

/**
 * Where the card came from, with its age — row 2 of the card, and the answer to
 * "what is this and why do I have it" that used to require opening the modal.
 *
 * This replaced the ⟲ origin badge. The badge said "from session" in a chip; the
 * line says which session, how long ago, links to the transcript and hovers the
 * captured opening prompt. Keeping both would have printed the same fact twice
 * on a card whose whole budget is four rows. The totality fence the badge's
 * Record carried moved with it: `sourceLine` returns on every branch, so an
 * origin nobody has handled yet changes the words, not the render.
 */
function SourceRow({ task, now }: { task: BoardTask; now: number }): JSX.Element {
  const sessionHref = useSessionHref();
  const line: SourceLine = sourceLine(task);
  const age = ageLabel(task, now);
  const stale = staleLabel(task, now);
  const href =
    line.target === null
      ? null
      : line.target.kind === 'session'
        ? sessionHref(line.target.sessionId)
        : `/p/${line.target.slug}/plans`;
  return (
    <div className="mt-1 flex items-baseline gap-1.5 font-mono text-[9.5px] leading-snug text-ink-faint">
      {href === null ? (
        <span data-tip={line.tip} className="min-w-0 truncate">
          {line.text}
        </span>
      ) : (
        <Link
          to={href}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          data-tip={line.tip}
          className="min-w-0 truncate transition-colors hover:text-ink"
        >
          {line.text}
        </Link>
      )}
      {age !== null && <span className="shrink-0">· {age}</span>}
      {stale !== null && (
        <span data-tip="the inbox sweeper retires captured cards left untriaged" className="ml-auto shrink-0 text-amber">
          {stale}
        </span>
      )}
    </div>
  );
}

/** Tone → how loud the signal row reads. A failure is red because it is a
 * decision only a human can take; a dependency block is amber because it will
 * clear itself; an awaited review is neither. */
const SIGNAL_STYLE: Record<AttentionTone, string> = {
  bad: 'text-red',
  warn: 'text-amber',
  info: 'text-ink-dim',
};

/** The glyph that used to sit in the title row as a bare ⚠ with a tooltip. It
 * reads the same way, next to words that say what happened. */
const SIGNAL_GLYPH: Record<AttentionTone, string> = {
  bad: '⚠',
  warn: '⏸',
  info: '◇',
};

/**
 * Row 3: the ONE thing this card wants, or nothing at all. `attentionSignal`
 * owns the precedence — see its doc for why a card never stacks two.
 */
function SignalRow({ signal }: { signal: AttentionSignal }): JSX.Element {
  return (
    <div
      data-tip={signal.tip}
      className={`mt-1 flex items-baseline gap-1 font-mono text-[9.5px] leading-snug ${SIGNAL_STYLE[signal.tone]}`}
    >
      <span aria-hidden="true" className="shrink-0">
        {SIGNAL_GLYPH[signal.tone]}
      </span>
      <span className="min-w-0 truncate">{signal.text}</span>
    </div>
  );
}

/** One label chip: a small colored pill, no icon. Color is a pure hash of the
 * label text (see `labelColor`) so e.g. "jira-ticket" always reads the same
 * accent everywhere it appears — stable across renders because nothing but
 * the label string feeds it. */
function LabelBadge({ label }: { label: string }): JSX.Element {
  const hsl = labelColor(label);
  return (
    <span
      className="rounded-full border px-1.5 py-[1px] font-mono text-[9px]"
      style={{
        borderColor: `hsl(${hsl} / 0.4)`,
        backgroundColor: `hsl(${hsl} / 0.12)`,
        color: `hsl(${hsl})`,
      }}
    >
      {label}
    </span>
  );
}

/** Renders a card's label chips: up to `MAX_VISIBLE_LABELS` directly, the rest
 * rolled into a single "+N" chip whose tooltip lists every label. Nothing
 * renders for an empty array — an unlabeled card looks exactly as before. */
function LabelBadges({ labels }: { labels: readonly string[] }): JSX.Element | null {
  if (labels.length === 0) return null;
  const { shown, overflow } = visibleLabels(labels);
  return (
    <>
      {shown.map((l) => (
        <LabelBadge key={l} label={l} />
      ))}
      {overflow > 0 && (
        <span
          data-tip={labels.join(', ')}
          className="rounded-full border border-line px-1.5 py-[1px] font-mono text-[9px] text-ink-dim"
        >
          +{overflow}
        </span>
      )}
    </>
  );
}

/** One size for every card verb. */
const ACTION_BTN =
  'rounded border px-1.5 py-[2px] font-mono text-[9.5px] transition-colors disabled:opacity-50';
/** The primary verb of a lane — the one the card is usually here to receive. */
const ACTION_PRIMARY = `${ACTION_BTN} border-brand/45 bg-brand/10 text-brand hover:bg-brand/20`;
/** A secondary verb: available, not the point of the lane. */
const ACTION_PLAIN = `${ACTION_BTN} border-line text-ink-dim hover:border-line-strong hover:text-ink`;
/** A retiring verb, pushed to the right edge of its row. */
const ACTION_QUIET = `${ACTION_BTN} border-transparent text-ink-faint hover:border-line hover:text-ink-dim`;

/**
 * Shared chrome of every action row. stopPropagation sits on the ROW, not on
 * each button: the whole card is a click target that opens the modal, and every
 * control in here is an alternative to that.
 */
function ActionRow({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div
      className="mt-2 flex items-center gap-1.5"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => e.stopPropagation()}
    >
      {children}
    </div>
  );
}

/**
 * The three one-click exits from the Inbox. A captured card is a suggestion, and
 * a suggestion needs a decision: Run accepts it into the dispatch queue, Plan
 * carries it to Planning Mode, Dismiss retires it. Only Plan is a new
 * capability — Run and Dismiss are the existing column move, surfaced as a verb
 * so triaging 200 cards is 200 clicks instead of 200 drags.
 */
function TriageActions({
  onRun,
  onPlan,
  onDismiss,
}: {
  onRun: () => void;
  onPlan: () => void;
  onDismiss: () => void;
}): JSX.Element {
  return (
    <ActionRow>
      <button
        type="button"
        onClick={onRun}
        data-tip="accept into the Working queue — the dispatcher picks it up"
        className={ACTION_PRIMARY}
      >
        ▶ Run
      </button>
      <button
        type="button"
        onClick={onPlan}
        data-tip="open Planning Mode prefilled with this card"
        className={ACTION_PLAIN}
      >
        ◇ Plan
      </button>
      <button
        type="button"
        onClick={onDismiss}
        data-tip="archive — it stays findable, it stops being an inbox item"
        className={`${ACTION_QUIET} ml-auto`}
      >
        Dismiss
      </button>
    </ActionRow>
  );
}

/**
 * Exits for a QUEUED card — one that has been accepted but has not started. All
 * three are still cheap here, which is the point of showing them: the window
 * between "accepted" and "running" is the last moment a wrong card costs
 * nothing, so taking it back has to be as easy as sending it was.
 */
function QueuedActions({
  onBackToInbox,
  onTogglePause,
  paused,
  onEdit,
}: {
  onBackToInbox: () => void;
  onTogglePause: (() => void) | undefined;
  paused: boolean;
  onEdit: () => void;
}): JSX.Element {
  return (
    <ActionRow>
      <button
        type="button"
        onClick={onBackToInbox}
        data-tip="take it back out of the queue — nothing has run yet"
        className={ACTION_PLAIN}
      >
        ↩ Inbox
      </button>
      {onTogglePause !== undefined && (
        <button
          type="button"
          onClick={onTogglePause}
          data-tip={
            paused
              ? 'let the dispatcher consider this card again'
              : 'hold this card in the queue without losing its place'
          }
          className={ACTION_PLAIN}
        >
          {paused ? '▶ Resume' : '❙❙ Pause'}
        </button>
      )}
      <button type="button" onClick={onEdit} data-tip="open the full card" className={`${ACTION_QUIET} ml-auto`}>
        Edit
      </button>
    </ActionRow>
  );
}

/**
 * Exits for a RUNNING card. Deliberately the thinnest row on the board: the two
 * useful things to do to work in flight are stop feeding it and go look at it.
 * Everything else is a decision for when it lands in Review.
 */
function RunningActions({
  onTogglePause,
  paused,
  onOpenTerminal,
}: {
  onTogglePause: (() => void) | undefined;
  paused: boolean;
  onOpenTerminal: (() => void) | undefined;
}): JSX.Element | null {
  if (onTogglePause === undefined && onOpenTerminal === undefined) return null;
  return (
    <ActionRow>
      {onTogglePause !== undefined && (
        <button
          type="button"
          onClick={onTogglePause}
          data-tip={paused ? 'unpause this card' : 'pause — the run finishes, nothing new starts'}
          className={ACTION_PLAIN}
        >
          {paused ? '▶ Resume' : '❙❙ Pause'}
        </button>
      )}
      {onOpenTerminal !== undefined && (
        <button
          type="button"
          onClick={onOpenTerminal}
          data-tip="open a terminal in this card's worktree"
          className={`${ACTION_PLAIN} ml-auto`}
        >
          ❯_ Terminal
        </button>
      )}
    </ActionRow>
  );
}

/**
 * Exits for a card in REVIEW. The four real decisions — Land, Re-run with
 * feedback, Discard, Re-verify — live in the TaskModal, because each of them
 * needs something the card has no room for (a confirm, a feedback textarea, the
 * diff, the verdict). So the card offers the one-click override that needs no
 * context, and a labelled door to the rest rather than making the reviewer guess
 * that clicking the card is where the decisions are.
 */
function ReviewActions({ onMarkDone, onReview }: { onMarkDone: () => void; onReview: () => void }): JSX.Element {
  return (
    <ActionRow>
      <button
        type="button"
        onClick={onReview}
        data-tip="Land, Re-run with feedback, Discard, Re-verify — with the diff and the verdict"
        className={ACTION_PRIMARY}
      >
        ◇ Review…
      </button>
      <button
        type="button"
        onClick={onMarkDone}
        data-tip="mark done without landing a branch — the manual override"
        className={`${ACTION_QUIET} ml-auto`}
      >
        ✓ Mark done
      </button>
    </ActionRow>
  );
}

/** The two history columns, grouped under their own heading in ColumnMenu —
 * `laneOf` returns null for both, which is exactly why they need a fallback
 * group rather than being silently dropped from the list. */
const HISTORY_COLUMNS: BoardColumn[] = ['done', 'archived'];

/**
 * The escape hatch: a native <select> covering every legal column, on every
 * card. The lane verbs above are the paths worth naming; this is what makes the
 * remaining transitions reachable at all — and, being a real <select>, it is
 * what keeps them reachable from a keyboard and a screen reader. Grouped into
 * `<optgroup>`s by the lane each column now renders in (plus a History group
 * for done/archived) so the list reads in the three-lane vocabulary the rest
 * of the post-redesign board uses, rather than a flat pre-redesign column
 * list — the `value`s underneath are still raw COLUMNS, because that is what
 * the PATCH carries.
 */
function ColumnMenu({
  column,
  onMove,
}: {
  column: BoardColumn;
  onMove: (to: BoardColumn) => void;
}): JSX.Element {
  return (
    <select
      value={column}
      aria-label="move task to column"
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => {
        const to = e.target.value as BoardColumn;
        if (to !== column) onMove(to);
      }}
      className="rounded-md border border-line bg-field px-1 py-[1px] font-mono text-[9.5px] text-ink-dim outline-none transition-colors hover:border-line-strong focus:border-ink-dim"
    >
      {BOARD_LANES.map((lane) => (
        <optgroup key={lane} label={LANE_TITLES[lane]}>
          {BOARD_COLUMNS.filter((c) => laneOf(c) === lane).map((c) => (
            <option key={c} value={c}>
              {COLUMN_LABELS[c]}
            </option>
          ))}
        </optgroup>
      ))}
      <optgroup label="History">
        {HISTORY_COLUMNS.map((c) => (
          <option key={c} value={c}>
            {COLUMN_LABELS[c]}
          </option>
        ))}
      </optgroup>
    </select>
  );
}

export function TaskCard({
  task,
  onOpen,
  onMove,
  onPlan,
  onTogglePause,
  onOpenTerminal,
}: {
  task: BoardTask;
  onOpen: () => void;
  onMove: (to: BoardColumn) => void;
  /**
   * Hand-off to Planning Mode for this card. Optional because it is the one
   * triage action the card cannot perform on its own (Run and Dismiss are just
   * `onMove`) — omit it and the Inbox action row does not render, which is what
   * every history caller wants anyway.
   */
  onPlan?: (() => void) | undefined;
  /** Flip `user_paused`. Omitted in the history drawer, where it is meaningless. */
  onTogglePause?: (() => void) | undefined;
  /** Open a terminal in this card's worktree; omitted when there is no worktree
   * to open, or outside a workspace layout that owns a dock. */
  onOpenTerminal?: (() => void) | undefined;
}): JSX.Element {
  const blocked = task.paused || task.userPaused;
  // Triage is the only column where a card is still a question. Elsewhere it is
  // committed work, and the "why is this still here" hint would be noise.
  const inTriage = task.boardColumn === 'triage';
  const signal = attentionSignal(task);
  const priorityDot = PRIORITY_DOT[task.priority];
  // ONE clock read for the whole card: the dimming below and the age + archive
  // caption in SourceRow are three readings of the same instant, and taking them
  // from separate `Date.now()` calls lets a card dim without saying why.
  const now = Date.now();
  // A card the sweeper is about to retire is dimmed rather than hidden or
  // badged: it is still triageable right up to the sweep, and the caption in the
  // source row says by when. `isStale` is false for every card the sweeper
  // cannot touch — which is most of the board — so this dims nothing that is not
  // actually expiring.
  const stale = isStale(task, now);
  // A non-default playbook only. `null` and the literal 'standard' are the same
  // recipe (types.ts: null = default 'standard'), and the dispatcher stamps the
  // autopicked name onto the row, so a chip that fired on non-null would appear
  // on nearly every dispatched card.
  const playbook = task.playbook !== null && task.playbook !== DEFAULT_PLAYBOOK ? task.playbook : null;
  // A fail verdict is the attention signal above, with its detail. Repeating it
  // as a chip would print the same word twice. Keyed on the verdict itself, not
  // on the signal's tone: a PASSED card that also failed to dispatch has a 'bad'
  // signal about the dispatch, and its verdict is still worth a chip.
  const verdict =
    task.verifyVerdict !== null && task.verifyVerdict.toLowerCase() !== 'fail' ? task.verifyVerdict : null;
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`task ${task.externalId}: ${task.title}`}
      onClick={onOpen}
      onKeyDown={(e) => {
        // Only the card itself opens on Enter/Space. Without this guard the
        // preventDefault below would run during the bubble phase of a keydown
        // aimed at a nested control (the action buttons, the column select,
        // the session link) and cancel that control's own activation — the
        // card would open instead of the button firing.
        if (e.target !== e.currentTarget) return;
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onOpen();
        }
      }}
      className={`group cursor-pointer rounded-lg border border-line bg-surface p-2.5 transition-colors hover:border-line-strong focus:border-ink-dim focus:outline-none ${stale ? 'opacity-60' : ''}`}
    >
      <div className="flex items-start gap-2">
        {priorityDot !== undefined && (
          <span
            aria-hidden="true"
            data-tip={`${task.priority} priority`}
            className={`mt-[5px] h-[7px] w-[7px] shrink-0 rounded-full ${priorityDot}`}
          />
        )}
        <span className="min-w-0 flex-1 text-[12.5px] leading-snug text-ink">{task.title}</span>
      </div>

      <SourceRow task={task} now={now} />

      {signal !== null && <SignalRow signal={signal} />}

      <div className="mt-2 flex flex-wrap items-center gap-1.5">
        <span className="rounded border border-line px-1 py-[1px] font-mono text-[9px] text-ink-faint">
          {task.externalId}
        </span>
        {task.model !== null && (
          <span className="rounded border border-line px-1 py-[1px] font-mono text-[9px] text-ink-dim">
            {task.model}
          </span>
        )}
        {playbook !== null && (
          <span
            data-tip={`playbook: ${playbook}`}
            className="rounded border border-brand/40 bg-brand/5 px-1 py-[1px] font-mono text-[9px] text-brand"
          >
            ▤ {playbook}
          </span>
        )}
        {task.agent !== null && (
          <span
            data-tip={`dispatches as @${task.agent}`}
            className="rounded border border-line px-1 py-[1px] font-mono text-[9px] text-ink-dim"
          >
            @{task.agent}
          </span>
        )}
        {verdict !== null && <VerdictBadge verdict={verdict} />}
        <LabelBadges labels={task.labels} />
        {blocked && (
          <span className="rounded-full border border-amber/40 bg-amber/10 px-1.5 py-[1px] font-mono text-[9px] text-amber">
            paused
          </span>
        )}
        {task.branch !== null && (
          <a
            href={`/sessions?scope=${task.projectSlug ?? ''}`}
            onClick={(e) => e.stopPropagation()}
            data-tip={`branch ${task.branch}`}
            aria-label={`sessions for ${task.branch}`}
            className="font-mono text-[9px] text-ink-faint transition-colors hover:text-ink"
          >
            ❯ session
          </a>
        )}
        <span className="ml-auto opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
          <ColumnMenu column={task.boardColumn} onMove={onMove} />
        </span>
      </div>

      {/* Why this card is still here. The server has computed this on every
          board read since staleness landed and nothing ever rendered it; on a
          Triage card it is the difference between Dismiss as a guess and
          Dismiss as an informed click. */}
      {inTriage && task.stalenessReason !== undefined && task.stalenessReason !== '' && (
        <div className="mt-1.5 font-mono text-[9.5px] leading-snug text-ink-faint">
          {task.stalenessReason}
        </div>
      )}

      {/* The lane's own verbs. Every card carries exactly the exits that make
          sense where it is standing — which is what replaced dragging it
          somewhere else. History cards (done/archived) get none: they are a
          record, and the card body still opens the full modal. */}
      {task.boardColumn === 'triage' && onPlan !== undefined && (
        <TriageActions onRun={() => onMove('todo')} onPlan={onPlan} onDismiss={() => onMove('archived')} />
      )}
      {task.boardColumn === 'todo' && (
        <QueuedActions
          onBackToInbox={() => onMove('triage')}
          onTogglePause={onTogglePause}
          paused={task.userPaused}
          onEdit={onOpen}
        />
      )}
      {task.boardColumn === 'in_progress' && (
        <RunningActions
          onTogglePause={onTogglePause}
          paused={task.userPaused}
          onOpenTerminal={onOpenTerminal}
        />
      )}
      {task.boardColumn === 'in_review' && (
        <ReviewActions onMarkDone={() => onMove('done')} onReview={onOpen} />
      )}
    </div>
  );
}
