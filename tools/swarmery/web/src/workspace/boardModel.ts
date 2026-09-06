// Board presentation model (fusion phase 4): the closed column order, their
// display labels, the three-lane collapse the board renders through (phase 4 of
// the board redesign), and the client-side derivation of the status-bar counts.
// Kept pure so it is trivially unit-testable and shared by Board + StatusBar.

import type { BoardColumn, BoardTask, TaskPriority } from '../api/types';

/** Left-to-right column order on the board. */
export const BOARD_COLUMNS: BoardColumn[] = [
  'triage',
  'todo',
  'in_progress',
  'in_review',
  'done',
  'archived',
];

/**
 * The ONE word each column is called in the UI. `todo` reads 'Queued' because
 * that is what the Working lane already calls the group it renders (Board.tsx)
 * — the pair 'Todo'/'Queued' was two words for one state, and the modal printed
 * a third (the raw `queued` status). Everything user-visible now derives from
 * this map; the `todo` VALUE on the wire is untouched, it is what the dispatcher
 * triggers on.
 */
export const COLUMN_LABELS: Record<BoardColumn, string> = {
  triage: 'Triage',
  todo: 'Queued',
  in_progress: 'In Progress',
  in_review: 'In Review',
  done: 'Done',
  archived: 'Archived',
};

/**
 * What state the card is in, in the board's own vocabulary — the modal's
 * replacement for rendering the raw `status` field.
 *
 * `status` is the DISPATCHER's column: a card that has never run reads `queued`
 * there whether it is sitting in the Inbox awaiting triage or actually waiting
 * for a slot, so printing it told the reader something that was true of the row
 * and false of the card. The board column is what the board is about, and the
 * two pause flags are the only thing the column does not already say — the
 * dispatcher parks a card (`paused`) for a reason the card cannot show, and the
 * user parks it (`userPaused`) deliberately, and those need different words.
 *
 * Total over `BoardColumn` by construction: the base comes out of a total
 * Record, so a new column changes what a card SAYS, never whether it renders.
 */
export function stateLabel(task: BoardTask): string {
  const base = COLUMN_LABELS[task.boardColumn];
  if (task.userPaused) return `${base} · paused by you`;
  if (task.paused) return `${base} · paused by the dispatcher`;
  return base;
}

// --- lanes (board redesign phase 4) -------------------------------------------
//
// The board renders three LANES, not six columns. The `board_column` enum above
// is untouched — it is still what the API speaks, what the dispatcher triggers
// on (`board_column='todo'`), and what every PATCH carries. Lanes are a pure
// presentation collapse derived at render time, which is what keeps this
// revertable and keeps the wire contract independent of how the board looks.

/** The three lanes a live card can sit in. */
export type BoardLane = 'inbox' | 'working' | 'review';

/** Left-to-right lane order on the board. */
export const BOARD_LANES: BoardLane[] = ['inbox', 'working', 'review'];

/**
 * Which lane each LIVE column collapses into. `done` and `archived` are
 * deliberately excluded from the key type rather than mapped to a lane: they
 * render in the history drawer, and excluding them makes "which lane does done
 * go in?" a compile error instead of a silently wrong answer.
 */
export const LANE_OF: Record<Exclude<BoardColumn, 'done' | 'archived'>, BoardLane> = {
  triage: 'inbox',
  todo: 'working',
  in_progress: 'working',
  in_review: 'review',
};

export const LANE_TITLES: Record<BoardLane, string> = {
  inbox: 'Inbox',
  working: 'Working',
  review: 'Review',
};

/** The lane a column renders in, or null for the two history columns. */
export function laneOf(column: BoardColumn): BoardLane | null {
  if (column === 'done' || column === 'archived') return null;
  return LANE_OF[column];
}

/** Priority tokens, highest first — the option order of every priority select. */
export const TASK_PRIORITIES: TaskPriority[] = ['urgent', 'high', 'normal', 'low'];

/**
 * The INTEGER priority scale the server stores, mirrored token for token from
 * api/tasks_board.go `priorityLabels`. Ascending — urgent sorts first — because
 * that is the direction dispatch/service.go `candidates()` sorts in, and the
 * Queued group exists to show that order. The absolute values matter only in
 * that they preserve the same total ordering as the server's.
 */
export const PRIORITY_RANK: Record<TaskPriority, number> = {
  urgent: 1,
  high: 3,
  normal: 5,
  low: 7,
};

/**
 * The dispatcher's candidate order, comparator for comparator: priority asc →
 * createdAt asc → id asc (dispatch/service.go `candidates()`). The Queued group
 * displays `todo` cards through this so the top card on screen is the next one
 * the dispatcher will actually pick up — a board that ordered them any other way
 * would be lying about what happens next.
 *
 * One honest difference from the server: `candidates()` also filters out paused
 * cards, while Queued shows them (badged `paused`) so a parked card does not
 * vanish. Order among the unpaused cards is identical.
 */
export function compareDispatchOrder(a: BoardTask, b: BoardTask): number {
  const pa = PRIORITY_RANK[a.priority];
  const pb = PRIORITY_RANK[b.priority];
  if (pa !== pb) return pa - pb;
  if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
  return a.id - b.id;
}

/**
 * The board split into what each lane renders. Working is pre-split into its two
 * groups because they are ordered by different rules and carry different
 * actions: `queued` is dispatcher-ordered and still cancellable, `running` is
 * in-flight. `archived` is absent by construction — it has its own lazy fetch
 * and never appears in the live board list.
 */
export interface BoardLanes {
  readonly inbox: readonly BoardTask[];
  /** `todo` — in the dispatcher's own candidate order. */
  readonly queued: readonly BoardTask[];
  /** `in_progress` — in list order (the dispatcher no longer ranks these). */
  readonly running: readonly BoardTask[];
  readonly review: readonly BoardTask[];
  /** `done` — most-recently-moved first; the history drawer's eager half. */
  readonly done: readonly BoardTask[];
}

/** Group a board list into its lanes. Pure: one pass, then the two sorts that
 * each group's own contract requires. */
export function splitLanes(tasks: readonly BoardTask[]): BoardLanes {
  const inbox: BoardTask[] = [];
  const queued: BoardTask[] = [];
  const running: BoardTask[] = [];
  const review: BoardTask[] = [];
  const done: BoardTask[] = [];
  for (const t of tasks) {
    switch (t.boardColumn) {
      case 'triage':
        inbox.push(t);
        break;
      case 'todo':
        queued.push(t);
        break;
      case 'in_progress':
        running.push(t);
        break;
      case 'in_review':
        review.push(t);
        break;
      case 'done':
        done.push(t);
        break;
      default:
        break; // archived — lazy-fetched separately, never in this list
    }
  }
  queued.sort(compareDispatchOrder);
  done.sort((a, b) => (b.columnMovedAt ?? '').localeCompare(a.columnMovedAt ?? ''));
  return { inbox, queued, running, review, done };
}

/**
 * Model tokens the dispatcher passes to `claude --model`. 'default' is the UI's
 * name for "inherit" and maps to a null `model` on the wire — every editor of a
 * task's model (the create modal, the detail modal) reads this one
 * list so they can never drift apart.
 */
export const TASK_MODELS = ['default', 'fable', 'opus', 'sonnet', 'haiku'] as const;

export interface BoardCounts {
  waiting: number;
  running: number;
  blocked: number;
}

/** A task is BLOCKED when either pause flag is set (mirrors the dispatcher's
 * two-flag park semantics). */
export function isBlocked(t: BoardTask): boolean {
  return t.paused || t.userPaused;
}

/**
 * Status-bar counts derived from the board (phase-4 spec):
 *   waiting = triage + todo (not blocked)
 *   running = in_progress (not blocked)
 *   blocked = any task parked by a pause flag (across live columns)
 * Blocked wins over waiting/running so a paused in_progress task counts once,
 * as Blocked. Done/archived never contribute.
 */
export function boardCounts(tasks: BoardTask[]): BoardCounts {
  let waiting = 0;
  let running = 0;
  let blocked = 0;
  for (const t of tasks) {
    if (t.boardColumn === 'done' || t.boardColumn === 'archived') continue;
    if (isBlocked(t)) {
      blocked += 1;
      continue;
    }
    if (t.boardColumn === 'triage' || t.boardColumn === 'todo') waiting += 1;
    else if (t.boardColumn === 'in_progress') running += 1;
  }
  return { waiting, running, blocked };
}

// --- card readout (board redesign v2 phase 1) ---------------------------------
//
// What the card answers without opening the modal: what this is, where it came
// from, and whether it wants something from me. All four selectors below are
// pure functions of ONE flat BoardTask (plus `now` where time matters), which is
// the plan's architecture decision: the DTO stays flat and the grouping lives
// here, where it is unit-testable without a server.

/** Milliseconds in a day — every age and expiry sum below. */
const DAY_MS = 86_400_000;

/**
 * `staleAfter` as epoch ms, or null when the card cannot expire.
 *
 * Null, and an empty string, BOTH mean "the sweeper will never touch this card".
 * The sweeper only considers non-manual cards sitting in triage with no
 * worktree, so null is the common case on a live board — a manual card, a
 * running card, anything in review. Arithmetic straight off the raw field
 * (`new Date(task.staleAfter) < now`) reads a null as 1970 and would mark most
 * of the board as about to be archived, which is the exact defect this helper
 * exists to make unrepresentable.
 */
function staleAfterMs(task: BoardTask): number | null {
  if (task.staleAfter === null || task.staleAfter === '') return null;
  const ms = Date.parse(task.staleAfter);
  return Number.isNaN(ms) ? null : ms;
}

/**
 * How close to its archive date a card starts reading as stale. Three days is
 * the last window in which "I should look at this" is still actionable — dimming
 * a card the moment the server dates it (14 days out) would dim every captured
 * card on the board and say nothing.
 */
export const STALE_WARN_DAYS = 3;

/**
 * Whether the card is at, or within `STALE_WARN_DAYS` of, its automatic-archive
 * date. False for every card the sweeper cannot touch — see `staleAfterMs`.
 *
 * This is the ONE staleness predicate on the board: the card dims on it, the
 * card's caption comes from it, and the `stale` filter chip selects on it.
 */
export function isStale(task: BoardTask, nowMs: number): boolean {
  const at = staleAfterMs(task);
  return at !== null && at - nowMs < STALE_WARN_DAYS * DAY_MS;
}

/**
 * The already-past subset of `isStale`: the archive date has been and gone, so
 * the sweeper's next pass takes this card. `isExpired ⇒ isStale` by construction
 * (both read the one `staleAfterMs`), which is what lets the amnesty banner and
 * the `stale` chip share a definition without sharing a set.
 *
 * They must not share the SET. The banner offers a write, and the server's own
 * predicate for that write is `idleSince < now - TTL` — which, since
 * `staleAfter = idleSince + TTL`, is this comparison exactly, strict `<` and all.
 * Counting the warn window in the banner would put a number in the pitch that
 * the server would then decline to act on, which is the drift phase 1 removed
 * when it deleted the client's own `AMNESTY_AGE_DAYS`.
 */
export function isExpired(task: BoardTask, nowMs: number): boolean {
  const at = staleAfterMs(task);
  return at !== null && at < nowMs;
}

/**
 * The stale card's own caption, or null when the card is not stale (so the
 * caption and the dimming can never disagree — both read this pair).
 *
 * A date already in the past does not become a negative countdown: the sweeper
 * runs on its own cadence, so the honest statement is that the next pass takes
 * the card.
 */
export function staleLabel(task: BoardTask, nowMs: number): string | null {
  const at = staleAfterMs(task);
  if (at === null || !isStale(task, nowMs)) return null;
  const remaining = at - nowMs;
  if (remaining <= 0) return 'archived at the next sweep';
  return `archived in ${String(Math.ceil(remaining / DAY_MS))}d`;
}

/**
 * The card's age for the source line: "today" under a day, "12d" beyond. Null
 * when `createdAt` is unparseable — the line then shows its source alone rather
 * than the string "NaNd".
 */
export function ageLabel(task: BoardTask, nowMs: number): string | null {
  const at = Date.parse(task.createdAt);
  if (Number.isNaN(at)) return null;
  const days = Math.max(0, Math.floor((nowMs - at) / DAY_MS));
  return days === 0 ? 'today' : `${String(days)}d`;
}

/**
 * What the source line opens, as a target rather than a path: routing lives in
 * the component (a session href is mode-preserving — lib/sessionHref.ts — and
 * this module must not learn URL shapes to stay a pure model).
 */
export type SourceTarget =
  | { readonly kind: 'session'; readonly sessionId: number }
  | { readonly kind: 'plans'; readonly slug: string };

/** How a card's provenance reads on one line. */
export interface SourceLine {
  /** The prose: "from session #1867", "plan 2026-07-18-…", "fix for T-12". */
  readonly text: string;
  /** What `text` opens, or null when there is nothing to open. */
  readonly target: SourceTarget | null;
  /** Hover detail — the captured quote, or what the link leads to. */
  readonly tip: string | null;
}

/**
 * Where the card came from, as one line. Total over `TaskOrigin` by
 * construction: every branch returns, so widening the union can change what a
 * card SAYS but can never crash the renderer the way an incomplete Record
 * indexed by origin did.
 *
 * Order is provenance-first. A dispatched card materializes a micro-plan, so
 * most running cards carry `planExternalId` too — but a card captured from a
 * session came from that session, and the plan is where its outcome was
 * recorded. The session wins; the plan chip lives in the modal.
 */
export function sourceLine(task: BoardTask): SourceLine {
  // A fix card's own external_id IS the id of the card it repairs — the verifier
  // writes `external_id=<root external id>` so a fix's failure charges the root
  // (verify/service.go createFixTask). That is what makes "fix for T-…"
  // derivable from the flat DTO with no extra field.
  if (task.origin === 'verify-fix') {
    return {
      text: `fix for ${task.externalId}`,
      target: null,
      tip: `spawned by verification to repair ${task.externalId}`,
    };
  }
  const sessionId = task.source?.sessionId ?? task.originSessionId;
  if (sessionId !== null) {
    const id = String(sessionId);
    return {
      text: `${task.origin === 'llm' ? 'suggested from session' : 'from session'} #${id}`,
      target: { kind: 'session', sessionId },
      tip: task.source?.quote ?? `captured from session #${id}`,
    };
  }
  if (task.planExternalId !== null) {
    return {
      text: `plan ${task.planExternalId}`,
      // The Plans page is project-scoped only (main.tsx: /p/:slug/plans), so a
      // card with no slug gets the prose without a link that would 404. It also
      // deep-links by numeric task id rather than external id, which is why the
      // target is the project's plan list and not this one plan.
      target: task.projectSlug === null ? null : { kind: 'plans', slug: task.projectSlug },
      tip: `plan ${task.planExternalId} — acceptance criteria and completion report`,
    };
  }
  if (task.origin === 'manual') return { text: 'added by hand', target: null, tip: null };
  // A captured card with no session id: rows that predate 0048, and rows whose
  // origin session was pruned. It still says where it came from, without a link
  // that would 404.
  return { text: task.origin === 'llm' ? 'suggested' : 'from a session', target: null, tip: null };
}

/**
 * The marker the dispatcher puts on a `dispatch_error` it wrote because a
 * dependency was not satisfied (dispatch/service.go `depBlockPrefix`). A real
 * failure — a crashed runner, a parked no-progress marker — lands in the same
 * column, so this prefix is the only thing separating "waiting on T-14" from
 * "the run broke", and the two must not read alike on a card.
 */
export const DEP_BLOCK_PREFIX = 'blocked by dependency ';

/** How much detail the one-line signal carries before it is clipped; the full
 * text stays in the hover tip. */
export const SIGNAL_CLIP = 80;

function clip(s: string, max: number): string {
  return s.length <= max ? s : `${s.slice(0, max - 1).trimEnd()}…`;
}

/** How loudly a signal reads: a failure, something waiting on another card, or
 * a plain statement of where the card stands. */
export type AttentionTone = 'bad' | 'warn' | 'info';

export interface AttentionSignal {
  readonly text: string;
  readonly tone: AttentionTone;
  /** The full text behind `text` (which may be clipped); null when there is no
   * more to show than the line itself. */
  readonly tip: string | null;
}

/**
 * The ONE thing this card wants from a human, or null when it wants nothing.
 *
 * Exactly one, in a fixed order — a failed verdict, then a broken dispatch, then
 * a blocking dependency, then an awaited review. A card that stacked four
 * warnings would be a card nobody reads; the modal is where everything about a
 * card is visible at once. The order is by what blocks whom: a FAIL is a
 * decision only a human can take, a dispatch error stops this card, a dependency
 * stops it on someone else's card, and a review is work waiting rather than work
 * stuck.
 */
/**
 * Verification came back FAIL on this card.
 *
 * Verdict tokens are lowercase on the wire (internal/verify/verdict.go), but a
 * case fold costs nothing and a mixed-case row would otherwise silently lose the
 * loudest signal on the board. Shared by the card's signal and the `needsMe`
 * filter so the chip and the card can never disagree about what failed.
 */
export function hasFailedVerdict(task: BoardTask): boolean {
  return (task.verifyVerdict ?? '').toLowerCase() === 'fail';
}

/**
 * The dispatcher's error on this card, trimmed; `''` when there is none. Null
 * and a whitespace-only string mean the same thing — the column has held both —
 * and every reader treats them alike by going through here.
 */
export function dispatchErrorText(task: BoardTask): string {
  return (task.dispatchError ?? '').trim();
}

export function attentionSignal(task: BoardTask): AttentionSignal | null {
  if (hasFailedVerdict(task)) {
    const detail = (task.verifyDetail ?? '').trim();
    return {
      text: detail === '' ? 'verdict FAIL' : `verdict FAIL: ${clip(detail, SIGNAL_CLIP)}`,
      tone: 'bad',
      tip: detail === '' ? null : detail,
    };
  }
  const err = dispatchErrorText(task);
  if (err.startsWith(DEP_BLOCK_PREFIX)) {
    const dep = err.slice(DEP_BLOCK_PREFIX.length);
    return { text: `blocked by ${clip(dep, SIGNAL_CLIP)}`, tone: 'warn', tip: err };
  }
  if (err !== '') {
    return { text: `dispatch error: ${clip(err, SIGNAL_CLIP)}`, tone: 'bad', tip: err };
  }
  if (task.boardColumn === 'in_review') {
    return { text: 'waiting for review', tone: 'info', tip: null };
  }
  return null;
}

// --- board filters (board redesign v2 phase 5) --------------------------------
//
// The board answers three questions a person actually asks of it: what is
// waiting on ME, what is running, what has gone off. Before this the only filter
// was `label`, which answers a question about how the cards were tagged rather
// than about what state they are in.
//
// One filter at a time, deliberately. These three are not orthogonal facets to
// intersect — they are three different readings of the same board, and a chip
// row that let you ask for "needs me AND running" would mostly produce empty
// lanes and a puzzle. The label filter IS orthogonal (it is about the card's
// subject, not its state), so it composes with all three, and the two live in
// the URL side by side.

/** The three state filters, as they appear in `?f=`. */
export type BoardFilter = 'needsMe' | 'running' | 'stale';

/** Chip order, left to right. */
export const BOARD_FILTERS: BoardFilter[] = ['needsMe', 'running', 'stale'];

/** The words on the chips. `needsMe` is a camelCase token on the wire and a
 * sentence fragment on screen; the two are not the same string. */
export const BOARD_FILTER_LABELS: Record<BoardFilter, string> = {
  needsMe: 'needs me',
  running: 'running',
  stale: 'stale',
};

/** What each chip promises, on hover — the chip's own name is three words at
 * most, and `needs me` in particular covers four different situations. */
export const BOARD_FILTER_TIPS: Record<BoardFilter, string> = {
  needsMe: 'waiting for review, a failed verdict, a dispatch error, or an unanswered permission request',
  running: 'cards the dispatcher has in flight right now',
  stale: 'cards at or near their automatic-archive date',
};

/**
 * The card is waiting on a HUMAN — the predicate behind the `needs me` chip.
 *
 * Four disjuncts, each a different way a card stops moving without a person:
 *
 *   review        by definition of the lane — a decision only a human takes.
 *   FAIL verdict  verification said no; the next step is a judgement call.
 *   dispatch error the run did not start, or broke.
 *   pending approval the agent is alive and parked on a permission prompt.
 *
 * The fourth is why the DTO grew a field: it is invisible from the tasks row —
 * `permission_requests` has no `task_id` and the link runs through the run
 * session. Narrowing the chip to the first three would have been the cheap
 * option and it would have made the chip lie about its own name, so the count
 * is computed server-side instead (api/tasks_board.go `pendingApprovalPairs`).
 *
 * A dependency-blocked card matches through `dispatchError`, because that is
 * where the dispatcher writes the block (`DEP_BLOCK_PREFIX`). Arguably it is
 * waiting on another CARD rather than on a person — but the card it waits for
 * may itself be stuck, and the plan defined this predicate as "any dispatch
 * error". Kept literal; the card's own signal row still tells the two apart.
 */
export function needsMe(task: BoardTask): boolean {
  return (
    laneOf(task.boardColumn) === 'review' ||
    hasFailedVerdict(task) ||
    dispatchErrorText(task) !== '' ||
    task.pendingApprovalCount > 0
  );
}

/** Whether one card survives one filter. `null` — no filter — matches every
 * card, mirroring `matchesLabelFilter`. */
export function matchesBoardFilter(task: BoardTask, filter: BoardFilter | null, nowMs: number): boolean {
  switch (filter) {
    case null:
      return true;
    case 'needsMe':
      return needsMe(task);
    case 'running':
      return task.boardColumn === 'in_progress';
    case 'stale':
      return isStale(task, nowMs);
  }
}

/** The board under one filter. Pure; `null` returns the list unchanged. */
export function filterTasks(
  tasks: readonly BoardTask[],
  filter: BoardFilter | null,
  nowMs: number,
): readonly BoardTask[] {
  if (filter === null) return tasks;
  return tasks.filter((t) => matchesBoardFilter(t, filter, nowMs));
}

/**
 * `?f=` → a filter, or null. Anything unrecognised reads as "no filter": a URL
 * is user-editable and arrives from bookmarks written against older builds, and
 * the board showing everything is the honest response to a word it does not
 * know — quietly showing nothing would look like an empty board.
 */
export function parseBoardFilter(raw: string | null): BoardFilter | null {
  return BOARD_FILTERS.find((f) => f === raw) ?? null;
}

/** Board header view mode: the lanes, or the dependency graph. */
export type BoardView = 'board' | 'graph';

/** `?view=` → a view. Same tolerance as `parseBoardFilter`, defaulting to the
 * board because that is what the page is. */
export function parseBoardView(raw: string | null): BoardView {
  return raw === 'graph' ? 'graph' : 'board';
}

/**
 * What an empty lane says. A lane is empty far more often than it is full, and
 * an empty bordered box teaches nobody what the lane is for — these sentences
 * are the only place the board explains its own shape, so each one names what
 * arrives here and what leaves.
 */
export const LANE_EMPTY: Record<BoardLane, string> = {
  inbox: 'Tasks captured from sessions and routines land here — Run, Plan or Dismiss each one.',
  working: 'The dispatcher picks cards up from here in priority order.',
  review: 'Cards that have run and carry a verdict wait here — Land, Re-run or Discard.',
};

/**
 * What an empty lane says when a filter is on. The sentence above would be a
 * non-sequitur there: the lane is not empty, the filter is hiding it, and the
 * reader needs to know which of the two they are looking at.
 */
export function laneEmptyNote(lane: BoardLane, filter: BoardFilter | null): string {
  if (filter === null) return LANE_EMPTY[lane];
  return `Nothing in ${LANE_TITLES[lane]} matches “${BOARD_FILTER_LABELS[filter]}”.`;
}

// --- inbox amnesty (board inbox lifecycle) ------------------------------------

/**
 * Above this many Triage cards the inbox has stopped being an inbox: nobody
 * reads a 50-card list top to bottom, so the board offers to clear the old
 * captured ones instead of pretending they are still triageable.
 */
export const AMNESTY_THRESHOLD = 50;

/**
 * When a card's idle clock started. Mirrors taskcap.InboxIdleSince: capture
 * never writes columnMovedAt, so a card that has only ever sat in Triage is
 * dated by createdAt — keying on columnMovedAt alone would count zero of the
 * cards this feature exists for.
 */
export function idleSince(task: BoardTask): string {
  return task.columnMovedAt ?? task.createdAt;
}

/**
 * The sweeper's TTL, recovered from any card the server already dated:
 * `staleAfter` IS `idleSince + SWARMERY_INBOX_TTL`, so the difference between
 * the two is the TTL itself. Null when no loaded card is dated (the sweep is
 * off, or nothing in the inbox is eligible).
 *
 * This is what replaced the client's own `AMNESTY_AGE_DAYS = 7`: a second,
 * unsynchronised copy of a number the daemon owns. It read 7 while the sweeper
 * ran on 14, so the banner offered to archive cards the automatic sweep would
 * not have touched for another week. Derived from the server's own answer, the
 * two can no longer disagree.
 */
export function inboxTtlMs(tasks: readonly BoardTask[]): number | null {
  for (const t of tasks) {
    const at = staleAfterMs(t);
    if (at === null) continue;
    const from = Date.parse(idleSince(t));
    if (Number.isNaN(from)) continue;
    if (at > from) return at - from;
  }
  return null;
}

/**
 * The cutoff instant the amnesty runs against — `now - TTL`, as an RFC3339
 * string, or null when no card is dated and there is therefore nothing to offer.
 *
 * `toISOString()` renders exactly the millisecond-Z shape the server stores, and
 * the server's predicate is `idleSince < before`. Since `staleAfter =
 * idleSince + TTL`, this cutoff selects precisely the cards whose `staleAfter`
 * has already passed: the number in the banner and the set the write touches are
 * the same by construction rather than by two agreeing constants.
 */
export function amnestyBefore(tasks: readonly BoardTask[], nowMs: number): string | null {
  const ttl = inboxTtlMs(tasks);
  if (ttl === null) return null;
  return new Date(nowMs - ttl).toISOString();
}

/**
 * How many inbox cards the sweeper is already entitled to retire: the ones whose
 * server-published `staleAfter` is in the past. It only ever labels the button —
 * the server recounts under `dryRun` before anything is written, so a drift here
 * misleads nobody into a wrong write.
 *
 * No origin / column / worktree conjuncts any more: `staleAfter` is non-null
 * only for a card that already satisfies every one of them (taskcap.StaleAfter
 * shares the sweeper's own predicate), so re-testing them here would be a second
 * copy to drift.
 */
export function amnestyCandidates(tasks: readonly BoardTask[], nowMs: number): number {
  let n = 0;
  for (const t of tasks) if (isExpired(t, nowMs)) n += 1;
  return n;
}

// --- card labels (0049 UI) ----------------------------------------------------

/** A card renders at most this many label chips before rolling the rest into
 * a single "+N" overflow chip, so a card with a dozen labels never blows out
 * its width. */
export const MAX_VISIBLE_LABELS = 3;

/** Split a task's labels into what a chip row shows directly vs. what rolls
 * into the "+N" overflow chip. Order is preserved (the server already
 * lowercases/trims/dedupes on write, first-seen order). */
export function visibleLabels(labels: readonly string[]): { shown: readonly string[]; overflow: number } {
  if (labels.length <= MAX_VISIBLE_LABELS) return { shown: labels, overflow: 0 };
  return { shown: labels.slice(0, MAX_VISIBLE_LABELS), overflow: labels.length - MAX_VISIBLE_LABELS };
}

// Reserved hue bands mirror the project-identity hasher (lib/colors.ts): red
// and green stay out because they already mean fail/pass via VerdictBadge on
// the same card. Kept as an independent hash rather than imported from there —
// labels and projects are different identity spaces and must never collide by
// construction.
const LABEL_HUE_RANGES: ReadonlyArray<readonly [number, number]> = [
  [20, 90], // orange → amber → yellow
  [175, 345], // cyan → blue → indigo → violet → purple → magenta → pink
];
const LABEL_HUE_SPAN = LABEL_HUE_RANGES.reduce((sum, [lo, hi]) => sum + (hi - lo), 0);

function hueFromLabel(label: string): number {
  // Avalanche mix (xmur3-style) so short labels that differ by one character
  // (e.g. "bug" / "bud") land far apart instead of adjacent hues.
  let hash = 1779033703 ^ label.length;
  for (let i = 0; i < label.length; i += 1) {
    hash = Math.imul(hash ^ label.charCodeAt(i), 3432918353);
    hash = (hash << 13) | (hash >>> 19);
  }
  hash = Math.imul(hash ^ (hash >>> 16), 2246822507);
  hash = Math.imul(hash ^ (hash >>> 13), 3266489909);
  let remaining = ((hash ^ (hash >>> 16)) >>> 0) % LABEL_HUE_SPAN;
  for (const [lo, hi] of LABEL_HUE_RANGES) {
    const span = hi - lo;
    if (remaining < span) return lo + remaining;
    remaining -= span;
  }
  /* istanbul ignore next -- unreachable: remaining < LABEL_HUE_SPAN by construction */
  return 20;
}

/**
 * Deterministic "H S% L%" HSL components for a label chip — a pure hash of
 * the label text, so the same label paints the same color on every render,
 * every card, and after a reload; there is no lookup table to keep in sync.
 * Consumers append their own alpha, e.g. `hsl(${labelColor(l)} / 0.4)`.
 */
export function labelColor(label: string): string {
  return `${String(hueFromLabel(label))} 58% 62%`;
}

/** Unique labels across a task list, sorted for a stable, scannable filter
 * dropdown. Each task's own array is already deduped by the server; this
 * dedups ACROSS tasks. */
export function uniqueLabels(tasks: readonly BoardTask[]): string[] {
  const set = new Set<string>();
  for (const t of tasks) for (const l of t.labels) set.add(l);
  return [...set].sort();
}

/** Board label-filter predicate: null/empty matches every task. */
export function matchesLabelFilter(task: BoardTask, filter: string | null): boolean {
  return filter === null || filter === '' || task.labels.includes(filter);
}

/** One entry in the label-filter `<select>` — `count` is how many currently-
 * loaded tasks carry it. */
export interface LabelFilterOption {
  readonly label: string;
  readonly count: number;
}

/**
 * Options for the board's label-filter dropdown, built so the `<select>` can
 * never hold a `value` that has no matching `<option>`. `uniqueLabels` alone
 * omits a `filter` that no task carries any more (a stale `?label=` from a
 * bookmark, or the last card carrying it just lost the label) — a controlled
 * select bound to that value then renders as if nothing were filtered while
 * the filter is still applied, making the board look broken instead of
 * filtered. Folding the orphaned filter in here, with `count: 0`, keeps the
 * dropdown and the applied filter permanently in agreement.
 */
export function labelFilterOptions(tasks: readonly BoardTask[], filter: string | null): LabelFilterOption[] {
  const counts = new Map<string, number>();
  for (const t of tasks) for (const l of t.labels) counts.set(l, (counts.get(l) ?? 0) + 1);
  if (filter !== null && filter !== '' && !counts.has(filter)) counts.set(filter, 0);
  return [...counts.keys()].sort().map((label) => ({ label, count: counts.get(label) ?? 0 }));
}
