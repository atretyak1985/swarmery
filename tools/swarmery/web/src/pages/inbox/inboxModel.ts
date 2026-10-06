// The Inbox model (Canvas v3 phase 3, artboards 1b/2b) — pure, no React.
//
// Seven sources each hold something that waits on the operator: pending
// approvals, lesson candidates, advisor recommendations, agent-change proposals,
// the classifier's label queue, lesson retirements, and alerts — the findings
// that mean work is stopped right now (an account the daemon has paused, or
// Claude Code's auto mode permission check not answering). There is
// no inbox endpoint: the page fetches the seven lists (useInboxItems) and this
// module normalises them into one InboxItem shape, one sort order and one set of
// tabs.
//
// Wording follows the code → UI dictionary (lib/glossary.ts UI_TERMS): a
// context line says "off-plan", never the code name `surprise`.

import { ACCOUNT_BREAKER_RULE, AUTO_MODE_NO_VERDICT_RULE, type Alert } from '../../api/alerts';
import type { QueueItem } from '../../api/decisions';
import type { TriageVerdict } from '../../api/triage';
import type { Lesson, RetireReason, RetirementProposal } from '../../api/lessons';
import type { AgentChangeProposal, PermissionRequest, Recommendation } from '../../api/types';
import { questionsOf, requestSummary } from '../../lib/approvals';
import { UI_TERMS } from '../../lib/glossary';

export type InboxKind = 'approval' | 'lesson' | 'advisor' | 'proposal' | 'classifier' | 'retire' | 'alert';

export const INBOX_KINDS: readonly InboxKind[] = [
  'approval',
  'lesson',
  'advisor',
  'proposal',
  'classifier',
  'retire',
  'alert',
];

/** What the triage agent left on an item (attachSuggestions). */
export interface AgentSuggestion {
  verdictIds: number[];
  value: string;
  reason: string;
  /** Audit sample: the agent's label is shown, the operator still answers. */
  sample: boolean;
  /** classifier only: decision id → the agent's answer. */
  answers?: Record<number, string>;
  /** advisor fix-card: the task the agent wrote. */
  card?: { title: string; prompt: string };
}

interface InboxItemBase {
  /** Stable across refetches: `<kind>:<source id>`. */
  key: string;
  /** The triage agent's open suggestion for this item, if it left one. */
  suggestion?: AgentSuggestion;
  title: string;
  /** Mono context line under the title (list) / after the kind label (detail). */
  context: string;
  /** When the decision started waiting. */
  ageIso: string;
  /** Sorted first: pending approvals (they expire) and alerts (they stop work). */
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
    | { kind: 'alert'; raw: Alert }
  );

/** URL value of `?tab=` — plural words, as the redirects and 1b/2b say them. */
export type InboxTabId =
  | 'all'
  | 'approvals'
  | 'lessons'
  | 'advisor'
  | 'proposals'
  | 'classifier'
  | 'retire'
  | 'alerts'
  | 'handled';

/** Tab order and wording follow 1b/2b; `kind` is what the tab filters to. */
export const INBOX_TABS: readonly { id: InboxTabId; label: string; kind?: InboxKind }[] = [
  { id: 'all', label: 'all' },
  { id: 'approvals', label: 'approvals', kind: 'approval' },
  { id: 'lessons', label: 'lessons', kind: 'lesson' },
  { id: 'advisor', label: 'advisor', kind: 'advisor' },
  { id: 'proposals', label: 'proposals', kind: 'proposal' },
  { id: 'classifier', label: 'classifier', kind: 'classifier' },
  { id: 'retire', label: 'stop using a lesson?', kind: 'retire' },
  { id: 'alerts', label: 'alerts', kind: 'alert' },
  // Rows of this tab are triage verdicts, not items: a component renders them.
  { id: 'handled', label: 'handled by agent' },
];

/** Per-kind presentation: label after the dot, and the dot / label colour token. */
export const KIND_META: Record<InboxKind, { label: string; dot: string; text: string }> = {
  approval: { label: 'approval', dot: 'bg-amber', text: 'text-amber' },
  lesson: { label: 'new lesson', dot: 'bg-purple', text: 'text-purple' },
  advisor: { label: UI_TERMS.recommendation.ui.replace('…', ''), dot: 'bg-blue', text: 'text-blue' },
  proposal: { label: 'agent change', dot: 'bg-ink-dim', text: 'text-ink-dim' },
  classifier: { label: 'check the classifier', dot: 'bg-green', text: 'text-green' },
  retire: { label: 'stop using a lesson?', dot: 'bg-red', text: 'text-red' },
  alert: { label: 'alert', dot: 'bg-red', text: 'text-red' },
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
  alerts?: readonly Alert[] | undefined;
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

/** Whether an alert is an open account breaker — the one alert with an action. */
export function isAccountBreaker(a: Alert): a is Alert & { account: string } {
  return a.rule === ACCOUNT_BREAKER_RULE && a.account !== undefined && a.account !== '';
}

/**
 * Title and context line of an alert that has no action, by rule. The alert's
 * own sentence (`message`) is the detail's body, so the title stays short. A
 * rule missing here falls back to its message and target.
 */
const ALERT_RULE_UI: Readonly<Record<string, { title: string; context: string } | undefined>> = {
  [AUTO_MODE_NO_VERDICT_RULE]: {
    title: 'Permission checks are getting no verdict',
    context: 'Claude Code auto mode',
  },
};

/**
 * An alert is urgent by definition, whatever its severity: work is stopped
 * while it is open. A paused account's quota alert also expires — the breaker
 * closes itself at `resetsAt`.
 */
function alertItem(a: Alert): InboxItem {
  const account = isAccountBreaker(a) ? a.account : null;
  const ui = ALERT_RULE_UI[a.rule];
  const base = {
    key: `alert:${String(a.id)}`,
    kind: 'alert' as const,
    title: account === null ? (ui?.title ?? a.message) : `Account ${account} is paused`,
    context:
      account === null ? (ui?.context ?? a.target) : a.kind === 'quota' ? 'usage limit' : 'sign-in or access',
    ageIso: a.openedAt ?? a.detectedAt,
    urgent: true,
    raw: a,
  };
  return a.resetsAt === undefined || a.resetsAt === '' ? base : { ...base, expiresIso: a.resetsAt };
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
    ...(src.alerts ?? []).map(alertItem),
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

/** Item count per tab, `all` included; `handled` is the agent-closed count the caller owns. */
export function tabCounts(items: readonly InboxItem[], handled = 0): Record<InboxTabId, number> {
  const counts = {} as Record<InboxTabId, number>;
  for (const t of INBOX_TABS) {
    if (t.id === 'handled') counts[t.id] = handled;
    else counts[t.id] = t.kind === undefined ? items.length : items.filter((i) => i.kind === t.kind).length;
  }
  return counts;
}

/** The items a tab shows. `handled` lists verdicts, not items, so it has none here. */
export function filterTab(items: readonly InboxItem[], tab: InboxTabId): InboxItem[] {
  if (tab === 'handled') return [];
  const kind = INBOX_TABS.find((t) => t.id === tab)?.kind;
  return kind === undefined ? [...items] : items.filter((i) => i.kind === kind);
}

/** Kinds the triage agent can take on. */
export const AGENT_KINDS: ReadonlySet<InboxKind> = new Set<InboxKind>(['classifier', 'advisor', 'lesson', 'retire']);

/** An item the agent left alone this long is worth offering a run for. */
export const AGENT_WAIT_MS = 24 * 3600 * 1000;

function isOpenVerdict(v: TriageVerdict): boolean {
  return v.state === 'suggested' || v.state === 'sample';
}

function cardOf(v: TriageVerdict): { title: string; prompt: string } | undefined {
  const p = v.payload;
  if (v.value !== 'fix-card' || p === null) return undefined;
  const { title, prompt } = p;
  return typeof title === 'string' && typeof prompt === 'string' ? { title, prompt } : undefined;
}

function suggestionOf(vs: readonly TriageVerdict[]): AgentSuggestion | undefined {
  const first = vs[0];
  if (first === undefined) return undefined;
  const card = cardOf(first);
  return {
    verdictIds: vs.map((v) => v.id),
    value: first.value,
    reason: first.reason,
    sample: vs.every((v) => v.state === 'sample'),
    ...(card === undefined ? {} : { card }),
  };
}

/**
 * Pair the agent's open verdicts (`suggested` / `sample`) with the items they are
 * about. Matching is by kind + source id; a classifier verdict's `ref` is a
 * decision id and lands on the session group that holds that decision. Verdicts
 * in any other state attach nothing; an item without a verdict comes back as is.
 */
export function attachSuggestions(items: readonly InboxItem[], verdicts: readonly TriageVerdict[]): InboxItem[] {
  const open = verdicts.filter(isOpenVerdict);
  if (open.length === 0) return [...items];
  const byKindRef = new Map<string, TriageVerdict[]>();
  for (const v of open) {
    const k = `${v.kind}:${v.ref}`;
    const list = byKindRef.get(k);
    if (list === undefined) byKindRef.set(k, [v]);
    else list.push(v);
  }
  return items.map((item): InboxItem => {
    if (item.kind === 'classifier') {
      const matched: TriageVerdict[] = [];
      const answers: Record<number, string> = {};
      for (const q of item.raw) {
        for (const v of byKindRef.get(`classifier:${String(q.id)}`) ?? []) {
          matched.push(v);
          answers[q.id] = v.value;
        }
      }
      const s = suggestionOf(matched);
      return s === undefined ? item : { ...item, suggestion: { ...s, answers } };
    }
    const id =
      item.kind === 'lesson' || item.kind === 'advisor' || item.kind === 'retire' ? item.raw.id : undefined;
    if (id === undefined) return item;
    const s = suggestionOf(byKindRef.get(`${item.kind}:${String(id)}`) ?? []);
    return s === undefined ? item : { ...item, suggestion: s };
  });
}

const VALUE_WORDING: Readonly<Record<string, string | undefined>> = {
  accept: 'accept',
  'not-useful': 'not useful',
  stop: 'stop using it',
  keep: 'keep it',
  dismiss: 'dismiss',
  track: 'track this',
  'fix-card': 'open a fix task',
  improve: 'draft a change',
};

/** The agent's verdict value in the operator's words; an unknown value stays raw. */
export function valueWording(value: string): string {
  return VALUE_WORDING[value] ?? value;
}

const SUGGESTION_BUTTON: Readonly<Record<string, string | undefined>> = {
  accept: 'accept',
  'not-useful': 'mark not useful',
  stop: 'stop using it',
  keep: 'keep it',
  dismiss: 'dismiss',
  track: 'track this',
  improve: 'draft a change',
  'fix-card': 'open the fix task',
};

/** The action a suggestion performs, in the button's words ("mark not useful"). */
export function suggestionActionLabel(value: string): string {
  return SUGGESTION_BUTTON[value] ?? value;
}

/** The primary button's text for an agent suggestion: the action, then whose idea it is. */
export function suggestionButtonLabel(value: string): string {
  return `${suggestionActionLabel(value)} · agent's suggestion`;
}

/** What pressing `e` does when the agent left a (non-sample) suggestion. Null for an unknown value. */
export function suggestionConsequence(item: InboxItem, value: string): string | null {
  switch (value) {
    case 'accept': {
      if (item.kind !== 'lesson') return null;
      const areas = item.raw.areaGlobs.length > 0 ? item.raw.areaGlobs.join(', ') : 'these areas';
      return `Every future run touching ${areas} gets this sentence in its brief. We then watch whether those runs land closer to plan.`;
    }
    case 'not-useful':
      return 'The candidate is closed as not useful. Nothing is added to any brief.';
    case 'stop':
      return 'The lesson stops appearing in briefs. Its history stays.';
    case 'keep':
      return 'The proposal closes and this reason is held off for 30 days. The lesson stays in use.';
    case 'dismiss':
      return 'The recommendation is closed. It reopens on its own if the rule keeps firing.';
    case 'track':
      return "We snapshot today's number as the baseline and tell you in a week whether it moved.";
    case 'improve':
      return 'An agent drafts a change to its instructions. Nothing is applied: the draft comes back to this Inbox for your approval.';
    case 'fix-card':
      return 'A task is created on the project\'s board with the text below, and an agent picks it up.';
    default:
      return null;
  }
}

export interface SuggestionBreakdown {
  /** Open suggestions "accept all" will confirm (everything but fix tasks). */
  acceptable: TriageVerdict[];
  /** Open fix-task suggestions: never bulk-accepted. */
  fixTasks: number;
  /** "2 accept · 1 dismiss": counts per action, in first-seen order. */
  parts: string[];
}

/** Group the agent's open suggestions by what accepting each would do. */
export function suggestionBreakdown(open: readonly TriageVerdict[]): SuggestionBreakdown {
  const acceptable: TriageVerdict[] = [];
  const counts = new Map<string, number>();
  let fixTasks = 0;
  for (const v of open) {
    if (v.state !== 'suggested') continue;
    if (v.value === 'fix-card') {
      fixTasks += 1;
      continue;
    }
    acceptable.push(v);
    const word = valueWording(v.value);
    counts.set(word, (counts.get(word) ?? 0) + 1);
  }
  return { acceptable, fixTasks, parts: [...counts].map(([word, n]) => `${String(n)} ${word}`) };
}

export interface AgentOffer {
  /** Items the agent could take on and has not looked at. */
  agent: number;
  /** Items that need the operator, with or without an agent suggestion. */
  you: number;
  /** Offer a run: something agent-shaped has waited past AGENT_WAIT_MS. */
  show: boolean;
}

export function agentOffer(items: readonly InboxItem[], now: number): AgentOffer {
  const untouched = items.filter((i) => AGENT_KINDS.has(i.kind) && i.suggestion === undefined);
  const waited = untouched.some((i) => now - Date.parse(i.ageIso) > AGENT_WAIT_MS);
  return { agent: untouched.length, you: items.length - untouched.length, show: waited };
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
