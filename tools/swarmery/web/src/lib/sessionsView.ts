// Sessions page view model — the whole arrangement decision, as pure functions
// over the loaded session window. No React, no DOM: the page renders whatever
// this module returns, and the vitest suite next to it locks the
// rules the UI used to bury inside JSX.
//
// The contract the page relies on:
//   • the list is three groups (handoff §5): Needs you, Working, Done today, and
//     an Earlier group under them so history stays reachable (buildGroups);
//   • each row is ONE sentence of state (rowSummary), and the detail repeats
//     the state nowhere below its own sentence (stateSentence);
//   • the Plan runs group keeps the fan-out audit view (buildDays, runsOnly);
//   • a plan run is ONE item, positioned by its NEWEST row, merged by taskId —
//     so an infinite-scroll page boundary that splits a fan-out re-buckets into
//     the same single run instead of duplicating it;
//   • status filtering happens IN the grouping pass: a run card is kept whole
//     when ANY of its rows matches, so a filter never splits a fan-out;
//   • no row ever falls back to `session.title` for a plan row — every plan
//     session opens with the same boilerplate controller/phase prompt, so the
//     title tells them apart from nothing.

import { i18n, type MessageDescriptor } from '@lingui/core';
import { msg, t } from '@lingui/core/macro';
import type { NeedsYouItem, NeedsYouKind, Session, SessionStatus } from '../api/types';
import { fmtDate } from './format';
import { sessionState } from './sessionState';

export const STATUSES: SessionStatus[] = [
  'active',
  'waiting_approval',
  'awaiting_reply',
  'idle',
  'completed',
  'killed',
];

/** Status words — getters, so each read is in the active locale. */
export const STATUS_LABELS: Readonly<Record<SessionStatus, string>> = {
  get active() {
    return t`active`;
  },
  get waiting_approval() {
    return t`waiting`;
  },
  get awaiting_reply() {
    return t`awaiting reply`;
  },
  get idle() {
    return t`idle`;
  },
  get completed() {
    return t`done`;
  },
  get killed() {
    return t`killed`;
  },
};

/** Case-insensitive substring match over title / project / slug / branch, plus
 * the plan run's own title and phase name — a plan row's `title` is boilerplate,
 * so without these two a search for the plan would match nothing that shows. */
export function matchesQuery(s: Session, q: string): boolean {
  if (q === '') return true;
  return [
    s.title,
    s.projectName,
    s.projectSlug,
    s.gitBranch,
    s.planGroup?.title,
    s.planGroup?.phaseName,
  ].some((v) => v != null && v.toLowerCase().includes(q));
}

/* ----- plan runs ---------------------------------------------------------- */

export interface PlanRun {
  taskId: number;
  title: string;
  /** Project of the run's newest row — the Plans page is project-scoped. */
  projectSlug: string;
  projectName: string | null;
  /** startedAt of the newest row: the run's position in the day timeline. */
  newestAt: string;
  rows: Session[];
}

/** Controller (0) → phases (1) → anything else the run pulled in (2). */
function planRank(s: Session): number {
  const role = s.planGroup?.role;
  if (role === 'controller') return 0;
  if (role === 'phase') return 1;
  return 2;
}

/** In-group order: controller first, then phases by seq, then start time desc. */
export function comparePlanRows(a: Session, b: Session): number {
  const ra = planRank(a);
  const rb = planRank(b);
  if (ra !== rb) return ra - rb;
  if (ra === 1) {
    // A phase with no seq sorts after the numbered ones rather than at 0.
    const sa = a.planGroup?.phaseSeq ?? Number.MAX_SAFE_INTEGER;
    const sb = b.planGroup?.phaseSeq ?? Number.MAX_SAFE_INTEGER;
    if (sa !== sb) return sa - sb;
  }
  return b.startedAt.localeCompare(a.startedAt);
}

/** A run is "running" while any row is live or blocked on an approval. */
export function runIsRunning(rows: readonly Session[]): boolean {
  return rows.some((r) => r.status === 'active' || r.status === 'waiting_approval');
}

/** Per-status tallies for a run header, in the page's chip vocabulary. */
export function statusSummary(rows: readonly Session[]): string {
  const counts: Record<SessionStatus, number> = {
    active: 0,
    waiting_approval: 0,
    awaiting_reply: 0,
    idle: 0,
    completed: 0,
    killed: 0,
  };
  for (const s of rows) counts[s.status] += 1;
  return STATUSES.filter((s) => counts[s] > 0)
    .map((s) => `${STATUS_LABELS[s]} ${String(counts[s])}`)
    .join(' · ');
}

/* ----- fan-out row identity ----------------------------------------------
 * Two columns instead of one headline: a mono ROLE TAG (`ctl` / `#5`) and a
 * plain-language title. Never the session's own title — see the header note. */

export const CONTROLLER_NOTE: MessageDescriptor = msg`dispatches phases in dependency order, merges results back`;

export function planRowTag(s: Session): string {
  const g = s.planGroup;
  if (g == null) return '—';
  if (g.role === 'controller') return 'ctl';
  return g.phaseSeq != null ? `#${String(g.phaseSeq)}` : '#?';
}

export function planRowTitle(s: Session): string {
  const g = s.planGroup;
  if (g == null) return t`session`;
  if (g.role === 'controller') return t`plan controller`;
  return g.phaseName ?? t`phase`;
}

const ORDINALS: readonly MessageDescriptor[] = [
  msg`first`,
  msg`second`,
  msg`third`,
  msg`fourth`,
  msg`fifth`,
  msg`sixth`,
];

function ordinal(n: number): string {
  const word = ORDINALS[n - 1];
  if (word !== undefined) return i18n._(word);
  const count = String(n);
  return t`attempt ${count}`;
}

/** Phase-retry key: the seq is the phase's identity within the run; fall back to
 * the phase id so unnumbered phases still collapse onto themselves. */
function retryKey(s: Session): string {
  const g = s.planGroup;
  if (g == null || g.role !== 'phase') return '';
  return g.phaseSeq != null ? `seq:${String(g.phaseSeq)}` : `id:${String(g.phaseId ?? -1)}`;
}

/**
 * Dim sub-lines for one run's rows, keyed by session id: the controller gets a
 * one-line "what this row even is", and a re-dispatched phase gets an attempt
 * note so two identical `#5` rows are not a mystery. Everything else gets none.
 */
export function planRowNotes(rows: readonly Session[]): Map<number, string> {
  const notes = new Map<number, string>();
  const attempts = new Map<string, Session[]>();
  for (const s of rows) {
    if (s.planGroup?.role === 'controller') {
      notes.set(s.id, i18n._(CONTROLLER_NOTE));
      continue;
    }
    const key = retryKey(s);
    if (key === '') continue;
    const list = attempts.get(key);
    if (list === undefined) attempts.set(key, [s]);
    else list.push(s);
  }
  for (const list of attempts.values()) {
    if (list.length < 2) continue;
    // Oldest first, so "second attempt" means the second one that ran.
    const ordered = [...list].sort((a, b) => a.startedAt.localeCompare(b.startedAt));
    ordered.forEach((s, i) => {
      if (i === 0) return;
      const prev = ordered[i - 1];
      const prevStatus = STATUS_LABELS[prev?.status ?? 'idle'];
      const tail =
        prev?.status === 'killed'
          ? t`the previous run exited with an error`
          : t`the previous run ended ${prevStatus}`;
      const nth = ordinal(i + 1);
      notes.set(s.id, t`${nth} attempt — ${tail}`);
    });
  }
  return notes;
}

/* ----- day timeline ------------------------------------------------------- */

export type DayItem =
  | { kind: 'run'; group: PlanRun }
  | { kind: 'session'; session: Session };

export interface DayBucket {
  label: string;
  items: DayItem[];
}

export function dayLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return t`unknown day`;
  const name = fmtDate(d, { weekday: 'short', month: 'short', day: 'numeric' }).toLowerCase();
  return d.toDateString() === new Date().toDateString() ? t`today · ${name}` : name;
}

function itemDate(it: DayItem): string {
  return it.kind === 'run' ? it.group.newestAt : it.session.startedAt;
}

function itemMatchesStatus(it: DayItem, status: SessionStatus | null): boolean {
  if (status === null) return true;
  // ANY-row rule: the card is the unit, so one matching row keeps the whole
  // fan-out. Pre-filtering rows out of the run would show a half-run instead.
  return it.kind === 'run'
    ? it.group.rows.some((r) => r.status === status)
    : it.session.status === status;
}

/**
 * The page's single arrangement pass. `sorted` MUST be newest-first (the page
 * sorts by startedAt desc): that is what makes each run's first-seen row its
 * newest, and what keeps the emitted day buckets contiguous.
 */
export function buildDays(
  sorted: readonly Session[],
  status: SessionStatus | null = null,
): DayBucket[] {
  const runs = new Map<number, PlanRun>();
  const items: DayItem[] = [];
  for (const s of sorted) {
    const g = s.planGroup;
    if (g == null) {
      items.push({ kind: 'session', session: s });
      continue;
    }
    const existing = runs.get(g.taskId);
    if (existing === undefined) {
      const run: PlanRun = {
        taskId: g.taskId,
        title: g.title,
        projectSlug: s.projectSlug,
        projectName: s.projectName ?? null,
        newestAt: s.startedAt,
        rows: [s],
      };
      runs.set(g.taskId, run);
      items.push({ kind: 'run', group: run });
    } else {
      // Merge by taskId — a page boundary that splits a fan-out must NOT
      // produce a second card for the same run.
      existing.rows.push(s);
    }
  }
  for (const run of runs.values()) run.rows.sort(comparePlanRows);

  const buckets: DayBucket[] = [];
  for (const it of items) {
    if (!itemMatchesStatus(it, status)) continue;
    const label = dayLabel(itemDate(it));
    const last = buckets[buckets.length - 1];
    if (last !== undefined && last.label === label) last.items.push(it);
    else buckets.push({ label, items: [it] });
  }
  return buckets;
}

/** Same buckets, runs only (the `plan runs` audit view). Empty days drop out. */
export function runsOnly(days: readonly DayBucket[]): DayBucket[] {
  return days
    .map((d) => ({ label: d.label, items: d.items.filter((it) => it.kind === 'run') }))
    .filter((d) => d.items.length > 0);
}

/** The bulk "continue phase" selection against what the page renders now. */
export interface LinkSelection {
  /** Flat session rows on screen whose link mod polls (they get a checkbox). */
  selectable: Session[];
  /** Selected ids among them, in selection order: what a send sends. */
  visible: number[];
  /** Selected ids a filter hides now: kept, never sent. */
  hidden: number;
  /** Selected ids still on screen whose link mod stopped polling: kept,
   * never sent. */
  notPolling: number;
}

export function linkSelection(days: readonly DayBucket[], selected: readonly number[]): LinkSelection {
  const shown = days.flatMap((d) => d.items.flatMap((it) => (it.kind === 'session' ? [it.session] : [])));
  const selectable = shown.filter((s) => s.link?.controllable === true);
  const shownIds = new Set(shown.map((s) => s.id));
  const ids = new Set(selectable.map((s) => s.id));
  const visible = selected.filter((id) => ids.has(id));
  const onScreen = selected.filter((id) => shownIds.has(id)).length;
  return { selectable, visible, hidden: selected.length - onScreen, notPolling: onScreen - visible.length };
}

/** How many session ROWS the given buckets render (run rows included). */
export function countSessions(days: readonly DayBucket[]): number {
  let n = 0;
  for (const d of days) {
    for (const it of d.items) n += it.kind === 'run' ? it.group.rows.length : 1;
  }
  return n;
}

export function countRuns(days: readonly DayBucket[]): number {
  let n = 0;
  for (const d of days) {
    for (const it of d.items) if (it.kind === 'run') n += 1;
  }
  return n;
}

/* ----- the groups of the list (handoff §5) -------------------------------- */

/** A row's state: a StateGlyph name. `pending` is a stuck session, quiet past
 * the stuck window: nothing is known to be working, and nothing has ended. */
export type RowState = 'needs' | 'working' | 'done' | 'failed' | 'stopped' | 'pending';

export type SessionGroup = 'needs' | 'working' | 'done' | 'earlier';

/** The blockers a session's detail can answer, which is what makes it `needs`. */
const GATE_KINDS: ReadonlySet<NeedsYouKind> = new Set(['approval', 'question', 'prod_deploy_local', 'awaiting_reply']);

/** The oldest gate of each session (the queue arrives oldest first). The list
 * cannot rely on `status` alone: a pending approval leaves it `active`. */
export function gatesBySession(items: readonly NeedsYouItem[]): Map<number, NeedsYouItem> {
  const gates = new Map<number, NeedsYouItem>();
  for (const item of items) {
    if (item.sessionId > 0 && GATE_KINDS.has(item.kind) && !gates.has(item.sessionId)) gates.set(item.sessionId, item);
  }
  return gates;
}

export function rowState(s: Session, nowMs: number, gate?: NeedsYouItem): RowState {
  if (gate !== undefined || s.status === 'waiting_approval' || s.status === 'awaiting_reply') return 'needs';
  const live = sessionState(s, nowMs);
  if (live === 'running') return 'working';
  if (live === 'stuck') return 'pending';
  if (s.outcome === 'abandoned') return 'stopped';
  // A killed row exited with an error (planRowNotes reads it the same way).
  if (s.outcome === 'fail' || s.status === 'killed') return 'failed';
  return 'done';
}

function sameDay(aMs: number, bMs: number): boolean {
  return new Date(aMs).toDateString() === new Date(bMs).toDateString();
}

export function groupOf(s: Session, state: RowState, nowMs: number): SessionGroup {
  if (state === 'needs') return 'needs';
  if (state === 'working' || state === 'pending') return 'working';
  return sameDay(new Date(s.endedAt ?? s.startedAt).getTime(), nowMs) ? 'done' : 'earlier';
}

export type SessionGroups = Record<SessionGroup, Session[]>;

/** Partitions a newest-first list; the order inside each group is kept. */
export function buildGroups(
  sorted: readonly Session[],
  nowMs: number,
  gates: ReadonlyMap<number, NeedsYouItem>,
): SessionGroups {
  const groups: SessionGroups = { needs: [], working: [], done: [], earlier: [] };
  for (const s of sorted) groups[groupOf(s, rowState(s, nowMs, gates.get(s.id)), nowMs)].push(s);
  return groups;
}

/** Compact span: 40s, 12m, 3h 43m, 2d. */
export function compactSpan(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000));
  if (sec < 60) return `${String(sec)}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${String(min)}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return min % 60 === 0 ? `${String(h)}h` : `${String(h)}h ${String(min % 60)}m`;
  return `${String(Math.floor(h / 24))}d`;
}

const spanSince = (iso: string, nowMs: number): number => nowMs - new Date(iso).getTime();

/** The row's age: how long it has blocked, run or sat finished. */
export function rowAge(s: Session, state: RowState, nowMs: number, gate?: NeedsYouItem): string {
  if (state === 'needs') return compactSpan(spanSince(gate?.blockingSince ?? s.endedAt ?? s.startedAt, nowMs));
  if (state === 'working' || state === 'pending') return compactSpan(spanSince(s.startedAt, nowMs));
  return compactSpan(spanSince(s.endedAt ?? s.startedAt, nowMs));
}

/** A plan row's title: the run, then the phase. Never the boilerplate prompt. */
export function planRowLabel(s: Session): string {
  const g = s.planGroup;
  const name = planRowTitle(s);
  return g == null ? name : `${g.title} · ${name}`;
}

function gateSummary(gate: NeedsYouItem): string {
  const { toolName, preview } = gate;
  if (gate.kind === 'approval' || gate.kind === 'prod_deploy_local') {
    return preview === '' ? t`Allow ${toolName}?` : t`Allow ${toolName}: ${preview}?`;
  }
  return preview !== '' ? preview : gate.question;
}

/** The row's one sentence. `now` is the live action line; a plan row never falls
 * back to `why`, which is its boilerplate dispatch prompt. */
export function rowSummary(s: Session, state: RowState, nowMs: number, now: string | null, gate?: NeedsYouItem): string {
  const plan = s.planGroup != null;
  const intent = plan ? null : (s.why ?? null);
  const place = [s.model, s.gitBranch].filter((v) => v !== null).join(' · ');
  switch (state) {
    case 'needs':
      if (gate !== undefined) return gateSummary(gate);
      return s.status === 'awaiting_reply' ? t`waiting for your reply` : t`waiting for your permission`;
    case 'working':
      return now ?? intent ?? place;
    case 'pending': {
      const quiet = compactSpan(spanSince(s.endedAt ?? s.startedAt, nowMs));
      return t`quiet ${quiet} · it may have died silently`;
    }
    default:
      return intent ?? place;
  }
}

export interface StateSentence {
  lead: string;
  rest: string;
}

/** Handoff §5: the state in one sentence, repeated nowhere below it. */
export function stateSentence(
  s: Session,
  state: RowState,
  nowMs: number,
  lastEventAt: string | null,
  now: string | null,
  gate?: NeedsYouItem,
): StateSentence {
  const plan = s.planGroup != null;
  const intent = plan ? null : (s.why ?? null);
  const elapsed = compactSpan(spanSince(s.startedAt, new Date(s.endedAt ?? nowMs).getTime()));
  switch (state) {
    case 'needs': {
      const waited = rowAge(s, state, nowMs, gate);
      const permission = gate?.kind === 'approval' || gate?.kind === 'prod_deploy_local' || s.status === 'waiting_approval';
      return {
        lead: permission ? t`Waiting for your permission` : t`Waiting for your answer`,
        rest: t`for ${waited} · the session does nothing until you decide.`,
      };
    }
    case 'working': {
      const ago = compactSpan(spanSince(lastEventAt ?? s.endedAt ?? s.startedAt, nowMs));
      const doing = now ?? intent;
      return {
        lead: plan ? t`Phase running` : t`Working`,
        rest: doing === null ? t`· last action ${ago} ago.` : t`· last action ${ago} ago · ${doing}.`,
      };
    }
    case 'pending': {
      const quiet = compactSpan(spanSince(s.endedAt ?? s.startedAt, nowMs));
      return { lead: t`Quiet`, rest: t`for ${quiet} · no transcript activity, it may have died silently.` };
    }
    case 'done':
      return { lead: t`Finished`, rest: intent === null ? t`in ${elapsed}.` : t`in ${elapsed} · ${intent}.` };
    case 'failed':
      return { lead: t`Failed`, rest: intent === null ? t`· the transcript stays.` : t`· ${intent}. The transcript stays.` };
    case 'stopped':
      return { lead: t`Stopped by you`, rest: intent === null ? '.' : t`· ${intent}.` };
  }
}

/* ----- the filter field (handoff: `filter · n:name s:state o:result`) ------ */

export interface ListFilter {
  text: string;
  name: string;
  state: string;
  outcome: string;
}

/** Splits `n:`, `s:` and `o:` tokens out of the field; the rest is plain text. */
export function parseListQuery(raw: string): ListFilter {
  const filter: ListFilter = { text: '', name: '', state: '', outcome: '' };
  const words: string[] = [];
  for (const word of raw.trim().toLowerCase().split(/\s+/)) {
    const token = /^([nso]):(.+)$/.exec(word);
    if (token === null) words.push(word);
    else if (token[1] === 'n') filter.name = token[2] ?? '';
    else if (token[1] === 's') filter.state = token[2] ?? '';
    else filter.outcome = token[2] ?? '';
  }
  filter.text = words.join(' ');
  return filter;
}

export function matchesFilter(s: Session, f: ListFilter, state: RowState): boolean {
  if (!matchesQuery(s, f.text)) return false;
  if (f.name !== '' && !`${s.title ?? ''} ${s.planGroup?.title ?? ''}`.toLowerCase().includes(f.name)) return false;
  if (f.state !== '') {
    const words = [state, s.status, STATUS_LABELS[s.status]];
    if (!words.some((w) => w.toLowerCase().includes(f.state))) return false;
  }
  return f.outcome === '' || (s.outcome ?? '').includes(f.outcome);
}
