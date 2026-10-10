// activate(locale) under a fast locale switch: the latest request wins even
// when an earlier one finishes loading after it. Needs the compiled catalogs
// (`npm test` runs `lingui compile` first).

import { afterEach, describe, expect, it } from 'vitest';
import { activate, i18n } from './index';

afterEach(async () => {
  await activate('en');
});

describe('activate', () => {
  // First in the file on purpose: the uk chunks are not loaded yet, so the
  // earlier uk request finishes after the later (warm) en one.
  it('lets a later request win over a slower earlier one', async () => {
    await activate('en');
    const slower = activate('uk');
    const faster = activate('en');
    await Promise.all([slower, faster]);
    expect(i18n.locale).toBe('en');
    expect(document.documentElement.lang).toBe('en');
  });

  it('switches the active locale and the document language', async () => {
    await activate('uk');
    expect(i18n.locale).toBe('uk');
    expect(document.documentElement.lang).toBe('uk');
  });
});
