// @vitest-environment jsdom
//
// System → Mods tab (mods-swarmery-link phase 9). The claims:
//
//   1. Rows render from GET /api/system/mods with their risk badges, coloured
//      by level (red / yellow / unknown) and labelled by rule.
//   2. An empty list shows "No mods installed".
//   3. Selecting a row opens the full validate report (GET …/mods/{id}): the
//      folder path, warnings, env, gating hooks — never module contents.
//   4. Rescan POSTs, is disabled while pending, then refetches the list — and
//      the open validate report, which a failed rescan leaves alone.
//   5. A failed list fetch shows the error with a retry; a failed rescan shows
//      the server's message.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/system/ModsTab.test.tsx`.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ModDetail, ModRescanResult, ModSummary } from '../../api/types';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { ModsTab } from './ModsTab';

vi.mock('../../api/system', () => ({
  fetchSystemMods: vi.fn(),
  fetchSystemMod: vi.fn(),
  rescanSystemMods: vi.fn(),
}));

import { fetchSystemMod, fetchSystemMods, rescanSystemMods } from '../../api/system';

const listMock = vi.mocked(fetchSystemMods);
const detailMock = vi.mocked(fetchSystemMod);
const rescanMock = vi.mocked(rescanSystemMods);

const LINK: ModSummary = {
  id: 1,
  name: 'link-spike',
  pluginId: 'link-spike@local',
  dir: '/home/me/.claude/plugins/cache/local/link-spike/0.1.0',
  source: 'cache',
  version: '0.1.0',
  moduleSha: 'abcdef0123456789',
  events: ['session.start', 'tool.check', 'prompt.submit'],
  uses: ['http.fetch', 'fs.read'],
  env: { reads: ['HOME'], writes: [] },
  state: [],
  risk: [
    { level: 'red', rule: 'tool.check', label: 'can approve tool calls', matches: ['tool.check'] },
    { level: 'red', rule: 'http.fetch', label: 'network', matches: ['http.fetch'] },
    { level: 'yellow', rule: 'fs.read', label: 'reads files', matches: ['fs.read'] },
  ],
  errors: [],
  validatedAt: '2026-10-13T20:00:00Z',
};

const BROKEN: ModSummary = {
  id: 2,
  name: 'broken',
  pluginId: null,
  dir: '/dev/broken',
  source: 'plugin-dir',
  version: null,
  moduleSha: null,
  events: [],
  uses: [],
  env: { reads: [], writes: [] },
  state: [],
  risk: [{ level: 'unknown', rule: 'validate', label: 'not validated', matches: [] }],
  errors: ['validate /dev/broken: invalid JSON'],
  validatedAt: null,
};

const LINK_DETAIL: ModDetail = {
  ...LINK,
  report: {
    validated: true,
    success: false,
    errors: [],
    warnings: ['plugin.json: missing author'],
    events: LINK.events,
    uses: LINK.uses,
    env: LINK.env,
    state: ['link.token'],
    gatingHooks: [{ module: './register.tsx', pattern: '*', hook: 'tool.check', hasCatch: true }],
  },
};

const RESCAN: ModRescanResult = { mods: 2, validated: 2, parseErrors: 1, removed: 0 };

beforeEach(() => {
  listMock.mockReset();
  detailMock.mockReset();
  rescanMock.mockReset();
});
afterEach(cleanup);

describe('ModsTab', () => {
  it('renders rows with risk badges coloured by level', async () => {
    listMock.mockResolvedValue([LINK, BROKEN]);
    render(<ModsTab />);

    expect(await screen.findByRole('button', { name: /link-spike/ })).toBeTruthy();
    expect(screen.getByText('link-spike@local')).toBeTruthy();
    expect(screen.getByText('session.start, tool.check, prompt.submit')).toBeTruthy();
    expect(screen.getByText('3 events')).toBeTruthy();
    expect(screen.getByText('2 calls')).toBeTruthy();
    expect(screen.getByText('1 validate error')).toBeTruthy();

    const approve = screen.getByText('can approve tool calls').closest('[data-level]');
    expect(approve?.getAttribute('data-level')).toBe('red');
    expect(approve?.className).toContain('text-red');
    const reads = screen.getByText('reads files').closest('[data-level]');
    expect(reads?.getAttribute('data-level')).toBe('yellow');
    expect(reads?.className).toContain('text-amber');
    const unknown = screen.getByText('not validated').closest('[data-level]');
    expect(unknown?.getAttribute('data-level')).toBe('unknown');
    expect(unknown?.className).toContain('text-ink-dim');
  });

  it('shows the empty state when no mod is installed', async () => {
    listMock.mockResolvedValue([]);
    render(<ModsTab />);
    expect(await screen.findByText('No mods installed')).toBeTruthy();
  });

  it('opens the full validate report for a selected row', async () => {
    listMock.mockResolvedValue([LINK]);
    detailMock.mockResolvedValue(LINK_DETAIL);
    render(<ModsTab />);

    const name = await screen.findByRole('button', { name: /show the validate report of link-spike/ });
    expect(name.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(name);

    expect(detailMock).toHaveBeenCalledWith(1);
    expect(await screen.findByText('plugin.json: missing author')).toBeTruthy();
    expect(screen.getByText(LINK.dir)).toBeTruthy();
    expect(screen.getByText('link.token')).toBeTruthy();
    expect(screen.getByText('./register.tsx')).toBeTruthy();
    expect(screen.getByText('failed')).toBeTruthy();
    expect(screen.getByRole('button', { name: /hide the validate report/ }).getAttribute('aria-expanded')).toBe(
      'true',
    );

    fireEvent.click(screen.getByRole('button', { name: 'close' }));
    await waitFor(() => expect(screen.queryByText('plugin.json: missing author')).toBeNull());
  });

  it('rescans, disables the button while pending, then refetches', async () => {
    listMock.mockResolvedValueOnce([LINK]).mockResolvedValueOnce([LINK, BROKEN]);
    let finish: (r: ModRescanResult) => void = () => undefined;
    rescanMock.mockImplementation(
      () =>
        new Promise<ModRescanResult>((resolve) => {
          finish = resolve;
        }),
    );
    render(<ModsTab />);
    await screen.findByRole('button', { name: /link-spike/ });

    fireEvent.click(screen.getByRole('button', { name: 'Rescan' }));
    const pending = screen.getByRole('button', { name: 'rescanning…' }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);
    expect(rescanMock).toHaveBeenCalledTimes(1);

    finish(RESCAN);
    expect(await screen.findByRole('button', { name: /broken/ })).toBeTruthy();
    expect(listMock).toHaveBeenCalledTimes(2);
    expect(screen.getByText('rescan: 2 mods · 2 validated · 1 parse error · 0 removed')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Rescan' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('refetches an open validate report after a successful rescan', async () => {
    listMock.mockResolvedValue([LINK]);
    const rescanned: ModDetail = {
      ...LINK_DETAIL,
      report: { ...LINK_DETAIL.report, warnings: ['plugin.json: missing license'] },
    };
    detailMock.mockResolvedValueOnce(LINK_DETAIL).mockResolvedValueOnce(rescanned);
    rescanMock.mockResolvedValue(RESCAN);
    render(<ModsTab />);

    fireEvent.click(await screen.findByRole('button', { name: /show the validate report of link-spike/ }));
    expect(await screen.findByText('plugin.json: missing author')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Rescan' }));
    expect(await screen.findByText('plugin.json: missing license')).toBeTruthy();
    expect(screen.queryByText('plugin.json: missing author')).toBeNull();
    expect(detailMock).toHaveBeenCalledTimes(2);
    expect(detailMock).toHaveBeenLastCalledWith(1);
  });

  it('keeps the open report as is when a rescan fails', async () => {
    listMock.mockResolvedValue([LINK]);
    detailMock.mockResolvedValue(LINK_DETAIL);
    rescanMock.mockRejectedValue(new Error('boom'));
    render(<ModsTab />);

    fireEvent.click(await screen.findByRole('button', { name: /show the validate report of link-spike/ }));
    expect(await screen.findByText('plugin.json: missing author')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Rescan' }));
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(detailMock).toHaveBeenCalledTimes(1);
  });

  it('shows a failed list fetch with a retry, and a failed rescan with its message', async () => {
    listMock.mockRejectedValueOnce(new Error('GET /api/system/mods: 500')).mockResolvedValueOnce([LINK]);
    render(<ModsTab />);

    expect(await screen.findByText('GET /api/system/mods: 500')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'retry' }));
    expect(await screen.findByRole('button', { name: /link-spike/ })).toBeTruthy();

    rescanMock.mockRejectedValueOnce(new Error('mods scanner not attached (serve without --no-ingest)'));
    fireEvent.click(screen.getByRole('button', { name: 'Rescan' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toBe('rescan failed: mods scanner not attached (serve without --no-ingest)');
    expect(listMock).toHaveBeenCalledTimes(2);
  });
});
