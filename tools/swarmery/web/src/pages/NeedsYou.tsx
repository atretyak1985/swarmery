// "Needs you" (needs-you-queue phase 5): every session blocked on the operator,
// oldest blocker first, from GET /api/needs-you — live over the shared WS
// stream (lib/useNeedsYou). One row per blocker:
//
//   approval / question  the existing Approvals PendingCard (approve / deny /
//                        answer), joined by requestId to the pending row
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
//
// Nothing here delivers a reply to a session: the operator pastes it.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { fetchApprovals, resolveApproval, type ApprovalAction } from '../api';
import type { NeedsYouItem, NeedsYouKind, PermissionRequest } from '../api/types';
import { PendingCard } from './Approvals';
import { ProjectName } from '../components/ProjectName';
import { Empty, ErrorBox, Loading } from '../components/ui';
import type { AnswerMap } from '../lib/approvals';
import { fmtSpan } from '../lib/format';
import { kindLabel, replyPrefix, replyWithOption } from '../lib/needsYou';
import { findProject } from '../lib/projectSlug';
import { useScope } from '../lib/scope';
import { useSessionHref } from '../lib/sessionHref';
import { useNowMs } from '../lib/sessionState';
import { useNeedsYou } from '../lib/useNeedsYou';
import { plansPath } from './plans/plansUrl';

/** Kind badge tones — the label always carries the meaning, colour only echoes it. */
const KIND_BADGE: Record<NeedsYouKind, string> = {
  approval: 'border-amber/40 text-amber',
  question: 'border-brand/50 text-brand',
  prod_deploy_local: 'border-amber/60 bg-amber/10 text-amber',
  awaiting_reply: 'border-amber/40 text-amber',
  failed: 'border-red/40 text-red',
  manual_phase: 'border-brand/40 text-brand',
};

const FOCUS = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand';
const BTN = `rounded-lg border px-3 py-1.5 font-mono text-[11.5px] transition-colors disabled:opacity-50 ${FOCUS}`;
const LINK = `font-mono text-[11px] text-ink-dim transition-colors hover:text-brand ${FOCUS}`;
const COPIED_MS = 2_000;

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
  const options = item.suggestion?.options ?? [];

  return (
    <>
      {question !== '' && (
        <blockquote className="mt-2 border-l-2 border-amber/40 pl-3 text-[13px] leading-[1.55] whitespace-pre-wrap text-ink-3 [text-wrap:pretty]">
          {question}
        </blockquote>
      )}
      {options.length > 0 && (
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5" role="group" aria-label="suggested replies">
          {options.map((opt) => {
            const recommended = opt === item.suggestion?.recommended;
            return (
              <button
                key={opt}
                type="button"
                onClick={() => copy(replyWithOption(item, opt))}
                aria-label={`Copy reply with option: ${opt}${recommended ? ' (suggested)' : ''}`}
                className={`${BTN} ${recommended ? 'border-green/45 text-green hover:bg-green/10' : 'border-line-strong text-ink-3 hover:bg-surface2'}`}
              >
                {opt}
                {recommended && <span className="ml-1.5 text-[10px] opacity-80">· suggested</span>}
              </button>
            );
          })}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => copy(replyPrefix(item))}
          aria-label={`Copy reply to ${item.sessionName}`}
          className={`${BTN} border-brand/50 bg-brand/10 font-semibold text-brand hover:bg-brand/20`}
        >
          Copy reply
        </button>
        <button
          type="button"
          onClick={onDismiss}
          aria-label={`Dismiss ${item.sessionName}`}
          className={`${BTN} border-line-strong text-ink-3 hover:bg-surface2`}
        >
          Dismiss
        </button>
        <span aria-live="polite" className="font-mono text-[11px] text-ink-dim">
          {copied === 'ok' ? 'Copied — paste it into the session' : copied === 'failed' ? 'Copy failed' : ''}
        </span>
        <Link to={sessionTo} className={`ml-auto ${LINK}`}>
          open session →
        </Link>
      </div>
    </>
  );
}

/* ----- prod_deploy_local: confirm in the terminal, no allow / deny ----- */

function ConfirmLocally({ item, sessionTo }: { item: NeedsYouItem; sessionTo: string }): JSX.Element {
  return (
    <>
      <p className="mt-2 text-[13px] leading-[1.55] text-ink-2">
        Production deploy — confirm in the session's terminal
      </p>
      {item.preview !== '' && (
        <code className="mt-2 block overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[11.5px] break-all whitespace-pre-wrap text-ink-3">
          {item.preview}
        </code>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-3">
        {item.termFocusUrl !== null && item.termFocusUrl !== '' && (
          <a
            href={item.termFocusUrl}
            aria-label={`Focus the terminal of ${item.sessionName}`}
            className={`${BTN} border-amber/50 text-amber hover:bg-amber/10`}
          >
            Focus terminal
          </a>
        )}
        <Link to={sessionTo} className={`ml-auto ${LINK}`}>
          open session →
        </Link>
      </div>
    </>
  );
}

/* ----- manual_phase: a plan phase only the operator can finish ----- */

/** The phase's Criteria tab — where the [MANUAL] criteria are ticked by hand. */
function manualPhaseHref(item: NeedsYouItem): string | null {
  if (item.phase === undefined || item.phase.planExternalId === '') return null;
  return plansPath(item.projectSlug, {
    plan: item.phase.planExternalId,
    detail: { kind: 'phase', seq: item.phase.seq, tab: 'criteria' },
  });
}

function ManualPhase({ item, phaseTo }: { item: NeedsYouItem; phaseTo: string | null }): JSX.Element {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1 text-[12.5px] text-ink-3">{item.preview}</span>
      {phaseTo !== null && (
        <Link to={phaseTo} className={`ml-auto ${LINK}`}>
          open criteria →
        </Link>
      )}
    </div>
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
}): JSX.Element {
  const age = fmtSpan(item.blockingSince, new Date(nowMs).toISOString());

  let body: JSX.Element;
  switch (item.kind) {
    case 'approval':
    case 'question':
      body =
        request !== null ? (
          <PendingCard
            request={request}
            session={null}
            nowMs={nowMs}
            busy={busy}
            onResolve={(action, reason, answers) => onResolve(request, action, reason, answers)}
          />
        ) : (
          <div className="mt-2 flex flex-wrap items-center gap-3">
            <code className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink-3">{item.preview}</code>
            <Link to={inboxHref} className={LINK}>
              review in Inbox →
            </Link>
          </div>
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
            open session →
          </Link>
        </div>
      );
      break;
    case 'manual_phase':
      body = <ManualPhase item={item} phaseTo={manualPhaseHref(item)} />;
      break;
  }
  // A manual_phase row names a plan phase, not a session: its title opens the phase.
  const titleTo = item.kind === 'manual_phase' ? (manualPhaseHref(item) ?? sessionTo) : sessionTo;

  return (
    <li
      data-testid="needs-you-row"
      data-kind={item.kind}
      aria-label={`${kindLabel(item.kind)}: ${item.sessionName}`}
      className="rounded-[14px] border border-line bg-surface px-4 py-3.5"
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span
          className={`rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] font-semibold whitespace-nowrap ${KIND_BADGE[item.kind]}`}
        >
          {kindLabel(item.kind)}
        </span>
        <Link to={titleTo} className={`min-w-0 truncate text-[13.5px] font-medium text-ink hover:text-brand ${FOCUS}`}>
          {item.sessionName}
        </Link>
        <ProjectName name={projectName} slug={item.projectSlug} className="truncate font-mono text-[10.5px]" />
        <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-ink-dim">blocked {age}</span>
      </div>
      {body}
    </li>
  );
}

/* ----- page ----- */

export function NeedsYou(): JSX.Element {
  const { slug } = useParams<{ slug?: string }>();
  const scope = slug ?? null;
  const { items, loading, error, reload, dismiss } = useNeedsYou(scope);
  const { projects } = useScope();
  const sessionHref = useSessionHref();
  const nowMs = useNowMs();
  const inboxHref = slug === undefined ? '/inbox?tab=approvals' : `/p/${slug}/inbox?tab=approvals`;

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
        <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">Needs you</h1>
        <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
          {loading ? 'sessions blocked on you, oldest first' : `${String(items.length)} blocked on you · oldest first`}
        </div>
      </div>

      {error !== null && <ErrorBox message={error} onRetry={reload} />}
      {actionError !== null && (
        <div role="alert" className="mb-3 font-mono text-[11px] text-red">
          {actionError}
        </div>
      )}
      {loading && error === null && <Loading label="needs you…" />}
      {!loading && error === null && items.length === 0 && (
        <Empty>nothing needs you — approvals, questions and sessions awaiting your reply land here live</Empty>
      )}

      {items.length > 0 && (
        <ul className="m-0 mt-2 flex list-none flex-col gap-2.5 p-0">
          {items.map((item) => (
            <Row
              key={`${item.kind}:${String(item.sessionId)}:${String(item.requestId)}:${String(item.phase?.phaseId)}:${item.blockingSince}`}
              item={item}
              request={item.requestId !== null ? (requests.get(item.requestId) ?? null) : null}
              nowMs={nowMs}
              busy={item.requestId !== null && busyId === item.requestId}
              inboxHref={inboxHref}
              projectName={findProject(projects, item.projectSlug)?.name ?? null}
              sessionTo={sessionHref(item.sessionId)}
              onResolve={resolve}
              onDismiss={() => dismiss(item)}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
