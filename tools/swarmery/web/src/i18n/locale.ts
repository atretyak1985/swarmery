// The operator's UI language (plan D5): read and persist the choice, switch
// the singleton to it, and expose the BCP-47 tag every date and number
// formatter uses (through lib/format.ts). No navigator.language detection: the
// default is English until the operator picks otherwise in Settings.

import { isLocale, type Locale, SOURCE_LOCALE } from './catalogs';
import { activate, i18n } from './index';

/** localStorage key holding the chosen locale, next to swarmery.lastProject. */
export const LOCALE_KEY = 'swarmery.locale';

/** The BCP-47 tag Intl formats each UI locale with. */
const INTL_TAG: Record<Locale, string> = { en: 'en-US', uk: 'uk-UA' };

/** The persisted locale, or the source locale when none (or storage is blocked). */
export function readLocale(): Locale {
  try {
    const value = localStorage.getItem(LOCALE_KEY);
    return isLocale(value) ? value : SOURCE_LOCALE;
  } catch {
    return SOURCE_LOCALE;
  }
}

/** Persist `locale` and switch the UI to it without a reload: I18nProvider
 * re-renders every <Trans> once activate() has loaded the catalogs, and
 * activate() sets document.documentElement.lang. A blocked storage still
 * switches the current tab. */
export async function setLocale(locale: Locale): Promise<void> {
  // Persist only once the catalogs loaded, so storage never names a locale the
  // app could not activate (the picker falls back to English on failure).
  await activate(locale);
  try {
    localStorage.setItem(LOCALE_KEY, locale);
  } catch {
    // Private mode / blocked storage: the choice lasts for this page only.
  }
}

/** The active UI locale as a BCP-47 tag for Intl ('uk-UA' | 'en-US'). */
export function currentLocale(): string {
  return isLocale(i18n.locale) ? INTL_TAG[i18n.locale] : INTL_TAG[SOURCE_LOCALE];
}
