// Shared editors for a board task's fields. Extracted from the task detail editor when the
// New-Task modal grew the same set of controls: one implementation of the
// uppercase field label and of the list-of-strings chip editor (file scope,
// dependencies), so the create form and the edit drawer stay identical.

import { useLingui } from '@lingui/react/macro';
import { useState } from 'react';

/** The uppercase mono caption every task field sits under. */
export function FieldLabel({ children }: { children: string }): JSX.Element {
  return (
    <div className="mb-1 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">{children}</div>
  );
}

/** An editable list-of-strings field rendered as removable chips + an add input. */
export function ChipEditor({
  label,
  values,
  placeholder,
  disabled = false,
  onChange,
}: {
  label: string;
  values: string[];
  placeholder: string;
  disabled?: boolean;
  onChange: (next: string[]) => void;
}): JSX.Element {
  const { t } = useLingui();
  const [draft, setDraft] = useState('');
  const add = (): void => {
    const v = draft.trim();
    if (v === '' || values.includes(v)) {
      setDraft('');
      return;
    }
    onChange([...values, v]);
    setDraft('');
  };
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="flex flex-wrap gap-1.5">
        {values.map((v) => (
          <span
            key={v}
            className="flex items-center gap-1 rounded-full border border-line bg-field px-2 py-0.5 font-mono text-[10.5px] text-ink-2"
          >
            {v}
            <button
              type="button"
              aria-label={t`remove ${v}`}
              disabled={disabled}
              onClick={() => onChange(values.filter((x) => x !== v))}
              className="text-ink-faint transition-colors hover:text-red disabled:opacity-50"
            >
              ×
            </button>
          </span>
        ))}
      </div>
      <input
        type="text"
        value={draft}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            add();
          }
        }}
        onBlur={add}
        placeholder={placeholder}
        aria-label={label}
        className="mt-1.5 w-full rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11px] text-ink outline-none placeholder:text-ink-faint focus:border-ink-dim disabled:opacity-50"
      />
    </div>
  );
}
