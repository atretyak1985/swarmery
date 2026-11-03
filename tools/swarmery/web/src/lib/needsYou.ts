// Pure helpers for the "Needs you" queue (GET /api/needs-you; pages/NeedsYou.tsx).
// No React, no storage, no network — the page and useNeedsYou own those.

import { i18n, type MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import type { NeedsYouItem, NeedsYouKind } from '../api/types';
import type { GlyphState } from '../components/place/StateGlyph';

/** The quoted question is clipped so a pasted reply stays readable. */
export const REPLY_QUOTE_MAX = 300;

/**
 * The head of a copied reply: `[<session name>] Re: <question>` and a blank
 * line, ready for the operator to type the answer under. A phase-4 suggestion
 * question wins over the raw last paragraph; the preview is the last resort.
 */
export function replyPrefix(item: NeedsYouItem): string {
  const q = (item.suggestion?.question || item.question || item.preview).trim().slice(0, REPLY_QUOTE_MAX);
  return `[${item.sessionName}] Re: ${q}\n\n`;
}

/** The copied reply with one suggested option already filled in. */
export function replyWithOption(item: NeedsYouItem, option: string): string {
  return `${replyPrefix(item)}${option}`;
}

// The kind as a word (artboard 3b): the row's first line and the pane's eyebrow.
// No badge and no colour of its own; only a guard stop is red, and the page owns that.
const KIND_LABELS: Record<NeedsYouKind, MessageDescriptor> = {
  approval: msg`Permission`,
  stalled_phase: msg`Phase stalled`,
  question: msg`Question`,
  phase_decision: msg`Guard stopped the run`,
  prod_deploy_local: msg`Confirm in terminal`,
  awaiting_reply: msg`Awaiting reply`,
  failed: msg`Session failed`,
  manual_phase: msg`Check by hand`,
  awaiting_merge: msg`Awaiting merge`,
  worktree_cleanup: msg`Worktree cleanup`,
};

/** The kind's label in the active locale (looked up at call time). */
export function kindLabel(k: NeedsYouKind): string {
  return i18n._(KIND_LABELS[k]);
}

/** The one kind whose word is red: a run the guard stopped. */
export function isAlarmKind(k: NeedsYouKind): boolean {
  return k === 'phase_decision';
}

/** Kinds that are about a plan phase, so the phase names the row. */
const PHASE_KINDS: ReadonlySet<NeedsYouKind> = new Set<NeedsYouKind>([
  'stalled_phase',
  'manual_phase',
  'awaiting_merge',
  'phase_decision',
]);

const PHASE_PREFIX = /^\s*phase\s+\d+\s*[—–:.-]\s*/i;

/** `Phase 3 — Rollout` → `Rollout`; a repeated prefix goes too. Never empties a name. */
export function stripPhasePrefix(name: string): string {
  let rest = name.trim();
  while (PHASE_PREFIX.test(rest)) rest = rest.replace(PHASE_PREFIX, '');
  return rest === '' ? name.trim() : rest;
}

/**
 * The row's title. A phase kind is named by its phase, without the `Phase N —`
 * prefix: the plan and the phase number are in the detail's where line. Without a
 * phase block it falls back to the `<plan> · Phase N: <name>` tail of the session
 * name. Every other kind is its session (or, for a cleanup, its repository).
 */
export function itemTitle(item: NeedsYouItem): string {
  if (!PHASE_KINDS.has(item.kind)) return item.sessionName;
  const name = item.phase?.name ?? '';
  if (name.trim() !== '') return stripPhasePrefix(name);
  const cut = item.sessionName.lastIndexOf(' · ');
  const tail = cut < 0 ? item.sessionName : item.sessionName.slice(cut + 3);
  return PHASE_PREFIX.test(tail) ? stripPhasePrefix(tail) : item.sessionName;
}

/**
 * The row's grey hint. A guard decision says what stopped it in one word: the
 * stop's trigger, or how many protected paths the run touched; the reason waits in
 * the detail. Every other kind shows the queue's own preview.
 */
export function itemPreview(item: NeedsYouItem): string {
  const d = item.decision;
  if (item.kind !== 'phase_decision' || d === undefined) return item.preview;
  if (d.trigger !== undefined && d.trigger !== '') return d.trigger;
  const n = d.guard.protectedChanged?.length || d.paths?.length || 0;
  if (n > 0) {
    return plural(n, {
      one: '# protected path',
      few: '# protected paths',
      many: '# protected paths',
      other: '# protected paths',
    });
  }
  return item.preview;
}

/** Decide here, or open another screen and finish it there (handoff §3). */
export type NeedsYouGroupId = 'decide' | 'open';

const OPEN_ELSEWHERE: ReadonlySet<NeedsYouKind> = new Set<NeedsYouKind>([
  'manual_phase',
  'awaiting_merge',
  'stalled_phase',
  'failed',
]);

export interface NeedsYouGroup {
  id: NeedsYouGroupId;
  items: NeedsYouItem[];
}

/** The list's groups in order; a group with nothing in it is left out. */
export function groupItems(items: readonly NeedsYouItem[]): NeedsYouGroup[] {
  const groups: NeedsYouGroup[] = [
    { id: 'decide', items: items.filter((i) => !OPEN_ELSEWHERE.has(i.kind)) },
    { id: 'open', items: items.filter((i) => OPEN_ELSEWHERE.has(i.kind)) },
  ];
  return groups.filter((g) => g.items.length > 0);
}

const GLYPHS: Record<NeedsYouKind, GlyphState> = {
  approval: 'permission',
  stalled_phase: 'stopped',
  question: 'question',
  phase_decision: 'guard',
  prod_deploy_local: 'permission',
  awaiting_reply: 'question',
  failed: 'failed',
  manual_phase: 'manual',
  awaiting_merge: 'pending',
  worktree_cleanup: 'pending',
};

/** The list row's state glyph. */
export function itemGlyph(k: NeedsYouKind): GlyphState {
  return GLYPHS[k];
}

/** A row's identity in the list; one session can hold several requests at once. */
export function itemKey(item: NeedsYouItem): string {
  return `${dismissKey(item)}:${String(item.requestId)}`;
}

/** The list's filter: the session, the row's title or its one-line summary. */
export function filterItems(items: readonly NeedsYouItem[], query: string): NeedsYouItem[] {
  const q = query.trim().toLowerCase();
  if (q === '') return [...items];
  return items.filter((i) =>
    [i.sessionName, itemTitle(i), i.preview, itemPreview(i)].some((s) => s.toLowerCase().includes(q)),
  );
}

/**
 * Identity of one blocking episode. A dismissed item comes back when the same
 * session blocks again: a new episode carries a new `blockingSince`. A
 * `manual_phase` item is keyed by its phase and a `worktree_cleanup` item by
 * its proposal (the sessionId of both is always 0).
 */
export function dismissKey(item: NeedsYouItem): string {
  const who =
    item.phase !== undefined
      ? `phase-${String(item.phase.phaseId)}`
      : item.cleanup !== undefined
        ? `cleanup-${String(item.cleanup.id)}`
        : String(item.sessionId);
  return `${item.kind}:${who}:${item.blockingSince}`;
}

/** Items not dismissed by this browser. */
export function visibleItems(
  items: readonly NeedsYouItem[],
  dismissed: ReadonlySet<string>,
): NeedsYouItem[] {
  return items.filter((it) => !dismissed.has(dismissKey(it)));
}

/**
 * Oldest blocker first. Stable, so the server's same-instant tie-break
 * (approval < stalled_phase < question < prod_deploy_local < awaiting_reply <
 * failed < …) holds.
 */
export function oldestFirst(items: readonly NeedsYouItem[]): NeedsYouItem[] {
  const at = (it: NeedsYouItem): number => {
    const t = Date.parse(it.blockingSince);
    return Number.isNaN(t) ? Number.POSITIVE_INFINITY : t;
  };
  return [...items].sort((a, b) => at(a) - at(b));
}
