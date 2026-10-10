// Display formatting helpers (JetBrains Mono numeric style from the mockup).

import { t } from '@lingui/core/macro';
import { currentLocale } from '../i18n/locale';

// Every Intl / toLocale* call of the SPA goes through the helpers below, so
// dates and numbers follow the UI language picked in Settings (plan D5).
export { currentLocale };

/** A Date, an ISO string, or epoch milliseconds. */
type DateInput = Date | string | number;

const TIME_24: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false };
const DATE_TIME_24: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric', ...TIME_24 };
const DATE_MEDIUM: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric', year: 'numeric' };

function toValidDate(value: DateInput): Date | null {
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** Date part in the UI locale → "Oct 7, 2026" / "7 жовт. 2026 р."; "—" when invalid. */
export function fmtDate(value: DateInput, opts: Intl.DateTimeFormatOptions = DATE_MEDIUM): string {
  const d = toValidDate(value);
  return d === null ? '—' : d.toLocaleDateString(currentLocale(), opts);
}

/** Number in the UI locale → "1,234.5" / "1 234,5". */
export function fmtNum(n: number, opts?: Intl.NumberFormatOptions): string {
  return n.toLocaleString(currentLocale(), opts);
}

/** 1234567 → "1.2M", 412300 → "412K", 950 → "950". */
export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`;
  return String(n);
}

/** null → "—" (contract: cost_usd may be null). */
export function fmtCost(n: number | null): string {
  if (n === null) return '—';
  return `$${n.toFixed(2)}`;
}

/**
 * Clean project display label: prefer the project name, fall back to the
 * slug ("-home-dev-swarmery") only while the name is still unset.
 */
export function projectLabel(name: string | null | undefined, slug: string): string {
  return name != null && name !== '' ? name : slug;
}

/** Duration in ms → "0.3s" / "8.4s" / "4m 12s". */
export function fmtDurationMs(ms: number | null): string {
  if (ms === null) return '';
  if (ms < 100) return `${ms}ms`;
  // 59 950–59 999 would render "60.0s" — treat as minute territory instead.
  if (ms < 59_950) return `${(ms / 1000).toFixed(1)}s`;
  let m = Math.floor(ms / 60_000);
  let s = Math.round((ms % 60_000) / 1000);
  if (s === 60) {
    // Seconds rounding must carry into minutes: 479 700ms is "8m 00s", not "7m 60s".
    m += 1;
    s = 0;
  }
  return `${m}m ${s.toString().padStart(2, '0')}s`;
}

/** Time of day in the UI locale (local time) → "14:52" by default; "—" when invalid. */
export function fmtTime(value: DateInput, opts: Intl.DateTimeFormatOptions = TIME_24): string {
  const d = toValidDate(value);
  return d === null ? '—' : d.toLocaleTimeString(currentLocale(), opts);
}

/** Date and time in the UI locale → "Jul 10, 14:52" by default; "—" when invalid. */
export function fmtDateTime(value: DateInput, opts: Intl.DateTimeFormatOptions = DATE_TIME_24): string {
  const d = toValidDate(value);
  return d === null ? '—' : d.toLocaleString(currentLocale(), opts);
}

/** The page eyebrow clock → "Sunday · Jul 12 · 14:52" (Today and Overview). */
export function fmtEyebrowClock(now: Date): string {
  return fmtDateTime(now, { weekday: 'long', month: 'short', day: 'numeric', ...TIME_24 }).replace(/,/g, ' ·');
}

/** Wall-clock span from start to end (or now) → "18 min" / "2 h 05 min" / "41 s". */
export function fmtSpan(startIso: string, endIso: string | null): string {
  const start = new Date(startIso).getTime();
  const end = endIso !== null ? new Date(endIso).getTime() : Date.now();
  if (Number.isNaN(start) || Number.isNaN(end)) return '—';
  const sec = Math.max(0, Math.round((end - start) / 1000));
  if (sec < 60) return `${sec} s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} min`;
  const h = Math.floor(min / 60);
  return `${h} h ${(min % 60).toString().padStart(2, '0')} min`;
}

/** Compact elapsed time since `fromIso` as of `now` → "42s" / "7m 03s" / "2h 14m".
 * Denser than fmtSpan (no spaces around the unit) because it rides inside the
 * run chips, and it takes `now` explicitly so a ticking clock drives re-renders.
 * Lives here rather than in Plans.tsx so RunOutcomeModal can reuse it without
 * importing a page (which would close an import cycle). */
export function fmtElapsed(fromIso: string, now: number): string {
  const s = Math.max(0, Math.floor((now - Date.parse(fromIso)) / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${String(m)}m ${String(s % 60)}s`;
  return `${String(Math.floor(m / 60))}h ${String(m % 60)}m`;
}

/** ISO timestamp → "9 s ago" / "4 min ago" / "3 h ago". */
export function fmtAgo(iso: string): string {
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return '—';
  const sec = Math.max(0, Math.round((Date.now() - at) / 1000));
  if (sec < 60) return t`${sec} s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return t`${min} min ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return t`${h} h ago`;
  const d = Math.floor(h / 24);
  return t`${d} d ago`;
}

/** Today's header, e.g. "Sat, Jul 12". */
export function fmtTodayHeader(): string {
  return fmtDate(new Date(), { weekday: 'short', month: 'short', day: 'numeric' });
}

/* ----- day-key helpers (local YYYY-MM-DD, for /api/stats/overview?day=) ----- */

/** Local calendar day of a Date → "2026-07-12". */
export function isoDay(d: Date = new Date()): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

/** Parse a "YYYY-MM-DD" day key as a LOCAL date (not UTC). */
export function parseDay(day: string): Date {
  const [y = 1970, m = 1, d = 1] = day.split('-').map(Number);
  return new Date(y, m - 1, d);
}

/** Shift a "YYYY-MM-DD" day key by ±n days. */
export function addDays(day: string, delta: number): string {
  const d = parseDay(day);
  d.setDate(d.getDate() + delta);
  return isoDay(d);
}

/** "2026-07-12" → "Sunday, Jul 12" (day-title header of the Overview). */
export function fmtDayTitle(day: string): string {
  return fmtDate(parseDay(day), {
    weekday: 'long',
    month: 'short',
    day: 'numeric',
  });
}

/** "2026-07-12" → "Sun, Jul 12". */
export function fmtDayShort(day: string): string {
  return fmtDate(parseDay(day), {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  });
}
