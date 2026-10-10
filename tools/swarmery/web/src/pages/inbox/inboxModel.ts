// The Inbox model (Canvas v3 phase 3, artboards 1b/2b) — pure, no React.
//
// Seven sources each hold something that waits on the operator: pending
// approvals, lesson candidates, advisor recommendations, agent-change proposals,
// the classifier's label queue, lesson retirements, and alerts — the findings
// that mean work is stopped right now (an account the daemon has paused, or
// Claude Code's auto mode permission check not answering) — plus, eighth, the
// plan branch reviews not yet acknowledged. There is
// no inbox endpoint: the page fetches the lists (useInboxItems) and this
// module normalises them into one InboxItem shape, one sort order and one set of
// tabs.
//
// Wording follows the code → UI dictionary (lib/glossary.ts UI_TERMS): a
// context line says "off-plan", never the code name `surprise`.

import { plural, t } from '@lingui/core/macro';
import { ACCOUNT_BREAKER_RULE, AUTO_MODE_NO_VERDICT_RULE, type Alert } from '../../api/alerts';
import type { QueueItem } from '../../api/decisions';
import type { TriageVerdict } from '../../api/triage';
import type { Lesson, RetireReason, RetirementProposal } from '../../api/lessons';
import { countFindings, type Review, type ReviewVerdict } from '../../api/reviews';
import type { AgentChangeProposal, PermissionRequest, Recommendation } from '../../api/types';
import { questionsOf, requestSummary } from '../../lib/approvals';
import { UI_TERMS } from '../../lib/glossary';

export type InboxKind =
  | 'approval'
  | 'lesson'
  | 'advisor'
  | 'proposal'
  | 'classifier'
  | 'retire'
  | 'alert'
  | 'review';

export const INBOX_KINDS: readonly InboxKind[] = [
  'approval',
  'lesson',
  'advisor',
  'proposal',
  'classifier',
  'retire',
  'alert',
  'review',
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
    | { kind: 'review'; raw: Review }
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
  | 'reviews'
  | 'handled';

/** Tab order and wording follow 1b/2b; `kind` is what the tab filters to.
 * Labels are getters so each read goes through the active locale. */
export const INBOX_TABS: readonly { id: InboxTabId; label: string; kind?: InboxKind }[] = [
  {
    id: 'all',
    get label() {
      return t`all`;
    },
  },
  {
    id: 'approvals',
    get label() {
      return t`approvals`;
    },
    kind: 'approval',
  },
  {
    id: 'lessons',
    get label() {
      return t`lessons`;
    },
    kind: 'lesson',
  },
  {
    id: 'advisor',
    get label() {
      return t`advisor`;
    },
    kind: 'advisor',
  },
  {
    id: 'proposals',
    get label() {
      return t`proposals`;
    },
    kind: 'proposal',
  },
  {
    id: 'classifier',
    get label() {
      return t`classifier`;
    },
    kind: 'classifier',
  },
  {
    id: 'retire',
    get label() {
      return t`stop using a lesson?`;
    },
    kind: 'retire',
  },
  {
    id: 'alerts',
    get label() {
      return t`alerts`;
    },
    kind: 'alert',
  },
  {
    id: 'reviews',
    get label() {
      return t`reviews`;
    },
    kind: 'review',
  },
  // Rows of this tab are triage verdicts, not items: a component renders them.
  {
    id: 'handled',
    get label() {
      return t`handled by agent`;
    },
  },
];

/** Per-kind presentation: label after the dot, and the dot / label colour token.
 * Labels are getters so each read goes through the active locale. */
export const KIND_META: Record<InboxKind, { label: string; dot: string; text: string }> = {
  approval: {
    get label() {
      return t`approval`;
    },
    dot: 'bg-amber',
    text: 'text-amber',
  },
  lesson: {
    get label() {
      return t`new lesson`;
    },
    dot: 'bg-purple',
    text: 'text-purple',
  },
  advisor: { label: UI_TERMS.recommendation.ui.replace('…', ''), dot: 'bg-blue', text: 'text-blue' },
  proposal: {
    get label() {
      return t`agent change`;
    },
    dot: 'bg-ink-dim',
    text: 'text-ink-dim',
  },
  classifier: {
    get label() {
      return t`check the classifier`;
    },
    dot: 'bg-green',
    text: 'text-green',
  },
  retire: {
    get label() {
      return t`stop using a lesson?`;
    },
    dot: 'bg-red',
    text: 'text-red',
  },
  alert: {
    get label() {
      return t`alert`;
    },
    dot: 'bg-red',
    text: 'text-red',
  },
  review: {
    get label() {
      return t`plan review`;
    },
    dot: 'bg-brand',
    text: 'text-brand',
  },
};

/** Plain-language retirement reasons (never the enum), read through the active locale. */
export const RETIRE_REASON_UI: Record<RetireReason, string> = {
  get ineffective() {
    return t`not helping`;
  },
  get stale() {
    return t`out of date`;
  },
  get unused_60d() {
    return t`unused for 60 days`;
  },
  get superseded() {
    return t`replaced by a newer lesson`;
  },
};

export interface InboxSources {
  approvals?: readonly PermissionRequest[] | undefined;
  lessons?: readonly Lesson[] | undefined;
  recommendations?: readonly Recommendation[] | undefined;
  proposals?: readonly AgentChangeProposal[] | undefined;
  classifier?: readonly QueueItem[] | undefined;
  retirements?: readonly RetirementProposal[] | undefined;
  alerts?: readonly Alert[] | undefined;
  reviews?: readonly Review[] | undefined;
}

function approvalItem(r: PermissionRequest): InboxItem {
  const questions = questionsOf(r);
  const title =
    questions !== null
      ? `${r.toolName} · ${plural(questions.length, { one: '# question', few: '# questions', many: '# questions', other: '# questions' })}`
      : `${r.toolName} · ${requestSummary(r)}`;
  const sessionId = String(r.sessionId);
  return {
    key: `approval:${String(r.id)}`,
    kind: 'approval',
    title,
    context: t`session ${sessionId}`,
    ageIso: r.requestedAt,
    urgent: r.status === 'pending',
    expiresIso: r.expiresAt,
    raw: r,
  };
}

function lessonItem(l: Lesson): InboxItem {
  const lessonTitle = l.title;
  return {
    key: `lesson:${String(l.id)}`,
    kind: 'lesson',
    title: t`New lesson: ${lessonTitle}`,
    context: l.phaseName === '' ? t`from a phase run` : l.phaseName,
    ageIso: l.createdAt,
    urgent: false,
    raw: l,
  };
}

function advisorItem(r: Recommendation): InboxItem {
  const recommendationTitle = r.title;
  return {
    key: `advisor:${String(r.id)}`,
    kind: 'advisor',
    title: t`Advisor: ${recommendationTitle}`,
    context: `${r.rule} · ${r.target}`,
    ageIso: r.created_at,
    urgent: false,
    raw: r,
  };
}

function proposalItem(p: AgentChangeProposal): InboxItem {
  const path = p.target_path !== '' ? p.target_path : p.agent_path;
  const agent = p.agent;
  return {
    key: `proposal:${String(p.id)}`,
    kind: 'proposal',
    title: t`Change to ${agent}`,
    context: path === '' ? p.target_kind : path,
    ageIso: p.created_at,
    urgent: false,
    raw: p,
  };
}

function retireItem(p: RetirementProposal): InboxItem {
  const lessonTitle = p.title;
  return {
    key: `retire:${String(p.id)}`,
    kind: 'retire',
    title: t`Stop using “${lessonTitle}”?`,
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
    get title() {
      return t`Permission checks are getting no verdict`;
    },
    get context() {
      return t`Claude Code auto mode`;
    },
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
    title: account === null ? (ui?.title ?? a.message) : t`Account ${account} is paused`,
    context:
      account === null ? (ui?.context ?? a.target) : a.kind === 'quota' ? t`usage limit` : t`sign-in or access`,
    ageIso: a.openedAt ?? a.detectedAt,
    urgent: true,
    raw: a,
  };
  return a.resetsAt === undefined || a.resetsAt === '' ? base : { ...base, expiresIso: a.resetsAt };
}

/** The verdict in the operator's words — the card's badge and its context line. */
export const REVIEW_VERDICT_UI: Record<ReviewVerdict, string> = {
  pass: 'passed',
  fail: 'failed',
  inconclusive: 'inconclusive',
};

/** "2 findings" / "no findings" — P0/P1 lines counted client-side. */
export function findingsLabel(findings: string): string {
  const n = countFindings(findings);
  return n === 0 ? 'no findings' : `${String(n)} finding${n === 1 ? '' : 's'}`;
}

/**
 * A plan branch review waits until it is acknowledged; it blocks nothing, so it
 * is never urgent. It is dated by when the reviewer started.
 */
function reviewItem(r: Review): InboxItem {
  const plan = r.planTitle === '' ? `plan #${String(r.taskId)}` : r.planTitle;
  return {
    key: `review:${String(r.id)}`,
    kind: 'review',
    title: `Plan review: ${plan}`,
    context: `${REVIEW_VERDICT_UI[r.verdict] ?? r.verdict} · ${findingsLabel(r.findings)}`,
    ageIso: r.startedAt,
    urgent: false,
    ...(r.projectSlug === '' ? {} : { project: r.projectSlug }),
    raw: r,
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
      title: t`Check the classifier · ${name}`,
      context: plural(group.length, {
        one: '# question · grouped',
        few: '# questions · grouped',
        many: '# questions · grouped',
        other: '# questions · grouped',
      }),
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
    ...(src.reviews ?? []).map(reviewItem),
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

/**
 * The kinds the Inbox banner's run visits: the same set the banner counts, so the
 * two cannot drift apart. AGENT_KINDS is listed in the engine's kindOrder
 * (internal/triage/policy.go), which its insertion order carries over here.
 */
export const TRIAGE_INBOX_KINDS: readonly string[] = [...AGENT_KINDS];

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

/** The agent's verdict value in the operator's words; an unknown value stays raw. */
export function valueWording(value: string): string {
  switch (value) {
    case 'accept':
      return t`accept`;
    case 'not-useful':
      return t`not useful`;
    case 'stop':
      return t`stop using it`;
    case 'keep':
      return t`keep it`;
    case 'dismiss':
      return t`dismiss`;
    case 'track':
      return t`track this`;
    case 'fix-card':
      return t`open a fix task`;
    case 'improve':
      return t`draft a change`;
    default:
      return value;
  }
}

/** The action a suggestion performs, in the button's words ("mark not useful"). */
export function suggestionActionLabel(value: string): string {
  switch (value) {
    case 'accept':
      return t`accept`;
    case 'not-useful':
      return t`mark not useful`;
    case 'stop':
      return t`stop using it`;
    case 'keep':
      return t`keep it`;
    case 'dismiss':
      return t`dismiss`;
    case 'track':
      return t`track this`;
    case 'improve':
      return t`draft a change`;
    case 'fix-card':
      return t`open the fix task`;
    default:
      return value;
  }
}

/** The primary button's text for an agent suggestion: the action, then whose idea it is. */
export function suggestionButtonLabel(value: string): string {
  const action = suggestionActionLabel(value);
  return t`${action} · agent's suggestion`;
}

/** What pressing `e` does when the agent left a (non-sample) suggestion. Null for an unknown value. */
export function suggestionConsequence(item: InboxItem, value: string): string | null {
  switch (value) {
    case 'accept': {
      if (item.kind !== 'lesson') return null;
      const areas = item.raw.areaGlobs.length > 0 ? item.raw.areaGlobs.join(', ') : t`these areas`;
      return t`Every future run touching ${areas} gets this sentence in its brief. We then watch whether those runs land closer to plan.`;
    }
    case 'not-useful':
      return t`The candidate is closed as not useful. Nothing is added to any brief.`;
    case 'stop':
      return t`The lesson stops appearing in briefs. Its history stays.`;
    case 'keep':
      return t`The proposal closes and this reason is held off for 30 days. The lesson stays in use.`;
    case 'dismiss':
      return t`The recommendation is closed. It reopens on its own if the rule keeps firing.`;
    case 'track':
      return t`We snapshot today's number as the baseline and tell you in a week whether it moved.`;
    case 'improve':
      return t`An agent drafts a change to its instructions. Nothing is applied: the draft comes back to this Inbox for your approval.`;
    case 'fix-card':
      return t`A task is created on the project's board with the text below, and an agent picks it up.`;
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
  if (items.length === 0) return t`nothing waiting`;
  const oldest = items.reduce((min, i) => (i.ageIso < min ? i.ageIso : min), (items[0] as InboxItem).ageIso);
  const count = String(items.length);
  const age = ageLabel(oldest, now);
  return t`${count} waiting · oldest ${age}`;
}
