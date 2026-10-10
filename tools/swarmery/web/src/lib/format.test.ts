// lib/format.ts locale helpers (plan D5): dates and numbers follow the active
// UI locale through currentLocale(). Needs the compiled catalogs (`npm test`
// runs `lingui compile` first).

import { afterEach, describe, expect, it } from 'vitest';
import { activate } from '../i18n';
import { currentLocale, fmtDate, fmtDateTime, fmtNum, fmtTime } from './format';

const OCT_7 = new Date('2026-10-07T12:00:00Z');

afterEach(async () => {
  await activate('en');
});

describe('locale-aware formatters', () => {
  it('formats in English by default', () => {
    expect(currentLocale()).toBe('en-US');
    expect(fmtDate(OCT_7)).toContain('Oct');
    expect(fmtNum(1234.5)).toBe('1,234.5');
  });

  it('formats in Ukrainian once uk is active', async () => {
    await activate('uk');
    expect(currentLocale()).toBe('uk-UA');
    expect(fmtDate(OCT_7)).toContain('жовт');
    expect(fmtDateTime(OCT_7)).toContain('жовт');
    // uk groups thousands with a (narrow) no-break space and uses a decimal comma.
    expect(fmtNum(1234.5)).toMatch(/^1\s234,5$/u);
  });

  it('returns a dash for an invalid date', () => {
    expect(fmtDate('not a date')).toBe('—');
    expect(fmtTime('not a date')).toBe('—');
    expect(fmtDateTime(Number.NaN)).toBe('—');
  });
});
