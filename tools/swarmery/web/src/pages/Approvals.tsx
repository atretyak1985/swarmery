// Approvals screen (design §3.2, phase 2): PENDING cards — tool name, the
// collapsed tool_input essential (expandable to the full hook stdin JSON),
// session attribution (lazy /api/sessions join), live "hangs Ns" age and
// expiry countdown against the 120 s window — with Approve / Deny (inline
// optional reason) / Open session actions. HISTORY below: terminal-status
// chips, resolved_via, relative time. Fed by GET /api/approvals?status= +
// WS permission_requested/permission_resolved (upsert by id — the client's
// own decision comes back over WS too; refetch reconciles races/409s).
//
// AskUserQuestion pending cards (hooks-protocol amendment 1, spike E12) swap
// the approve button for an answer form: per question a radio (single) /
// checkbox (multiSelect) option list plus an «own answer» free-text input;
// submit → {action:"answer"}. «answer in terminal →» is the {action:"terminal"}
// no-decision handoff — NOT a plain approve, which would resolve the questions
// unanswered (E12d). Unparseable questions fall back to the generic card.

import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type { ApprovalRule, PermissionRequest, PermissionRequestStatus, Project, Session, WSMessage } from '../api/types';
import {
  createApprovalRule,
  deleteApprovalRule,
  fetchApprovalRules,
  fetchApprovals,
  fetchProjects,
  fetchSessions,
  resolveApproval,
  toggleApprovalRule,
  type ApprovalAction,
} from '../api';
import {
  buildAnswers,
  EMPTY_DRAFT,
  fmtClock,
  questionsOf,
  requestJsonPretty,
  requestSummary,
  suggestRulePattern,
  type AnswerDraft,
  type AnswerMap,
  type ParsedQuestion,
} from '../lib/approvals';
import { fmtAgo } from '../lib/format';
import { useScope } from '../lib/scope';
import { applyPermissionMessage, useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { ProjectName } from '../components/ProjectName';

const HISTORY_LIMIT = 50;

/* ----- terminal-status chips (approved sage / denied danger / rest dim) ----- */

const APPROVAL_CHIP: Record<PermissionRequestStatus, string> = {
  pending: 'border-amber/40 text-amber',
  approved: 'border-green/40 text-green',
  denied: 'border-red/40 text-red',
  expired: 'border-line-strong text-ink-dim',
  resolved_elsewhere: 'border-line-strong text-ink-dim',
};

const APPROVAL_LABEL: Record<PermissionRequestStatus, string> = {
  pending: 'pending',
  approved: 'approved',
  denied: 'denied',
  expired: 'expired',
  resolved_elsewhere: 'elsewhere',
};

/* ----- optimistic status per action (the WS/200 row is authoritative) ----- */

const OPTIMISTIC_STATUS: Record<ApprovalAction, PermissionRequestStatus> = {
  approve: 'approved',
  deny: 'denied',
  answer: 'approved', // the daemon approves the row with the answer summary as reason
  terminal: 'resolved_elsewhere', // no-decision handoff — the shim fails open (E12d/E12e)
};

/* ----- session attribution (project + title when resolvable) ----- */

function SessionLabel({
  sessionId,
  session,
}: {
  sessionId: number;
  session: Session | null;
}): JSX.Element {
  if (session === null) return <>session #{String(sessionId)}</>;
  return (
    <>
      <ProjectName name={session.projectName} slug={session.projectSlug} />
      {session.title !== null ? ` · ${session.title}` : ''}
    </>
  );
}

/* ----- auto-approve rules (control-plane v2) ----- */

interface RuleDraft {
  projectId: number | null;
  toolPattern: string;
  note: string;
}

const EMPTY_RULE_DRAFT: RuleDraft = { projectId: null, toolPattern: '', note: '' };

function RuleRow({
  rule,
  busy,
  onToggle,
  onDelete,
}: {
  rule: ApprovalRule;
  busy: boolean;
  onToggle: () => void;
  onDelete: () => void;
}): JSX.Element {
  return (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-[15px] py-[9px]">
      <code
        className={`font-mono text-[12px] font-semibold ${
          rule.enabled ? 'text-ink' : 'text-ink-faint line-through'
        }`}
      >
        {rule.toolPattern}
      </code>
      <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim">
        {rule.projectSlug ?? 'all projects'}
      </span>
      {rule.note !== null && rule.note !== '' && (
        <span className="min-w-0 flex-1 basis-[140px] truncate text-[11.5px] text-ink-dim">
          {rule.note}
        </span>
      )}
      <span className="ml-auto font-mono text-[10px] whitespace-nowrap text-ink-faint">
        {fmtAgo(rule.createdAt)}
      </span>
      <button
        type="button"
        disabled={busy}
        onClick={onToggle}
        aria-pressed={rule.enabled}
        className={`rounded-lg border px-2.5 py-0.5 font-mono text-[10.5px] transition-colors disabled:opacity-50 ${
          rule.enabled
            ? 'border-green/40 text-green hover:bg-green/10'
            : 'border-line-strong text-ink-dim hover:bg-surface2'
        }`}
      >
        {rule.enabled ? 'on' : 'off'}
      </button>
      <button
        type="button"
        disabled={busy}
        onClick={onDelete}
        aria-label={`delete rule ${rule.toolPattern}`}
        className="rounded-lg border border-red/40 px-2.5 py-0.5 font-mono text-[10.5px] text-red transition-colors hover:bg-red/10 disabled:opacity-50"
      >
        delete
      </button>
    </div>
  );
}

function AddRuleForm({
  projects,
  draft,
  busy,
  error,
  onChange,
  onSubmit,
  onCancel,
}: {
  projects: Project[];
  draft: RuleDraft;
  busy: boolean;
  error: string | null;
  onChange: (next: RuleDraft) => void;
  onSubmit: () => void;
  onCancel: () => void;
}): JSX.Element {
  return (
    <form
      className="mt-2 flex flex-wrap items-center gap-2 rounded-xl border border-line bg-surface px-3 py-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <select
        value={draft.projectId === null ? '' : String(draft.projectId)}
        onChange={(e) =>
          onChange({ ...draft, projectId: e.target.value === '' ? null : Number(e.target.value) })
        }
        aria-label="rule project scope"
        className="rounded-lg border border-line bg-field px-2 py-[5px] font-mono text-[11.5px] text-ink outline-none focus:border-green/40"
      >
        <option value="">all projects</option>
        {projects.map((p) => (
          <option key={p.id} value={String(p.id)}>
            {p.name ?? p.slug}
          </option>
        ))}
      </select>
      <input
        type="text"
        value={draft.toolPattern}
        onChange={(e) => onChange({ ...draft, toolPattern: e.target.value })}
        placeholder="Tool or Tool(arg glob) — e.g. Read, Bash(git *)"
        aria-label="tool pattern"
        className="min-w-0 flex-1 basis-[180px] rounded-lg border border-line bg-field px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-green/40"
      />
      <input
        type="text"
        value={draft.note}
        onChange={(e) => onChange({ ...draft, note: e.target.value })}
        placeholder="note (optional)"
        aria-label="rule note"
        className="min-w-0 flex-1 basis-[120px] rounded-lg border border-line bg-field px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-green/40"
      />
      <button
        type="submit"
        disabled={busy || draft.toolPattern.trim() === ''}
        className="rounded-lg border border-green/45 bg-green/12 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
      >
        add rule
      </button>
      <button
        type="button"
        onClick={onCancel}
        className="rounded-lg border border-line-strong px-3 py-1.5 font-mono text-[11.5px] text-ink-3 transition-colors hover:bg-surface2"
      >
        cancel
      </button>
      <div className="basis-full font-mono text-[10px] leading-snug text-ink-faint">
        Bash patterns match the command PREFIX — `Bash(git *)` also matches `git status &amp;&amp;
        rm -rf /`. Keep rules narrow; auto-approvals stay in History (via rule).
      </div>
      {error !== null && <div className="basis-full text-[11.5px] text-red">{error}</div>}
    </form>
  );
}

/* ----- one pending card ----- */

const ACTION_BTN =
  'flex-1 rounded-lg border px-4 py-[7px] text-center font-mono text-[11.5px] transition-colors disabled:opacity-50 desk:flex-none';

/* ----- one AskUserQuestion question: options + «own answer» free text ----- */

function QuestionBlock({
  question,
  index,
  draft,
  group,
  onToggle,
  onFreeText,
}: {
  question: ParsedQuestion;
  index: number;
  draft: AnswerDraft;
  /** Radio/checkbox group namespace — unique per card and question. */
  group: string;
  onToggle: (label: string) => void;
  onFreeText: (text: string) => void;
}): JSX.Element {
  return (
    <fieldset className="rounded-[10px] border border-line px-3 py-2.5">
      <legend className="px-1 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        {question.header !== '' ? question.header : `question ${String(index + 1)}`}
        {question.multiSelect ? ' · multi' : ''}
      </legend>
      <div className="mt-[5px] text-[13px] leading-snug text-ink">{question.question}</div>
      <div className="mt-2 flex flex-col gap-[3px]">
        {question.options.map((opt) => (
          <label
            key={opt.label}
            className="flex min-h-11 cursor-pointer items-baseline gap-[9px] rounded-[7px] px-[7px] py-[5px] transition-colors hover:bg-surface2"
          >
            <input
              type={question.multiSelect ? 'checkbox' : 'radio'}
              name={group}
              checked={draft.selected.includes(opt.label)}
              onChange={() => onToggle(opt.label)}
              className="translate-y-px accent-green focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
            />
            <span className="font-mono text-[11.5px] whitespace-nowrap text-ink">{opt.label}</span>
            {opt.description !== '' && (
              <span className="min-w-0 flex-1 text-[11.5px] leading-snug text-ink-dim">
                {opt.description}
              </span>
            )}
          </label>
        ))}
      </div>
      <input
        type="text"
        value={draft.freeText}
        onChange={(e) => onFreeText(e.target.value)}
        placeholder={
          question.multiSelect
            ? 'own answer — added to the selection'
            : 'own answer — overrides the selection'
        }
        aria-label={`own answer for “${question.question}”`}
        className="mt-1.5 w-full rounded-lg border border-line bg-field px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-green/40"
      />
    </fieldset>
  );
}

function PendingCard({
  request,
  session,
  nowMs,
  busy,
  onResolve,
  onAlwaysAllow,
}: {
  request: PermissionRequest;
  session: Session | null;
  /** Shared 1 s page ticker — one interval for all cards, not per-card timers. */
  nowMs: number;
  busy: boolean;
  onResolve: (action: ApprovalAction, reason?: string, answers?: AnswerMap) => void;
  onAlwaysAllow: () => void;
}): JSX.Element {
  const [expanded, setExpanded] = useState(false);
  const [denying, setDenying] = useState(false);
  const [reason, setReason] = useState('');

  // AskUserQuestion (hooks-protocol amendment 1): parseable questions swap the
  // approve button for the answer form; null falls back to the generic card.
  const questions = request.toolName === 'AskUserQuestion' ? questionsOf(request) : null;
  const [drafts, setDrafts] = useState<readonly AnswerDraft[]>(() =>
    (questions ?? []).map(() => EMPTY_DRAFT),
  );
  const answers = questions !== null ? buildAnswers(questions, drafts) : null;

  const updateDraft = (i: number, patch: Partial<AnswerDraft>): void => {
    setDrafts((prev) => prev.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  };
  const toggleOption = (i: number, q: ParsedQuestion, label: string): void => {
    const draft = drafts[i] ?? EMPTY_DRAFT;
    if (!q.multiSelect) {
      updateDraft(i, { selected: [label] });
      return;
    }
    updateDraft(i, {
      selected: draft.selected.includes(label)
        ? draft.selected.filter((l) => l !== label)
        : [...draft.selected, label],
    });
  };

  const hangSec = (nowMs - new Date(request.requestedAt).getTime()) / 1000;
  const expireSec = (new Date(request.expiresAt).getTime() - nowMs) / 1000;
  const sessionTo = `/sessions/${String(request.sessionId)}`;

  const submitDeny = (): void => {
    const trimmed = reason.trim();
    onResolve('deny', trimmed === '' ? undefined : trimmed);
  };

  return (
    <div className="mt-4 rounded-[14px] border border-amber/30 bg-surface px-[18px] py-4">
      <div className="flex flex-wrap items-center gap-2.5">
        <span
          className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-amber"
          aria-hidden="true"
        />
        <span className="font-mono text-[13px] font-bold text-ink">{request.toolName}</span>
        <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
          hangs {fmtClock(hangSec)} · {expireSec > 0 ? `expires ${fmtClock(expireSec)}` : 'expiring…'}
        </span>
      </div>

      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-label={expanded ? 'collapse request JSON' : 'expand request JSON'}
        className="mt-2.5 flex w-full items-start gap-1.5 rounded-lg border border-line bg-bg px-3 py-2 text-left transition-colors hover:border-line-strong"
      >
        <span aria-hidden="true" className="mt-px shrink-0 font-mono text-[10px] text-ink-dim">
          {expanded ? '▾' : '▸'}
        </span>
        <code
          className={`min-w-0 flex-1 font-mono text-[11.5px] text-ink-3 ${
            expanded ? 'break-all whitespace-pre-wrap' : 'block truncate whitespace-pre'
          }`}
        >
          {requestSummary(request)}
        </code>
      </button>
      {expanded && (
        <pre className="mt-1.5 max-h-72 overflow-y-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[10.5px] leading-relaxed break-all whitespace-pre-wrap text-ink-3">
          {requestJsonPretty(request)}
        </pre>
      )}

      <Link
        to={sessionTo}
        className="mt-2.5 flex items-center gap-[7px] font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
      >
        <span className="truncate"><SessionLabel sessionId={request.sessionId} session={session} /></span>
      </Link>

      {questions !== null && (
        <div className="mt-3 flex flex-col gap-2.5">
          {questions.map((q, i) => (
            <QuestionBlock
              key={q.question}
              question={q}
              index={i}
              draft={drafts[i] ?? EMPTY_DRAFT}
              group={`ask-${String(request.id)}-${String(i)}`}
              onToggle={(label) => toggleOption(i, q, label)}
              onFreeText={(text) => updateDraft(i, { freeText: text })}
            />
          ))}
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {questions !== null ? (
          <button
            type="button"
            disabled={busy || answers === null}
            onClick={() => {
              if (answers !== null) onResolve('answer', undefined, answers);
            }}
            className={`${ACTION_BTN} border-green/45 bg-green/12 font-bold text-green hover:bg-green/20`}
          >
            submit answers
          </button>
        ) : (
          <button
            type="button"
            disabled={busy}
            onClick={() => onResolve('approve')}
            className={`${ACTION_BTN} border-green/45 bg-green/12 font-bold text-green hover:bg-green/20`}
          >
            approve
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          aria-expanded={denying}
          onClick={() => setDenying((v) => !v)}
          className={`${ACTION_BTN} border-red/40 text-red hover:bg-red/10`}
        >
          deny{denying ? ' ▴' : ''}
        </button>
        {questions === null && (
          <button
            type="button"
            disabled={busy}
            onClick={onAlwaysAllow}
            title="pre-fill an auto-approve rule from this request (the rule catches future requests; this one still needs your decision)"
            className={`${ACTION_BTN} border-line-strong font-normal text-ink-3 hover:bg-surface2`}
          >
            always allow…
          </button>
        )}
        {questions !== null && (
          <button
            type="button"
            disabled={busy}
            onClick={() => onResolve('terminal')}
            title="release with no decision — the native selector renders in the terminal (E12d/E12e)"
            className={`${ACTION_BTN} border-line-strong font-normal text-ink-3 hover:bg-surface2`}
          >
            answer in terminal →
          </button>
        )}
        <Link
          to={sessionTo}
          className={`${ACTION_BTN} border-line-strong font-normal text-ink-3 hover:bg-surface2`}
        >
          open session →
        </Link>
      </div>

      {denying && (
        <form
          className="mt-2.5 flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            submitDeny();
          }}
        >
          <input
            type="text"
            autoFocus
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="reason (optional) — delivered to Claude verbatim"
            aria-label="deny reason"
            className="min-w-0 flex-1 basis-[200px] rounded-lg border border-line bg-field px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-red/40"
          />
          <button
            type="submit"
            disabled={busy}
            className="rounded-lg border border-red/40 bg-red/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20 disabled:opacity-50"
          >
            confirm deny
          </button>
        </form>
      )}
    </div>
  );
}

/* ----- one history row ----- */

function HistoryRow({
  request,
  session,
}: {
  request: PermissionRequest;
  session: Session | null;
}): JSX.Element {
  return (
    <Link
      to={`/sessions/${String(request.sessionId)}`}
      className="block px-[15px] py-[11px] transition-colors hover:bg-surface2"
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span
          className={`rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] whitespace-nowrap ${APPROVAL_CHIP[request.status]}`}
        >
          {APPROVAL_LABEL[request.status]}
        </span>
        <span className="font-mono text-[12px] font-semibold text-ink-2">{request.toolName}</span>
        <code className="min-w-0 flex-1 basis-[160px] truncate font-mono text-[11px] text-ink-faint">
          {requestSummary(request)}
        </code>
        {request.resolvedVia !== null && (
          <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim">
            via {request.resolvedVia}
          </span>
        )}
        <span className="font-mono text-[10px] whitespace-nowrap text-ink-faint">
          {fmtAgo(request.resolvedAt ?? request.requestedAt)}
        </span>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[12px] text-ink-dim">
        <span className="truncate font-mono text-[10.5px]"><SessionLabel sessionId={request.sessionId} session={session} /></span>
        {request.reason !== null && (
          <span className="min-w-0 [text-wrap:pretty]">reason: “{request.reason}”</span>
        )}
      </div>
    </Link>
  );
}

/* ----- screen ----- */

export function Approvals(): JSX.Element {
  const [requests, setRequests] = useState<PermissionRequest[] | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [nowMs, setNowMs] = useState(() => Date.now());
  const { scope } = useScope();

  // Global scope, applied client-side over the lazy sessions join. A request
  // whose session is not resolvable stays VISIBLE (safety: a pending approval
  // must never be silently hidden by a stale scope).
  const inScope = useCallback(
    (r: PermissionRequest): boolean => {
      if (scope === null) return true;
      const s = sessions?.find((x) => x.id === r.sessionId);
      return s === undefined || s.projectSlug === scope;
    },
    [scope, sessions],
  );

  // Auto-approve rules (control-plane v2).
  const [rules, setRules] = useState<ApprovalRule[] | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [ruleFormOpen, setRuleFormOpen] = useState(false);
  const [ruleDraft, setRuleDraft] = useState<RuleDraft>(EMPTY_RULE_DRAFT);
  const [ruleError, setRuleError] = useState<string | null>(null);
  const [ruleBusy, setRuleBusy] = useState(false);

  const loadRules = useCallback((): void => {
    fetchApprovalRules()
      .then(setRules)
      .catch(() => setRules([])); // rules API absent → empty section
  }, []);
  useEffect(loadRules, [loadRules]);

  // Project dropdown data — fetched once, when the form first opens.
  useEffect(() => {
    if (!ruleFormOpen || projects.length > 0) return;
    fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([]));
  }, [ruleFormOpen, projects.length]);

  const submitRule = (): void => {
    setRuleBusy(true);
    setRuleError(null);
    createApprovalRule({
      projectId: ruleDraft.projectId,
      toolPattern: ruleDraft.toolPattern.trim(),
      ...(ruleDraft.note.trim() !== '' ? { note: ruleDraft.note.trim() } : {}),
    })
      .then((rule) => {
        setRules((prev) => [rule, ...(prev ?? [])]);
        setRuleDraft(EMPTY_RULE_DRAFT);
        setRuleFormOpen(false);
      })
      .catch((e: unknown) => setRuleError(e instanceof Error ? e.message : String(e)))
      .finally(() => setRuleBusy(false));
  };

  const toggleRule = (rule: ApprovalRule): void => {
    setRuleBusy(true);
    toggleApprovalRule(rule.id, !rule.enabled)
      .then((updated) =>
        setRules((prev) => (prev ?? []).map((r) => (r.id === updated.id ? updated : r))),
      )
      .catch(loadRules)
      .finally(() => setRuleBusy(false));
  };

  const removeRule = (rule: ApprovalRule): void => {
    setRuleBusy(true);
    deleteApprovalRule(rule.id)
      .then(() => setRules((prev) => (prev ?? []).filter((r) => r.id !== rule.id)))
      .catch(loadRules)
      .finally(() => setRuleBusy(false));
  };

  const load = useCallback((): void => {
    // Pending + history in one page state; `resolved` is the terminal-status
    // meta-filter (limit 50 server-side — see web/CONTRACT-REQUESTS.md).
    Promise.all([fetchApprovals('pending'), fetchApprovals('resolved')])
      .then(([pending, resolved]) => {
        // De-dupe by id in case a request raced between the two fetches.
        const byId = new Map<number, PermissionRequest>();
        for (const r of [...pending, ...resolved]) byId.set(r.id, r);
        setRequests([...byId.values()]);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, []);

  useEffect(load, [load]);

  // Session attribution — fetched lazily, once, only when there is something
  // to attribute (plain "session #N" fallback until/unless it resolves).
  useEffect(() => {
    if (requests === null || requests.length === 0 || sessions !== null) return;
    fetchSessions()
      .then((page) => setSessions(page.sessions))
      .catch(() => setSessions([]));
  }, [requests, sessions]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type !== 'permission_requested' && msg.type !== 'permission_resolved') return;
    setRequests((prev) => (prev === null ? prev : applyPermissionMessage(prev, msg)));
  }, []);
  useLiveUpdates(onMessage, load);

  const pending = (requests ?? [])
    .filter((r) => r.status === 'pending')
    .filter(inScope)
    .sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)); // oldest (most urgent) first
  const history = (requests ?? [])
    .filter((r) => r.status !== 'pending')
    .filter(inScope)
    .sort((a, b) =>
      (b.resolvedAt ?? b.requestedAt).localeCompare(a.resolvedAt ?? a.requestedAt),
    )
    .slice(0, HISTORY_LIMIT); // newest first

  // One shared 1 s ticker for every live age/expiry clock on the page.
  const hasPending = pending.length > 0;
  useEffect(() => {
    if (!hasPending) return undefined;
    const t = setInterval(() => setNowMs(Date.now()), 1_000);
    return () => clearInterval(t);
  }, [hasPending]);

  const sessionOf = (id: number): Session | null =>
    sessions?.find((s) => s.id === id) ?? null;

  const alwaysAllow = (request: PermissionRequest): void => {
    setRuleDraft({
      projectId: sessionOf(request.sessionId)?.projectId ?? null,
      toolPattern: suggestRulePattern(request),
      note: `always allow — from request #${String(request.id)}`,
    });
    setRuleError(null);
    setRuleFormOpen(true);
  };

  // "resolved today" for the subline — local calendar day, derived from the
  // same `history` list (no extra fetch).
  const todayKey = new Date(nowMs).toDateString();
  const resolvedToday = history.filter(
    (r) => new Date(r.resolvedAt ?? r.requestedAt).toDateString() === todayKey,
  ).length;

  const resolve = (
    request: PermissionRequest,
    action: ApprovalAction,
    reason?: string,
    answers?: AnswerMap,
  ): void => {
    setBusyId(request.id);
    // Optimistic transfer to history; the WS permission_resolved for our own
    // decision (and the 200 body) upsert the authoritative row by id.
    const optimistic: PermissionRequest = {
      ...request,
      status: OPTIMISTIC_STATUS[action],
      resolvedAt: new Date().toISOString(),
      resolvedVia: 'dashboard',
      reason: reason ?? null,
    };
    setRequests((prev) =>
      prev === null ? prev : prev.map((r) => (r.id === request.id ? optimistic : r)),
    );
    resolveApproval(request.id, action, reason, answers)
      .then((updated) => {
        setRequests((prev) =>
          prev === null ? prev : prev.map((r) => (r.id === updated.id ? updated : r)),
        );
      })
      .catch(() => {
        // 409 (resolved elsewhere / expired first) or transport failure —
        // silent refetch; the server list is the truth.
        load();
      })
      .finally(() => setBusyId(null));
  };

  return (
    <div className="px-4 pb-10 desk:px-10 desk:pb-[60px]">
      {/* Header stays pinned; only the pending/history context below scrolls. */}
      <div className="sticky top-0 z-10 -mx-4 border-b border-line bg-bg px-4 pt-6 pb-3.5 desk:-mx-10 desk:px-10 desk:pt-[34px]">
        <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
          Waiting on you
        </h1>
        <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
          {requests !== null
            ? `${String(pending.length)} pending · ${String(resolvedToday)} resolved today · a pause is a feature, not a failure`
            : 'a pause is a feature, not a failure'}
        </div>
      </div>

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {requests === null && error === null && <Loading label="approvals…" />}

      {requests !== null && (
        <>
          {pending.length === 0 && (
            <Empty>
              no pending approvals — agents are running unattended.{' '}
              <span className="font-mono text-ink">permission_requested</span> pushes new ones here
              live
            </Empty>
          )}
          {pending.map((r) => (
            <PendingCard
              key={r.id}
              request={r}
              session={sessionOf(r.sessionId)}
              nowMs={nowMs}
              busy={busyId === r.id}
              onResolve={(action, reason, answers) => resolve(r, action, reason, answers)}
              onAlwaysAllow={() => alwaysAllow(r)}
            />
          ))}

          <div className="mt-[30px] flex items-center gap-3">
            <span className="font-mono text-[10.5px] tracking-[0.14em] text-ink-faint uppercase">
              Auto-approve rules
            </span>
            <span className="h-px flex-1 bg-line" aria-hidden="true" />
            <button
              type="button"
              onClick={() => {
                setRuleFormOpen((v) => !v);
                setRuleError(null);
              }}
              className="rounded-lg border border-line px-2.5 py-0.5 font-mono text-[10.5px] text-ink-3 transition-colors hover:bg-surface2"
            >
              {ruleFormOpen ? 'close' : '+ add rule'}
            </button>
          </div>
          {ruleFormOpen && (
            <AddRuleForm
              projects={projects}
              draft={ruleDraft}
              busy={ruleBusy}
              error={ruleError}
              onChange={setRuleDraft}
              onSubmit={submitRule}
              onCancel={() => setRuleFormOpen(false)}
            />
          )}
          {rules !== null && rules.length > 0 && (
            <div className="mt-2 divide-y divide-line-soft overflow-hidden rounded-xl border border-line">
              {rules.map((rule) => (
                <RuleRow
                  key={rule.id}
                  rule={rule}
                  busy={ruleBusy}
                  onToggle={() => toggleRule(rule)}
                  onDelete={() => removeRule(rule)}
                />
              ))}
            </div>
          )}
          {rules !== null && rules.length === 0 && !ruleFormOpen && (
            <Empty>
              no rules — matching requests auto-approve with{' '}
              <span className="font-mono text-ink">via rule</span> and stay in History for audit
            </Empty>
          )}

          <div className="mt-[30px] flex items-center gap-3">
            <span className="font-mono text-[10.5px] tracking-[0.14em] text-ink-faint uppercase">
              History
            </span>
            <span className="h-px flex-1 bg-line" aria-hidden="true" />
          </div>
          {history.length === 0 ? (
            <Empty>no decisions yet — resolved requests land here with their audit trail</Empty>
          ) : (
            <div className="mt-2 divide-y divide-line-soft overflow-hidden rounded-xl border border-line">
              {history.map((r) => (
                <HistoryRow key={r.id} request={r} session={sessionOf(r.sessionId)} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
