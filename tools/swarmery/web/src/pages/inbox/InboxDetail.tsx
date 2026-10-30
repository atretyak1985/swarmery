// The detail of the Inbox (handoff §8). Every kind renders ONE anatomy:
//
//   kind dot + mono context line
//   a sentence title
//   what happened, then the evidence in a mono block
//   the two cards: "if you accept" / "if you dismiss"
//   ONE primary button (+ secondaries, + the deny/dismiss)
//
// The decisions themselves are the existing endpoints (acceptLesson,
// patchRecommendation, …) — primaryAction / denyAction are exported so the page
// binds the SAME calls to the e / x keys. An approval never gets here: it is
// decided in Needs you, so both return null for one.

import { t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { patchProposal, patchRecommendation } from '../../api';
import { AUTO_MODE_NO_VERDICT_RULE, resumeAccount } from '../../api/alerts';
import { postGroundTruth, type QueueItem } from '../../api/decisions';
import { acceptLesson, confirmRetirement, dismissLesson, keepLesson } from '../../api/lessons';
import { ackReview } from '../../api/reviews';
import { acceptTriageVerdict, type TriageAudit } from '../../api/triage';
import { UI_TERMS } from '../../lib/glossary';
import {
  ageLabel,
  expiresInLabel,
  isAccountBreaker,
  KIND_META,
  suggestionButtonLabel,
  suggestionConsequence,
  valueWording,
  type InboxItem,
} from './inboxModel';
import { ReviewDetail } from './ReviewItem';

type Action = () => Promise<unknown>;

/** The one primary decision of an item — bound to the `e` key. Null when the
 * item has no single yes (an alert that only reports). */
export function primaryAction(item: InboxItem): Action | null {
  return suggestionAction(item) ?? manualPrimaryAction(item);
}

/** Accepting the agent's open (non-sample) suggestion, when the item has one. */
function suggestionAction(item: InboxItem): Action | null {
  const suggested = item.suggestion;
  if (suggested === undefined || suggested.sample) return null;
  const id = suggested.verdictIds[0];
  return id === undefined ? null : () => acceptTriageVerdict(id);
}

/** The item's own yes — what `e` did before the agent could suggest anything. */
function manualPrimaryAction(item: InboxItem): Action | null {
  switch (item.kind) {
    case 'approval':
      return null;
    case 'lesson':
      return () => acceptLesson(item.raw.id);
    case 'advisor':
      return () => patchRecommendation(item.raw.id, 'accepted');
    case 'proposal':
      return () => patchProposal(item.raw.id, 'approved');
    case 'classifier': {
      const group = item.raw;
      return () => Promise.all(group.map((q) => postGroundTruth(q.id, q.answer)));
    }
    case 'retire':
      return () => confirmRetirement(item.raw.id);
    case 'alert': {
      // "Probe & resume" — only a paused account has an action to take.
      const alert = item.raw;
      return isAccountBreaker(alert) ? () => resumeAccount(alert.account) : null;
    }
    case 'review':
      return () => ackReview(item.raw.id);
  }
}

/** The dismiss / keep decision — bound to the `x` key. The classifier has no
 * "no" (a wrong answer is worse than none): its `x` skips, which the page owns. */
export function denyAction(item: InboxItem): Action | null {
  switch (item.kind) {
    case 'approval':
      return null;
    case 'lesson':
      // i18n-ignore: dismissal reason stored server-side, not UI copy
      return () => dismissLesson(item.raw.id, 'not useful');
    case 'advisor':
      return () => patchRecommendation(item.raw.id, 'dismissed');
    case 'proposal':
      return () => patchProposal(item.raw.id, 'rejected');
    case 'classifier':
      return null;
    case 'retire':
      return () => keepLesson(item.raw.id);
    case 'alert':
      // An alert cannot be dismissed: it goes away when what it reports does.
      return null;
    case 'review':
      // Acknowledging is the only decision; there is nothing to refuse.
      return null;
  }
}

const BTN = 'rounded-[7px] border px-[13px] py-[7px] font-mono text-[11.5px] leading-[normal] disabled:opacity-50';
const PRIMARY = `${BTN} border-brand bg-brand-bg font-bold text-brand`;
const SECONDARY = `${BTN} border-line-strong bg-transparent text-ink-2`;
const LINK = 'ml-auto font-mono text-[10.5px] leading-[normal] text-ink-faint transition-colors hover:text-brand';

function KeyHint({ k }: { k: string }): JSX.Element | null {
  return k === '' ? null : <span className="ml-1 font-normal opacity-55">{k}</span>;
}

function Consequences({ yes, no }: { yes: [string, string]; no?: [string, string] }): JSX.Element {
  const box = (label: string, text: string, tone: string): JSX.Element => (
    <div className="rounded-[8px] border border-line px-3 py-[10px]">
      <div className={`font-mono text-[10px] tracking-[0.1em] uppercase ${tone}`}>{label}</div>
      <div className="mt-1 text-[12.5px] leading-[1.5] text-ink-3">{text}</div>
    </div>
  );
  return (
    <div className="mt-[14px] grid max-w-[680px] grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-[10px]">
      {box(yes[0], yes[1], 'text-green')}
      {no !== undefined && box(no[0], no[1], 'text-red')}
    </div>
  );
}

function Body({ children }: { children: ReactNode }): JSX.Element {
  return <p className="mt-[10px] mb-0 max-w-[70ch] text-[13.5px] leading-[1.6] text-ink-2">{children}</p>;
}

/** The `evidence` block of handoff §8: a mono block under a small label. */
function Evidence({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="mt-3 max-w-[620px] rounded-[8px] border border-line px-3 py-[10px]">
      <div className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        <Trans>evidence</Trans>
      </div>
      <div className="mt-1 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap text-ink-3">{children}</div>
    </div>
  );
}

function Code({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="mt-[10px] max-w-[620px] overflow-x-auto rounded-[6px] border border-line bg-bg px-[10px] py-2 font-mono text-[12px] whitespace-pre-wrap text-ink-2">
      {children}
    </div>
  );
}

/** A change to an agent, added lines tinted. */
function Diff({ text }: { text: string }): JSX.Element {
  const lines = text.split('\n').map((line, n) => ({ id: String(n), line }));
  return (
    <div className="mt-3 max-h-[260px] max-w-[680px] overflow-auto rounded-[8px] border border-line bg-bg py-2 font-mono text-[11px] leading-[1.6]">
      {lines.map(({ id, line }) => (
        <div
          key={id}
          className={`px-3 whitespace-pre ${
            line.startsWith('+') ? 'bg-green-bg text-green' : line.startsWith('-') ? 'bg-red-bg text-red' : 'text-ink-3'
          }`}
        >
          {line === '' ? ' ' : line}
        </div>
      ))}
    </div>
  );
}

function agreeLine(agree: number, answered: number): string {
  const matched = String(agree);
  const total = String(answered);
  return t`agent matched you on ${matched} of ${total}`;
}

function resumesLine(resetsIn: string): string {
  return t`resumes on its own in ${resetsIn}, when the limit resets`;
}

function questionLabel(q: QueueItem): string {
  const tail = q.questionId.split('.').pop() ?? q.questionId;
  return tail.replace(/_/g, ' ');
}

export function InboxDetail({
  item,
  fleetWide,
  busy,
  error,
  onAct,
  onSkip,
  now = Date.now(),
  audit = null,
}: {
  item: InboxItem;
  /** Shown under a project scope for the sources that do not narrow. */
  fleetWide: boolean;
  busy: boolean;
  error: string | null;
  /** Runs a decision; the page owns busy/error state and the refetch. */
  onAct: (action: Action) => void;
  /** Moves on without answering: the classifier's `x`. */
  onSkip: () => void;
  now?: number;
  /** How often the agent's audit-sample labels matched the operator's. */
  audit?: TriageAudit | null;
}): JSX.Element {
  const { t } = useLingui();
  const meta = KIND_META[item.kind];
  const primary = manualPrimaryAction(item);
  const suggestion = item.suggestion;
  const accept = suggestionAction(item);
  const hasSuggestion = accept !== null;
  // With a suggestion, "accept suggestion" owns the primary slot and the `e`
  // key; the item's own buttons stay as secondaries.
  const manualPrimary = hasSuggestion ? {} : { 'data-primary': '' };
  const manualKey = hasSuggestion ? '' : 'e';
  const primaryClass = hasSuggestion ? SECONDARY : PRIMARY;
  const deny = denyAction(item);
  const act = (a: Action | null) => (): void => {
    if (a !== null) onAct(a);
  };
  // While a suggestion is open the box describes what `e` does, not the item's ordinary yes/no.
  const suggestedBox =
    hasSuggestion && suggestion !== undefined ? suggestionConsequence(item, suggestion.value) : null;
  const conseq = (yes: [string, string], no?: [string, string]): JSX.Element =>
    suggestedBox === null ? (
      <Consequences yes={yes} {...(no === undefined ? {} : { no })} />
    ) : (
      <Consequences yes={[t`if you press e`, suggestedBox]} />
    );

  let title: ReactNode = item.title;
  let body: ReactNode = null;
  let buttons: ReactNode = null;

  switch (item.kind) {
    case 'approval':
      break;
    case 'lesson': {
      const l = item.raw;
      title = `“${l.title}”`;
      const offPlan = l.surpriseIndex === null ? null : l.surpriseIndex.toFixed(2);
      const recurrences = l.recurrences;
      const areas = l.areaGlobs.length > 0 ? l.areaGlobs.join(', ') : t`these areas`;
      body = (
        <>
          <Body>{l.cause !== '' ? l.cause : l.sourceParagraph}</Body>
          {l.guidance !== '' && <Code>{l.guidance}</Code>}
          <Evidence>
            <Trans>seen {recurrences}×</Trans>
            {offPlan !== null && ` · ${UI_TERMS.surprise.ui} ${offPlan}`}
            {l.phaseName !== '' && ` · ${l.phaseName}`}
          </Evidence>
          {conseq(
            [
              t`if you accept`,
              t`Every future run touching ${areas} gets this sentence in its brief. We then watch whether those runs land closer to plan.`,
            ],
            [t`if you dismiss`, t`The candidate is closed as not useful. Nothing is added to any brief.`],
          )}
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
            <Trans>accept lesson</Trans>
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            <Trans>dismiss</Trans>
            <KeyHint k="x" />
          </button>
          <Link to="/lessons" className={LINK}>
            <Trans>edit wording →</Trans>
          </Link>
        </>
      );
      break;
    }
    case 'advisor': {
      const r = item.raw;
      title = r.title;
      body = (
        <>
          <Body>{r.detail}</Body>
          <Evidence>{`${r.rule} · ${r.target}`}</Evidence>
          {conseq(
            [
              t`if you track`,
              t`We snapshot today's number as the baseline, notice when the target changes, and a week later tell you whether it moved.`,
            ],
            [t`if you dismiss`, t`The recommendation is closed. It reopens on its own if the rule keeps firing.`],
          )}
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
            <Trans>track this</Trans>
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            <Trans>dismiss</Trans>
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'proposal': {
      const p = item.raw;
      const agent = p.agent;
      title = t`A change to ${agent}, drafted from its evidence`;
      body = (
        <>
          <Body>{p.rationale}</Body>
          <Diff text={p.diff} />
          <Consequences
            yes={[t`if you approve`, t`The proposal is marked approved; applying it opens a pull request you review.`]}
            no={[t`if you reject`, t`The draft is closed. Nothing in the agent changes.`]}
          />
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
            <Trans>approve</Trans>
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            <Trans>reject</Trans>
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'classifier': {
      const group = item.raw;
      const and = t`and`;
      const answers = (
        <>
          {group.map((q, i) => (
            <span key={q.id}>
              {i > 0 && (i === group.length - 1 ? ` ${and} ` : ', ')}
              <span className="text-brand">{q.answer}</span>
            </span>
          ))}
        </>
      );
      const sessionTitle = group[0]?.sessionTitle ?? '';
      const groupSize = group.length;
      title = <Trans>Was this {answers}?</Trans>;
      body = (
        <>
          <Body>
            <Trans>
              The local model's guess about “{sessionTitle}”. Your answer is the only thing that tells us
              whether to trust it. If you don't remember the session, skip — a wrong answer is worse than
              none.
            </Trans>
          </Body>
          <div className="mt-3 flex flex-col gap-2.5">
            {group.map((q) => (
              <div key={q.id}>
                <div className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
                  {questionLabel(q)}
                </div>
                <div className="mt-1 flex flex-wrap gap-1.5">
                  {q.options.map((opt) => (
                    <button
                      key={opt}
                      type="button"
                      disabled={busy}
                      onClick={() => onAct(() => postGroundTruth(q.id, opt))}
                      className={`${BTN} ${opt === q.answer ? 'border-brand text-brand' : 'border-line-strong text-ink-3 hover:text-ink'}`}
                    >
                      {opt}
                      {suggestion?.answers?.[q.id] === opt && (
                        <span className="ml-1.5 font-normal text-ink-faint">
                          <Trans>· agent says</Trans>
                        </span>
                      )}
                    </button>
                  ))}
                </div>
              </div>
            ))}
            {audit !== null && audit.answered > 0 && (
              <div className="font-mono text-[10.5px] text-ink-faint">
                {agreeLine(audit.agree, audit.answered)}
              </div>
            )}
          </div>
          {conseq(
            [t`if right`, t`The label is confirmed as the ground truth for this session.`],
            [t`if wrong`, t`Pick the correct answer above: it is recorded instead of the guess.`],
          )}
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
            {group.length === 1 ? t`✓ right` : t`✓ right, all ${groupSize}`}
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={onSkip}>
            <Trans>skip</Trans>
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'retire': {
      const p = item.raw;
      const retiresIn = p.autoRetireAt !== null ? expiresInLabel(p.autoRetireAt, now) : '';
      body = (
        <>
          <Body>{p.detail}</Body>
          <Code>{p.guidance}</Code>
          {conseq(
            [t`if you retire`, t`The lesson stops appearing in briefs. Its history stays.`],
            [t`if you keep`, t`The proposal closes and this reason is held off for 30 days.`],
          )}
          {p.autoRetireAt !== null && (
            <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
              <Trans>retires on its own in {retiresIn} if nobody answers</Trans>
            </div>
          )}
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
            <Trans>retire</Trans>
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            <Trans>keep</Trans>
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'alert': {
      const a = item.raw;
      if (!isAccountBreaker(a)) {
        // An alert with no action of its own: say what it reports and where.
        body = (
          <>
            <Body>{a.message}</Body>
            {a.rule === AUTO_MODE_NO_VERDICT_RULE && (
              <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
                <Trans>
                  nothing to decide here — the check runs on Claude's side, and this alert closes on its own
                  after 30 minutes without a failed check
                </Trans>
              </div>
            )}
          </>
        );
        break;
      }
      const quota = a.kind === 'quota';
      const account = a.account;
      const stoppedAgo = ageLabel(item.ageIso, now);
      title = t`Account ${account} is paused`;
      body = (
        <>
          <Body>
            {quota
              ? t`This account hit a Claude usage limit.`
              : t`Claude refused this account: its sign-in expired, or its access was switched off.`}{' '}
            <Trans>
              The daemon stopped starting cards, phases and plans on it {stoppedAgo} ago, so no more runs
              are spent finding that out.
            </Trans>
          </Body>
          {a.reason !== undefined && a.reason !== '' && <Code>{a.reason}</Code>}
          <Consequences
            yes={[
              t`if you probe & resume`,
              t`The account is checked for real — its sign-in, then one short model call. If it answers, runs start again at once; if not, it stays paused and you see why.`,
            ]}
          />
          <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
            {quota && a.resetsAt !== undefined && a.resetsAt !== ''
              ? resumesLine(expiresInLabel(a.resetsAt, now))
              : t`stays paused until a check succeeds — sign in to the account again first if it needs it`}
          </div>
        </>
      );
      buttons = (
        <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
          <Trans>probe &amp; resume</Trans>
          <KeyHint k={manualKey} />
        </button>
      );
      break;
    }
    case 'review': {
      body = (
        <>
          <ReviewDetail review={item.raw} now={now} />
          <Consequences
            yes={[t`if you ack`, t`The review leaves the Inbox. It stays on record; nothing else changes.`]}
          />
        </>
      );
      buttons = (
        <button type="button" {...manualPrimary} className={primaryClass} disabled={busy} onClick={act(primary)}>
          <Trans>ack</Trans>
          <KeyHint k={manualKey} />
        </button>
      );
      break;
    }
  }

  const expiresIn = item.expiresIso !== undefined ? expiresInLabel(item.expiresIso, now) : '';
  const suggestedValue = suggestion !== undefined ? valueWording(suggestion.value) : '';

  return (
    <article
      aria-label={item.title}
      className="box-border flex min-h-full max-w-[900px] flex-col px-7 pt-[22px] pb-10"
    >
      <div className="flex min-w-0 items-center gap-2 font-mono text-[10.5px] leading-[normal]">
        <span className={`shrink-0 whitespace-nowrap ${meta.text}`}>● {meta.label}</span>
        <span className="min-w-0 truncate text-ink-faint">· {item.context}</span>
        {fleetWide && (
          <span className="shrink-0 text-ink-faint">
            <Trans>· fleet-wide</Trans>
          </span>
        )}
        {item.expiresIso !== undefined && item.urgent && (
          <span className="ml-auto shrink-0 text-amber">
            <Trans>expires in {expiresIn}</Trans>
          </span>
        )}
      </div>
      <h2 className="mt-[10px] mb-0 text-[20px] leading-[1.3] font-semibold tracking-[-0.015em] text-balance text-ink">
        {title}
      </h2>
      {body}
      {hasSuggestion && suggestion !== undefined && (
        <section
          aria-label={t`agent suggestion`}
          className="mt-[14px] max-w-[680px] rounded-[8px] border border-brand/30 bg-brand-bg px-3 py-[10px]"
        >
          <div className="text-[12.5px] font-medium text-ink-2">
            <Trans>agent suggests: {suggestedValue}</Trans>
          </div>
          {suggestion.reason !== '' && (
            <div className="mt-1 text-[12px] leading-[1.5] text-ink-3">{suggestion.reason}</div>
          )}
          {suggestion.card !== undefined && (
            <>
              <div className="mt-2 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
                <Trans>the task</Trans>
              </div>
              <Code>
                {suggestion.card.title}
                {'\n\n'}
                {suggestion.card.prompt}
              </Code>
              <div className="mt-1.5 text-[11.5px] text-ink-faint">
                <Trans>Accepting creates a task on the project's board; an agent picks it up.</Trans>
              </div>
            </>
          )}
        </section>
      )}
      {error !== null && (
        <div role="alert" className="mt-3 font-mono text-[11px] text-red">
          {error}
        </div>
      )}
      <div className="mt-auto flex flex-wrap items-center gap-2 pt-[22px]">
        {hasSuggestion && (
          <button type="button" data-primary="" className={PRIMARY} disabled={busy} onClick={act(accept)}>
            {suggestion === undefined ? t`accept` : suggestionButtonLabel(suggestion.value)}
            <KeyHint k="e" />
          </button>
        )}
        {buttons}
        {item.kind === 'classifier' && (
          <Link to={`/sessions/${item.raw[0]?.sessionUuid ?? ''}`} className={LINK}>
            <Trans>open session →</Trans>
          </Link>
        )}
      </div>
    </article>
  );
}
