// What the limits button and panel say, derived from the shared usage poller's
// snapshot (Swarmery v4). Pure: nothing here fetches, and an account with no numbers
// says so rather than reading as 0 %.

import { t } from '@lingui/core/macro';
import type { UsageAccount, UsageProvider, UsageWindow } from '../api/types';
import { fmtResetAt, fmtResetsIn } from '../components/usage/format';
import { DEFAULT_ACCOUNT } from './sessionAccount';

export type LimitTone = 'red' | 'brand' | 'green';
export type LimitState = 'active' | 'limit' | 'unavailable';

export interface AccountLimit {
  name: string;
  state: LimitState;
  /** The live card, null when the account has none. */
  provider: UsageProvider | null;
  windows: UsageWindow[];
  /** The short window every run spends first, null without numbers. */
  session: UsageWindow | null;
}

/** The design's ladder for a bar and its number. */
export function limitTone(percent: number): LimitTone {
  if (percent >= 90) return 'red';
  return percent >= 70 ? 'brand' : 'green';
}

function sessionWindow(windows: readonly UsageWindow[]): UsageWindow | null {
  return windows.find((w) => w.key.startsWith('session')) ?? windows[0] ?? null;
}

export function accountLimit(account: UsageAccount): AccountLimit {
  const provider = account.providers.find((p) => p.source === 'oauth') ?? null;
  const windows = provider?.status === 'ok' ? provider.windows : [];
  const session = sessionWindow(windows);
  const state: LimitState =
    session === null ? 'unavailable' : windows.some((w) => w.percentUsed >= 100) ? 'limit' : 'active';
  return { name: account.account, state, provider, windows, session };
}

/** A window's two-line name: the daemon's "Session (5h)", "Weekly" and "Weekly (Fable)"
 * read as 5-hour · all models, Weekly · all models and Weekly · Fable. */
export function windowName(window: UsageWindow): { name: string; scope: string } {
  const match = /^(.*?)\s*\((.*)\)$/.exec(window.label);
  const base = match?.[1] ?? window.label;
  const inner = match?.[2] ?? '';
  if (window.key.startsWith('session') || base === 'Session') return { name: t`5-hour`, scope: t`all models` };
  return { name: base, scope: inner === '' ? t`all models` : inner };
}

export interface PaceLine {
  glyph: string;
  text: string;
  tone: LimitTone;
}

/** The line under a bar. The daemon's pace reads backwards at first: `ahead` burns faster
 * than the window elapses, `behind` slower (components/usage/UsageWindowRow.tsx). */
export function paceLine(window: UsageWindow): PaceLine | null {
  if (window.percentUsed >= 100) return { glyph: '■', text: t`Limit reached · sessions wait for the reset`, tone: 'red' };
  const pace = window.pace;
  if (pace === undefined) return null;
  if (pace.status === 'ahead') return { glyph: '▲', text: t`Ahead of time elapsed · may run out before reset`, tone: 'brand' };
  if (pace.status === 'on-track') return { glyph: '✓', text: t`On pace with time elapsed`, tone: 'green' };
  return { glyph: '✓', text: t`Well under pace`, tone: 'green' };
}

/** `resets in 4h 12m`: the live countdown when the window has an instant, else the
 * server's own text. */
export function resetsIn(window: UsageWindow, now: number): string {
  if (window.resetAt !== undefined) {
    const text = fmtResetsIn(window.resetAt, now);
    if (text !== '') return text;
  }
  return window.resetText ?? '';
}

/** `today 18:00`, `Tue 03:00`: when the window resets. */
export function resetWhen(window: UsageWindow, now: number): string {
  if (window.resetAt === undefined) return '';
  const at = fmtResetAt(window.resetAt, now);
  if (at === '') return '';
  return new Date(window.resetAt).toDateString() === new Date(now).toDateString() ? t`today ${at}` : at;
}

export interface LimitsHead {
  text: string;
  tone: LimitTone | 'faint';
}

/** The right edge of the button: the worst thing true of the accounts. */
export function limitsHead(limits: readonly AccountLimit[]): LimitsHead {
  const stopped = limits.find((l) => l.state === 'limit');
  if (stopped !== undefined) return { text: t`${stopped.name} at limit`, tone: 'brand' };
  const blind = limits.filter((l) => l.state === 'unavailable').length;
  if (blind === 0 && limits.length > 0) return { text: t`all clear`, tone: 'green' };
  if (blind === limits.length) return { text: t`no data`, tone: 'faint' };
  return { text: t`${blind} without data`, tone: 'faint' };
}

/** The panel's one sentence. It repeats the upstream reason only for the account on
 * screen: a 429 here is about the usage request, not about the account's quota. */
export function limitsSentence(limits: readonly AccountLimit[], now: number): string {
  const stopped = limits.find((l) => l.state === 'limit');
  if (stopped !== undefined) {
    const full = stopped.windows.find((w) => w.percentUsed >= 100);
    const account = stopped.name;
    if (full === undefined) return t`${account} hit a limit`;
    const window = windowName(full).name;
    const when = resetWhen(full, now);
    return when === '' ? t`${account} hit its ${window} window` : t`${account} hit its ${window} window · resets ${when}`;
  }
  if (limits.every((l) => l.state === 'active') && limits.length > 0) return t`every window has room`;
  return t`some numbers are not available`;
}

/** Which account the panel opens on: one at its limit, else the project's own. */
export function openingAccount(limits: readonly AccountLimit[], active: string | null): string | null {
  return (
    limits.find((l) => l.state === 'limit')?.name ??
    limits.find((l) => l.name === active)?.name ??
    limits.find((l) => l.name === DEFAULT_ACCOUNT)?.name ??
    limits[0]?.name ??
    null
  );
}
