// @vitest-environment jsdom
//
// Health (Canvas v3 phase 5). The claims:
//
//   1. `?tab=advisor` selects the Advisor tab and renders the retro advisor
//      section; the strip carries one range control and six tabs.
//   2. Changing the range rewrites `?days=` and refetches with the new from/to.
//   3. The strip's decision cells link to Inbox and to Learning's proof tab,
//      in the fleet and in a project.
//   4. The embedded Retro (Agents) and Analytics (Cost) render no range row of
//      their own: exactly one range control on the page.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/health`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import { addDays, isoDay } from '../../lib/format';
import { Health } from './Health';

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return {
    ...real,
    fetchRetroAgents: vi.fn(),
    fetchRetroFriction: vi.fn(),
    fetchRecommendations: vi.fn(),
    fetchProposals: vi.fn(),
    fetchHealth: vi.fn(),
    unmuteFrictionGroup: vi.fn(),
  };
});

const agentsResp = {
  from: '2026-09-15',
  to: '2026-09-28',
  approx: false,
  main: { cost_usd: 10, tokens_out: 0, errors: 0 },
  agents: [
    {
      agent: 'implementation-agent',
      runs: 60,
      sessions: 20,
      cost_usd: 30,
      tokens_out: 0,
      errors: 6,
      error_rate: 0.1,
      avg_ms: null,
      p95_ms: null,
      success_rate: null,
      re_dispatch_rate: null,
      eval: null,
      prev: { runs: 60, errors: 30, error_rate: 0.5, cost_usd: 20 },
    },
  ],
};

const frictionResp = {
  denied_tools: [{ tool: 'Bash', denied: 7, calls: 12, has_rule: false }],
  error_groups: [{ key: 'k', example: 'TestRender failed', count: 4, last_ts: new Date().toISOString(), sessions: ['a'] }],
  approvals: { resolved: 0, avg_resolve_sec: null, wait_total_min: 0, pending: 0 },
  approx: false,
};

function rec(id: number, status: string): Record<string, unknown> {
  return {
    id,
    rule: 'R2',
    target_kind: 'agent',
    target: 'x',
    title: `rec ${String(id)}`,
    detail: '',
    evidence: {},
    baseline: null,
    status,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
}

function LocationProbe(): JSX.Element {
  const loc = useLocation();
  return <div data-testid="loc">{`${loc.pathname}${loc.search}`}</div>;
}

function renderAt(url: string): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route
          path="/health"
          element={
            <>
              <Health />
              <LocationProbe />
            </>
          }
        />
        <Route
          path="/p/:slug/health"
          element={
            <>
              <Health />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  // Anything the embedded pages fetch beyond the four mocks fails fast and
  // lands in their own catch branches.
  vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new Error('offline in test'))));
  vi.mocked(api.fetchRetroAgents).mockResolvedValue(agentsResp as never);
  vi.mocked(api.fetchRetroFriction).mockResolvedValue(frictionResp as never);
  vi.mocked(api.fetchRecommendations).mockResolvedValue({
    recommendations: [rec(1, 'proposed'), rec(2, 'verified'), rec(3, 'verified'), rec(4, 'accepted')],
  } as never);
  vi.mocked(api.fetchProposals).mockResolvedValue({
    proposals: [{ id: 9, status: 'proposed' }],
  } as never);
  // The daemon is unreachable unless a test says otherwise: no auto mode row.
  vi.mocked(api.fetchHealth).mockRejectedValue(new Error('offline in test'));
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('Health', () => {
  it('selects the Advisor tab from ?tab=advisor and renders six tabs under one range', async () => {
    renderAt('/health?tab=advisor');
    const tablist = screen.getByRole('tablist', { name: 'Health' });
    expect(within(tablist).getAllByRole('tab')).toHaveLength(6);
    expect(within(tablist).getByRole('tab', { name: /Advisor/ }).getAttribute('aria-selected')).toBe('true');
    expect(within(tablist).getByRole('tab', { name: /Overview/ }).getAttribute('aria-selected')).toBe('false');
    // The retro advisor section (lazy) renders its recommendations rail.
    expect(await screen.findByText('Recommendations')).toBeTruthy();
    await waitFor(() => expect(within(tablist).getByRole('tab', { name: /Advisor/ }).textContent).toContain('2 open'));
    expect(screen.getAllByRole('radiogroup')).toHaveLength(1);
  });

  it('refetches with the new from/to when the range changes', async () => {
    const today = isoDay();
    renderAt('/health');
    await waitFor(() =>
      expect(api.fetchRetroAgents).toHaveBeenCalledWith({ from: addDays(today, -13), to: today }),
    );
    fireEvent.click(screen.getByRole('radio', { name: '30 d' }));
    await waitFor(() =>
      expect(api.fetchRetroAgents).toHaveBeenLastCalledWith({ from: addDays(today, -29), to: today }),
    );
    expect(screen.getByTestId('loc').textContent).toBe('/health?days=30');
    expect(screen.getByRole('radio', { name: '30 d' }).getAttribute('aria-checked')).toBe('true');
  });

  it('links the decision cells to Inbox and Learning proof', async () => {
    renderAt('/health');
    const waiting = await screen.findByRole('link', { name: /waiting on you/i });
    expect(waiting.getAttribute('href')).toBe('/inbox');
    await waitFor(() => expect(waiting.textContent).toContain('1 Advisor finding · 1 agent rewrite to approve'));
    const because = screen.getByRole('link', { name: /because of you/i });
    expect(because.getAttribute('href')).toBe('/learning?tab=proof');
    expect(because.textContent).toContain('2 changes verified · 1 gathering proof');
    // The window cell: 60 runs, 10 % failed, down from 50 %, $40 spent.
    await waitFor(() => expect(screen.getByText(/60 runs · 10 % failed/)).toBeTruthy());
    expect(screen.getByText('↓ from 50 %')).toBeTruthy();
  });

  it('links the Inbox cell into the project under /p/:slug', async () => {
    renderAt('/p/shop/health');
    const waiting = await screen.findByRole('link', { name: /waiting on you/i });
    expect(waiting.getAttribute('href')).toBe('/p/shop/inbox');
  });

  it('renders the overview: one sentence, agent rows and friction', async () => {
    renderAt('/health');
    expect(
      await screen.findByText(
        'The fleet is failing half as often as the 14 days before; almost all of the change is implementation-agent.',
      ),
    ).toBeTruthy();
    expect(screen.getByText('50 % → 10 % failed')).toBeTruthy();
    expect(screen.getByRole('button', { name: '+ always allow' })).toBeTruthy();
    expect(screen.getByText('Same error 4 times in this window')).toBeTruthy();
  });

  it('shows the auto mode permission check on the overview, alerting into the Inbox', async () => {
    vi.mocked(api.fetchHealth).mockResolvedValue({
      status: 'ok',
      version: '0.2.1',
      db_size_bytes: 1,
      watching: true,
      autoModeClassifier: {
        noVerdictLastHour: 9,
        sessionsLastHour: 2,
        lastAt: new Date().toISOString(),
        alerting: true,
      },
    });
    renderAt('/p/shop/health');
    const row = await screen.findByRole('status', { name: 'Auto mode permission check' });
    expect(row.textContent).toContain('9 checks got no verdict in the last hour · in 2 sessions');
    expect(within(row).getByRole('link', { name: /alerting/ }).getAttribute('href')).toBe(
      '/p/shop/inbox?tab=alerts',
    );
  });

  it('renders no auto mode row when the daemon does not report the field', async () => {
    vi.mocked(api.fetchHealth).mockResolvedValue({ status: 'ok', version: '0.2.0', db_size_bytes: 1, watching: true });
    renderAt('/health');
    await screen.findByRole('link', { name: /waiting on you/i });
    expect(screen.queryByRole('status', { name: 'Auto mode permission check' })).toBeNull();
  });

  it('keeps exactly one range control on the Agents tab', async () => {
    renderAt('/health?tab=agents');
    expect(await screen.findByText(/Agent scorecards/)).toBeTruthy();
    expect(screen.getAllByRole('radiogroup')).toHaveLength(1);
    expect(screen.queryByRole('button', { name: '14d' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Retro' })).toBeNull();
  });

  it('keeps exactly one range control on the Cost tab', async () => {
    renderAt('/health?tab=cost');
    expect(await screen.findByText('Metric', {}, { timeout: 5000 })).toBeTruthy();
    expect(screen.getAllByRole('radiogroup')).toHaveLength(1);
    expect(screen.queryByRole('button', { name: '14d' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Analytics' })).toBeNull();
  });
});

describe('standalone pages keep their own range row without props', () => {
  it('Retro with no props still renders its H1, presets and every section', async () => {
    const { Retro } = await import('../Retro');
    render(
      <MemoryRouter>
        <Retro />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'Retro' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '14d' })).toBeTruthy();
    expect(await screen.findByText(/Agent scorecards/)).toBeTruthy();
    expect(await screen.findByText('Recommendations')).toBeTruthy();
    expect(await screen.findByText(/Friction board/)).toBeTruthy();
  });

  it('Retro with a section renders only that section', async () => {
    const { Retro } = await import('../Retro');
    render(
      <MemoryRouter>
        <Retro section="friction" range={{ from: '2026-09-01', to: '2026-09-28' }} />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/Friction board/)).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Retro' })).toBeNull();
    expect(screen.queryByText(/Agent scorecards/)).toBeNull();
    expect(screen.queryByText('Recommendations')).toBeNull();
    expect(api.fetchRetroAgents).not.toHaveBeenCalled();
    expect(api.fetchRetroFriction).toHaveBeenCalledWith({ from: '2026-09-01', to: '2026-09-28' });
  });

  it('Analytics with no props still renders its H1 and date presets', async () => {
    const { Analytics } = await import('../Analytics');
    render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'Analytics' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '14d' })).toBeTruthy();
  });
});

describe('friction triage', () => {
  const now = new Date().toISOString();
  const group = (key: string, example: string, count: number, triage?: object): object => ({
    key,
    example,
    count,
    last_ts: now,
    sessions: [],
    ...(triage !== undefined ? { triage } : {}),
  });
  const triagedResp = {
    ...frictionResp,
    error_groups: [
      group('m', 'muted failure', 9, {
        state: 'muted',
        reason: 'flaky upstream, nothing to fix',
        mutedUntil: '2026-08-10T00:00:00Z',
      }),
      group('t', 'tracked failure', 8, { state: 'tracked', recommendationId: 5 }),
      group('f', 'fix proposed failure', 7, { state: 'fix_proposed', recommendationId: 6 }),
      group('u', 'plain untriaged failure', 4),
      group('u2', 'explicit untriaged failure', 2, { state: 'untriaged' }),
    ],
  };

  beforeEach(() => {
    vi.mocked(api.fetchRetroFriction).mockResolvedValue(triagedResp as never);
    vi.mocked(api.unmuteFrictionGroup).mockResolvedValue(undefined);
  });

  function rowOrder(): string[] {
    return screen
      .getAllByRole('button')
      .filter((b) => b.hasAttribute('aria-expanded'))
      .map((b) => b.textContent ?? '')
      .filter((t) => t.includes('failure'));
  }

  it('renders each state as a chip and lists untriaged groups first', async () => {
    renderAt('/health?tab=friction');
    expect(await screen.findByText(/noise · until/)).toBeTruthy();
    expect(screen.getByText('tracked')).toBeTruthy();
    expect(screen.getByText('fix proposed')).toBeTruthy();
    const order = rowOrder();
    expect(order).toHaveLength(5);
    expect(order[0]).toContain('plain untriaged failure');
    expect(order[1]).toContain('explicit untriaged failure');
    expect(order[2]).toContain('fix proposed failure');
    expect(order[3]).toContain('tracked failure');
    expect(order[4]).toContain('muted failure');
    // Both untriaged rows, with and without a triage block, carry no chip.
    expect(order[0]).not.toMatch(/tracked|fix proposed|noise/);
    expect(order[1]).not.toMatch(/tracked|fix proposed|noise/);
  });

  it('shows the reason of a muted group once its row is expanded', async () => {
    renderAt('/health?tab=friction');
    const row = (await screen.findByText(/noise · until/)).closest('button') as HTMLElement;
    expect(screen.queryByText(/flaky upstream/)).toBeNull();
    fireEvent.click(row);
    expect(screen.getByText(/flaky upstream, nothing to fix/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'not noise: muted failure' })).toBeTruthy();
  });

  it('links tracked groups to the scoped Inbox advisor tab', async () => {
    renderAt('/p/shop/health?tab=friction');
    const row = (await screen.findByText('tracked')).closest('button') as HTMLElement;
    fireEvent.click(row);
    expect(screen.getByRole('link', { name: /open in Inbox/ }).getAttribute('href')).toBe(
      '/p/shop/inbox?tab=advisor',
    );
  });

  it('links to the fleet Inbox advisor tab outside a project', async () => {
    renderAt('/health?tab=friction');
    const row = (await screen.findByText('fix proposed')).closest('button') as HTMLElement;
    fireEvent.click(row);
    expect(screen.getByRole('link', { name: /open in Inbox/ }).getAttribute('href')).toBe(
      '/inbox?tab=advisor',
    );
  });

  it('unmutes with the group key; the panel and the tab count both show the fresh server state', async () => {
    renderAt('/health?tab=friction');
    fireEvent.click((await screen.findByText(/noise · until/)).closest('button') as HTMLElement);
    // After the unmute the server reports the group as untriaged.
    vi.mocked(api.fetchRetroFriction).mockResolvedValue({
      ...triagedResp,
      error_groups: [group('m', 'muted failure', 9), ...triagedResp.error_groups.slice(1)],
    } as never);
    fireEvent.click(screen.getByRole('button', { name: 'not noise: muted failure' }));
    await waitFor(() => expect(api.unmuteFrictionGroup).toHaveBeenCalledWith('m'));
    // The panel (Retro's own state) lost the chip …
    await waitFor(() => expect(screen.queryByText(/noise · until/)).toBeNull());
    // … and the tab count (Health's own state) gained the group: 1 denial + 3 untriaged.
    await waitFor(() => expect(screen.getByRole('tab', { name: /Friction/ }).textContent).toMatch(/4/));
  });

  it('disables every "not noise" button while one unmute is in flight', async () => {
    let release = (): void => undefined;
    vi.mocked(api.unmuteFrictionGroup).mockReturnValue(
      new Promise<void>((resolve) => {
        release = resolve;
      }),
    );
    vi.mocked(api.fetchRetroFriction).mockResolvedValue({
      ...frictionResp,
      error_groups: [
        group('m', 'muted failure', 9, { state: 'muted' }),
        group('m2', 'other muted failure', 3, { state: 'muted' }),
      ],
    } as never);
    renderAt('/health?tab=friction');
    await screen.findAllByText('noise');
    const row = (example: string): HTMLElement =>
      screen
        .getAllByRole('button')
        .find((b) => b.hasAttribute('aria-expanded') && (b.textContent ?? '').includes(`▸ ${example}`)) as HTMLElement;
    fireEvent.click(row('muted failure'));
    fireEvent.click(screen.getByRole('button', { name: 'not noise: muted failure' }));
    fireEvent.click(row('other muted failure'));
    const other = screen.getByRole('button', { name: 'not noise: other muted failure' }) as HTMLButtonElement;
    expect(other.disabled).toBe(true);
    fireEvent.click(other);
    expect(api.unmuteFrictionGroup).toHaveBeenCalledTimes(1);
    release();
    await waitFor(() => expect(other.disabled).toBe(false));
  });

  it('treats a state this client does not know as untriaged: no chip, listed first, counted', async () => {
    vi.mocked(api.fetchRetroFriction).mockResolvedValue({
      ...frictionResp,
      error_groups: [
        group('t', 'tracked failure', 8, { state: 'tracked', recommendationId: 5 }),
        group('s', 'snoozed failure', 5, { state: 'snoozed' }),
      ],
    } as never);
    renderAt('/health?tab=friction');
    expect(await screen.findByText('tracked')).toBeTruthy();
    expect(screen.queryByText('fix proposed')).toBeNull();
    const order = rowOrder();
    expect(order[0]).toContain('snoozed failure');
    expect(order[0]).not.toMatch(/tracked|fix proposed|noise/);
    // 1 uncovered denial + the group whose state is unknown.
    await waitFor(() => expect(screen.getByRole('tab', { name: /Friction/ }).textContent).toMatch(/2/));
  });

  it('shows an alert and does not refetch when the unmute fails', async () => {
    vi.mocked(api.unmuteFrictionGroup).mockRejectedValue(new Error('no active mute'));
    renderAt('/health?tab=friction');
    fireEvent.click((await screen.findByText(/noise · until/)).closest('button') as HTMLElement);
    const before = vi.mocked(api.fetchRetroFriction).mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'not noise: muted failure' }));
    expect((await screen.findByRole('alert')).textContent).toContain('no active mute');
    expect(vi.mocked(api.fetchRetroFriction).mock.calls.length).toBe(before);
  });

  it('does not offer a muted top group on the overview', async () => {
    renderAt('/health');
    expect(await screen.findByText('Same error 4 times in this window')).toBeTruthy();
    expect(screen.queryByText('Same error 9 times in this window')).toBeNull();
  });

  it('counts only untriaged groups and uncovered denials on the Friction tab', async () => {
    renderAt('/health');
    const tab = await screen.findByRole('tab', { name: /Friction/ });
    // 1 uncovered denial + 2 untriaged groups; muted, tracked and fix_proposed are out.
    expect(tab.textContent).toMatch(/3/);
    expect(tab.textContent).not.toMatch(/[4-9]/);
  });
});
