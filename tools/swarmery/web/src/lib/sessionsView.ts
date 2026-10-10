// Sessions page view model — the whole arrangement decision, as pure functions
// over the loaded session window. No React, no DOM: the page renders whatever
// this module returns, and the vitest suite next to it locks the
// rules the UI used to bury inside JSX.
//
// The contract the page relies on:
//   • the ONLY top-level structure is the day timeline (dayLabel/buildDays);
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
import type { Session, SessionStatus } from '../api/types';
import { fmtDate } from './format';

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
