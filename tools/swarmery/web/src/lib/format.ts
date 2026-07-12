// Display formatting helpers (JetBrains Mono numeric style from the mockup).

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
 * slug ("-Volumes-Work-swarmery") only while the name is still unset.
 */
export function projectLabel(name: string | null | undefined, slug: string): string {
  return name != null && name !== '' ? name : slug;
}

/** Duration in ms → "0.3s" / "8.4s" / "4m 12s". */
export function fmtDurationMs(ms: number | null): string {
  if (ms === null) return '';
  if (ms < 100) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s.toString().padStart(2, '0')}s`;
}

/** ISO timestamp → "14:52" (local time). */
export function fmtTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
}

/** ISO timestamp → "Jul 10, 14:52". */
export function fmtDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
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

/** ISO timestamp → "9 s ago" / "4 min ago" / "3 h ago". */
export function fmtAgo(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '—';
  const sec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (sec < 60) return `${sec} s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} min ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}

/** Today's header, e.g. "Sat, Jul 12". */
export function fmtTodayHeader(): string {
  return new Date().toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
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
  return parseDay(day).toLocaleDateString([], {
    weekday: 'long',
    month: 'short',
    day: 'numeric',
  });
}

/** "2026-07-12" → "Sun, Jul 12". */
export function fmtDayShort(day: string): string {
  return parseDay(day).toLocaleDateString([], {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  });
}
