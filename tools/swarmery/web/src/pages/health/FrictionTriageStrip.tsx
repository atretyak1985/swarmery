// Health → Friction's own triage trigger: a run over the recurring error groups
// and the agents that fail in most runs (kinds `friction` + `agent`), nothing
// from the Inbox. Independent lines, each shown only when it applies, after the
// Inbox's TriageBanner: progress of a running run; the result of a run this tab
// saw end (dismissable); the offer to run triage (not while one runs); an error
// (dismissable, never shown while a run is active). Driven entirely by props.

import { i18n } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { TriageProgress, TriageSummary } from '../inbox/TriageBanner';

const BTN =
  'rounded-[8px] border border-line-strong px-3 py-[5px] font-mono text-[11px] text-ink-3 transition-colors hover:text-ink disabled:opacity-50';
const BTN_PRIMARY =
  'rounded-[8px] border border-brand/50 bg-brand/10 px-3 py-[5px] font-mono text-[11px] font-bold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50';
const LINE = 'flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[10px] border px-3.5 py-2 text-[12.5px]';

export interface FrictionTriageStripProps {
  /** Repeated error groups nobody has dealt with yet. */
  untriaged: number;
  /** Progress of the active run, or null when none is running. */
  running: TriageProgress | null;
  /** The run that ended during this visit, or null. */
  summary: TriageSummary | null;
  /** The operator closed the run-result line. */
  dismissed: boolean;
  /** Start error, or the failed run's error text; null hides the error line. */
  error: string | null;
  busy: boolean;
  onStart: () => void;
  onDismiss: () => void;
  onDismissError: () => void;
}

/** The idle line's text; a failing agent can exist with no friction, so 0 still offers a run. */
export function frictionOfferText(untriaged: number): string {
  const groups =
    untriaged > 0
      ? plural(untriaged, {
          one: '# untriaged group',
          few: '# untriaged groups',
          many: '# untriaged groups',
          other: '# untriaged groups',
        })
      : i18n._(msg`no untriaged groups`);
  return i18n._(msg`${groups} · agents are checked by rule`);
}

export function FrictionTriageStrip(p: FrictionTriageStripProps): JSX.Element {
  const running = p.running !== null;
  const showResult = p.summary !== null && !p.dismissed;
  const showError = p.error !== null && !running;
  const { t } = useLingui();
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

  return (
    <div className="mx-4 mt-3 flex flex-col gap-2 desk:mx-7">
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
          <button type="button" className={`${BTN} ml-auto`} onClick={p.onDismiss}>
            <Trans>dismiss</Trans>
          </button>
        </div>
      )}

      {!running && (
        <div className={`${LINE} border-line bg-bg text-ink-2`}>
          <span>{frictionOfferText(p.untriaged)}</span>
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
