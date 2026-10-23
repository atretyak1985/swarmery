// An agent-declared stop (verify-not-trust phase 10): the phase run hit one of
// its guard's stop triggers and asked instead of working around it. Rendered in
// Needs you and inline on the phase card — the trigger, the reason, the
// executor's two options as buttons (with what each leads to), and a free-text
// answer. Go: phaserun.answerStopDecision via POST …/decisions/{decisionId}.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { answerPhaseDecision } from '../../api/guard';
import type { NeedsYouDecision } from '../../api/types';

const FOCUS = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand';
const BTN = `rounded-lg border px-3 py-1.5 font-mono text-[11.5px] transition-colors disabled:opacity-50 ${FOCUS}`;

/** The free-text option every stop offers beside the executor's own (Go
 *  phaserun.StopOptionOther). */
export const STOP_OPTION_OTHER = 'other';

const TRIGGER_LABEL: Record<string, MessageDescriptor> = {
  'out-of-scope': msg`out of scope`,
  'protected-path': msg`protected path`,
  'access-error': msg`access or login error`,
  'criterion-impossible': msg`criterion impossible`,
};

/** "waits on decision D-n": the stop's question and its answers. Renders
 *  nothing for a decision that is not an agent stop. */
export function StopDecision({
  decision,
  onAnswered,
  compact = false,
}: {
  decision: NeedsYouDecision;
  onAnswered: () => void;
  /** The phase card's inline form: the heading names the decision. */
  compact?: boolean;
}): JSX.Element | null {
  const { t, i18n } = useLingui();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const stop = decision.stop;
  if (stop === undefined) return null;
  const id = decision.decisionId;
  const label = TRIGGER_LABEL[stop.trigger];
  const trigger = label !== undefined ? i18n._(label) : stop.trigger;
  const optionCount = stop.options.length;
  const lints = stop.lints ?? [];

  const answer = (option: string): void => {
    setBusy(true);
    setError(null);
    answerPhaseDecision(decision.taskId, decision.phaseId, id, option, text.trim())
      .then(onAnswered)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div
      data-testid="stop-decision"
      className={compact ? 'mt-3 rounded-[10px] border border-amber/40 bg-amber/5 px-3 py-2.5' : 'mt-2'}
    >
      <div className="flex flex-wrap items-center gap-2">
        {compact && (
          <span className="text-[12.5px] font-medium text-amber">
            <Trans>waits on decision {id}</Trans>
          </span>
        )}
        {!compact && <span className="font-mono text-[11px] font-semibold text-ink-2">{id}</span>}
        <span className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[10.5px] text-amber">
          <Trans>stop: {trigger}</Trans>
        </span>
        {lints.includes('stop_trigger_unknown') && (
          <span className="font-mono text-[10.5px] text-ink-dim" data-tip={t`the run named a trigger outside the closed list`}>
            <Trans>unknown trigger</Trans>
          </span>
        )}
        {lints.includes('stop_options_count') && (
          <span className="font-mono text-[10.5px] text-ink-dim" data-tip={t`the contract asks for exactly two options`}>
            <Trans>{optionCount} option(s), not 2</Trans>
          </span>
        )}
      </div>
      <p className="mt-1.5 text-[13px] leading-[1.55] text-ink-2 [text-wrap:pretty]">{stop.reason}</p>
      {stop.paths !== undefined && stop.paths.length > 0 && (
        <p className="mt-1 font-mono text-[11px] text-ink-3">{stop.paths.join(', ')}</p>
      )}
      <div className="mt-2.5 flex flex-wrap items-start gap-2" role="group" aria-label={t`answer ${id}`}>
        {stop.options.map((opt) => (
          <div key={opt.id} className="flex max-w-[22rem] flex-col gap-1">
            <button
              type="button"
              disabled={busy}
              onClick={() => answer(opt.id)}
              className={`${BTN} border-brand/50 bg-brand/10 text-left font-semibold text-brand hover:bg-brand/20`}
            >
              {opt.label}
            </button>
            {opt.consequence !== '' && (
              <span className="px-1 text-[11.5px] leading-[1.45] text-ink-3">→ {opt.consequence}</span>
            )}
          </div>
        ))}
      </div>
      <div className="mt-2.5 flex flex-wrap items-end gap-2">
        <label className="min-w-[14rem] flex-1">
          <span className="font-mono text-[10.5px] text-ink-dim">
            <Trans>or answer in your own words — recorded as “other” in the phase doc</Trans>
          </span>
          <input
            type="text"
            value={text}
            onChange={(e) => setText(e.target.value)}
            disabled={busy}
            maxLength={2000}
            className={`mt-1 block w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 text-[12.5px] text-ink ${FOCUS}`}
          />
        </label>
        <button
          type="button"
          disabled={busy || text.trim() === ''}
          onClick={() => answer(STOP_OPTION_OTHER)}
          className={`${BTN} border-line-strong text-ink-3 hover:bg-surface2`}
        >
          <Trans>Answer</Trans>
        </button>
      </div>
      {error !== null && (
        <p role="alert" className="mt-1.5 font-mono text-[11px] text-red">
          {error}
        </p>
      )}
    </div>
  );
}
