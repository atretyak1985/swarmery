// @vitest-environment jsdom
//
// Knowledge (Canvas v3 phase 8). The claims:
//
//   1. Five tabs in order — Memory · Architecture · Serena · Graphify · Docs —
//      with Memory selected when `?tab=` is absent.
//   2. `?tab=graphify` selects Graphify and renders the project Graphify body.
//   3. Clicking a tab writes `?tab=` and swaps the body.
//   4. The Docs tab does NOT see the project slug as its doc slug: Docs reads
//      `useParams().slug`, which under /p/:slug would otherwise be the project.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/knowledge
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Knowledge } from './Knowledge';

vi.mock('../Memory', () => ({ Memory: () => <div>memory body</div> }));
vi.mock('../../workspace/ScopedPages', () => ({
  ScopedArchitecture: () => <div>architecture body</div>,
  ScopedSerena: () => <div>serena body</div>,
  ScopedGraphify: () => <div>graphify body</div>,
}));
vi.mock('../Docs', () => ({
  Docs: () => {
    const { slug } = useParams<{ slug: string }>();
    return <div>docs body · slug={slug ?? 'none'}</div>;
  },
}));

function Where(): JSX.Element {
  const { search } = useLocation();
  return <div data-testid="search">{search}</div>;
}

function renderAt(url: string): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route
          path="/p/:slug/knowledge"
          element={
            <>
              <Knowledge />
              <Where />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('Knowledge', () => {
  it('renders the five tabs in order, Memory by default', async () => {
    renderAt('/p/shop/knowledge');
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['Memory', 'Architecture', 'Serena', 'Graphify', 'Docs']);
    expect(screen.getByRole('tab', { name: 'Memory' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('memory body')).toBeTruthy();
  });

  it('selects the tab named by ?tab=graphify', async () => {
    renderAt('/p/shop/knowledge?tab=graphify');
    expect(screen.getByRole('tab', { name: 'Graphify' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('graphify body')).toBeTruthy();
    expect(screen.queryByText('memory body')).toBeNull();
  });

  it('writes ?tab= on a tab click', async () => {
    renderAt('/p/shop/knowledge');
    fireEvent.click(screen.getByRole('tab', { name: 'Serena' }));
    expect(await screen.findByText('serena body')).toBeTruthy();
    expect(screen.getByTestId('search').textContent).toBe('?tab=serena');
  });

  it('opens Docs without handing it the project slug', async () => {
    renderAt('/p/shop/knowledge?tab=docs');
    expect(await screen.findByText('docs body · slug=none')).toBeTruthy();
  });
});
