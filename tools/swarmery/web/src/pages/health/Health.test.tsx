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
// Dev-only suite. Run with
//   npx vitest run src/pages/health
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

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
    expect(because.getAttribute('href')).toBe('/lessons?tab=proof');
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
