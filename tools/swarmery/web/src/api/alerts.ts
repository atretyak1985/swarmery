// Typed client for alerts (Go DTOs in internal/api/alerts.go): the findings
// that stop work until the operator acts. The first is an open account circuit
// breaker — while it is open the daemon admits no card, phase or plan onto that
// account. MOCK mode serves one fixture alert so the Inbox renders offline.

import { MOCK } from '../api';

/** Why a breaker opened: the login / access is gone, or a usage limit was hit. */
export type AccountBreakerKind = 'auth' | 'quota';

/** One open alert (GET /api/alerts). The first six fields are the finding; the
 *  rest are present only for an account-breaker alert. No field is CLI output. */
export interface Alert {
  id: number;
  /** The rule that raised it, e.g. `account_breaker_open`. */
  rule: string;
  /** What it is about, e.g. `account:work`. */
  target: string;
  severity: 'info' | 'warn' | 'error';
  /** A fixed sentence per rule and kind. */
  message: string;
  detectedAt: string;
  /** Account key of a breaker alert — what resumeAccount takes. */
  account?: string;
  kind?: AccountBreakerKind;
  /** The breaker's fixed reason phrase. */
  reason?: string;
  openedAt?: string;
  /** When a quota breaker closes on its own; absent for an auth one. */
  resetsAt?: string;
}

/** The rule an open account breaker is surfaced under. */
export const ACCOUNT_BREAKER_RULE = 'account_breaker_open';

const MOCK_ALERTS: Alert[] = [
  {
    id: 1,
    rule: ACCOUNT_BREAKER_RULE,
    target: 'account:work',
    severity: 'error',
    message: 'Claude refused this account. Runs on it are paused until a probe succeeds.',
    detectedAt: '2026-09-30T12:00:00Z',
    account: 'work',
    kind: 'auth',
    reason: "Claude refused this account's access",
    openedAt: '2026-09-30T12:00:00Z',
  },
];

/** GET /api/alerts — every unresolved alert, oldest first. */
export async function fetchAlerts(): Promise<Alert[]> {
  if (MOCK) return MOCK_ALERTS;
  const res = await fetch('/api/alerts');
  if (!res.ok) throw new Error(`GET /api/alerts: ${String(res.status)}`);
  const body = (await res.json()) as { alerts: Alert[] };
  return body.alerts;
}

/**
 * POST /api/accounts/{account}/breaker/resume — "Probe & resume". The daemon
 * checks the account for real (the login, then a one-turn model call) and
 * closes the breaker only when it can run again. A 409 means it still cannot;
 * the thrown message is the daemon's fixed reason phrase.
 */
export async function resumeAccount(account: string): Promise<void> {
  if (MOCK) {
    const i = MOCK_ALERTS.findIndex((a) => a.account === account);
    if (i >= 0) MOCK_ALERTS.splice(i, 1);
    return;
  }
  const path = `/api/accounts/${encodeURIComponent(account)}/breaker/resume`;
  const res = await fetch(path, { method: 'POST' });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `POST ${path}: ${String(res.status)}`);
  }
}
