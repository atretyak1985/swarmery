// The right half of the Inbox split-pane (Canvas v3 artboard 2b; card anatomy
// from 1b). Every kind renders ONE anatomy:
//
//   kind dot + mono context line
//   a sentence title
//   what happened
//   "if you approve / if you accept …" consequence
//   ONE primary button (+ secondaries, + the deny/dismiss)
//
// The decisions themselves are the existing endpoints (resolveApproval,
// acceptLesson, patchRecommendation, …) — primaryAction / denyAction are
// exported so the page binds the SAME calls to the e / x keys.

import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { patchProposal, patchRecommendation, resolveApproval } from '../../api';
import { AUTO_MODE_NO_VERDICT_RULE, resumeAccount } from '../../api/alerts';
import { postGroundTruth, type QueueItem } from '../../api/decisions';
import { acceptLesson, confirmRetirement, dismissLesson, keepLesson } from '../../api/lessons';
import { acceptTriageVerdict, type TriageAudit } from '../../api/triage';
import { QuestionForm } from '../../components/QuestionForm';
import { questionsOf, requestSummary } from '../../lib/approvals';
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

type Action = () => Promise<unknown>;

/** The one primary decision of an item — bound to the `e` key. Null when the
 * item has no single yes (an AskUserQuestion approval needs its answers; a
 * production deploy is confirmed only in the session's own terminal). */
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
      if (item.raw.riskClass === 'prod-deploy') return null;
      return questionsOf(item.raw) === null ? () => resolveApproval(item.raw.id, 'approve') : null;
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
  }
}

/** The deny / dismiss / keep decision — bound to the `x` key. The classifier
 * has no "no" (a wrong answer is worse than none): skip instead. */
export function denyAction(item: InboxItem): Action | null {
  switch (item.kind) {
    case 'approval':
      return () => resolveApproval(item.raw.id, 'deny');
    case 'lesson':
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
  }
}

const BTN = 'rounded-[8px] border py-[7px] font-mono text-[11.5px] transition-colors disabled:opacity-50';
const PRIMARY_GREEN = `${BTN} border-green/45 bg-green/12 px-[18px] font-bold text-green hover:bg-green/20`;
const PRIMARY_BRAND = `${BTN} border-brand/50 bg-brand/10 px-[18px] font-bold text-brand hover:bg-brand/20`;
const SECONDARY = `${BTN} border-line-strong px-3 text-ink-3 hover:text-ink`;
const DENY = `${BTN} border-red/40 px-3 text-red hover:bg-red/10`;
const LINK = 'ml-auto font-mono text-[10.5px] text-ink-faint hover:text-ink-dim';

function KeyHint({ k }: { k: string }): JSX.Element | null {
  return k === '' ? null : <span className="ml-1 font-normal opacity-60">{k}</span>;
}

function Consequences({ yes, no }: { yes: [string, string]; no?: [string, string] }): JSX.Element {
  const box = (label: string, text: string, tone: string): JSX.Element => (
    <div className="rounded-[10px] border border-line px-3 py-2.5">
      <div className={`font-mono text-[10px] tracking-[0.1em] uppercase ${tone}`}>{label}</div>
      <div className="mt-1 text-[12px] leading-[1.5] text-ink-3">{text}</div>
    </div>
  );
  return (
    <div className={`mt-3.5 grid gap-2.5 ${no === undefined ? 'grid-cols-1' : 'grid-cols-2'}`}>
      {box(yes[0], yes[1], 'text-green')}
      {no !== undefined && box(no[0], no[1], 'text-red')}
    </div>
  );
}

function Body({ children }: { children: ReactNode }): JSX.Element {
  return <p className="mt-3 text-[13px] leading-[1.6] text-ink-3">{children}</p>;
}

function Code({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="mt-2.5 overflow-x-auto rounded-[8px] border border-line bg-bg px-3 py-2.5 font-mono text-[12.5px] whitespace-pre-wrap text-ink-2">
      {children}
    </div>
  );
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
  now?: number;
  /** How often the agent's audit-sample labels matched the operator's. */
  audit?: TriageAudit | null;
}): JSX.Element {
  const meta = KIND_META[item.kind];
  const primary = manualPrimaryAction(item);
  const suggestion = item.suggestion;
  const accept = suggestionAction(item);
  const hasSuggestion = accept !== null;
  // With a suggestion, "accept suggestion" owns the primary slot and the `e`
  // key; the item's own buttons stay as secondaries.
  const manualPrimary = hasSuggestion ? {} : { 'data-primary': '' };
  const manualKey = hasSuggestion ? '' : 'e';
  const primaryGreen = hasSuggestion ? SECONDARY : PRIMARY_GREEN;
  const primaryBrand = hasSuggestion ? SECONDARY : PRIMARY_BRAND;
  const deny = denyAction(item);
  const act = (a: Action | null) => (): void => {
    if (a !== null) onAct(a);
  };
  // While a suggestion is open the box describes what `e` does, not the item's ordinary yes/no.
  const suggestedBox =
    hasSuggestion && suggestion !== undefined ? suggestionConsequence(item, suggestion.value) : null;
  const Conseq = ({ yes, no }: { yes: [string, string]; no?: [string, string] }): JSX.Element =>
    suggestedBox === null ? (
      <Consequences yes={yes} {...(no === undefined ? {} : { no })} />
    ) : (
      <Consequences yes={['if you press e', suggestedBox]} />
    );

  let title: ReactNode = item.title;
  let body: ReactNode = null;
  let buttons: ReactNode = null;

  switch (item.kind) {
    case 'approval': {
      const r = item.raw;
      const questions = questionsOf(r);
      if (r.riskClass === 'prod-deploy') {
        // The daemon refuses a remote approve/answer with 403: no allow here.
        title = `Production deploy via ${r.toolName} — confirm locally`;
        body = (
          <>
            <Code>{requestSummary(r)}</Code>
            <Body>Production deploy — confirm in the session's terminal. Nothing runs until you do.</Body>
          </>
        );
        buttons = (
          <button type="button" className={DENY} disabled={busy} onClick={act(deny)}>
            deny
            <KeyHint k="x" />
          </button>
        );
      } else if (questions !== null) {
        title = `Claude is asking you ${String(questions.length)} question${questions.length === 1 ? '' : 's'}`;
        body = (
          <>
            <Body>The session waits for your answers; they go back to Claude as the tool's result.</Body>
            <div className="mt-3.5">
              <QuestionForm
                questions={questions}
                idNamespace={`inbox-${String(r.id)}`}
                busy={busy}
                onSubmit={(answers) => onAct(() => resolveApproval(r.id, 'answer', undefined, answers))}
              />
            </div>
          </>
        );
        buttons = (
          <button type="button" className={DENY} disabled={busy} onClick={act(deny)}>
            deny
            <KeyHint k="x" />
          </button>
        );
      } else {
        title = `Claude wants to use ${r.toolName}`;
        body = (
          <>
            <Code>{requestSummary(r)}</Code>
            <Body>
              Session {r.sessionId} asked {ageLabel(r.requestedAt, now)} ago and is waiting on you. Nothing
              runs until you answer.
            </Body>
            <Consequences
              yes={['if you approve', 'The call runs now and the session continues. Nothing else changes.']}
              no={['if you deny', 'Claude is told no and looks for another way, or stops.']}
            />
          </>
        );
        buttons = (
          <>
            <button type="button" {...manualPrimary} className={primaryGreen} disabled={busy} onClick={act(primary)}>
              approve
              <KeyHint k={manualKey} />
            </button>
            <button type="button" className={DENY} disabled={busy} onClick={act(deny)}>
              deny
              <KeyHint k="x" />
            </button>
          </>
        );
      }
      break;
    }
    case 'lesson': {
      const l = item.raw;
      title = `“${l.title}”`;
      const offPlan = l.surpriseIndex === null ? null : l.surpriseIndex.toFixed(2);
      body = (
        <>
          <Body>
            <b className="font-medium text-ink-2">What happened:</b>{' '}
            {l.cause !== '' ? l.cause : l.sourceParagraph}
          </Body>
          {l.guidance !== '' && <Code>{l.guidance}</Code>}
          <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
            seen {l.recurrences}×{offPlan !== null && ` · ${UI_TERMS.surprise.ui} ${offPlan}`}
            {l.phaseName !== '' && ` · ${l.phaseName}`}
          </div>
          <Conseq
            yes={[
              'if you accept',
              `Every future run touching ${l.areaGlobs.length > 0 ? l.areaGlobs.join(', ') : 'these areas'} gets this sentence in its brief. We then watch whether those runs land closer to plan.`,
            ]}
          />
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryBrand} disabled={busy} onClick={act(primary)}>
            accept
            <KeyHint k={manualKey} />
          </button>
          <Link to="/lessons" className={SECONDARY}>
            edit wording
          </Link>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            not useful
            <KeyHint k="x" />
          </button>
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
          <Conseq
            yes={[
              'if you accept',
              "We snapshot today's number as the baseline, notice when the target changes, and a week later tell you whether it moved.",
            ]}
          />
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryBrand} disabled={busy} onClick={act(primary)}>
            I'll fix it — track this
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            dismiss
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'proposal': {
      const p = item.raw;
      title = `A change to ${p.agent}, drafted from its evidence`;
      body = (
        <>
          <Body>{p.rationale}</Body>
          <div className="mt-2.5 max-h-[260px] overflow-auto rounded-[8px] border border-line bg-bg px-3 py-2.5 font-mono text-[11.5px] whitespace-pre text-ink-2">
            {p.diff}
          </div>
          <Consequences
            yes={['if you approve', 'The proposal is marked approved; applying it opens a pull request you review.']}
            no={['if you reject', 'The draft is closed. Nothing in the agent changes.']}
          />
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryBrand} disabled={busy} onClick={act(primary)}>
            approve
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={DENY} disabled={busy} onClick={act(deny)}>
            reject
            <KeyHint k="x" />
          </button>
        </>
      );
      break;
    }
    case 'classifier': {
      const group = item.raw;
      title = (
        <>
          Was this{' '}
          {group.map((q, i) => (
            <span key={q.id}>
              {i > 0 && (i === group.length - 1 ? ' and ' : ', ')}
              <span className="text-brand">{q.answer}</span>
            </span>
          ))}
          ?
        </>
      );
      body = (
        <>
          <Body>
            The local model's guess about “{group[0]?.sessionTitle ?? ''}”. Your answer is the only thing that
            tells us whether to trust it. If you don't remember the session, skip — a wrong answer is worse
            than none.
          </Body>
          <div className="mt-3.5 flex flex-col gap-2.5">
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
                      className={`${BTN} px-2.5 ${opt === q.answer ? 'border-brand/50 text-brand' : 'border-line-strong text-ink-3 hover:text-ink'}`}
                    >
                      {opt}
                      {suggestion?.answers?.[q.id] === opt && (
                        <span className="ml-1.5 font-normal text-ink-faint">· agent says</span>
                      )}
                    </button>
                  ))}
                </div>
              </div>
            ))}
            {audit !== null && audit.answered > 0 && (
              <div className="font-mono text-[10.5px] text-ink-faint">
                agent matched you on {audit.agree} of {audit.answered}
              </div>
            )}
          </div>
        </>
      );
      buttons = (
        <button type="button" {...manualPrimary} className={primaryGreen} disabled={busy} onClick={act(primary)}>
          {group.length === 1 ? 'yes' : `yes, all ${String(group.length)}`}
          <KeyHint k={manualKey} />
        </button>
      );
      break;
    }
    case 'retire': {
      const p = item.raw;
      body = (
        <>
          <Body>
            <b className="font-medium text-ink-2">What happened:</b> {p.detail}
          </Body>
          <Code>{p.guidance}</Code>
          <Conseq
            yes={['if you confirm', 'The lesson stops appearing in briefs. Its history stays.']}
            no={['if you keep it', 'The proposal closes and this reason is held off for 30 days.']}
          />
          {p.autoRetireAt !== null && (
            <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
              retires on its own in {expiresInLabel(p.autoRetireAt, now)} if nobody answers
            </div>
          )}
        </>
      );
      buttons = (
        <>
          <button type="button" {...manualPrimary} className={primaryBrand} disabled={busy} onClick={act(primary)}>
            stop using it
            <KeyHint k={manualKey} />
          </button>
          <button type="button" className={SECONDARY} disabled={busy} onClick={act(deny)}>
            keep it
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
            <Body>
              <b className="font-medium text-ink-2">What happened:</b> {a.message}
            </Body>
            {a.rule === AUTO_MODE_NO_VERDICT_RULE && (
              <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
                nothing to decide here — the check runs on Claude's side, and this alert closes on its own after
                30 minutes without a failed check
              </div>
            )}
          </>
        );
        break;
      }
      const quota = a.kind === 'quota';
      title = `Account ${a.account} is paused`;
      body = (
        <>
          <Body>
            <b className="font-medium text-ink-2">What happened:</b>{' '}
            {quota
              ? 'This account hit a Claude usage limit.'
              : 'Claude refused this account: its sign-in expired, or its access was switched off.'}{' '}
            The daemon stopped starting cards, phases and plans on it {ageLabel(item.ageIso, now)} ago, so no
            more runs are spent finding that out.
          </Body>
          {a.reason !== undefined && a.reason !== '' && <Code>{a.reason}</Code>}
          <Consequences
            yes={[
              'if you probe & resume',
              'The account is checked for real — its sign-in, then one short model call. If it answers, runs start again at once; if not, it stays paused and you see why.',
            ]}
          />
          <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
            {quota && a.resetsAt !== undefined && a.resetsAt !== ''
              ? `resumes on its own in ${expiresInLabel(a.resetsAt, now)}, when the limit resets`
              : 'stays paused until a check succeeds — sign in to the account again first if it needs it'}
          </div>
        </>
      );
      buttons = (
        <button type="button" {...manualPrimary} className={primaryBrand} disabled={busy} onClick={act(primary)}>
          probe &amp; resume
          <KeyHint k={manualKey} />
        </button>
      );
      break;
    }
  }

  return (
    <article aria-label={item.title} className="flex min-h-full flex-col px-[26px] py-[22px]">
      <div className="flex items-center gap-2 font-mono text-[10px] tracking-[0.12em] uppercase">
        <span className={meta.text}>● {meta.label}</span>
        <span className="truncate text-ink-faint">· {item.context}</span>
        {fleetWide && <span className="text-ink-faint">· fleet-wide</span>}
        {item.expiresIso !== undefined && item.urgent && (
          <span className="ml-auto tracking-normal text-amber normal-case">
            expires in {expiresInLabel(item.expiresIso, now)}
          </span>
        )}
      </div>
      <h2 className="mt-3 font-display text-[19px] leading-[1.3] font-medium text-ink">{title}</h2>
      {body}
      {hasSuggestion && suggestion !== undefined && (
        <section aria-label="agent suggestion" className="mt-3.5 rounded-[10px] border border-brand/30 bg-brand/5 px-3 py-2.5">
          <div className="text-[12.5px] font-medium text-ink-2">agent suggests: {valueWording(suggestion.value)}</div>
          {suggestion.reason !== '' && (
            <div className="mt-1 text-[12px] leading-[1.5] text-ink-3">{suggestion.reason}</div>
          )}
          {suggestion.card !== undefined && (
            <>
              <div className="mt-2 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">the task</div>
              <Code>
                {suggestion.card.title}
                {'\n\n'}
                {suggestion.card.prompt}
              </Code>
              <div className="mt-1.5 text-[11.5px] text-ink-faint">
                Accepting creates a task on the project's board; an agent picks it up.
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
      <div className="mt-auto flex flex-wrap items-center gap-2 pt-[18px]">
        {hasSuggestion && (
          <button type="button" data-primary="" className={PRIMARY_GREEN} disabled={busy} onClick={act(accept)}>
            {suggestion === undefined ? 'accept' : suggestionButtonLabel(suggestion.value)}
            <KeyHint k="e" />
          </button>
        )}
        {buttons}
        {(item.kind === 'approval' || item.kind === 'classifier') && (
          <Link
            to={`/sessions/${item.kind === 'approval' ? String(item.raw.sessionId) : (item.raw[0]?.sessionUuid ?? '')}`}
            className={LINK}
          >
            open session →
          </Link>
        )}
      </div>
    </article>
  );
}
