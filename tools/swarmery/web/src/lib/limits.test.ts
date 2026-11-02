// What the limits button and panel say. The claims:
//
//   1. The design's colour ladder, and no reading of a missing number as 0 %.
//   2. An account is at its limit when any window is full, not only the session one.
//   3. The head and the sentence name the worst true thing, and a failed usage
//      request is never called a limit.
//   4. The panel opens on the account at its limit, else the project's own.
//
// Runs with the rest of the web suite: `npm test`.

import { describe, expect, it } from 'vitest';
import type { UsageAccount, UsageProvider, UsageWindow } from '../api/types';
import {
  accountLimit,
  limitsHead,
  limitsSentence,
  limitTone,
  openingAccount,
  paceLine,
  resetsIn,
  resetWhen,
  windowName,
} from './limits';

const NOW = Date.parse('2026-11-01T12:00:00Z');

const win = (key: string, percentUsed: number, over: Partial<UsageWindow> = {}): UsageWindow =>
  ({ key, label: key, percentUsed, percentLeft: 100 - percentUsed, source: 'oauth', ...over }) as UsageWindow;

const account = (name: string, windows: UsageWindow[], over: Partial<UsageProvider> = {}): UsageAccount => ({
  account: name,
  providers: [{ account: name, name: 'Claude', source: 'oauth', status: 'ok', windows, ...over } as UsageProvider],
});

describe('limitTone', () => {
  it('walks the design’s ladder', () => {
    expect(limitTone(69)).toBe('green');
    expect(limitTone(70)).toBe('brand');
    expect(limitTone(90)).toBe('red');
  });
});

describe('accountLimit', () => {
  it('leads with the session window and keeps the others', () => {
    const limit = accountLimit(account('a', [win('weekly', 20), win('session', 41)]));
    expect(limit.session?.key).toBe('session');
    expect(limit.windows).toHaveLength(2);
    expect(limit.state).toBe('active');
  });

  it('is at its limit when any window is full', () => {
    expect(accountLimit(account('a', [win('session', 12), win('weekly', 100)])).state).toBe('limit');
  });

  it('has no numbers rather than zero when the usage request failed', () => {
    const limit = accountLimit(account('a', [], { status: 'error', error: 'HTTP 429' }));
    expect(limit.state).toBe('unavailable');
    expect(limit.session).toBeNull();
  });

  it('ignores an estimate card', () => {
    const estimate = { account: 'a', name: 'Estimate', source: 'estimate', status: 'ok', windows: [win('session', 5)] };
    expect(accountLimit({ account: 'a', providers: [estimate as UsageProvider] }).state).toBe('unavailable');
  });
});

describe('windowName', () => {
  it('reads the daemon’s labels as the design’s two lines', () => {
    expect(windowName(win('session-5h', 1, { label: 'Session (5h)' }))).toEqual({ name: '5-hour', scope: 'all models' });
    expect(windowName(win('weekly', 1, { label: 'Weekly' }))).toEqual({ name: 'Weekly', scope: 'all models' });
    expect(windowName(win('weekly-fable', 1, { label: 'Weekly (Fable)' }))).toEqual({ name: 'Weekly', scope: 'Fable' });
  });
});

describe('paceLine', () => {
  const paced = (status: 'ahead' | 'on-track' | 'behind', used = 40): UsageWindow =>
    win('weekly', used, { pace: { status, percentElapsed: 50, message: '' } });

  it('reads the daemon’s pace the right way round', () => {
    expect(paceLine(paced('ahead'))).toMatchObject({ glyph: '▲', tone: 'brand' });
    expect(paceLine(paced('on-track'))?.text).toBe('On pace with time elapsed');
    expect(paceLine(paced('behind'))).toMatchObject({ glyph: '✓', text: 'Well under pace', tone: 'green' });
  });

  it('says a full window waits, pace or not', () => {
    expect(paceLine(win('session', 100))).toMatchObject({ glyph: '■', tone: 'red' });
    expect(paceLine(win('session', 40))).toBeNull();
  });
});

describe('resetsIn and resetWhen', () => {
  it('counts down and names the day', () => {
    const today = win('session', 1, { resetAt: new Date(NOW + 2 * 3_600_000).toISOString() });
    expect(resetsIn(today, NOW)).toBe('resets in 2h 0m');
    expect(resetWhen(today, NOW)).toMatch(/^today /);
    const later = win('weekly', 1, { resetAt: new Date(NOW + 3 * 86_400_000).toISOString() });
    expect(resetWhen(later, NOW)).not.toMatch(/^today /);
  });

  it('falls back to the server’s words', () => {
    expect(resetsIn(win('session', 1, { resetText: 'resets in 3h' }), NOW)).toBe('resets in 3h');
    expect(resetWhen(win('session', 1), NOW)).toBe('');
  });
});

describe('limitsHead and limitsSentence', () => {
  const ok = accountLimit(account('default', [win('session', 41)]));
  const full = accountLimit(account('na', [win('session', 100)]));
  const blind = accountLimit(account('off', [], { status: 'error' }));

  it('names the account at its limit first', () => {
    expect(limitsHead([ok, full, blind])).toEqual({ text: 'na at limit', tone: 'brand' });
    expect(limitsSentence([ok, full], NOW)).toBe('na hit its 5-hour window');
    const timed = accountLimit(
      account('na', [win('session', 100, { resetAt: new Date(NOW + 3_600_000).toISOString() })]),
    );
    expect(limitsSentence([timed], NOW)).toMatch(/^na hit its 5-hour window · resets today /);
  });

  it('says all clear only when every account answered', () => {
    expect(limitsHead([ok]).text).toBe('all clear');
    expect(limitsHead([ok, blind]).text).toBe('1 without data');
    expect(limitsHead([blind]).text).toBe('no data');
    expect(limitsHead([]).text).toBe('no data');
  });

  it('does not call a failed request a limit', () => {
    expect(limitsSentence([blind], NOW)).toBe('some numbers are not available');
    expect(limitsSentence([ok], NOW)).toBe('every window has room');
  });
});

describe('openingAccount', () => {
  const ok = accountLimit(account('default', [win('session', 41)]));
  const other = accountLimit(account('other', [win('session', 10)]));
  const full = accountLimit(account('na', [win('session', 100)]));

  it('opens on the account at its limit, else the project’s, else the default', () => {
    expect(openingAccount([ok, full], 'default')).toBe('na');
    expect(openingAccount([ok, other], 'other')).toBe('other');
    expect(openingAccount([other, ok], null)).toBe('default');
    expect(openingAccount([], null)).toBeNull();
  });
});
