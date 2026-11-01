// What the limits button and panel say, derived from the shared usage poller's
// snapshot (Swarmery v4). Pure: nothing here fetches, and an account with no numbers
// says so rather than reading as 0 %.

import { t } from '@lingui/core/macro';
import type { UsageAccount, UsageProvider, UsageWindow } from '../api/types';
import { fmtResetAt } from '../components/usage/format';
import { DEFAULT_ACCOUNT } from './sessionAccount';
import { compactSpan } from './sessionsView';

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

/** `resets 4:00 PM · 2h 12m`, or the server's own text when it has no instant. */
export function resetLine(window: UsageWindow, now: number): string {
  if (window.resetAt !== undefined) {
    const at = fmtResetAt(window.resetAt, now);
    if (at !== '') {
      const left = new Date(window.resetAt).getTime() - now;
      return left > 0 ? t`resets ${at} · ${compactSpan(left)}` : t`resets ${at}`;
    }
  }
  return window.resetText ?? '';
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
    const reset = full === undefined ? '' : resetLine(full, now);
    return reset === '' ? t`${stopped.name} hit a limit` : t`${stopped.name} hit a limit · ${reset}`;
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
