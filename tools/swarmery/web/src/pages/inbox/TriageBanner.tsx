// The Inbox's status strip for the triage agent. Independent lines, each shown
// only when it applies, in this order: progress of a running run; the result of
// a run this tab saw end (dismissable); the result of "accept all"; the
// suggestions waiting (whenever any are open, with or without a finished run);
// the offer to run triage (not while one runs); an error (dismissable, never
// shown while a run is active, and it never hides the offer or the
// suggestions). Driven entirely by props — the page owns the data and the
// actions; `error` alone decides whether the error line shows.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { AgentOffer } from './inboxModel';

const BTN =
  'rounded-[8px] border border-line-strong px-3 py-[5px] font-mono text-[11px] text-ink-3 transition-colors hover:text-ink disabled:opacity-50';
const BTN_PRIMARY =
  'rounded-[8px] border border-brand/50 bg-brand/10 px-3 py-[5px] font-mono text-[11px] font-bold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50';
const LINE = 'flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[10px] border px-3.5 py-2 text-[12.5px]';

export interface TriageProgress {
  done: number;
  total: number;
}

export interface TriageSummary {
  applied: number;
  suggested: number;
  failed: number;
}

/** The agent's open (non-sample) suggestions, as suggestionBreakdown groups them. */
export interface TriageSuggestions {
  /** Every open suggestion, fix tasks included. */
  total: number;
  /** How many "accept all" will confirm (everything but fix tasks). */
  acceptable: number;
  /** Open fix-task suggestions: left for the operator to open one by one. */
  fixTasks: number;
  /** "2 accept · 1 dismiss" pieces. */
  parts: readonly string[];
}

export interface TriageBannerProps {
  offer: AgentOffer;
  /** Progress of the active run, or null when none is running. */
  running: TriageProgress | null;
  /** The run that ended during this visit, or null. */
  summary: TriageSummary | null;
  /** Start error, or the failed run's error text; null hides the error line. */
  error: string | null;
  suggestions: TriageSuggestions;
  /** One-line result of the last "accept all". */
  acceptResult: string | null;
  /** The operator closed the run-result line. */
  dismissed: boolean;
  busy: boolean;
  onStart: () => void;
  onAcceptAll: () => void;
  onOpenHandled: () => void;
  onDismiss: () => void;
  onDismissError: () => void;
}

export function TriageBanner(p: TriageBannerProps): JSX.Element | null {
  const { t } = useLingui();
  const running = p.running !== null;
  const showResult = p.summary !== null && !p.dismissed;
  const showError = p.error !== null && !running;
  const { suggestions: s } = p;
  const showSuggestions = s.total > 0;
  const showOffer = p.offer.show && !running;
  if (!running && !showResult && p.acceptResult === null && !showSuggestions && !showOffer && !showError) {
    return null;
  }
  const nAccept = s.acceptable;
  const done = p.running?.done ?? 0;
  const total = p.running?.total ?? 0;
  const applied = p.summary?.applied ?? 0;
  const suggested = p.summary?.suggested ?? 0;
  const leftText = plural(suggested, {
    one: '# suggestion',
    few: '# suggestions',
    many: '# suggestions',
    other: '# suggestions',
  });
  const failedCount = p.summary?.failed ?? 0;
  const failed = String(failedCount);
  const suggestionTotal = s.total;
  const totalText = plural(suggestionTotal, {
    one: '# suggestion',
    few: '# suggestions',
    many: '# suggestions',
    other: '# suggestions',
  });
  const fixTasks = s.fixTasks;
  const fixTasksText = plural(fixTasks, {
    one: '# fix task',
    few: '# fix tasks',
    many: '# fix tasks',
    other: '# fix tasks',
  });
  const acceptText = plural(nAccept, {
    one: '# suggestion',
    few: '# suggestions',
    many: '# suggestions',
    other: '# suggestions',
  });
  const offerAgent = p.offer.agent;
  const offerYou = p.offer.you;

  return (
    <div className="mx-9 my-3 flex flex-col gap-2">
      {p.running !== null && (
        <div role="status" aria-live="polite" className={`${LINE} border-brand/30 bg-brand/5 text-ink-2`}>
          <Trans>
            triage running · {done} of {total}
          </Trans>
        </div>
      )}

      {showResult && p.summary !== null && (
        <div className={`${LINE} border-green/30 bg-green/5 text-ink-2`}>
          <span role="status">
            <Trans>
              agent closed {applied} · left {leftText}
            </Trans>
            {failedCount > 0 && ` · ${t`${failed} failed`}`}
          </span>
          <span className="ml-auto flex flex-wrap items-center gap-2">
            <button type="button" className={BTN} onClick={p.onOpenHandled}>
              <Trans>see what it closed</Trans>
            </button>
            <button type="button" className={BTN} onClick={p.onDismiss}>
              <Trans>dismiss</Trans>
            </button>
          </span>
        </div>
      )}

      {p.acceptResult !== null && (
        <div role="status" className={`${LINE} border-line bg-bg text-ink-2`}>
          {p.acceptResult}
        </div>
      )}

      {showSuggestions && (
        <div className={`${LINE} border-brand/30 bg-brand/5 text-ink-2`}>
          <span>
            <Trans>{totalText} from the agent</Trans>
            {nAccept > 0 && <span className="text-ink-3"> · {s.parts.join(' · ')}</span>}
            {s.fixTasks > 0 && (
              <span className="text-ink-3">
                {' '}
                · <Trans>{fixTasksText} to read first</Trans>
              </span>
            )}
          </span>
          {nAccept > 0 && (
            <button type="button" className={`${BTN_PRIMARY} ml-auto`} disabled={p.busy} onClick={p.onAcceptAll}>
              {p.busy ? t`accepting…` : t`accept ${acceptText}`}
            </button>
          )}
        </div>
      )}

      {showOffer && (
        <div className={`${LINE} border-line bg-bg text-ink-2`}>
          <span>
            <Trans>
              {offerAgent} can be handled by an agent · {offerYou} need you
            </Trans>
          </span>
          <button type="button" className={`${BTN_PRIMARY} ml-auto`} disabled={p.busy} onClick={p.onStart}>
            <Trans>run triage</Trans>
          </button>
        </div>
      )}

      {showError && (
        <div className={`${LINE} border-red/40 bg-red/5 text-red`}>
          <span role="alert">{p.error}</span>
          <span className="ml-auto flex flex-wrap items-center gap-2">
            <button type="button" className={BTN} disabled={p.busy} onClick={p.onStart}>
              <Trans>retry</Trans>
            </button>
            <button type="button" className={BTN} aria-label={t`dismiss error`} onClick={p.onDismissError}>
              <Trans>dismiss</Trans>
            </button>
          </span>
        </div>
      )}
    </div>
  );
}
