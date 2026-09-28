// @vitest-environment jsdom
//
// Knowledge (Canvas v3 phase 8). The claims:
//
//   1. Four tabs in order — Memory · Architecture · Serena · Graphify — with
//      Memory selected when `?tab=` is absent. Docs is a sidebar place now.
//   2. `?tab=graphify` selects Graphify and renders the project Graphify body.
//   3. Clicking a tab writes `?tab=` and swaps the body.
//   4. A stale `?tab=docs` link falls back to Memory.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/knowledge
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Knowledge } from './Knowledge';

vi.mock('../Memory', () => ({ Memory: () => <div>memory body</div> }));
vi.mock('../../workspace/ScopedPages', () => ({
  ScopedArchitecture: () => <div>architecture body</div>,
  ScopedSerena: () => <div>serena body</div>,
  ScopedGraphify: () => <div>graphify body</div>,
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
  it('renders the four tabs in order, Memory by default', async () => {
    renderAt('/p/shop/knowledge');
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['Memory', 'Architecture', 'Serena', 'Graphify']);
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

  it('falls back to Memory for a stale ?tab=docs link', async () => {
    renderAt('/p/shop/knowledge?tab=docs');
    expect(screen.getByRole('tab', { name: 'Memory' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('memory body')).toBeTruthy();
  });
});
