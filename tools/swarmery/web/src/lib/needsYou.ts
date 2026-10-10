// Pure helpers for the "Needs you" queue (GET /api/needs-you; pages/NeedsYou.tsx).
// No React, no storage, no network — the page and useNeedsYou own those.

import type { NeedsYouItem, NeedsYouKind } from '../api/types';

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

const KIND_LABELS: Record<NeedsYouKind, string> = {
  approval: 'Approval',
  question: 'Question',
  prod_deploy_local: 'Confirm locally',
  awaiting_reply: 'Awaiting your reply',
  failed: 'Failed',
  manual_phase: 'Manual check',
};

export function kindLabel(k: NeedsYouKind): string {
  return KIND_LABELS[k];
}

/**
 * Identity of one blocking episode. A dismissed item comes back when the same
 * session blocks again: a new episode carries a new `blockingSince`. A
 * `manual_phase` item is keyed by its phase (its sessionId is always 0).
 */
export function dismissKey(item: NeedsYouItem): string {
  const who = item.phase !== undefined ? `phase-${String(item.phase.phaseId)}` : String(item.sessionId);
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
 * (approval < question < prod_deploy_local < awaiting_reply < failed) holds.
 */
export function oldestFirst(items: readonly NeedsYouItem[]): NeedsYouItem[] {
  const at = (it: NeedsYouItem): number => {
    const t = Date.parse(it.blockingSince);
    return Number.isNaN(t) ? Number.POSITIVE_INFINITY : t;
  };
  return [...items].sort((a, b) => at(a) - at(b));
}
