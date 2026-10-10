// LanguagePicker (plan D5): picking a language persists it, switches the
// Lingui singleton and sets the document language, without a reload. Needs
// the compiled catalogs (`npm test` runs `lingui compile` first).

import { afterEach, describe, expect, it } from 'vitest';
import { activate, i18n } from '../i18n';
import { LOCALE_KEY } from '../i18n/locale';
import { act, cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { LanguagePicker } from './LanguagePicker';

afterEach(async () => {
  cleanup();
  localStorage.clear();
  await act(() => activate('en'));
});

describe('LanguagePicker', () => {
  it('shows both languages, each named in itself, with English checked by default', () => {
    render(<LanguagePicker />);
    expect(screen.getByRole('radiogroup')).toBeTruthy();
    expect((screen.getByRole('radio', { name: 'English' }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('radio', { name: 'Українська' }) as HTMLInputElement).checked).toBe(false);
  });

  it('switches to Ukrainian and back to English', async () => {
    render(<LanguagePicker />);

    fireEvent.click(screen.getByRole('radio', { name: 'Українська' }));
    await waitFor(() => expect(i18n.locale).toBe('uk'));
    expect(localStorage.getItem(LOCALE_KEY)).toBe('uk');
    expect(document.documentElement.lang).toBe('uk');
    expect((screen.getByRole('radio', { name: 'Українська' }) as HTMLInputElement).checked).toBe(true);

    fireEvent.click(screen.getByRole('radio', { name: 'English' }));
    await waitFor(() => expect(i18n.locale).toBe('en'));
    expect(localStorage.getItem(LOCALE_KEY)).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect((screen.getByRole('radio', { name: 'English' }) as HTMLInputElement).checked).toBe(true);
  });
});
