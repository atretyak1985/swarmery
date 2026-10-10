// Locale-switch smoke test (plan D5): a mounted <Trans> re-renders in the new
// language when setLocale() switches the singleton — no reload, no remount.
//
// The message carries a test-only context, so its id (Lingui hashes message +
// context) is in no compiled catalog: the stub loaded below is the only uk
// translation it has, activate()'s merge of the real catalogs cannot replace
// it, and the assertion does not depend on what translators write for the
// app's own "daemon".

import { msg } from '@lingui/core/macro';
import { Trans } from '@lingui/react/macro';
import { afterEach, describe, expect, it } from 'vitest';
import { act, cleanup, render, screen } from '../test/render';
import { activate, i18n } from './index';
import { readLocale, setLocale } from './locale';

const SMOKE = msg({ message: 'daemon', context: 'locale-switch smoke test' });

afterEach(async () => {
  cleanup();
  localStorage.clear();
  await act(() => activate('en'));
});

describe('setLocale', () => {
  it('re-renders mounted messages in the new locale', async () => {
    i18n.load('uk', { [SMOKE.id]: 'демон' });
    render(
      <p>
        <Trans context="locale-switch smoke test">daemon</Trans>
      </p>,
    );
    expect(screen.getByText('daemon')).toBeTruthy();

    await act(() => setLocale('uk'));
    expect(screen.getByText('демон')).toBeTruthy();
    expect(readLocale()).toBe('uk');

    await act(() => setLocale('en'));
    expect(screen.getByText('daemon')).toBeTruthy();
  });

  it('falls back to English for a missing or unknown stored value', () => {
    expect(readLocale()).toBe('en');
    localStorage.setItem('swarmery.locale', 'fr');
    expect(readLocale()).toBe('en');
  });
});
