// @vitest-environment jsdom
//
// The Docs screen follows the UI language (dashboard-uk-locale phase 5): a
// language switch re-fetches the list and the open doc in place, and a
// Ukrainian interface showing an English doc (no translation yet) says so.
//
// web/tsconfig.json EXCLUDES *.test.tsx and vitest transpiles without type
// checking, so nothing type-checks this file — treat its types as documentation.

import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DocDetail, DocMeta } from '../api/types';
import { activate } from '../i18n';
import { act, cleanup, render, screen, waitFor } from '../test/render';
import { Docs } from './Docs';

const fetchDocs = vi.fn<() => Promise<DocMeta[]>>();
const fetchDoc = vi.fn<(slug: string) => Promise<DocDetail>>();
vi.mock('../api', () => ({
  fetchDocs: () => fetchDocs(),
  fetchDoc: (slug: string) => fetchDoc(slug),
}));

const NOTE_EN = 'This document is not translated yet; the English original is shown.';

function doc(lang: 'en' | 'uk'): DocDetail {
  return {
    slug: 'onboarding',
    title: lang === 'uk' ? 'Підключення' : 'Onboarding',
    file: 'ONBOARDING.md',
    lang,
    markdown: lang === 'uk' ? '# Підключення\n\nТекст.' : '# Onboarding\n\nText.',
  };
}

function mount(): void {
  render(
    <MemoryRouter initialEntries={['/docs/onboarding']}>
      <Routes>
        <Route path="/docs/:slug" element={<Docs />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(async () => {
  cleanup();
  fetchDocs.mockReset();
  fetchDoc.mockReset();
  await act(() => activate('en'));
});

describe('Docs language', () => {
  it('re-fetches the list and the open doc when the language switches', async () => {
    let lang: 'en' | 'uk' = 'en';
    fetchDocs.mockImplementation(() => Promise.resolve([doc(lang)]));
    fetchDoc.mockImplementation(() => Promise.resolve(doc(lang)));
    mount();
    await screen.findByText('Text.');
    expect(fetchDocs).toHaveBeenCalledTimes(1);
    expect(fetchDoc).toHaveBeenCalledTimes(1);

    lang = 'uk';
    await act(() => activate('uk'));
    await screen.findByText('Текст.');
    expect(fetchDocs).toHaveBeenCalledTimes(2);
    expect(fetchDoc).toHaveBeenCalledTimes(2);
    // Translated: no fallback note.
    expect(document.body.textContent).not.toContain('Цей документ ще не перекладено');
  });

  it('notes the English fallback under a Ukrainian interface only', async () => {
    fetchDocs.mockResolvedValue([doc('en')]);
    fetchDoc.mockResolvedValue(doc('en'));
    mount();
    await screen.findByText('Text.');
    expect(document.body.textContent).not.toContain(NOTE_EN);

    await act(() => activate('uk'));
    await waitFor(() =>
      expect(document.body.textContent).toContain(
        'Цей документ ще не перекладено; показано англійський оригінал.',
      ),
    );
  });
});
