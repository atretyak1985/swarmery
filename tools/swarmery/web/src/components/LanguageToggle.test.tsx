import { afterEach, describe, expect, it } from 'vitest';
import { i18n } from '../i18n';
import { LOCALE_KEY, setLocale } from '../i18n/locale';
import { cleanup, fireEvent, render, screen } from '../test/render';
import { LanguageToggle } from './LanguageToggle';

describe('LanguageToggle', () => {
  afterEach(async () => {
    cleanup();
    localStorage.removeItem(LOCALE_KEY);
    await setLocale('en');
  });

  it('shows the code of the language it switches to and flips on click', async () => {
    render(<LanguageToggle />);
    const button = screen.getByRole('button');
    expect(button.textContent).toBe('UK');
    fireEvent.click(button);
    await new Promise((r) => setTimeout(r, 50));
    expect(i18n.locale).toBe('uk');
    expect(localStorage.getItem(LOCALE_KEY)).toBe('uk');
    expect(screen.getByRole('button').textContent).toBe('EN');
  });
});
