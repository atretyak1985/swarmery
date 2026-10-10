// The SPA's i18n entry point (plan D1, D4): the Lingui singleton every macro
// compiles against, plus activate(locale), which lazy-loads the compiled
// catalogs of one locale and switches the UI to it.
//
// Importing this module activates the source locale with an empty catalog, so
// a test or a model that renders a message before any activate() call gets
// the English source text (non-production builds keep it next to the id).
// The production bundle strips source text, so main.tsx awaits activate()
// before the first render.

import { i18n, type Messages } from '@lingui/core';
import { CATALOGS, type CatalogName, isLocale, type Locale, SOURCE_LOCALE } from './catalogs';

export { i18n };

/** localStorage key holding the operator's chosen locale (plan D5). */
export const LOCALE_STORAGE_KEY = 'swarmery.locale';

i18n.load(SOURCE_LOCALE, {});
i18n.activate(SOURCE_LOCALE);

async function loadCatalog(locale: Locale, name: CatalogName): Promise<Messages> {
  // One chunk per catalog per locale: only the active locale is downloaded.
  const mod = (await import(`../locales/${locale}/${name}.mjs`)) as { messages: Messages };
  return mod.messages;
}

// Monotonic request token: each activate() takes the next number, and only the
// latest request may switch the locale once its catalogs arrive, so a slower
// earlier load (en → uk → en clicked fast) cannot overwrite a later choice.
let latestRequest = 0;

/** Load every catalog of `locale`, merge them, and make it the active locale.
 * Resolves without switching when a later activate() call superseded it. */
export async function activate(locale: Locale): Promise<void> {
  latestRequest += 1;
  const request = latestRequest;
  const parts = await Promise.all(CATALOGS.map((name) => loadCatalog(locale, name)));
  if (request !== latestRequest) return;
  i18n.load(locale, Object.assign({}, ...parts) as Messages);
  i18n.activate(locale);
  document.documentElement.lang = locale;
}

/** The persisted locale, or the source locale when none (or storage is blocked). */
export function storedLocale(): Locale {
  try {
    const value = localStorage.getItem(LOCALE_STORAGE_KEY);
    return isLocale(value) ? value : SOURCE_LOCALE;
  } catch {
    return SOURCE_LOCALE;
  }
}
