// @vitest-environment jsdom
//
// System shell tabs (Canvas v3 phase 8). The claims:
//
//   1. The tab bar reads Agents · Skills · Plugins · Hooks · Routines · Insights.
//   2. The retired /system/toolkit lands on Skills (the skills catalog), keeping
//      a deeper path and the query.
//   3. Routines renders the Routines page embedded (no heading of its own).
//   4. Plugins lists the project's plugins under /p/:slug/system and shows an
//      empty state in the fleet.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/system/SystemShell.tabs.test.tsx`.

import { cleanup, render, screen } from '../../test/render';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SystemShell } from '../SystemShell';

vi.mock('../AgentHub', () => ({ AgentHub: () => <div>agent hub</div> }));
vi.mock('../SystemHub', () => ({
  SystemHub: ({ forceCategory, routeBase }: { forceCategory: string; routeBase: string }) => (
    <div>
      system hub · {forceCategory} · {routeBase}
    </div>
  ),
}));
vi.mock('../Routines', () => ({
  Routines: ({ embedded }: { embedded?: boolean }) => <div>routines · embedded={String(embedded)}</div>,
}));
vi.mock('../../components/ProjectPlugins', () => ({
  ProjectPlugins: ({ projectId }: { projectId: number }) => <div>plugins of project {projectId}</div>,
}));
vi.mock('../../components/ScopeChip', () => ({ ScopeChip: () => null }));
vi.mock('../../api/systemHub', () => ({
  fetchSystemHubSummary: vi.fn(() => new Promise(() => undefined)),
}));
vi.mock('../../api/system', () => ({
  fetchSystemSummary: vi.fn(() => new Promise(() => undefined)),
}));
vi.mock('../../lib/ws', () => ({ useLiveUpdates: vi.fn() }));
vi.mock('../../lib/scope', () => ({
  useScope: () => ({ scope: null, projects: [] }),
  useProjectScope: (key: string | null | undefined) =>
    key == null || key === '' ? { id: null, pending: false } : { id: '7', pending: false },
}));

function Where(): JSX.Element {
  const { pathname, search } = useLocation();
  return <div data-testid="where">{`${pathname}${search}`}</div>;
}

function renderAt(url: string): void {
  const shell = (
    <>
      <SystemShell />
      <Where />
    </>
  );
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/system/*" element={shell} />
        <Route path="/p/:slug/system/*" element={shell} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('SystemShell tabs', () => {
  it('reads Agents · Skills · Plugins · Hooks · Routines · Insights', () => {
    renderAt('/system/agents');
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual([
      'Agents',
      'Skills',
      'Plugins',
      'Hooks',
      'Routines',
      'Insights',
    ]);
  });

  it('lands /system/toolkit on Skills', async () => {
    renderAt('/system/toolkit');
    expect(await screen.findByText('system hub · skills · /system/skills')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/system/skills');
    expect(screen.getByRole('tab', { name: 'Skills' }).getAttribute('aria-selected')).toBe('true');
  });

  it('keeps the deeper path and query of a toolkit link', async () => {
    renderAt('/p/shop/system/toolkit/commands?tab=docs');
    expect(await screen.findByText('system hub · skills · /p/shop/system/skills')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/p/shop/system/skills/commands?tab=docs');
  });

  it('renders Routines embedded on the Routines tab', () => {
    renderAt('/system/routines');
    expect(screen.getByText('routines · embedded=true')).toBeTruthy();
  });

  it('lists the project plugins under a project, an empty state in the fleet', () => {
    renderAt('/p/shop/system/plugins');
    expect(screen.getByText('plugins of project 7')).toBeTruthy();
    cleanup();
    renderAt('/system/plugins');
    expect(screen.getByText('Plugins are enabled per project — pick one in the switcher.')).toBeTruthy();
  });
});
