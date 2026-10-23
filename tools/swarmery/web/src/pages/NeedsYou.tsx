// "Needs you" (needs-you-queue phase 5): every session blocked on the operator,
// oldest blocker first, from GET /api/needs-you — live over the shared WS
// stream (lib/useNeedsYou). One row per blocker:
//
//   approval / question  the existing Approvals PendingCard (approve / deny /
//                        answer), joined by requestId to the pending row; when
//                        a plan phase is stalled on that very request, the
//                        server folds the stall in and the row adds an
//                        «open phase» link to it
//   stalled_phase        a plan phase whose active stage (executor, reviewer,
//                        verifier) stopped working: a link to the phase's Runs
//                        tab. Notification only — nothing here restarts it
//   prod_deploy_local    "Confirm locally" — no allow and no deny: the daemon
//                        hands a production deploy to the session's terminal
//   awaiting_reply       "Copy reply" puts `[<session>] Re: <question>` on the
//                        clipboard for the operator to answer in the terminal;
//                        suggestion options (phase 4) prefill it; Dismiss hides
//                        this blocking episode in this browser
//   failed               a link to the session
//   manual_phase         a plan phase whose every open criterion is [MANUAL]:
//                        a link to that phase's Criteria tab, where the
//                        operator ticks what they checked by hand
//   awaiting_merge       a plan phase whose every open criterion is [LAND] and
//                        whose run branch is ready, pushed or under an open
//                        PR: a link to the phase's Review tab (push / open PR)
//                        and, once a PR is open, a link to that PR
//   phase_decision       a phase run touched protected paths: the guard badge,
//                        one button per option (allow into scope / reject) and
//                        a free-text note, answered in place
//   worktree_cleanup     one repository's worktrees of closed phases / cards
//                        whose content is proven in main: a table with
//                        checkboxes, "Remove selected" (POST
//                        /api/worktree-cleanup/{id}/apply, salvage branch
//                        first) and a "without proof: K" count of the ones
//                        never offered
//
// Nothing here delivers a reply to a session: the operator pastes it.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { applyWorktreeCleanup, fetchApprovals, resolveApproval, type ApprovalAction } from '../api';
import { answerGuardDecision } from '../api/guard';
import type {
  CleanupProofKind,
  NeedsYouCleanupCandidate,
  NeedsYouItem,
  NeedsYouKind,
  PermissionRequest,
  WorktreeCleanupPathOutcome,
} from '../api/types';
import { PendingCard } from './Approvals';
import type { PhaseTab } from './plans/PhasePanel';
import { ChannelBadge } from '../components/ChannelBadge';
import { ProjectName } from '../components/ProjectName';
import { Empty, ErrorBox, Loading } from '../components/ui';
import type { AnswerMap } from '../lib/approvals';
import { fmtSpan } from '../lib/format';
import { itemLabel, replyPrefix, replyWithOption } from '../lib/needsYou';
import { findProject } from '../lib/projectSlug';
import { safeHref } from '../lib/safeHref';
import { useScope } from '../lib/scope';
import { useSessionHref } from '../lib/sessionHref';
import { useNowMs } from '../lib/sessionState';
import { useNeedsYou } from '../lib/useNeedsYou';
import { GuardBadge } from './plans/GuardBadge';
import { StopDecision } from './plans/StopDecision';
import { plansPath } from './plans/plansUrl';

/** Kind badge tones — the label always carries the meaning, colour only echoes it. */
const KIND_BADGE: Record<NeedsYouKind, string> = {
  approval: 'border-amber/40 text-amber',
  stalled_phase: 'border-amber/60 bg-amber/10 text-amber',
  question: 'border-brand/50 text-brand',
  phase_decision: 'border-red/45 bg-red/10 text-red',
  prod_deploy_local: 'border-amber/60 bg-amber/10 text-amber',
  awaiting_reply: 'border-amber/40 text-amber',
  failed: 'border-red/40 text-red',
  manual_phase: 'border-brand/40 text-brand',
  awaiting_merge: 'border-green/40 text-green',
  worktree_cleanup: 'border-line-strong text-ink-3',
};

const FOCUS = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand';
const BTN = `rounded-lg border px-3 py-1.5 font-mono text-[11.5px] transition-colors disabled:opacity-50 ${FOCUS}`;
const LINK = `font-mono text-[11px] text-ink-dim transition-colors hover:text-brand ${FOCUS}`;
const COPIED_MS = 2_000;
/** The deep-link schemes a terminal's focus URL may use (Warp's WARP_FOCUS_URL). */
const TERM_FOCUS_SCHEMES: readonly string[] = ['warp:'];

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false; // no clipboard (insecure context) or permission refused
  }
}

/* ----- awaiting_reply: copy reply / suggestion chips / dismiss ----- */

function AwaitingReply({
  item,
  sessionTo,
  onDismiss,
}: {
  item: NeedsYouItem;
  sessionTo: string;
  onDismiss: () => void;
}): JSX.Element {
  const { t } = useLingui();
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

  const question = item.suggestion?.question || item.question || item.preview;
  const sessionName = item.sessionName;
  const options = item.suggestion?.options ?? [];

  return (
    <>
      {question !== '' && (
        <blockquote className="mt-2 border-l-2 border-amber/40 pl-3 text-[13px] leading-[1.55] whitespace-pre-wrap text-ink-3 [text-wrap:pretty]">
          {question}
        </blockquote>
      )}
      {options.length > 0 && (
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5" role="group" aria-label={t`suggested replies`}>
          {options.map((opt) => {
            const recommended = opt === item.suggestion?.recommended;
            return (
              <button
                key={opt}
                type="button"
                onClick={() => copy(replyWithOption(item, opt))}
                aria-label={
                  recommended ? t`Copy reply with option: ${opt} (suggested)` : t`Copy reply with option: ${opt}`
                }
                className={`${BTN} ${recommended ? 'border-green/45 text-green hover:bg-green/10' : 'border-line-strong text-ink-3 hover:bg-surface2'}`}
              >
                {opt}
                {recommended && (
                  <span className="ml-1.5 text-[10px] opacity-80">
                    <Trans>· suggested</Trans>
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => copy(replyPrefix(item))}
          aria-label={t`Copy reply to ${sessionName}`}
          className={`${BTN} border-brand/50 bg-brand/10 font-semibold text-brand hover:bg-brand/20`}
        >
          <Trans>Copy reply</Trans>
        </button>
        <button
          type="button"
          onClick={onDismiss}
          aria-label={t`Dismiss ${sessionName}`}
          className={`${BTN} border-line-strong text-ink-3 hover:bg-surface2`}
        >
          <Trans>Dismiss</Trans>
        </button>
        <span aria-live="polite" className="font-mono text-[11px] text-ink-dim">
          {copied === 'ok' ? t`Copied — paste it into the session` : copied === 'failed' ? t`Copy failed` : ''}
        </span>
        <Link to={sessionTo} className={`ml-auto ${LINK}`}>
          <Trans>open session →</Trans>
        </Link>
      </div>
    </>
  );
}

/* ----- prod_deploy_local: confirm in the terminal, no allow / deny ----- */

function ConfirmLocally({ item, sessionTo }: { item: NeedsYouItem; sessionTo: string }): JSX.Element {
  const { t } = useLingui();
  const sessionName = item.sessionName;
  // The focus URL is whatever the session's terminal reported (Warp's deep
  // link): only that app scheme is a link, anything else renders no button.
  const focusHref = safeHref(item.termFocusUrl, TERM_FOCUS_SCHEMES);
  return (
    <>
      <p className="mt-2 text-[13px] leading-[1.55] text-ink-2">
        <Trans>Production deploy — confirm in the session's terminal</Trans>
      </p>
      {item.preview !== '' && (
        <code className="mt-2 block overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[11.5px] break-all whitespace-pre-wrap text-ink-3">
          {item.preview}
        </code>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-3">
        {focusHref !== null && (
          <a
            href={focusHref}
            aria-label={t`Focus the terminal of ${sessionName}`}
            className={`${BTN} border-amber/50 text-amber hover:bg-amber/10`}
          >
            <Trans>Focus terminal</Trans>
          </a>
        )}
        <Link to={sessionTo} className={`ml-auto ${LINK}`}>
          <Trans>open session →</Trans>
        </Link>
      </div>
    </>
  );
}

/* ----- manual_phase / awaiting_merge: a plan phase only the operator can finish ----- */

/**
 * A phase tab of the item's plan: Criteria for a manual_phase item (where the
 * [MANUAL] criteria are ticked by hand), Review for an awaiting_merge one
 * (where the run branch is pushed and its PR opened).
 */
function phaseHref(item: NeedsYouItem, tab: PhaseTab): string | null {
  if (item.phase === undefined || item.phase.planExternalId === '') return null;
  return plansPath(item.projectSlug, {
    plan: item.phase.planExternalId,
    detail: { kind: 'phase', seq: item.phase.seq, tab },
  });
}

/** The phase link a phase item's title and body open; null for a session item. */
function phaseItemHref(item: NeedsYouItem): string | null {
  if (item.kind === 'manual_phase') return phaseHref(item, 'criteria');
  if (item.kind === 'awaiting_merge' || item.kind === 'phase_decision') return phaseHref(item, 'review');
  if (item.kind === 'stalled_phase') return phaseHref(item, 'runs');
  return null;
}

/** A stalled phase: what stopped and for how long (the server's preview), and a
 *  link to the phase's Runs tab. Notification only — no restart action, by design. */
function StalledPhase({ item, phaseTo }: { item: NeedsYouItem; phaseTo: string | null }): JSX.Element {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1 text-[12.5px] text-ink-3">{item.preview}</span>
      {phaseTo !== null && (
        <Link to={phaseTo} className={`ml-auto ${LINK}`}>
          <Trans>open phase →</Trans>
        </Link>
      )}
    </div>
  );
}

/** The «open phase» second action of an approval a stalled phase waits on. */
function StalledOn({ item }: { item: NeedsYouItem }): JSX.Element | null {
  const to = item.stall !== undefined ? phaseHref(item, 'runs') : null;
  if (to === null || item.phase === undefined) return null;
  const { planTitle, seq, name } = item.phase;
  return (
    <div data-testid="needs-you-stalled-on" className="mt-2 flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1 truncate text-[12px] text-ink-3">
        <Trans>
          stalls {planTitle} · Phase {seq}: {name}
        </Trans>
      </span>
      <Link to={to} className={`ml-auto ${LINK}`}>
        <Trans>open phase →</Trans>
      </Link>
    </div>
  );
}

/** A guard decision: what the run touched, the options as buttons, a note — or
 *  an agent stop: its trigger, reason, two options and a free-text answer. */
export function PhaseDecision({
  item,
  reviewTo,
  onAnswered,
}: {
  item: NeedsYouItem;
  reviewTo: string | null;
  onAnswered: () => void;
}): JSX.Element | null {
  const { t } = useLingui();
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const decision = item.decision;
  if (decision === undefined) return null;
  const answer = (option: string): void => {
    setBusy(true);
    setError(null);
    answerGuardDecision(decision.taskId, decision.phaseId, decision.decisionId, option, note.trim())
      .then(onAnswered)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };
  const decisionId = decision.decisionId;
  // An agent-declared stop (verify-not-trust phase 10) brings its own options.
  if (decision.stop !== undefined) {
    return (
      <>
        <StopDecision decision={decision} onAnswered={onAnswered} />
        {reviewTo !== null && (
          <div className="mt-2 flex">
            <Link to={reviewTo} className={`ml-auto ${LINK}`}>
              <Trans>open phase →</Trans>
            </Link>
          </div>
        )}
      </>
    );
  }
  return (
    <>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] font-semibold text-ink-2">{decisionId}</span>
        <GuardBadge guard={decision.guard} />
      </div>
      <p className="mt-1.5 text-[13px] leading-[1.55] text-ink-2 [text-wrap:pretty]">{decision.question}</p>
      <label className="mt-2.5 block">
        <span className="font-mono text-[10.5px] text-ink-dim">
          <Trans>note (optional) — recorded with the answer in the phase doc</Trans>
        </span>
        <input
          type="text"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          disabled={busy}
          maxLength={2000}
          className={`mt-1 block w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 text-[12.5px] text-ink ${FOCUS}`}
        />
      </label>
      <div className="mt-3 flex flex-wrap items-center gap-2" role="group" aria-label={t`answer ${decisionId}`}>
        {decision.options.map((opt) => (
          <button
            key={opt.id}
            type="button"
            disabled={busy}
            onClick={() => answer(opt.id)}
            className={`${BTN} ${opt.id === 'reject' ? 'border-line-strong text-ink-3 hover:bg-surface2' : 'border-brand/50 bg-brand/10 font-semibold text-brand hover:bg-brand/20'}`}
          >
            {opt.id === 'allow-add-to-scope' ? t`Allow (add to scope)` : opt.id === 'reject' ? t`Reject` : opt.label}
          </button>
        ))}
        {error !== null && (
          <span role="alert" className="font-mono text-[11px] text-red">
            {error}
          </span>
        )}
        {reviewTo !== null && (
          <Link to={reviewTo} className={`ml-auto ${LINK}`}>
            <Trans>open review →</Trans>
          </Link>
        )}
      </div>
    </>
  );
}

function ManualPhase({ item, phaseTo }: { item: NeedsYouItem; phaseTo: string | null }): JSX.Element {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1 text-[12.5px] text-ink-3">{item.preview}</span>
      {phaseTo !== null && (
        <Link to={phaseTo} className={`ml-auto ${LINK}`}>
          <Trans>open criteria →</Trans>
        </Link>
      )}
    </div>
  );
}

/** Only the merge is left: Review pushes and opens the PR; an open PR merges on the host. */
function AwaitingMerge({ item, reviewTo }: { item: NeedsYouItem; reviewTo: string | null }): JSX.Element {
  const prUrl = item.phase?.landingState === 'pr_open' ? (safeHref(item.phase.prUrl) ?? '') : '';
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1 text-[12.5px] text-ink-3">{item.preview}</span>
      {prUrl !== '' && (
        <a href={prUrl} target="_blank" rel="noopener noreferrer" className={`ml-auto ${LINK}`}>
          <Trans>open PR ↗</Trans>
        </a>
      )}
      {reviewTo !== null && (
        <Link to={reviewTo} className={`${prUrl === '' ? 'ml-auto ' : ''}${LINK}`}>
          <Trans>open review →</Trans>
        </Link>
      )}
    </div>
  );
}

/* ----- worktree_cleanup: finished worktrees whose content is already in main ----- */

const PROOF_BADGE: Record<CleanupProofKind, string> = {
  landing: 'border-green/40 text-green',
  'content-diff': 'border-brand/40 text-brand',
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
 * card lists the ones whose phase or card is closed AND whose content is
 * proven in main; the operator ticks some and removes them. Nothing is removed
 * without that click, and the daemon writes a salvage branch before each one.
 */
function WorktreeCleanup({ item, onApplied }: { item: NeedsYouItem; onApplied: () => void }): JSX.Element {
  const { t } = useLingui();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcomes, setOutcomes] = useState<readonly WorktreeCleanupPathOutcome[]>([]);
  const cleanup = item.cleanup;
  if (cleanup === undefined) return <></>;

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
  const remove = (): void => {
    if (selected.size === 0) return;
    setBusy(true);
    setError(null);
    applyWorktreeCleanup(cleanup.id, [...selected])
      .then((res) => {
        setOutcomes(res.paths);
        setSelected(new Set());
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => {
        setBusy(false);
        onApplied();
      });
  };

  const proofLabel = (kind: CleanupProofKind): string => (kind === 'landing' ? t`PR merged` : t`content in main`);
  const selectedCount = String(selected.size);
  const withoutProof = String(cleanup.withoutProof);
  const repoRoot = cleanup.repoRoot;

  return (
    <>
      <p className="mt-2 text-[12.5px] leading-[1.55] text-ink-3">
        <Trans>
          Worktrees of finished phases and cards whose content is already in main. Removing one writes a salvage
          branch first.
        </Trans>
      </p>
      <div className="mt-2 overflow-x-auto rounded-md border border-line">
        <table className="w-full border-collapse text-left font-mono text-[11px]">
          <caption className="sr-only">{t`Worktrees ready to remove in ${repoRoot}`}</caption>
          <thead className="text-[10px] text-ink-dim">
            <tr className="border-b border-line">
              <th scope="col" className="w-8 px-2 py-1.5">
                <input
                  type="checkbox"
                  checked={allSelected}
                  onChange={toggleAll}
                  aria-label={t`Select all worktrees`}
                  className={FOCUS}
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
                <tr key={c.id} data-testid="cleanup-candidate" className="border-b border-line last:border-b-0 align-top">
                  <td className="px-2 py-1.5">
                    <input
                      type="checkbox"
                      checked={selected.has(c.path)}
                      onChange={() => toggle(c.path)}
                      aria-label={t`Select ${path}`}
                      className={FOCUS}
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
                      <Link to={phaseTo} className={LINK}>
                        {c.owner}
                      </Link>
                    ) : (
                      c.owner
                    )}
                  </td>
                  <td className="px-2 py-1.5">
                    <span
                      title={c.proofDetail}
                      className={`rounded-full border px-2 py-px text-[10px] whitespace-nowrap ${PROOF_BADGE[c.proofKind]}`}
                    >
                      {proofLabel(c.proofKind)}
                    </span>
                    {c.proofDetail !== '' && <div className="mt-0.5 text-[10px] text-ink-dim">{c.proofDetail}</div>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={remove}
          disabled={busy || selected.size === 0}
          className={`${BTN} border-red/50 text-red hover:bg-red/10`}
        >
          {busy ? t`Removing…` : t`Remove selected`}
        </button>
        <span className="font-mono text-[11px] text-ink-dim">{t`${selectedCount} selected`}</span>
        <span data-testid="cleanup-without-proof" className="ml-auto font-mono text-[11px] text-ink-dim">
          {t`without proof: ${withoutProof}`}
        </span>
      </div>
      {error !== null && (
        <div role="alert" className="mt-2 font-mono text-[11px] text-red">
          {error}
        </div>
      )}
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
  );
}

/* ----- one row ----- */

function Row({
  item,
  request,
  nowMs,
  busy,
  inboxHref,
  projectName,
  sessionTo,
  onResolve,
  onDismiss,
  onAnswered,
  onApplied,
}: {
  item: NeedsYouItem;
  /** The pending permission row behind an approval / question item, once loaded. */
  request: PermissionRequest | null;
  nowMs: number;
  busy: boolean;
  inboxHref: string;
  projectName: string | null;
  sessionTo: string;
  onResolve: (request: PermissionRequest, action: ApprovalAction, reason?: string, answers?: AnswerMap) => void;
  onDismiss: () => void;
  onAnswered: () => void;
  /** A worktree_cleanup apply finished: refetch the queue. */
  onApplied: () => void;
}): JSX.Element {
  const age = fmtSpan(item.blockingSince, new Date(nowMs).toISOString());

  let body: JSX.Element;
  switch (item.kind) {
    case 'approval':
    case 'question':
      body = (
        <>
          {request !== null ? (
            <PendingCard
              request={request}
              session={null}
              nowMs={nowMs}
              busy={busy}
              onResolve={(action, reason, answers) => onResolve(request, action, reason, answers)}
            />
          ) : (
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <ChannelBadge channel={item.channel} />
              <code className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink-3">{item.preview}</code>
              <Link to={inboxHref} className={LINK}>
                <Trans>review in Inbox →</Trans>
              </Link>
            </div>
          )}
          <StalledOn item={item} />
        </>
      );
      break;
    case 'prod_deploy_local':
      body = <ConfirmLocally item={item} sessionTo={sessionTo} />;
      break;
    case 'awaiting_reply':
      body = <AwaitingReply item={item} sessionTo={sessionTo} onDismiss={onDismiss} />;
      break;
    case 'failed':
      body = (
        <div className="mt-2 flex flex-wrap items-center gap-3">
          {item.preview !== '' && <span className="min-w-0 flex-1 text-[12.5px] text-ink-3">{item.preview}</span>}
          <Link to={sessionTo} className={`ml-auto ${LINK}`}>
            <Trans>open session →</Trans>
          </Link>
        </div>
      );
      break;
    case 'manual_phase':
      body = <ManualPhase item={item} phaseTo={phaseItemHref(item)} />;
      break;
    case 'awaiting_merge':
      body = <AwaitingMerge item={item} reviewTo={phaseItemHref(item)} />;
      break;
    case 'stalled_phase':
      body = <StalledPhase item={item} phaseTo={phaseItemHref(item)} />;
      break;
    case 'phase_decision':
      body = <PhaseDecision item={item} reviewTo={phaseItemHref(item)} onAnswered={onAnswered} />;
      break;
    case 'worktree_cleanup':
      body = <WorktreeCleanup item={item} onApplied={onApplied} />;
      break;
  }
  // A phase row names a plan phase, not a session: its title opens the phase.
  // A cleanup row names a repository: its title opens the janitor's journal.
  const titleTo = item.kind === 'worktree_cleanup' ? '/settings' : (phaseItemHref(item) ?? sessionTo);

  return (
    <li
      data-testid="needs-you-row"
      data-kind={item.kind}
      aria-label={`${itemLabel(item)}: ${item.sessionName}`}
      className="rounded-[14px] border border-line bg-surface px-4 py-3.5"
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span
          className={`rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] font-semibold whitespace-nowrap ${KIND_BADGE[item.kind]}`}
        >
          {itemLabel(item)}
        </span>
        <Link to={titleTo} className={`min-w-0 truncate text-[13.5px] font-medium text-ink hover:text-brand ${FOCUS}`}>
          {item.sessionName}
        </Link>
        <ProjectName name={projectName} slug={item.projectSlug} className="truncate font-mono text-[10.5px]" />
        <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
          <Trans>blocked {age}</Trans>
        </span>
      </div>
      {body}
    </li>
  );
}

/* ----- page ----- */

export function NeedsYou(): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const scope = slug ?? null;
  const { items, loading, error, reload, dismiss } = useNeedsYou(scope);
  const { projects } = useScope();
  const sessionHref = useSessionHref();
  const nowMs = useNowMs();
  const inboxHref = slug === undefined ? '/inbox?tab=approvals' : `/p/${slug}/inbox?tab=approvals`;
  const blockedCount = String(items.length);

  // The pending rows behind approval / question items, so their cards are the
  // Approvals page's own controls. Refetched whenever that id set changes.
  const [requests, setRequests] = useState<ReadonlyMap<number, PermissionRequest>>(new Map());
  const requestIds = items
    .filter((it) => it.requestId !== null && (it.kind === 'approval' || it.kind === 'question'))
    .map((it) => String(it.requestId))
    .join(',');
  const loadRequests = useCallback((): void => {
    if (requestIds === '') return;
    fetchApprovals('pending', scope)
      .then((rows) => setRequests(new Map(rows.map((r) => [r.id, r]))))
      .catch(() => undefined); // the row falls back to "review in Inbox"
  }, [requestIds, scope]);
  useEffect(loadRequests, [loadRequests]);

  const [busyId, setBusyId] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const resolve = (request: PermissionRequest, action: ApprovalAction, reason?: string, answers?: AnswerMap): void => {
    setBusyId(request.id);
    setActionError(null);
    resolveApproval(request.id, action, reason, answers)
      .catch((e: unknown) => setActionError(e instanceof Error ? e.message : String(e)))
      .finally(() => {
        setBusyId(null);
        reload();
        loadRequests();
      });
  };

  return (
    <div className="px-4 pb-10 desk:px-10 desk:pb-[60px]">
      <div className="pt-6 pb-3.5 desk:pt-[34px]">
        <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
          <Trans>Needs you</Trans>
        </h1>
        <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
          {loading ? t`sessions blocked on you, oldest first` : t`${blockedCount} blocked on you · oldest first`}
        </div>
      </div>

      {error !== null && <ErrorBox message={error} onRetry={reload} />}
      {actionError !== null && (
        <div role="alert" className="mb-3 font-mono text-[11px] text-red">
          {actionError}
        </div>
      )}
      {loading && error === null && <Loading label={t`needs you…`} />}
      {!loading && error === null && items.length === 0 && (
        <Empty>
          <Trans>nothing needs you — approvals, questions and sessions awaiting your reply land here live</Trans>
        </Empty>
      )}

      {items.length > 0 && (
        <ul className="m-0 mt-2 flex list-none flex-col gap-2.5 p-0">
          {items.map((item) => (
            <Row
              key={`${item.kind}:${String(item.sessionId)}:${String(item.requestId)}:${String(item.phase?.phaseId)}:${String(item.cleanup?.id)}:${item.blockingSince}`}
              item={item}
              request={item.requestId !== null ? (requests.get(item.requestId) ?? null) : null}
              nowMs={nowMs}
              busy={item.requestId !== null && busyId === item.requestId}
              inboxHref={inboxHref}
              projectName={findProject(projects, item.projectSlug)?.name ?? null}
              sessionTo={sessionHref(item.sessionId)}
              onResolve={resolve}
              onDismiss={() => dismiss(item)}
              onAnswered={reload}
              onApplied={reload}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
