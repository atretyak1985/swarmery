// @vitest-environment jsdom
//
// Docs in the frame (v4, handoff §9). The claims:
//
//   1. The docs are an accordion by group: the open doc's group is open, the others
//      show their label and count, and a group click opens its first doc.
//   2. A slug in the URL opens that doc; bare /docs opens the first.
//   3. The article keeps its "On this page" rail and its prev/next footer.
//   4. The list's filter finds a doc by its FILE.md.

import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DocDetail, DocMeta } from '../api/types';
import { cleanup, fireEvent, render, screen, within } from '../test/render';
import { Docs } from './Docs';

const LIST: DocMeta[] = [
  { slug: 'start', title: 'Getting started', file: 'guide-start.md' },
  { slug: 'more', title: 'Going further', file: 'guide-more.md' },
  { slug: 'api', title: 'The API', file: 'API.md' },
] as DocMeta[];

vi.mock('../api', () => ({
  fetchDocs: () => Promise.resolve(LIST),
  fetchDoc: (slug: string): Promise<DocDetail> =>
    Promise.resolve({
      slug,
      title: slug,
      file: `${slug}.md`,
      lang: 'en',
      markdown: `# Title of ${slug}\n\nBody of ${slug}.\n\n## First section\n\nText.\n`,
    } as DocDetail),
}));

function Where(): JSX.Element {
  return <div data-testid="where">{useLocation().pathname}</div>;
}

function mount(url: string): void {
  const page = (
    <>
      <Docs />
      <Where />
    </>
  );
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/docs" element={page} />
        <Route path="/docs/:slug" element={page} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('Docs place', () => {
  it('opens the first doc from bare /docs, in its open group', async () => {
    mount('/docs');
    expect(await screen.findByText('Body of start.')).toBeTruthy();
    const list = screen.getByRole('region', { name: 'Docs' });
    expect(within(list).getByRole('button', { name: /Going further/ })).toBeTruthy();
    expect(within(list).queryByRole('button', { name: /The API/ })).toBeNull();
  });

  it('opens a group on its first doc', async () => {
    mount('/docs/more');
    await screen.findByText('Body of more.');
    fireEvent.click(screen.getByRole('button', { name: /Reference/i }));
    expect(await screen.findByText('Body of api.')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/docs/api');
    expect(screen.getByRole('heading', { name: 'Title of api' })).toBeTruthy();
  });

  it('keeps the on-this-page rail and the prev/next footer', async () => {
    mount('/docs/more');
    await screen.findByText('Body of more.');
    expect(within(screen.getByRole('navigation', { name: 'On this page' })).getByText('First section')).toBeTruthy();
    expect(screen.getByRole('link', { name: /previous.*Getting started/ }).getAttribute('href')).toBe('/docs/start');
    expect(screen.getByRole('link', { name: /next.*The API/ }).getAttribute('href')).toBe('/docs/api');
  });

  it('filters by the file name', async () => {
    mount('/docs/start');
    await screen.findByText('Body of start.');
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'API.md' } });
    const list = screen.getByRole('region', { name: 'Docs' });
    expect(within(list).getByRole('button', { name: /The API/ })).toBeTruthy();
    expect(within(list).queryByRole('button', { name: /Getting started/ })).toBeNull();
  });
});
