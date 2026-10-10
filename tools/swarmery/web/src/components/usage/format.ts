// Pure formatters for the Usage modal — no React, no DOM state. Ported from
// Fusion's UsageIndicator (`formatResetAt`), kept as free functions so the
// window row stays presentational.

import { t } from '@lingui/core/macro';
import { fmtDate, fmtTime } from '../../lib/format';

/**
 * An ISO reset instant → the absolute wall-clock label shown in the reset chip.
 *
 * Tiers (identical to the reference implementation):
 *   same calendar day     → "1:50 PM"
 *   1–7 calendar days out → "Sat 4:59 PM"
 *   beyond 7 days         → "Jul 27, 1:50 PM"
 *
 * `now` is passed in (rather than read from Date.now()) so the caller's 30s
 * countdown tick drives re-formatting and the function stays pure/testable.
 *
 * Returns '' for an unparseable timestamp — callers render the chip only for a
 * non-empty result, so bad server data degrades to "no chip" instead of
 * "Invalid Date".
 */
export function fmtResetAt(iso: string, now: number): string {
  const date = new Date(iso);
  const ms = date.getTime();
  if (!Number.isFinite(ms)) return '';
  const ref = new Date(now);

  const timeStr = fmtTime(date, { hour: 'numeric', minute: '2-digit', hour12: true });

  if (date.toDateString() === ref.toDateString()) return timeStr;

  // Calendar-day distance measured from MIDNIGHT BOUNDARIES, not raw
  // millisecond division: dividing the raw delta by 86_400_000 makes the tier
  // depend on the time of day, so a reset 6 days and 20 hours out would fall
  // out of the "within a week" tier and print as a full date.
  const startOfRefDay = new Date(ref.getFullYear(), ref.getMonth(), ref.getDate());
  const startOfTargetDay = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  const calendarDaysUntil = Math.round(
    (startOfTargetDay.getTime() - startOfRefDay.getTime()) / 86_400_000,
  );

  if (calendarDaysUntil >= 1 && calendarDaysUntil <= 7) {
    const weekday = fmtDate(date, { weekday: 'short' });
    return `${weekday} ${timeStr}`;
  }

  const dateStr = fmtDate(date, { month: 'short', day: 'numeric' });
  return `${dateStr}, ${timeStr}`;
}

/**
 * Relative countdown FALLBACK — "resets in 3h 30m".
 *
 * The server owns this string (`window.resetText`); this exists only for the
 * window that carries `resetAt` without `resetText`, so the countdown still
 * reads live between the modal's 30-second polls. Never prefer it over the
 * server's own text.
 */
export function fmtResetsIn(iso: string, now: number): string {
  const ms = new Date(iso).getTime() - now;
  if (!Number.isFinite(ms)) return '';
  if (ms <= 0) return t`resets now`;
  const totalMin = Math.floor(ms / 60_000);
  const hours = Math.floor(totalMin / 60);
  const days = Math.floor(hours / 24);
  if (days > 0) {
    const remHours = hours % 24;
    return remHours > 0 ? t`resets in ${days}d ${remHours}h` : t`resets in ${days}d`;
  }
  const minutes = totalMin % 60;
  if (hours > 0) return t`resets in ${hours}h ${minutes}m`;
  return t`resets in ${totalMin}m`;
}

/** Epoch ms → the footer's "Last updated" clock. */
export function fmtClock(ms: number): string {
  return fmtTime(ms, {});
}
