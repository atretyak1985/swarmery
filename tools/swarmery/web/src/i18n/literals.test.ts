// @vitest-environment node
//
// The i18n literal gate (plan D3): scripts/i18n-literals.mjs counts the
// user-visible strings that have not gone through a Lingui macro yet, and
// this test keeps that count at or under the budget. A new unwrapped string
// fails `npm test`; wrapping strings lowers the count, and the budget follows
// it down until it reaches 0.
//
// See what is left: node scripts/i18n-literals.mjs --list

import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { I18N_LITERALS_BUDGET } from './budget';

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'scripts', 'i18n-literals.mjs');

interface ScanResult {
  count: number;
  items: { file: string; line: number; text: string }[];
}

describe('i18n literal scanner', () => {
  it('finds no more unwrapped strings than the budget', () => {
    const out = execFileSync(process.execPath, [SCRIPT], {
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
    });
    const { count, items } = JSON.parse(out) as ScanResult;
    expect(items).toHaveLength(count);
    expect(count, 'unwrapped strings over budget — run node scripts/i18n-literals.mjs --list').toBeLessThanOrEqual(
      I18N_LITERALS_BUDGET,
    );
  }, 60_000);
});
