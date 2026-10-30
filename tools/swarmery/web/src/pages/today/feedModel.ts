// The Today feed (handoff §3) — pure, no React. What happened, newest first,
// in two groups: Today and Yesterday. There is no events endpoint: the feed is
// derived from what the page already fetches (Canvas v3 D3, plan D-5), so each
// source answers one kind of event:
//
//   needs-you queue → a session or a phase asked for the operator
//   sessions        → started, finished, failed, stopped, went quiet
//   epics           → a phase run started or ended
//   inbox           → a lesson, an Advisor finding, an agent change proposed
//
// A thing is told once. A session the queue already holds is its needs-you event,
// and a phase run's session is its phase event: both would otherwise say the same
// thing twice, one line apart.
//
// Every event carries `to`, the entity it opens. A session blocker opens the
// session, whose gate card decides it; a phase blocker opens the phase.

import { t } from '@lingui/core/macro';
import type { Epic, EpicPhase, NeedsYouItem, Session } from '../../api/types';
import type { GlyphState } from '../../components/place/StateGlyph';
import { addDays, isoDay, projectLabel } from '../../lib/format';
import { itemGlyph, itemKey } from '../../lib/needsYou';
import { planRowLabel, rowState } from '../../lib/sessionsView';
import { INBOX_TABS, type InboxItem } from '../inbox/inboxModel';
import { plansPath } from '../plans/plansUrl';

export interface FeedEvent {
  key: string;
  /** When it happened; the feed sorts on this. */
  at: string;
  state: GlyphState;
  title: string;
  summary: string;
  to: string;
}

export interface FeedLinks {
  session: (id: number) => string;
  needsYou: string;
  inbox: string;
}

export interface FeedInputs {
  sessions: readonly Session[];
  needs: readonly NeedsYouItem[];
  epics: readonly Epic[];
  inbox: readonly InboxItem[];
  links: FeedLinks;
  nowMs: number;
}

export interface FeedGroups {
  today: FeedEvent[];
  yesterday: FeedEvent[];
}

type ProjectLabel = (slug: string) => string;

const joinParts = (parts: readonly (string | null | undefined)[]): string =>
  parts.filter((p) => p != null && p !== '').join(' · ');

/** The links of a scope: null is All projects. */
export function feedLinks(slug: string | null, session: (id: number) => string): FeedLinks {
  const root = slug === null ? '' : `/p/${slug}`;
  return { session, needsYou: `${root}/needs-you`, inbox: `${root}/inbox` };
}

/** Where a blocker is answered: its session's gate, or its phase. */
export function blockerHref(item: NeedsYouItem, links: FeedLinks): string {
  if (item.sessionId > 0) return links.session(item.sessionId);
  if (item.phase === undefined || item.phase.planExternalId === '') return links.needsYou;
  return plansPath(item.projectSlug, {
    plan: item.phase.planExternalId,
    detail: { kind: 'phase', seq: item.phase.seq, tab: 'review' },
  });
}

function needsTitle(item: NeedsYouItem): string {
  const name = item.sessionName;
  const seq = String(item.phase?.seq ?? 0);
  switch (item.kind) {
    case 'approval':
      return t`${name} asked for a permission`;
    case 'prod_deploy_local':
      return t`${name} asked for a destructive permission`;
    case 'question':
      return t`${name} asked a question`;
    case 'awaiting_reply':
      return t`${name} is waiting for your reply`;
    case 'failed':
      return t`${name} failed`;
    case 'phase_decision': {
      const decision = item.decision?.decisionId ?? '';
      return decision === '' ? t`Guard stopped phase ${seq}` : t`Guard stopped phase ${seq} · ${decision}`;
    }
    case 'stalled_phase':
      return t`Phase ${seq} stalled`;
    case 'manual_phase':
      return t`Phase ${seq} needs a manual check`;
    case 'awaiting_merge':
      return t`Phase ${seq} is waiting to merge`;
    case 'worktree_cleanup':
      return t`Worktrees to clean up in ${name}`;
  }
}

/** The feed tells what happened, so a blocker's own kind glyph gives way: a
 * session asking reads as `needs`, a guard stop as the failure it was. */
function needsState(item: NeedsYouItem): GlyphState {
  if (item.kind === 'phase_decision') return 'failed';
  const glyph = itemGlyph(item.kind);
  return glyph === 'permission' || glyph === 'question' ? 'needs' : glyph;
}

function needsEvent(item: NeedsYouItem, project: ProjectLabel, links: FeedLinks): FeedEvent {
  const detail = item.decision?.trigger ?? (item.preview !== '' ? item.preview : item.question);
  return {
    key: `needs:${itemKey(item)}`,
    at: item.blockingSince,
    state: needsState(item),
    title: needsTitle(item),
    summary: joinParts([project(item.projectSlug), detail]),
    to: blockerHref(item, links),
  };
}

function sessionEvent(s: Session, nowMs: number, project: ProjectLabel, links: FeedLinks): FeedEvent {
  const state = rowState(s, nowMs);
  const name = s.planGroup != null ? planRowLabel(s) : (s.title ?? t`Untitled session`);
  const base = {
    key: `session:${String(s.id)}`,
    at: s.endedAt ?? s.startedAt,
    state,
    summary: joinParts([project(s.projectSlug), s.gitBranch]),
    to: links.session(s.id),
  };
  switch (state) {
    case 'working':
      return { ...base, at: s.startedAt, title: t`${name} session started` };
    case 'needs':
      return { ...base, title: t`${name} is waiting on you` };
    case 'pending':
      return { ...base, title: t`${name} went quiet` };
    case 'failed':
      return { ...base, title: t`${name} failed` };
    case 'stopped':
      return { ...base, title: t`${name} was stopped` };
    case 'done':
      return { ...base, title: t`${name} finished` };
  }
}

function phaseEvent(e: Epic, p: EpicPhase, project: ProjectLabel): FeedEvent | null {
  const seq = String(p.seq);
  const plan = e.title;
  const base = {
    key: `phase:${String(p.id)}`,
    summary: joinParts([project(e.projectSlug), p.name]),
    to: plansPath(e.projectSlug, { plan: e.externalId, detail: { kind: 'phase', seq: p.seq, tab: 'runs' } }),
  };
  if (p.runState === 'running' && p.runStartedAt !== null) {
    return { ...base, at: p.runStartedAt, state: 'working', title: t`Phase ${seq} of ${plan} started` };
  }
  if (p.runEndedAt === null) return null;
  const at = p.runEndedAt;
  switch (p.runOutcome) {
    case 'completed':
      return { ...base, at, state: 'done', title: t`Phase ${seq} of ${plan} finished` };
    case 'partial':
      return { ...base, at, state: 'stopped', title: t`Phase ${seq} of ${plan} finished in part` };
    case 'noop':
      return { ...base, at, state: 'stopped', title: t`Phase ${seq} of ${plan} stopped with nothing done` };
    case 'failed':
      return { ...base, at, state: 'failed', title: t`Phase ${seq} of ${plan} failed` };
    default:
      return null;
  }
}

function inboxTitle(item: InboxItem): string | null {
  const { title } = item;
  switch (item.kind) {
    case 'lesson':
      return t`Lesson proposed: ${title}`;
    case 'advisor':
      return t`Advisor noticed: ${title}`;
    case 'proposal':
      return t`Agent change proposed: ${title}`;
    case 'retire':
      return t`Lesson may have stopped helping: ${title}`;
    case 'alert':
    case 'review':
      return title;
    // Approvals are the needs-you queue's; the classifier's queue is a backlog of
    // old sessions to label, nothing that happened today.
    default:
      return null;
  }
}

function inboxEvent(item: InboxItem, links: FeedLinks): FeedEvent | null {
  const title = inboxTitle(item);
  if (title === null) return null;
  const tab = INBOX_TABS.find((x) => x.kind === item.kind)?.id ?? 'all';
  return {
    key: `inbox:${item.key}`,
    at: item.ageIso,
    state: item.kind === 'alert' ? 'failed' : 'done',
    title,
    summary: item.context,
    to: `${links.inbox}?tab=${tab}`,
  };
}

const notNull = <T,>(v: T | null): v is T => v !== null;

/** Every event the sources hold, newest first. */
export function buildFeed({ sessions, needs, epics, inbox, links, nowMs }: FeedInputs): FeedEvent[] {
  const names = new Map(sessions.map((s) => [s.projectSlug, s.projectName ?? null]));
  const project: ProjectLabel = (slug) => projectLabel(names.get(slug) ?? null, slug);
  const told = new Set(needs.map((n) => n.sessionId));
  const heldPhases = new Set(needs.flatMap((n) => (n.phase === undefined ? [] : [n.phase.phaseId])));
  const phaseRuns = new Set(epics.flatMap((e) => e.phases.map((p) => p.runSessionUuid)));
  const events = [
    ...needs.map((n) => needsEvent(n, project, links)),
    ...sessions
      .filter((s) => !told.has(s.id) && !phaseRuns.has(s.sessionUuid))
      .map((s) => sessionEvent(s, nowMs, project, links)),
    ...epics.flatMap((e) =>
      e.phases.filter((p) => !heldPhases.has(p.id)).map((p) => phaseEvent(e, p, project)),
    ),
    ...inbox.map((i) => inboxEvent(i, links)),
  ].filter(notNull);
  return events.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
}

/** The two days the feed shows; anything older is left to its own place. */
export function groupFeed(events: readonly FeedEvent[], nowMs: number): FeedGroups {
  const today = isoDay(new Date(nowMs));
  const yesterday = addDays(today, -1);
  const of = (day: string): FeedEvent[] => events.filter((e) => isoDay(new Date(e.at)) === day);
  return { today: of(today), yesterday: of(yesterday) };
}

/** The list's filter: the title or the summary. */
export function filterFeed(events: readonly FeedEvent[], query: string): FeedEvent[] {
  const q = query.trim().toLowerCase();
  if (q === '') return [...events];
  return events.filter((e) => e.title.toLowerCase().includes(q) || e.summary.toLowerCase().includes(q));
}
