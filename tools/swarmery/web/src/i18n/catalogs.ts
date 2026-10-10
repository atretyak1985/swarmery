// The message catalogs the SPA is split into (plan D2): one per extraction
// leg, so four executors can extract and translate in parallel without
// touching the same .po file. lingui.config.ts maps each name to the source
// folders it owns; src/i18n/index.ts loads all of them on activate(locale).
// One list, read by both — add a catalog here and in the config's include map.

export const CATALOGS = ['today-sessions', 'plans', 'insights-system', 'shared'] as const;

export type CatalogName = (typeof CATALOGS)[number];

export const LOCALES = ['en', 'uk'] as const;

export type Locale = (typeof LOCALES)[number];

export const SOURCE_LOCALE: Locale = 'en';

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && (LOCALES as readonly string[]).includes(value);
}
