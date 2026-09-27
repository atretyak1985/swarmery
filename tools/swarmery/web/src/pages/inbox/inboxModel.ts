// The Inbox model (Canvas v3 phase 3, artboards 1b/2b) — pure, no React.
//
// Six sources each hold decisions that wait on the operator: pending approvals,
// lesson candidates, advisor recommendations, agent-change proposals, the
// classifier's label queue and lesson retirements. There is no inbox endpoint: the
// page fetches the six lists (useInboxItems) and this module normalises them
// into one InboxItem shape, one sort order and one set of tabs.
//
// Wording follows the code → UI dictionary (lib/glossary.ts UI_TERMS): a
// context line says "off-plan", never the code name `surprise`.

import type { QueueItem } from '../../api/decisions';
import type { Lesson, RetireReason, RetirementProposal } from '../../api/lessons';
import type { AgentChangeProposal, PermissionRequest, Recommendation } from '../../api/types';
import { questionsOf, requestSummary } from '../../lib/approvals';
import { UI_TERMS } from '../../lib/glossary';

export type InboxKind = 'approval' | 'lesson' | 'advisor' | 'proposal' | 'classifier' | 'retire';

export const INBOX_KINDS: readonly InboxKind[] = [
  'approval',
  'lesson',
  'advisor',
  'proposal',
  'classifier',
  'retire',
];

interface InboxItemBase {
  /** Stable across refetches: `<kind>:<source id>`. */
  key: string;
  title: string;
  /** Mono context line under the title (list) / after the kind label (detail). */
  context: string;
  /** When the decision started waiting. */
  ageIso: string;
  /** Pending approvals — the only thing here that expires. */
  urgent: boolean;
  expiresIso?: string;
  project?: string;
}

/** One decision. `raw` is the source row, typed per kind. */
export type InboxItem = InboxItemBase &
  (
    | { kind: 'approval'; raw: PermissionRequest }
    | { kind: 'lesson'; raw: Lesson }
    | { kind: 'advisor'; raw: Recommendation }
    | { kind: 'proposal'; raw: AgentChangeProposal }
    | { kind: 'classifier'; raw: readonly QueueItem[] }
    | { kind: 'retire'; raw: RetirementProposal }
  );

/** URL value of `?tab=` — plural words, as the redirects and 1b/2b say them. */
export type InboxTabId = 'all' | 'approvals' | 'lessons' | 'advisor' | 'proposals' | 'classifier' | 'retire';

/** Tab order and wording follow 1b/2b; `kind` is what the tab filters to. */
export const INBOX_TABS: readonly { id: InboxTabId; label: string; kind?: InboxKind }[] = [
  { id: 'all', label: 'all' },
  { id: 'approvals', label: 'approvals', kind: 'approval' },
  { id: 'lessons', label: 'lessons', kind: 'lesson' },
  { id: 'advisor', label: 'advisor', kind: 'advisor' },
  { id: 'proposals', label: 'proposals', kind: 'proposal' },
  { id: 'classifier', label: 'classifier', kind: 'classifier' },
  { id: 'retire', label: 'stop using a lesson?', kind: 'retire' },
];

/** Per-kind presentation: label after the dot, and the dot / label colour token. */
export const KIND_META: Record<InboxKind, { label: string; dot: string; text: string }> = {
  approval: { label: 'approval', dot: 'bg-amber', text: 'text-amber' },
  lesson: { label: 'new lesson', dot: 'bg-purple', text: 'text-purple' },
  advisor: { label: UI_TERMS.recommendation.ui.replace('…', ''), dot: 'bg-blue', text: 'text-blue' },
  proposal: { label: 'agent change', dot: 'bg-ink-dim', text: 'text-ink-dim' },
  classifier: { label: 'check the classifier', dot: 'bg-green', text: 'text-green' },
  retire: { label: 'stop using a lesson?', dot: 'bg-red', text: 'text-red' },
};

/** Plain-language retirement reasons (never the enum). */
export const RETIRE_REASON_UI: Record<RetireReason, string> = {
  ineffective: 'not helping',
  stale: 'out of date',
  unused_60d: 'unused for 60 days',
  superseded: 'replaced by a newer lesson',
};

export interface InboxSources {
  approvals?: readonly PermissionRequest[] | undefined;
  lessons?: readonly Lesson[] | undefined;
  recommendations?: readonly Recommendation[] | undefined;
  proposals?: readonly AgentChangeProposal[] | undefined;
  classifier?: readonly QueueItem[] | undefined;
  retirements?: readonly RetirementProposal[] | undefined;
}

function approvalItem(r: PermissionRequest): InboxItem {
  const questions = questionsOf(r);
  const title =
    questions !== null
      ? `${r.toolName} · ${String(questions.length)} question${questions.length === 1 ? '' : 's'}`
      : `${r.toolName} · ${requestSummary(r)}`;
  return {
    key: `approval:${String(r.id)}`,
    kind: 'approval',
    title,
    context: `session ${String(r.sessionId)}`,
    ageIso: r.requestedAt,
    urgent: r.status === 'pending',
    expiresIso: r.expiresAt,
    raw: r,
  };
}

function lessonItem(l: Lesson): InboxItem {
  return {
    key: `lesson:${String(l.id)}`,
    kind: 'lesson',
    title: `New lesson: ${l.title}`,
    context: l.phaseName === '' ? 'from a phase run' : l.phaseName,
    ageIso: l.createdAt,
    urgent: false,
    raw: l,
  };
}

function advisorItem(r: Recommendation): InboxItem {
  return {
    key: `advisor:${String(r.id)}`,
    kind: 'advisor',
    title: `Advisor: ${r.title}`,
    context: `${r.rule} · ${r.target}`,
    ageIso: r.created_at,
    urgent: false,
    raw: r,
  };
}

function proposalItem(p: AgentChangeProposal): InboxItem {
  const path = p.target_path !== '' ? p.target_path : p.agent_path;
  return {
    key: `proposal:${String(p.id)}`,
    kind: 'proposal',
    title: `Change to ${p.agent}`,
    context: path === '' ? p.target_kind : path,
    ageIso: p.created_at,
    urgent: false,
    raw: p,
  };
}

function retireItem(p: RetirementProposal): InboxItem {
  return {
    key: `retire:${String(p.id)}`,
    kind: 'retire',
    title: `Stop using “${p.title}”?`,
    context: RETIRE_REASON_UI[p.reason] ?? p.reason,
    ageIso: p.proposedAt,
    urgent: false,
    raw: p,
  };
}

/** One item per session: its questions are answered together. */
export function groupClassifier(queue: readonly QueueItem[]): InboxItem[] {
  const bySession = new Map<string, QueueItem[]>();
  for (const q of queue) {
    const list = bySession.get(q.sessionUuid);
    if (list === undefined) bySession.set(q.sessionUuid, [q]);
    else list.push(q);
  }
  return [...bySession.entries()].map(([uuid, group]) => {
    const first = group[0] as QueueItem;
    // The oldest question dates the group.
    const ageIso = group.reduce((min, q) => (q.createdAt < min ? q.createdAt : min), first.createdAt);
    const name = first.sessionTitle === '' ? uuid.slice(0, 8) : first.sessionTitle;
    return {
      key: `classifier:${uuid}`,
      kind: 'classifier',
      title: `Check the classifier · ${name}`,
      context: `${String(group.length)} question${group.length === 1 ? '' : 's'} · grouped`,
      ageIso,
      urgent: false,
      raw: group,
    };
  });
}

/** Normalise every available source; a missing source contributes nothing. */
export function toItems(src: InboxSources): InboxItem[] {
  return [
    ...(src.approvals ?? []).map(approvalItem),
    ...(src.lessons ?? []).map(lessonItem),
    ...(src.recommendations ?? []).map(advisorItem),
    ...(src.proposals ?? []).map(proposalItem),
    ...groupClassifier(src.classifier ?? []),
    ...(src.retirements ?? []).map(retireItem),
  ];
}

/**
 * Urgent first ("expires soon"), soonest expiry at the top; then the rest
 * ("when you have a minute") oldest-first, so nothing starves at the bottom.
 */
export function sortItems(items: readonly InboxItem[]): InboxItem[] {
  const urgent = items
    .filter((i) => i.urgent)
    .sort((a, b) => (a.expiresIso ?? a.ageIso).localeCompare(b.expiresIso ?? b.ageIso));
  const rest = items.filter((i) => !i.urgent).sort((a, b) => a.ageIso.localeCompare(b.ageIso));
  return [...urgent, ...rest];
}

/** Item count per tab, `all` included. */
export function tabCounts(items: readonly InboxItem[]): Record<InboxTabId, number> {
  const counts = {} as Record<InboxTabId, number>;
  for (const t of INBOX_TABS) {
    counts[t.id] = t.kind === undefined ? items.length : items.filter((i) => i.kind === t.kind).length;
  }
  return counts;
}

/** The items a tab shows. */
export function filterTab(items: readonly InboxItem[], tab: InboxTabId): InboxItem[] {
  const kind = INBOX_TABS.find((t) => t.id === tab)?.kind;
  return kind === undefined ? [...items] : items.filter((i) => i.kind === kind);
}

/** Compact age: "42 s", "12 min", "2 h", "1 d". */
export function ageLabel(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const sec = Math.max(0, Math.round((now - t) / 1000));
  if (sec < 60) return `${String(sec)} s`;
  if (sec < 3600) return `${String(Math.floor(sec / 60))} min`;
  if (sec < 86400) return `${String(Math.floor(sec / 3600))} h`;
  return `${String(Math.floor(sec / 86400))} d`;
}

/** Time left until `iso` as m:ss ("1:18"); "expired" once it has passed. */
export function expiresInLabel(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const sec = Math.round((t - now) / 1000);
  if (sec <= 0) return 'expired';
  if (sec >= 3600) return `${String(Math.floor(sec / 3600))} h`;
  return `${String(Math.floor(sec / 60))}:${String(sec % 60).padStart(2, '0')}`;
}

/** "9 waiting · oldest 1 d" — the header sub-line. */
export function waitingLine(items: readonly InboxItem[], now: number = Date.now()): string {
  if (items.length === 0) return 'nothing waiting';
  const oldest = items.reduce((min, i) => (i.ageIso < min ? i.ageIso : min), (items[0] as InboxItem).ageIso);
  return `${String(items.length)} waiting · oldest ${ageLabel(oldest, now)}`;
}
