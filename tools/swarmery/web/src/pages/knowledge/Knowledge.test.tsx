// @vitest-environment jsdom
//
// Knowledge (Canvas v3 phase 8). The claims:
//
//   1. Three accordion groups in order: Memory · Architecture · Tooling, with
//      Memory open when `?tab=` is absent and one file row per memory file.
//   2. `?tab=graphify` opens Tooling and renders the project Graphify body.
//   3. Clicking a row writes `?tab=` and swaps the body.
//   4. A stale `?tab=docs` link falls back to Memory.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/knowledge`.

import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import { Knowledge } from './Knowledge';

vi.mock('../Memory', () => ({
  KIND_LABELS: { 'claude-md': { id: 'instructions' }, 'auto-memory': { id: 'auto' }, serena: { id: 'serena' } },
  fmtBytes: (n: number) => `${String(n)} B`,
  Memory: () => <div>memory panels</div>,
  MemoryEditor: ({ path }: { path: string }) => <div>editor of {path}</div>,
}));
vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, fetchMemoryList: vi.fn() };
});
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

function group(name: string): HTMLElement {
  return screen.getByRole('tab', { name: new RegExp(`^${name.toLowerCase()}\\s*\\d*$`) });
}

beforeEach(() => {
  vi.mocked(api.fetchMemoryList).mockResolvedValue({
    files: [
      {
        kind: 'claude-md',
        path: '/repo/CLAUDE.md',
        name: 'CLAUDE.md',
        sizeBytes: 10,
        updatedAt: new Date().toISOString(),
        writable: true,
      },
    ],
  });
});

afterEach(cleanup);

describe('Knowledge', () => {
  it('renders three groups in order, Memory open with one row per file', async () => {
    renderAt('/p/shop/knowledge');
    const list = within(screen.getByRole('region', { name: 'Knowledge' }));
    expect(list.getAllByRole('tab')).toHaveLength(3);
    expect(list.getAllByRole('tab', { selected: true })).toHaveLength(1);
    expect(group('Memory').getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('editor of /repo/CLAUDE.md')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 2, name: 'CLAUDE.md' })).toBeTruthy();
    expect(group('Tooling').getAttribute('aria-selected')).toBe('false');
  });

  it('opens Tooling on the row named by ?tab=graphify', async () => {
    renderAt('/p/shop/knowledge?tab=graphify');
    expect(group('Tooling').getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('graphify body')).toBeTruthy();
    expect(screen.queryByText('memory panels')).toBeNull();
  });

  it('writes ?tab= on a row click', async () => {
    renderAt('/p/shop/knowledge');
    fireEvent.click(group('Tooling'));
    expect(await screen.findByText('serena body')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /Graphify/ }));
    expect(await screen.findByText('graphify body')).toBeTruthy();
    expect(screen.getByTestId('search').textContent).toBe('?tab=graphify');
  });

  it('keeps the index and stale facts panels on their own row', async () => {
    renderAt('/p/shop/knowledge');
    fireEvent.click(await screen.findByRole('button', { name: /Index and stale facts/ }));
    expect(await screen.findByText('memory panels')).toBeTruthy();
  });

  it('falls back to Memory for a stale ?tab=docs link', async () => {
    renderAt('/p/shop/knowledge?tab=docs');
    expect(group('Memory').getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('editor of /repo/CLAUDE.md')).toBeTruthy();
  });
});
