// Pure logic behind the System "Docs" section: the status badges of a parsed
// `# How to use` guide (tools/swarmery/docs/system-docs-format.md), the section
// a detail panel opens on, the missing-subsection notice, the file a guide
// belongs in, and the keyboard reducer of SectionTabs.
//
// No React and no DOM here on purpose: this is the half of the Docs section
// that can be asserted without a renderer, and docsSection.test.ts asserts all
// of it (`npx vitest run src/pages/system/docsSection.test.ts`).

import { t } from '@lingui/core/macro';
import type { SystemDocs } from '../../api/types';

/** The two sections a System detail panel can show. */
export type DocsSection = 'docs' | 'definition';

/** One pill in the Docs status row. */
export interface DocsBadge {
  /** Pill text. */
  readonly label: string;
  /** Semantic-token classes only — light and dark follow the app theme. */
  readonly tone: string;
  /** `data-tip` copy; null when the label needs no explanation. */
  readonly tip: string | null;
}

const TONE_GREEN = 'border-green/40 text-green';
const TONE_AMBER = 'border-amber/40 text-amber';
const TONE_RED = 'border-red/40 text-red';

/**
 * The badges a guide earns, in reading order: what it IS (its `docs.status`),
 * then what is WRONG with it (stale, duplicate).
 *
 * A list rather than one badge because the states genuinely overlap — a
 * reviewed guide can also be stale, and a duplicate block can carry any status.
 * Collapsing them to a single pill would hide one of two true findings.
 *
 * An absent guide earns nothing: the empty state already says everything, and
 * a pill over a guide that does not exist is noise.
 */
export function docsStatusTone(docs: SystemDocs): DocsBadge[] {
  if (!docs.present) return [];
  const out: DocsBadge[] = [];
  if (docs.status === 'reviewed') {
    out.push({
      label: t`reviewed`,
      tone: TONE_GREEN,
      // Deliberately does not claim a *human* read it. The 2026-08-06 wave was
      // reviewed by agents under operator direction, so a "a human has reviewed
      // this" tooltip would be false on all 134 items. The flag records that a
      // review happened and the guide was checked against the item it documents;
      // it does not record who performed it.
      tip: t`checked against the item it documents`,
    });
  } else if (docs.status === 'generated') {
    out.push({
      label: t`generated`,
      tone: TONE_AMBER,
      tip: t`written by the docs generator and not reviewed yet`,
    });
  } else if (docs.status !== '') {
    // §3 keeps an unknown docs.status verbatim so the UI can surface it rather
    // than silently normalising an author's typo into "generated".
    const status = docs.status;
    out.push({
      label: t`status: ${status}`,
      tone: TONE_AMBER,
      tip: t`unrecognised docs.status — the contract defines only generated and reviewed`,
    });
  }
  if (docs.stale) {
    out.push({
      label: t`stale`,
      tone: TONE_AMBER,
      tip: t`the item changed after this guide was written — docs.source_sha no longer matches the body`,
    });
  }
  if (docs.duplicate) {
    out.push({
      label: t`two How-to-use blocks`,
      tone: TONE_RED,
      tip: t`this file has two \`# How to use\` headings — the parser keeps the first and ignores the second`,
    });
  }
  return out;
}

/**
 * Which section a detail panel opens on: the guide when there is one, the
 * definition otherwise. Never open Docs onto an empty state by default — the
 * item's source is the more useful thing to show when no guide exists.
 */
export function defaultSection(docs: SystemDocs): DocsSection {
  return docs.present ? 'docs' : 'definition';
}

/**
 * The missing-required-subsections notice, or null when the guide is complete.
 * `missing` is the fixed §2 required set (never author text), already ordered
 * by the parser.
 */
export function missingLabel(missing: readonly string[]): string | null {
  if (missing.length === 0) return null;
  const list = missing.join(', ');
  return t`this guide is missing: ${list}`;
}

/**
 * The file a guide belongs in, from the registry path of an item.
 *
 * Agents and commands are registered by FILE path, skills by DIRECTORY path
 * (`skills.dir_path`), and a skill's guide lives in that directory's SKILL.md —
 * so the empty state can name a path the reader can actually open.
 */
export function guidePath(path: string, kind: 'agents' | 'skills' | 'commands'): string {
  if (kind !== 'skills') return path;
  if (path.toLowerCase().endsWith('.md')) return path;
  return `${path.replace(/\/+$/, '')}/SKILL.md`;
}

/**
 * Keyboard reducer of a tablist: the index the focus/selection moves to, or
 * null when the key is not ours (the caller then leaves the event alone).
 *
 * ArrowLeft/ArrowRight wrap around in both directions; Home/End jump to the
 * ends. `current` outside the list is treated as the first tab so an unknown
 * active value can still be navigated out of.
 */
export function nextTabIndex(key: string, current: number, count: number): number | null {
  if (count <= 0) return null;
  const from = current < 0 || current >= count ? 0 : current;
  switch (key) {
    case 'ArrowRight':
      return (from + 1) % count;
    case 'ArrowLeft':
      return (from - 1 + count) % count;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    default:
      return null;
  }
}

/**
 * Roving tabindex: exactly one tab of a tablist is in the tab order (the
 * active one), every other tab is reachable only by arrow key.
 *
 * When `active` is not in `tabs` the FIRST tab becomes the tabbable one. That
 * state is reachable — `?sec=` is user-editable and a tablist can be rendered
 * with an active value it does not contain — and without the fallback every
 * tab would be tabIndex=-1, which takes the whole tablist out of the tab order
 * and leaves it unreachable by keyboard. Matches nextTabIndex, which already
 * treats an out-of-range `current` as index 0.
 */
export function tabIndexOf(tab: string, active: string, tabs: readonly string[]): 0 | -1 {
  if (tabs.indexOf(active) < 0) return tab === tabs[0] ? 0 : -1;
  return tab === active ? 0 : -1;
}

/** Stable DOM ids tying a tab to the panel it controls (WAI-ARIA tabs). */
export function tabDomIds(idPrefix: string, tab: string): { tabId: string; panelId: string } {
  return { tabId: `${idPrefix}-tab-${tab}`, panelId: `${idPrefix}-panel-${tab}` };
}
