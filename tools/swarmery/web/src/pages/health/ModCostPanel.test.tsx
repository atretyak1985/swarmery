// @vitest-environment jsdom
//
// Insights → Cost source toggle (mods-swarmery-link phase 3). The claims:
//
//   1. Only sessions in range that carry a link-mod report are listed; with
//      none, the panel renders nothing (a fleet without the mod sees no change).
//   2. The toggle defaults to "mod" where a mod cost exists and flips to the
//      pricing table on click.
//   3. A row whose two costs differ by more than 10 % is highlighted, with
//      screen-reader text; a row within 10 % is not.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/health/ModCostPanel.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import type { Session, SessionLink } from '../../api/types';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { ModCostPanel } from './ModCostPanel';

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, fetchSessions: vi.fn() };
});

const link = (costUsd: number | null): SessionLink => ({
  status: 'done',
  seenAt: '2026-10-14T10:00:00.000Z',
  live: false,
  contextPct: 40,
  rateLimits: [],
  costUsd,
  controllable: false,
});

let nextId = 1;
function session(over: Partial<Session>): Session {
  const id = nextId++;
  return {
    id,
    projectId: 1,
    projectSlug: 'proj',
    projectName: 'proj',
    sessionUuid: `uuid-${String(id)}`,
    model: null,
    modelLast: null,
    modelChanged: false,
    modelFellBack: false,
    gitBranch: null,
    cwd: null,
    status: 'completed',
    startedAt: '2026-10-14T09:00:00.000Z',
    endedAt: null,
    title: `session ${String(id)}`,
    source: 'both',
    ...over,
  };
}

function mockSessions(sessions: Session[]): void {
  vi.mocked(api.fetchSessions).mockResolvedValue({ sessions, nextCursor: null });
}

function renderPanel(): void {
  render(
    <MemoryRouter>
      <ModCostPanel from="2026-10-01" to="2026-10-14" scope={null} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.mocked(api.fetchSessions).mockReset();
});
afterEach(cleanup);

describe('ModCostPanel', () => {
  it('renders nothing when no session in range carries a link', async () => {
    mockSessions([
      session({ costUsd: 3 }),
      // Out of range: started before `from`.
      session({ costUsd: 1, link: link(1), startedAt: '2026-09-01T00:00:00.000Z' }),
    ]);
    renderPanel();
    await waitFor(() => expect(api.fetchSessions).toHaveBeenCalled());
    expect(screen.queryByTestId('mod-cost-panel')).toBeNull();
  });

  it('defaults to the mod cost, flips to the pricing table, and highlights > 10 %', async () => {
    mockSessions([
      session({ title: 'diverging', costUsd: 1.0, link: link(1.25) }),
      session({ title: 'close', costUsd: 2.0, link: link(2.1) }),
      session({ title: 'no mod', costUsd: 5 }),
    ]);
    renderPanel();
    await screen.findByTestId('mod-cost-panel');

    const group = screen.getByRole('group', { name: 'cost source' });
    const mod = within(group).getByRole('button', { name: 'mod' });
    const pricing = within(group).getByRole('button', { name: 'pricing table' });
    expect(mod.getAttribute('aria-pressed')).toBe('true');
    expect(pricing.getAttribute('aria-pressed')).toBe('false');

    const rows = screen.getAllByTestId('mod-cost-row');
    expect(rows).toHaveLength(2); // the session without a link is not listed
    const [diverging, close] = rows;
    expect(within(diverging).getByTestId('mod-cost-value').textContent).toBe('$1.25');
    expect(diverging.getAttribute('data-diverges')).toBe('true');
    expect(within(diverging).getByTestId('mod-cost-divergence').textContent).toContain('Δ +25 %');
    expect(within(diverging).getByText('mod and pricing table differ by more than 10 %').className).toContain(
      'sr-only',
    );
    expect(close.getAttribute('data-diverges')).toBe('false');
    expect(within(close).queryByTestId('mod-cost-divergence')).toBeNull();

    fireEvent.click(pricing);
    expect(pricing.getAttribute('aria-pressed')).toBe('true');
    expect(within(diverging).getByTestId('mod-cost-value').textContent).toBe('$1.00');
    // The highlight does not depend on the chosen source.
    expect(diverging.getAttribute('data-diverges')).toBe('true');
  });

  it('defaults to the pricing table when no listed session has a mod cost', async () => {
    mockSessions([session({ costUsd: 1.0, link: link(null) })]);
    renderPanel();
    await screen.findByTestId('mod-cost-panel');
    const group = screen.getByRole('group', { name: 'cost source' });
    expect(within(group).getByRole('button', { name: 'pricing table' }).getAttribute('aria-pressed')).toBe('true');
  });
});
