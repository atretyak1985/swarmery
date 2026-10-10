// UI language picker (plan D5): two radios, both visible without opening
// anything. Each language is labelled in itself — "Українська" reads the same
// on an English UI — so the two labels are not messages.
//
// The choice is persisted and applied without a reload (i18n/locale.ts); the
// radios follow the active locale, so they flip back if the catalogs of the
// picked one fail to load.

import { useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { isLocale, type Locale, SOURCE_LOCALE } from '../i18n/catalogs';
import { setLocale } from '../i18n/locale';

const OPTIONS: readonly { value: Locale; label: string }[] = [
  { value: 'en', label: 'English' }, // i18n-ignore: each language is named in itself
  { value: 'uk', label: 'Українська' }, // i18n-ignore: each language is named in itself
];

export function LanguagePicker(): JSX.Element {
  // useLingui() re-renders this component on every i18n.activate().
  const { t, i18n } = useLingui();
  const active: Locale = isLocale(i18n.locale) ? i18n.locale : SOURCE_LOCALE;
  // The pending choice shows at once; the active locale catches up once the
  // catalogs load (a first switch to uk downloads them).
  const [pending, setPending] = useState<Locale | null>(null);
  const checked = pending ?? active;

  const pick = (next: Locale): void => {
    setPending(next);
    setLocale(next)
      .catch((err: unknown) => console.warn(`i18n: failed to switch to "${next}"`, err))
      .finally(() => setPending((p) => (p === next ? null : p)));
  };

  return (
    <div
      role="radiogroup"
      aria-label={t`interface language`}
      className="flex gap-1 rounded-lg border border-line-strong bg-field p-0.5"
    >
      {OPTIONS.map((o) => {
        const on = checked === o.value;
        return (
          <label
            key={o.value}
            lang={o.value}
            className={`flex min-h-8 flex-1 cursor-pointer items-center justify-center gap-1.5 rounded-[7px] px-2 py-1 font-mono text-[11px] font-semibold transition-colors has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-1 has-[:focus-visible]:outline-brand ${
              on ? 'bg-surface2 text-ink' : 'text-ink-dim hover:text-ink'
            }`}
          >
            <input
              type="radio"
              name="swarmery-locale"
              value={o.value}
              checked={on}
              onChange={() => pick(o.value)}
              className="sr-only"
            />
            {o.label}
          </label>
        );
      })}
    </div>
  );
}
