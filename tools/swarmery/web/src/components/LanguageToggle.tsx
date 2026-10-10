// One-click interface-language switch for the header, next to ThemeToggle
// (both shells carry it, so the header chrome stays identical across modes).
// The full picker with both languages named in themselves lives on /settings;
// this button shows the code of the language it switches TO, the same way the
// landing site's switch does.

import { useLingui } from '@lingui/react/macro';
import type { Locale } from '../i18n/catalogs';
import { setLocale } from '../i18n/locale';

const CODES: Record<Locale, string> = { en: 'EN', uk: 'UK' }; // i18n-ignore — language codes, not copy

export function LanguageToggle(): JSX.Element {
  const { t, i18n } = useLingui();
  const current: Locale = i18n.locale === 'uk' ? 'uk' : 'en';
  const next: Locale = current === 'uk' ? 'en' : 'uk';
  return (
    <button
      type="button"
      aria-label={next === 'uk' ? t`switch the interface to Ukrainian` : t`switch the interface to English`}
      lang={next}
      onClick={() => {
        void setLocale(next);
      }}
      className="flex h-[26px] shrink-0 items-center justify-center rounded-lg border border-line bg-field px-1.5 font-mono text-[10.5px] leading-none text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
    >
      {CODES[next]}
    </button>
  );
}
