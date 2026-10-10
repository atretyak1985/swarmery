// Session outcome controls (ops-hygiene): a ✓/✗/⊘ chip group for the detail
// header, plus the shared glyph map the Sessions list rows reuse. Clicking
// the selected chip clears the verdict (outcome = null).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import type { SessionOutcome } from '../api/types';

export const OUTCOME_GLYPH: Record<SessionOutcome, { glyph: string; className: string }> = {
  success: { glyph: '✓', className: 'text-green' },
  fail: { glyph: '✗', className: 'text-red' },
  abandoned: { glyph: '⊘', className: 'text-ink-dim' },
};

const OPTIONS: { v: SessionOutcome; label: MessageDescriptor; on: string }[] = [
  { v: 'success', label: msg`success`, on: 'border-green/50 bg-green/10 text-green' },
  { v: 'fail', label: msg`fail`, on: 'border-red/50 bg-red/10 text-red' },
  { v: 'abandoned', label: msg`abandoned`, on: 'border-line-strong bg-surface2 text-ink' },
];

export function OutcomePicker({
  value,
  onChange,
}: {
  value: SessionOutcome | null;
  onChange: (next: SessionOutcome | null) => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <div className="flex items-center gap-1" role="group" aria-label={t`session outcome`}>
      {OPTIONS.map((o) => {
        const selected = value === o.v;
        const label = i18n._(o.label);
        return (
          <button
            key={o.v}
            type="button"
            aria-pressed={selected}
            aria-label={label}
            data-tip={selected ? t`clear ${label}` : label}
            onClick={() => onChange(selected ? null : o.v)}
            className={`rounded-full border px-[9px] py-0.5 font-mono text-[11px] transition-colors ${
              selected ? o.on : 'border-line-strong text-ink-dim hover:text-ink'
            }`}
          >
            {OUTCOME_GLYPH[o.v].glyph}
          </button>
        );
      })}
    </div>
  );
}
