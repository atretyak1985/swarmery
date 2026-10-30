// @vitest-environment jsdom
//
// System in the frame (v4, handoff §3, §9). The claims:
//
//   1. The list is one accordion of Agents · Skills · Commands · Templates · Hooks ·
//      Routines · Plugins · Mods · Insights, with no tab bar, and every count shows
//      while only one group is open.
//   2. The retired /system/toolkit lands on Skills, keeping a deeper path and the
//      query, and /system/skills/commands opens Commands.
//   3. Bare /system opens Agents on its first row; /system/agents/:id selects by path.
//   4. Routines lists its routines and keeps the whole page as "All routines".
//   5. Plugins lists the project's plugins under /p/:slug/system and says why not in
//      the fleet; Mods renders the audit without the origin filter.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/system/SystemShell.tabs.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SystemShell } from '../SystemShell';

const ROSTER = [
  { id: 11, name: 'planner', scope: 'global', projectSlug: null, origin: 'plugin', pluginName: 'core', model: 'opus', runs30d: 4, failedShare: 0, lastActiveAt: null },
  { id: 12, name: 'reviewer', scope: 'project', projectSlug: 'shop', origin: 'local', pluginName: null, model: 'sonnet', runs30d: 2, failedShare: 0.5, lastActiveAt: null },
];

vi.mock('../AgentHub', () => ({
  AgentDetail: ({ id }: { id: number }) => <div>agent detail {id}</div>,
  agentItem: (agent: { id: number; name: string }) => ({ key: String(agent.id), glyph: '☷', title: agent.name }),
}));
vi.mock('../SystemHub', () => ({
  CATALOG: ['skills', 'commands', 'templates', 'hooks'],
  CatalogDetail: ({ category, selectedKey }: { category: string; selectedKey: string }) => (
    <div>
      catalog detail · {category} · {selectedKey}
    </div>
  ),
  fetchRoster: (category: string) =>
    Promise.resolve(
      category === 'skills'
        ? [{ kind: 'skills', item: { id: 21, name: 'plan-phase', scope: 'global' } }]
        : [],
    ),
  rosterItem: (r: { item: { id: number; name: string } }) => ({ key: String(r.item.id), glyph: '✦', title: r.item.name }),
  rowKey: (r: { item: { id: number } }) => String(r.item.id),
  rowScope: (r: { item: { scope: string } }) => r.item.scope,
}));
vi.mock('../Routines', () => ({
  Routines: ({ embedded }: { embedded?: boolean }) => <div>routines · embedded={String(embedded)}</div>,
  RoutineDetail: ({ routine }: { routine: { name: string } }) => <div>routine detail · {routine.name}</div>,
  describeCron: (expr: string) => expr,
}));
vi.mock('../../components/ProjectPlugins', () => ({
  ProjectPlugins: ({ projectId }: { projectId: number }) => <div>plugins of project {projectId}</div>,
}));
vi.mock('./ModsTab', () => ({ ModsTab: () => <div>mods audit</div> }));
vi.mock('./InsightsTab', () => ({ InsightsTab: () => <div>insights inbox</div> }));
vi.mock('./SkillsToExtendHint', () => ({ SkillsToExtendHint: () => null }));
vi.mock('../../components/ScopeChip', () => ({ ScopeChip: () => null }));
vi.mock('../../api', () => ({
  fetchProjects: vi.fn(() => Promise.resolve([])),
  fetchRoutines: vi.fn(() =>
    Promise.resolve([{ id: 'r1', name: 'Nightly retro', cronExpr: '30 2 * * *', enabled: true, lastRunAt: null }]),
  ),
}));
vi.mock('../../api/agentHub', () => ({
  fetchAgentRoster: vi.fn(() => Promise.resolve({ agents: ROSTER })),
}));
vi.mock('../../api/systemHub', () => ({
  fetchSystemHubSummary: vi.fn(() => new Promise(() => undefined)),
}));
vi.mock('../../api/system', () => ({
  fetchSystemSummary: vi.fn(() => new Promise(() => undefined)),
  fetchSkillAuditReport: vi.fn(() => new Promise(() => undefined)),
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
        <Route path="/system" element={shell} />
        <Route path="/system/*" element={shell} />
        <Route path="/p/:slug/system/*" element={shell} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('SystemShell', () => {
  it('is one tab per group, nine of them, under lower-case labels', async () => {
    renderAt('/system/agents');
    await screen.findByText('agent detail 11');
    expect(screen.getAllByRole('tab')).toHaveLength(9);
    for (const group of ['agents', 'skills', 'commands', 'templates', 'hooks', 'routines', 'plugins', 'mods', 'insights']) {
      expect(screen.getByRole('tab', { name: new RegExp(`^${group}`) })).toBeTruthy();
    }
    // One group picked: its rows show, another group's do not, its count does.
    expect(screen.getByRole('button', { name: /planner/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /plan-phase/ })).toBeNull();
  });

  it('opens Agents on its first row from bare /system, and selects by path', async () => {
    renderAt('/system');
    expect(await screen.findByText('agent detail 11')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/system/agents');
    cleanup();
    renderAt('/system/agents/12?tab=runs');
    expect(await screen.findByText('agent detail 12')).toBeTruthy();
  });

  it('selects a row in the URL and keeps the section', async () => {
    renderAt('/system/agents?tab=runs');
    fireEvent.click(await screen.findByRole('button', { name: /reviewer/ }));
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/system/agents/12?tab=runs'));
    expect(await screen.findByText('agent detail 12')).toBeTruthy();
  });

  it('picking another tab replaces the previous rows', async () => {
    renderAt('/system/agents');
    fireEvent.click(await screen.findByRole('tab', { name: /^skills/ }));
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/system/skills'));
    expect(await screen.findByText('catalog detail · skills · 21')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /planner/ })).toBeNull();
  });

  it('lands /system/toolkit on Skills', async () => {
    renderAt('/system/toolkit');
    expect(await screen.findByText('catalog detail · skills · 21')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/system/skills');
  });

  it('keeps the deeper path and query of a toolkit link', async () => {
    renderAt('/p/shop/system/toolkit/commands/5?tab=docs');
    expect(await screen.findByText('catalog detail · commands · 5')).toBeTruthy();
    expect(screen.getByTestId('where').textContent).toBe('/p/shop/system/skills/commands/5?tab=docs');
  });

  it('lists routines and keeps the whole page as All routines', async () => {
    renderAt('/system/routines');
    expect(await screen.findByText('routine detail · Nightly retro')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /All routines/ }));
    expect(await screen.findByText('routines · embedded=true')).toBeTruthy();
  });

  it('lists the project plugins under a project, a note in the fleet', async () => {
    renderAt('/p/shop/system/plugins');
    expect(await screen.findByText('plugins of project 7')).toBeTruthy();
    cleanup();
    renderAt('/system/plugins');
    expect(await screen.findByText('Plugins are enabled per project — pick one in the switcher.')).toBeTruthy();
  });

  it('renders the Mods audit without the origin filter', async () => {
    renderAt('/system/mods');
    expect(await screen.findByText('mods audit')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'all scopes' })).toBeNull();
    cleanup();
    renderAt('/system/agents');
    await screen.findByText('agent detail 11');
    expect(screen.getByRole('button', { name: 'all scopes' })).toBeTruthy();
  });
});
