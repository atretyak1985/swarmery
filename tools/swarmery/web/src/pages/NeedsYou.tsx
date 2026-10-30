// "Needs you" (handoff §1, §7): everything blocked on the operator, from
// GET /api/needs-you, live over the shared WS stream (lib/useNeedsYou). The list
// holds two groups, Decide here and Open & tick by hand; the detail is one
// DecisionCard, whatever the kind:
//
//   approval / question  allow, deny and always allow, or the question as a radio
//                        list; a prod-deploy request can only be denied here, the
//                        daemon refuses a remote approve. A plan phase stalled on
//                        the request adds «open phase»
//   stalled_phase        a link to the phase's Runs tab. Notification only: nothing
//                        here restarts it
//   prod_deploy_local    the terminal: no allow and no deny, the daemon hands a
//                        production deploy to the session's own terminal
//   awaiting_reply       the suggested replies as a radio list, sent to the session
//                        or copied as `[<session>] Re: <question>`; Dismiss hides
//                        this blocking episode in this browser
//   failed               a link to the session
//   manual_phase         a link to the phase's Criteria tab
//   awaiting_merge       a link to the phase's Review tab and, once open, the PR
//   phase_decision       a guard decision or an agent-declared stop: the options
//                        as a radio list, a note, and for a stop the operator's
//                        own words as answer `other`
//   worktree_cleanup     one repository's worktrees whose content is proven in
//                        main: a table with checkboxes, and "Remove selected"
//
// `e` and `x` run what the card's two buttons show; the card publishes them through
// a ref, the same way the session gate does.

import { plural, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
  type ReactNode,
} from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  applyWorktreeCleanup,
  createApprovalRule,
  fetchApprovals,
  resolveApproval,
  sendSessionMessage,
} from '../api';
import { answerGuardDecision, guardFindings } from '../api/guard';
import type {
  CleanupProofKind,
  NeedsYouCleanupCandidate,
  NeedsYouDecision,
  NeedsYouItem,
  PermissionRequest,
  WorktreeCleanupPathOutcome,
} from '../api/types';
import { ApprovalContext } from '../components/ApprovalContext';
import { ChannelBadge } from '../components/ChannelBadge';
import { DecisionCard, type DecisionCardProps } from '../components/place/DecisionCard';
import { ListGroup } from '../components/place/ListGroup';
import { ListPane } from '../components/place/ListPane';
import { ListRow } from '../components/place/ListRow';
import { PlaceLayout } from '../components/place/PlaceLayout';
import { StateGlyph } from '../components/place/StateGlyph';
import { QuestionBlock } from '../components/QuestionForm';
import { ErrorBox, Loading } from '../components/ui';
import {
  buildAnswers,
  EMPTY_DRAFT,
  questionsOf,
  requestJsonPretty,
  suggestRulePattern,
  type AnswerDraft,
  type ParsedQuestion,
} from '../lib/approvals';
import { projectLabel } from '../lib/format';
import {
  filterItems,
  groupItems,
  itemGlyph,
  itemKey,
  kindLabel,
  replyPrefix,
  replyWithOption,
} from '../lib/needsYou';
import { findProject } from '../lib/projectSlug';
import { safeHref } from '../lib/safeHref';
import { useScope } from '../lib/scope';
import { useSessionHref } from '../lib/sessionHref';
import { useNowMs } from '../lib/sessionState';
import { compactSpan } from '../lib/sessionsView';
import { stepKey } from '../lib/useGlobalKeys';
import { useNeedsYou } from '../lib/useNeedsYou';
import { gateTier } from './detail/SessionGate';
import type { PhaseTab } from './plans/PhasePanel';
import { plansPath } from './plans/plansUrl';

const COPIED_MS = 2_000;
/** The deep-link schemes a terminal's focus URL may use (Warp's WARP_FOCUS_URL). */
const TERM_FOCUS_SCHEMES: readonly string[] = ['warp:'];
/** The option id that lets a protected-path stop's run edit its paths. */
const ALLOW_OPTION = 'allow-add-to-scope';
const INLINE = 'font-mono text-[10.5px] text-ink-faint transition-colors hover:text-brand';

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false; // no clipboard (insecure context) or permission refused
  }
}

/** `e` and `x` for the open card: what its two buttons do right now. */
interface CardKeys {
  primary: (() => void) | undefined;
  secondary: (() => void) | undefined;
}

/** What every card takes from the page. */
interface CardContext {
  item: NeedsYouItem;
  /** The grey half of the eyebrow. */
  meta: string;
  sessionTo: string;
  /** Rules and history: Approvals keeps them. */
  manageTo: string;
  projectId: number | undefined;
  project: string;
  busy: boolean;
  /** Runs one write: the page owns busy, the error line and the refetch. */
  run: (work: () => Promise<unknown>, onOk?: (result: unknown) => void) => void;
  go: (to: string) => void;
  dismiss: () => void;
  keys: MutableRefObject<CardKeys>;
  nowMs: number;
}

/** A card's published `e` / `x`, renewed every render so they close over its state. */
function useCardKeys(ctx: CardContext, primary?: () => void, secondary?: () => void): void {
  const { keys, busy } = ctx;
  // A layout effect: the keys are published in the same commit as the card, not a
  // passive-effect tick later, when a window keydown could still see the old ones
  // (or none) — and a card that unmounts retires them in that commit too.
  useLayoutEffect(() => {
    keys.current = { primary: busy ? undefined : primary, secondary: busy ? undefined : secondary };
    // Once the card is gone its keys must not answer a request that left the queue.
    return () => {
      keys.current = { primary: undefined, secondary: undefined };
    };
  });
}

/** A phase tab of the item's plan. */
function phaseHref(item: NeedsYouItem, tab: PhaseTab): string | null {
  if (item.phase === undefined || item.phase.planExternalId === '') return null;
  return plansPath(item.projectSlug, {
    plan: item.phase.planExternalId,
    detail: { kind: 'phase', seq: item.phase.seq, tab },
  });
}

/** `open session →` as the card's link, for a session item. */
function sessionLink(ctx: CardContext, label: string): NonNullable<DecisionCardProps['link']> {
  return { label, onClick: () => ctx.go(ctx.sessionTo) };
}

/* ----- approval: allow, deny, always allow ----- */

function PermissionDecision({ ctx, request }: { ctx: CardContext; request: PermissionRequest | null }): JSX.Element {
  const { t } = useLingui();
  const { item, busy, run } = ctx;
  const [mode, setMode] = useState<'idle' | 'deny' | 'rule'>('idle');
  const [reason, setReason] = useState('');
  const [pattern, setPattern] = useState('');
  const [added, setAdded] = useState<string | null>(null);
  const [raw, setRaw] = useState(false);

  const requestId = item.requestId;
  const tool = item.toolName;
  const tier = gateTier(item, request);
  const localOnly = tier === 'destructive';
  const stallTo = item.stall !== undefined ? phaseHref(item, 'runs') : null;
  const projectId = ctx.projectId;
  const canRule = !localOnly && request !== null && projectId !== undefined;

  const resolve = (action: 'approve' | 'deny', note?: string): void => {
    if (requestId !== null) run(() => resolveApproval(requestId, action, note, undefined));
  };
  const deny = (): void => {
    if (mode !== 'deny') {
      setMode('deny');
      return;
    }
    const trimmed = reason.trim();
    resolve('deny', trimmed === '' ? undefined : trimmed);
  };
  const primary = (): void => {
    if (localOnly) deny();
    else resolve('approve');
  };
  // The rule catches later requests: this one still waits for an answer, so a
  // pattern gets a look before it is saved, as on the rules page.
  const alwaysAllow = (): void => {
    if (mode !== 'rule') {
      setPattern(request === null ? tool : suggestRulePattern(request));
      setMode('rule');
      return;
    }
    const toolPattern = pattern.trim();
    if (toolPattern === '' || projectId === undefined) return;
    run(
      () => createApprovalRule({ projectId, toolPattern }),
      () => {
        setAdded(toolPattern);
        setMode('idle');
      },
    );
  };
  useCardKeys(ctx, primary, localOnly ? undefined : deny);

  const project = ctx.project;
  const facts = localOnly
    ? t`${tool} · destructive: asks every time, no always-allow`
    : t`${tool} · write: asks once per scope`;
  const stall = item.stall !== undefined ? item.phase : undefined;
  const stallPlan = stall?.planTitle ?? '';
  const stallSeq = stall?.seq ?? 0;
  const stallName = stall?.name ?? '';
  const left = request === null ? null : Math.max(0, Math.round((Date.parse(request.expiresAt) - ctx.nowMs) / 1000));
  const clock = left === null ? null : `${String(Math.floor(left / 60))}:${String(left % 60).padStart(2, '0')}`;
  const note =
    mode === 'deny'
      ? { value: reason, onChange: setReason, placeholder: t`why you deny, for the session` }
      : mode === 'rule'
        ? { value: pattern, onChange: setPattern, placeholder: t`Tool or Tool(arg glob) — e.g. Read, Bash(git *)` }
        : undefined;

  return (
    <DecisionCard
      tier={tier}
      meta={ctx.meta}
      {...(clock === null ? {} : { expires: t`expires in ${clock}` })}
      title={
        localOnly ? t`This is a production deploy: confirm it in the session's own terminal` : t`The session asks to run ${tool}`
      }
      {...(item.preview === '' ? {} : { code: item.preview })}
      body={
        <>
          {localOnly ? (
            <Trans>Production deploy — confirm in the session's terminal</Trans>
          ) : (
            <Trans>The session waits on you. Nothing runs until you decide.</Trans>
          )}
          <span className="ml-2 inline-flex flex-wrap items-baseline gap-x-2 align-baseline">
            <ChannelBadge channel={request?.channel ?? item.channel} />
            {request !== null && (
              <button type="button" onClick={() => setRaw((v) => !v)} aria-expanded={raw} className={INLINE}>
                <Trans>raw</Trans> {raw ? '▾' : '▸'}
              </button>
            )}
            <Link to={ctx.manageTo} className={INLINE}>
              <Trans>rules &amp; history →</Trans>
            </Link>
            {stallTo !== null && (
              <Link to={stallTo} className={INLINE}>
                <Trans>open phase →</Trans>
              </Link>
            )}
          </span>
          {stall !== undefined && (
            <div data-testid="needs-you-stalled-on" className="mt-1 text-[12px] text-ink-3">
              <Trans>
                stalls {stallPlan} · Phase {stallSeq}: {stallName}
              </Trans>
            </div>
          )}
          {raw && request !== null && (
            <>
              <ApprovalContext request={request} />
              <pre className="mt-1.5 max-h-72 overflow-y-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[10.5px] leading-relaxed break-all whitespace-pre-wrap text-ink-3">
                {requestJsonPretty(request)}
              </pre>
            </>
          )}
          {mode === 'rule' && (
            <div className="mt-1.5 font-mono text-[10.5px] leading-[1.5] text-ink-faint">
              <Trans>
                Bash patterns match the command PREFIX — `Bash(git *)` also matches `git status &amp;&amp;
                rm -rf /`. Keep rules narrow; auto-approvals stay in History (via rule).
              </Trans>
            </div>
          )}
        </>
      }
      facts={facts}
      {...(note === undefined ? {} : { note })}
      primary={{ label: localOnly ? t`deny` : t`allow once`, onClick: primary, disabled: busy }}
      {...(canRule
        ? {
            extra: {
              label: mode === 'rule' ? t`add rule` : t`always allow in ${project}`,
              onClick: alwaysAllow,
              disabled: busy || (mode === 'rule' && pattern.trim() === ''),
            },
          }
        : {})}
      {...(localOnly ? {} : { secondary: { label: t`deny`, onClick: deny, disabled: busy } })}
      {...(added === null
        ? { link: sessionLink(ctx, t`open session →`) }
        : { foot: t`rule added: ${added} · this request still needs your answer` })}
    />
  );
}

/* ----- a question: the options as a radio list ----- */

function QuestionDecision({
  ctx,
  request,
  questions,
}: {
  ctx: CardContext;
  request: PermissionRequest;
  questions: readonly ParsedQuestion[];
}): JSX.Element {
  const { t } = useLingui();
  const { busy, run } = ctx;
  const requestId = request.id;
  const only = questions[0];
  const single = questions.length === 1 && only?.multiSelect === false ? only : null;
  const [pick, setPick] = useState(0);
  const [own, setOwn] = useState('');
  const [drafts, setDrafts] = useState<readonly AnswerDraft[]>(() => questions.map(() => EMPTY_DRAFT));
  const [denying, setDenying] = useState(false);
  const [reason, setReason] = useState('');

  const pickedLabel = single?.options[pick]?.label ?? '';
  const typed = own.trim();
  const answers =
    single !== null
      ? buildAnswers(questions, [{ selected: [pickedLabel], freeText: own }])
      : buildAnswers(questions, drafts);

  const updateDraft = (i: number, patch: Partial<AnswerDraft>): void => {
    setDrafts((prev) => prev.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  };
  const toggle = (i: number, q: ParsedQuestion, label: string): void => {
    const draft = drafts[i] ?? EMPTY_DRAFT;
    if (!q.multiSelect) updateDraft(i, { selected: [label] });
    else if (draft.selected.includes(label)) updateDraft(i, { selected: draft.selected.filter((l) => l !== label) });
    else updateDraft(i, { selected: [...draft.selected, label] });
  };

  const primary = (): void => {
    if (answers !== null) run(() => resolveApproval(requestId, 'answer', undefined, answers));
  };
  const deny = (): void => {
    if (!denying) {
      setDenying(true);
      return;
    }
    const trimmed = reason.trim();
    run(() => resolveApproval(requestId, 'deny', trimmed === '' ? undefined : trimmed, undefined));
  };
  useCardKeys(ctx, primary, deny);

  const count = questions.length;
  const note = denying
    ? { value: reason, onChange: setReason, placeholder: t`why you deny, for the session` }
    : single !== null
      ? { value: own, onChange: setOwn, placeholder: t`own answer — overrides the selection` }
      : undefined;

  return (
    <DecisionCard
      tier="question"
      meta={ctx.meta}
      title={single?.question ?? t`Claude is asking you ${plural(count, { one: '# question', few: '# questions', many: '# questions', other: '# questions' })}`}
      body={
        single !== null ? (
          <Trans>The session stopped to ask rather than guess. Your pick is sent as the reply; the session resumes with it.</Trans>
        ) : (
          <div className="flex flex-col gap-2.5">
            {questions.map((q, i) => (
              <QuestionBlock
                key={q.question}
                question={q}
                index={i}
                draft={drafts[i] ?? EMPTY_DRAFT}
                group={`ask-${String(requestId)}-${String(i)}`}
                onToggle={(label) => toggle(i, q, label)}
                onFreeText={(text) => updateDraft(i, { freeText: text })}
              />
            ))}
          </div>
        )
      }
      {...(single === null
        ? {}
        : {
            options: single.options.map((o, i) =>
              o.description === ''
                ? { id: String(i), label: o.label }
                : { id: String(i), label: o.label, consequence: o.description },
            ),
            picked: typed === '' ? String(pick) : null,
            onPick: (id: string) => {
              setPick(Number(id));
              setOwn('');
            },
          })}
      {...(note === undefined ? {} : { note })}
      primary={{
        label: single === null ? t`submit answers` : typed === '' ? t`reply` : t`reply: ${typed}`,
        onClick: primary,
        disabled: busy || answers === null,
      }}
      extra={{
        label: t`answer in terminal →`,
        onClick: () => run(() => resolveApproval(requestId, 'terminal', undefined, undefined)),
        disabled: busy,
      }}
      secondary={{ label: t`deny`, onClick: deny, disabled: busy }}
      link={sessionLink(ctx, t`open session →`)}
    />
  );
}

/* ----- awaiting_reply: reply, copy, dismiss ----- */

function ReplyDecision({ ctx }: { ctx: CardContext }): JSX.Element {
  const { t } = useLingui();
  const { item, busy, run } = ctx;
  const options = item.suggestion?.options ?? [];
  const recommended = options.indexOf(item.suggestion?.recommended ?? '');
  const [pick, setPick] = useState(Math.max(recommended, 0));
  const [copied, setCopied] = useState<'ok' | 'failed' | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  const copy = (text: string): void => {
    void copyText(text).then((ok) => {
      setCopied(ok ? 'ok' : 'failed');
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(null), COPIED_MS);
    });
  };

  const picked = options[pick] ?? null;
  const copyPick = (): void => copy(picked === null ? replyPrefix(item) : replyWithOption(item, picked));
  const primary = (): void => {
    if (picked === null) copyPick();
    else run(() => sendSessionMessage(item.sessionId, picked));
  };
  useCardKeys(ctx, primary, ctx.dismiss);

  const question = item.suggestion?.question || item.question || item.preview;
  return (
    <DecisionCard
      tier="question"
      meta={ctx.meta}
      title={question}
      body={<Trans>The session stopped to ask rather than guess. It does nothing until it hears from you.</Trans>}
      options={options.map((label, i) =>
        label === item.suggestion?.recommended
          ? { id: String(i), label, consequence: t`suggested` }
          : { id: String(i), label },
      )}
      picked={picked === null ? null : String(pick)}
      onPick={(id) => setPick(Number(id))}
      primary={{ label: picked === null ? t`copy reply` : t`reply`, onClick: primary, disabled: busy }}
      {...(picked === null ? {} : { extra: { label: t`copy reply`, onClick: copyPick } })}
      secondary={{ label: t`dismiss`, onClick: ctx.dismiss }}
      {...(copied === null
        ? { link: sessionLink(ctx, t`open session →`) }
        : { foot: copied === 'ok' ? t`Copied — paste it into the session` : t`Copy failed` })}
    />
  );
}

/* ----- prod_deploy_local: confirm in the terminal, no allow / deny ----- */

function ConfirmLocally({ ctx }: { ctx: CardContext }): JSX.Element {
  const { t } = useLingui();
  const { item } = ctx;
  // The focus URL is whatever the session's terminal reported (Warp's deep
  // link): only that app scheme is followed, anything else offers the session.
  const focusHref = safeHref(item.termFocusUrl, TERM_FOCUS_SCHEMES);
  const primary = (): void => {
    if (focusHref === null) ctx.go(ctx.sessionTo);
    else window.location.assign(focusHref);
  };
  useCardKeys(ctx, primary);
  const kind = kindLabel(item.kind);
  return (
    <DecisionCard
      tier="destructive"
      meta={`${kind} · ${ctx.meta}`}
      title={t`Production deploy — confirm in the session's terminal`}
      {...(item.preview === '' ? {} : { code: item.preview })}
      facts={t`destructive: asks every time, no always-allow`}
      primary={{ label: focusHref === null ? t`open session` : t`Focus terminal`, onClick: primary }}
      link={sessionLink(ctx, t`open session →`)}
    />
  );
}

/* ----- phase_decision: a guard decision or an agent-declared stop ----- */

/** What the guard found, as the card's one mono line. */
function guardFacts(decision: NeedsYouDecision): string {
  const f = guardFindings(decision.guard);
  const flagList = (decision.flags ?? []).join(', ');
  const parts: string[] = [];
  if (f !== null && f.changed.length > 0) {
    parts.push(
      plural(f.changed.length, {
        one: 'touched # protected file',
        few: 'touched # protected files',
        many: 'touched # protected files',
        other: 'touched # protected files',
      }),
    );
  }
  if (f?.docCopyMismatch === true) parts.push(t`doc copy mismatch`);
  if (f !== null && f.blocked > 0) {
    parts.push(
      plural(f.blocked, {
        one: '# blocked attempt',
        few: '# blocked attempts',
        many: '# blocked attempts',
        other: '# blocked attempts',
      }),
    );
  }
  if (f?.unreadable === true) parts.push(t`guard block unreadable`);
  if (flagList !== '') parts.push(t`contract broken: ${flagList}`);
  return parts.join(' · ');
}

function PhaseDecision({ ctx }: { ctx: CardContext }): JSX.Element | null {
  const { t } = useLingui();
  const { item, busy, run } = ctx;
  const [note, setNote] = useState('');
  const [pick, setPick] = useState(0);
  const decision = item.decision;
  const reviewTo = phaseHref(item, 'review');
  const picked = decision?.options[pick];
  const own = note.trim();
  const isStop = decision !== undefined && (decision.trigger !== undefined || decision.freeText === true);

  const answer = (option: string): void => {
    if (decision === undefined) return;
    run(() => answerGuardDecision(decision.taskId, decision.phaseId, decision.decisionId, option, own));
  };
  const primary = (): void => {
    if (picked !== undefined) answer(picked.id);
  };
  const ownAnswer = (): void => {
    if (own !== '') answer('other');
  };
  useCardKeys(ctx, primary, isStop ? ownAnswer : undefined);
  if (decision === undefined) return null;

  const decisionId = decision.decisionId;
  const trigger = decision.trigger !== undefined && decision.trigger !== '' ? decision.trigger : t`no trigger`;
  const reason = decision.reason !== undefined && decision.reason !== '' ? decision.reason : decision.question;
  const pathList = (decision.paths ?? []).join(', ');
  const facts = guardFacts(decision);
  const optionLabel = (id: string, label: string): string =>
    id === ALLOW_OPTION && !isStop ? t`Allow (add to scope)` : id === 'reject' && !isStop ? t`Reject` : label;

  return (
    <DecisionCard
      kind={`${kindLabel(item.kind)} · ${decisionId}`}
      kindTone="red"
      meta={ctx.meta}
      title={isStop ? t`The executor stopped: ${trigger}` : decision.question}
      {...(isStop ? { body: reason } : {})}
      {...(facts === '' ? {} : { facts })}
      options={decision.options.map((o) => {
        const released = o.id === ALLOW_OPTION && pathList !== '' ? t`Allowing this releases: ${pathList}` : '';
        const consequence = [o.consequence ?? '', released].filter((s) => s !== '').join(' · ');
        const label = optionLabel(o.id, o.label);
        return consequence === '' ? { id: o.id, label } : { id: o.id, label, consequence };
      })}
      picked={picked?.id ?? null}
      onPick={(id) => setPick(decision.options.findIndex((o) => o.id === id))}
      pickLimit={34}
      note={{
        value: note,
        onChange: setNote,
        placeholder: t`note — recorded with the answer in the phase doc (optional)`,
      }}
      primary={{ label: t`answer`, onClick: primary, disabled: busy || picked === undefined }}
      {...(isStop ? { secondary: { label: t`own answer…`, onClick: ownAnswer, disabled: busy || own === '' } } : {})}
      {...(reviewTo === null ? {} : { link: { label: t`phase review →`, onClick: () => ctx.go(reviewTo) } })}
    />
  );
}

/* ----- the kinds that are finished somewhere else ----- */

function OpenDecision({
  ctx,
  title,
  tone = 'amber',
  body,
  facts,
  label,
  to,
  link,
}: {
  ctx: CardContext;
  title: string;
  tone?: DecisionCardProps['kindTone'];
  body?: ReactNode;
  facts?: string;
  label: string;
  to: string | null;
  link?: DecisionCardProps['link'];
}): JSX.Element {
  const primary = (): void => {
    if (to !== null) ctx.go(to);
  };
  useCardKeys(ctx, primary);
  return (
    <DecisionCard
      kind={kindLabel(ctx.item.kind)}
      kindTone={tone}
      meta={ctx.meta}
      title={title}
      {...(body === undefined ? {} : { body })}
      {...(facts === undefined ? {} : { facts })}
      primary={{ label, onClick: primary, disabled: to === null }}
      {...(link === undefined ? {} : { link })}
    />
  );
}

function ManualPhase({ ctx }: { ctx: CardContext }): JSX.Element {
  const { t } = useLingui();
  const { item } = ctx;
  const to = phaseHref(item, 'criteria');
  const n = item.phase?.manualOpen ?? 0;
  return (
    <OpenDecision
      ctx={ctx}
      tone="default"
      title={plural(n, {
        one: '# criterion marked [MANUAL] — only you can close it',
        few: '# criteria marked [MANUAL] — only you can close them',
        many: '# criteria marked [MANUAL] — only you can close them',
        other: '# criteria marked [MANUAL] — only you can close them',
      })}
      body={
        <Trans>
          Every automatic criterion of this phase is green. What remains was declared as a human check when the plan
          was written; tick it on the phase's Criteria tab once you have verified it by hand.
        </Trans>
      }
      facts={item.preview}
      label={t`open criteria`}
      to={to}
      {...(to === null ? {} : { link: { label: t`phase →`, onClick: () => ctx.go(to) } })}
    />
  );
}

function AwaitingMerge({ ctx }: { ctx: CardContext }): JSX.Element {
  const { t } = useLingui();
  const { item } = ctx;
  const prUrl = item.phase?.landingState === 'pr_open' ? safeHref(item.phase.prUrl) : null;
  return (
    <OpenDecision
      ctx={ctx}
      title={item.sessionName}
      body={item.preview}
      label={t`open review`}
      to={phaseHref(item, 'review')}
      {...(prUrl === null
        ? {}
        : { link: { label: t`open PR ↗`, onClick: () => window.open(prUrl, '_blank', 'noopener,noreferrer') } })}
    />
  );
}

/* ----- worktree_cleanup: finished worktrees whose content is already in main ----- */

const PROOF_TONE: Record<CleanupProofKind, string> = {
  landing: 'text-green',
  'content-diff': 'text-ink-3',
};

/** A candidate's phase on the Plans page; null for a board card's worktree. */
function candidatePhaseHref(item: NeedsYouItem, c: NeedsYouCleanupCandidate): string | null {
  if (c.planExternalId === undefined || c.planExternalId === '' || c.phaseSeq === undefined || c.phaseSeq === 0) {
    return null;
  }
  return plansPath(item.projectSlug, {
    plan: c.planExternalId,
    detail: { kind: 'phase', seq: c.phaseSeq, tab: 'story' },
  });
}

/**
 * The janitor keeps a worktree whose branch has commits no other ref has. This
 * card lists the ones whose phase or card is closed AND whose content is proven
 * in main; the operator ticks some and removes them. Nothing is removed without
 * that click, and the daemon writes a salvage branch before each one.
 */
function WorktreeCleanup({ ctx }: { ctx: CardContext }): JSX.Element | null {
  const { t } = useLingui();
  const { item, busy, run } = ctx;
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [outcomes, setOutcomes] = useState<readonly WorktreeCleanupPathOutcome[]>([]);
  const cleanup = item.cleanup;

  const remove = (): void => {
    if (cleanup === undefined || selected.size === 0) return;
    run(
      () => applyWorktreeCleanup(cleanup.id, [...selected]),
      (res) => {
        setOutcomes((res as { paths: WorktreeCleanupPathOutcome[] }).paths);
        setSelected(new Set());
      },
    );
  };
  useCardKeys(ctx, remove);
  if (cleanup === undefined) return null;

  const candidates = cleanup.candidates;
  const allSelected = candidates.length > 0 && candidates.every((c) => selected.has(c.path));
  const toggle = (path: string): void => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };
  const toggleAll = (): void => {
    setSelected(allSelected ? new Set() : new Set(candidates.map((c) => c.path)));
  };
  const proofLabel = (kind: CleanupProofKind): string => (kind === 'landing' ? t`PR merged` : t`content in main`);
  const selectedCount = String(selected.size);
  const withoutProof = String(cleanup.withoutProof);
  const repoRoot = cleanup.repoRoot;

  return (
    <DecisionCard
      kind={kindLabel(item.kind)}
      kindTone="default"
      meta={ctx.meta}
      title={item.sessionName}
      body={
        <>
          <Trans>
            Worktrees of finished phases and cards whose content is already in main. Removing one writes a salvage
            branch first.
          </Trans>
          <div className="mt-2 overflow-x-auto rounded-[6px] border border-line">
            <table className="w-full border-collapse text-left font-mono text-[11px]">
              <caption className="sr-only">{t`Worktrees ready to remove in ${repoRoot}`}</caption>
              <thead className="text-[10px] text-ink-faint">
                <tr className="border-b border-line">
                  <th scope="col" className="w-8 px-2 py-1.5">
                    <input
                      type="checkbox"
                      checked={allSelected}
                      onChange={toggleAll}
                      aria-label={t`Select all worktrees`}
                    />
                  </th>
                  <th scope="col" className="px-2 py-1.5 font-normal">
                    <Trans>Path</Trans>
                  </th>
                  <th scope="col" className="px-2 py-1.5 font-normal">
                    <Trans>Branch</Trans>
                  </th>
                  <th scope="col" className="px-2 py-1.5 font-normal">
                    <Trans>Phase / card</Trans>
                  </th>
                  <th scope="col" className="px-2 py-1.5 font-normal">
                    <Trans>Proof</Trans>
                  </th>
                </tr>
              </thead>
              <tbody>
                {candidates.map((c) => {
                  const phaseTo = candidatePhaseHref(item, c);
                  const path = c.path;
                  return (
                    <tr key={c.id} data-testid="cleanup-candidate" className="border-b border-line align-top last:border-b-0">
                      <td className="px-2 py-1.5">
                        <input
                          type="checkbox"
                          checked={selected.has(c.path)}
                          onChange={() => toggle(c.path)}
                          aria-label={t`Select ${path}`}
                        />
                      </td>
                      <td className="px-2 py-1.5 break-all text-ink">
                        {c.path}
                        {c.error !== undefined && c.error !== '' && (
                          <div className="mt-0.5 text-[10.5px] text-red">{t`last attempt: ${c.error}`}</div>
                        )}
                      </td>
                      <td className="px-2 py-1.5 break-all text-ink-3">{c.branch}</td>
                      <td className="px-2 py-1.5 text-ink-3">
                        {phaseTo !== null ? (
                          <Link to={phaseTo} className={INLINE}>
                            {c.owner}
                          </Link>
                        ) : (
                          c.owner
                        )}
                      </td>
                      <td className="px-2 py-1.5">
                        <span title={c.proofDetail} className={`text-[10px] whitespace-nowrap ${PROOF_TONE[c.proofKind]}`}>
                          {proofLabel(c.proofKind)}
                        </span>
                        {c.proofDetail !== '' && <div className="mt-0.5 text-[10px] text-ink-faint">{c.proofDetail}</div>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {outcomes.length > 0 && (
            <ul aria-live="polite" className="m-0 mt-2 list-none p-0 font-mono text-[10.5px] text-ink-3">
              {outcomes.map((o) => (
                <li key={o.path} data-testid="cleanup-outcome" data-outcome={o.outcome} className="break-all">
                  {o.outcome} · {o.path}
                  {o.salvageBranch !== undefined && o.salvageBranch !== '' ? ` → ${o.salvageBranch}` : ''}
                  {o.reason !== undefined && o.reason !== '' ? ` (${o.reason})` : ''}
                </li>
              ))}
            </ul>
          )}
        </>
      }
      facts={t`${selectedCount} selected`}
      primary={{
        label: busy ? t`Removing…` : t`Remove selected`,
        onClick: remove,
        disabled: busy || selected.size === 0,
      }}
      foot={t`without proof: ${withoutProof}`}
      link={{ label: t`janitor journal →`, onClick: () => ctx.go('/settings') }}
    />
  );
}

/* ----- one decision, whatever its kind ----- */

function Decision({ ctx, request }: { ctx: CardContext; request: PermissionRequest | null }): JSX.Element | null {
  const { t } = useLingui();
  const { item } = ctx;
  switch (item.kind) {
    case 'approval':
    case 'question': {
      const questions = request?.toolName === 'AskUserQuestion' ? questionsOf(request) : null;
      if (request !== null && questions !== null && request.riskClass !== 'prod-deploy') {
        return <QuestionDecision ctx={ctx} request={request} questions={questions} />;
      }
      // The form needs the pending row; until it loads the session can answer.
      if (item.kind === 'question' && request === null) {
        return (
          <OpenDecision
            ctx={ctx}
            title={item.preview}
            label={t`open session`}
            to={ctx.sessionTo}
          />
        );
      }
      return <PermissionDecision ctx={ctx} request={request} />;
    }
    case 'prod_deploy_local':
      return <ConfirmLocally ctx={ctx} />;
    case 'awaiting_reply':
      return <ReplyDecision ctx={ctx} />;
    case 'failed':
      return (
        <OpenDecision
          ctx={ctx}
          tone="red"
          title={item.sessionName}
          {...(item.preview === '' ? {} : { body: item.preview })}
          label={t`open session`}
          to={ctx.sessionTo}
        />
      );
    case 'manual_phase':
      return <ManualPhase ctx={ctx} />;
    case 'awaiting_merge':
      return <AwaitingMerge ctx={ctx} />;
    case 'stalled_phase':
      return (
        <OpenDecision
          ctx={ctx}
          title={item.sessionName}
          body={item.preview}
          label={t`open phase`}
          to={phaseHref(item, 'runs')}
        />
      );
    case 'phase_decision':
      return <PhaseDecision ctx={ctx} />;
    case 'worktree_cleanup':
      return <WorktreeCleanup ctx={ctx} />;
  }
}

/* ----- page ----- */

export function NeedsYou(): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const scope = slug ?? null;
  const [params] = useSearchParams();
  const { items, loading, error, reload, dismiss } = useNeedsYou(scope);
  const { projects } = useScope();
  const sessionHref = useSessionHref();
  const navigate = useNavigate();
  const nowMs = useNowMs();
  const [query, setQuery] = useState('');
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const keys = useRef<CardKeys>({ primary: undefined, secondary: undefined });

  // The pending rows behind approval / question items, for their tool input and
  // questions. Refetched whenever that id set changes.
  const [requests, setRequests] = useState<ReadonlyMap<number, PermissionRequest>>(new Map());
  const requestIds = items
    .filter((it) => it.requestId !== null && (it.kind === 'approval' || it.kind === 'question'))
    .map((it) => String(it.requestId))
    .join(',');
  const loadRequests = useCallback((): void => {
    if (requestIds === '') return;
    fetchApprovals('pending', scope)
      .then((rows) => setRequests(new Map(rows.map((r) => [r.id, r]))))
      .catch(() => undefined); // the card falls back to the queue's preview
  }, [requestIds, scope]);
  useEffect(loadRequests, [loadRequests]);

  const run = (work: () => Promise<unknown>, onOk?: (result: unknown) => void): void => {
    setBusy(true);
    setActionError(null);
    work()
      .then((result) => onOk?.(result))
      .catch((e: unknown) => setActionError(e instanceof Error ? e.message : String(e)))
      .finally(() => {
        loadRequests();
        // Busy holds until the queue refetch lands: the answered card is still on
        // screen until then, and `e` / `x` must not answer it a second time.
        void reload().finally(() => setBusy(false));
      });
  };

  const visible = useMemo(() => filterItems(items, query), [items, query]);
  const groups = useMemo(() => groupItems(visible), [visible]);
  const ordered = useMemo(() => groups.flatMap((g) => g.items), [groups]);
  const orderedKeys = ordered.map(itemKey);
  // `/inbox?tab=approvals` and `/approvals` land here on a permission, where the
  // always-allow control is; a session's gate names its own with `&request=`. A
  // request that has since been answered falls back to the first permission.
  // `?item=` (a Today row) names the exact decision; one already gone falls back
  // like the rest, and a click in the list wins over the URL.
  const wantedRequest = params.get('request');
  const wantedItem = params.get('item');
  const wanted =
    ordered.find((i) => itemKey(i) === wantedItem) ??
    (params.get('tab') === 'approvals'
      ? (ordered.find((i) => i.requestId !== null && String(i.requestId) === wantedRequest) ??
        ordered.find((i) => i.kind === 'approval'))
      : undefined);
  const selected = ordered.find((i) => itemKey(i) === selectedKey) ?? wanted ?? ordered[0];
  const currentKey = selected === undefined ? null : itemKey(selected);

  const select = (key: string | undefined): void => {
    if (key === undefined) return;
    setSelectedKey(key);
    setActionError(null);
  };

  const hint = selected?.kind;
  const selectedRequest = selected?.requestId == null ? null : (requests.get(selected.requestId) ?? null);
  // A prod-deploy request can only be denied here, so `e` is the deny.
  const denyOnly = hint === 'approval' && selectedRequest?.riskClass === 'prod-deploy';
  const primaryHint = denyOnly
    ? t`deny`
    : hint === 'approval'
      ? t`approve`
      : hint === 'awaiting_reply' || hint === 'question'
        ? t`reply`
        : hint === 'phase_decision'
          ? t`answer`
          : undefined;
  const secondaryHint = denyOnly
    ? undefined
    : hint === 'approval' || hint === 'question'
      ? t`deny`
      : hint === 'awaiting_reply'
        ? t`dismiss`
        : undefined;
  const blockedCount = String(visible.length);

  const context = (item: NeedsYouItem): CardContext => {
    const project = findProject(projects, item.projectSlug);
    const label = projectLabel(project?.name ?? null, item.projectSlug);
    const age = compactSpan(nowMs - Date.parse(item.blockingSince));
    const phase = item.phase;
    const seq = String(phase?.seq ?? 0);
    const meta =
      phase !== undefined && item.kind !== 'approval'
        ? [label, phase.planTitle, t`phase ${seq}`, t`blocked ${age}`]
        : [label, item.sessionName, t`waiting ${age}`];
    return {
      item,
      meta: meta.join(' · '),
      sessionTo: sessionHref(item.sessionId),
      manageTo: slug === undefined ? '/approvals/manage' : `/p/${slug}/approvals/manage`,
      projectId: project?.id,
      project: label,
      busy,
      run,
      go: (to) => navigate(to),
      dismiss: () => dismiss(item),
      keys,
      nowMs,
    };
  };

  return (
    <PlaceLayout
      list={
        <ListPane
          title={t`Needs you`}
          sub={loading ? t`sessions blocked on you, oldest first` : t`${blockedCount} blocked on you · oldest first`}
          query={query}
          onQueryChange={setQuery}
          searchHint={t`filter`}
          keys={{
            onNext: () => select(stepKey(orderedKeys, currentKey, 1)),
            onPrev: () => select(stepKey(orderedKeys, currentKey, -1)),
            onPrimary: () => keys.current.primary?.(),
            onSecondary: () => keys.current.secondary?.(),
          }}
          {...(primaryHint === undefined ? {} : { primaryHint })}
          {...(secondaryHint === undefined ? {} : { secondaryHint })}
        >
          {loading && error === null && <Loading label={t`needs you…`} />}
          {groups.map((g) => (
            <ListGroup
              key={g.id}
              label={g.id === 'decide' ? t`Decide here` : t`Open & tick by hand`}
              count={g.items.length}
              tone={g.id === 'decide' ? 'amber' : 'default'}
            >
              {g.items.map((item) => {
                const key = itemKey(item);
                const project = findProject(projects, item.projectSlug);
                return (
                  <div key={key} data-testid="needs-you-row" data-kind={item.kind} data-name={item.sessionName}>
                    <ListRow
                      glyph={<StateGlyph state={itemGlyph(item.kind)} size={11} />}
                      title={item.phase?.name ?? item.sessionName}
                      {...(scope === null ? { tag: projectLabel(project?.name ?? null, item.projectSlug) } : {})}
                      {...(item.preview === '' ? {} : { summary: item.preview })}
                      summaryTone="needs"
                      age={compactSpan(nowMs - Date.parse(item.blockingSince))}
                      selected={key === currentKey}
                      onSelect={() => select(key)}
                    />
                  </div>
                );
              })}
            </ListGroup>
          ))}
        </ListPane>
      }
      detail={
        <div
          data-testid="needs-you-detail"
          data-kind={selected?.kind}
          className="flex min-h-full max-w-[900px] flex-col px-7 pt-[22px] pb-10 [&>*:last-child]:flex-1"
        >
          {error !== null && <ErrorBox message={error} onRetry={reload} />}
          {actionError !== null && (
            <div role="alert" className="mb-3 font-mono text-[11px] text-red">
              {actionError}
            </div>
          )}
          {selected !== undefined ? (
            <Decision
              key={currentKey}
              ctx={context(selected)}
              request={selectedRequest}
            />
          ) : (
            !loading &&
            error === null && (
              <div className="m-auto text-center text-ink-3">
                <div className="text-[18px] font-medium text-ink-2">
                  <Trans>Nothing blocked on you.</Trans>
                </div>
                <div className="mt-1.5 max-w-[42ch] text-[12.5px] leading-[1.55]">
                  <Trans>
                    Permissions, questions, guard stops and manual checks land here the moment a session or phase stops
                    for you.
                  </Trans>
                </div>
              </div>
            )
          )}
        </div>
      }
    />
  );
}
