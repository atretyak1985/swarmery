// @vitest-environment jsdom
//
// The routing report (complexity-routing phase 3): two tables when groups clear
// the n<20 gate, the "would it have been cheaper" lines for divergent cells,
// and the empty state when every group is hidden.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/RoutingReport.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RouteGroup, RouteReport } from '../api/route';
import { divergentPhrase, RoutingReport, RoutingTables } from './RoutingReport';

const fetchRouteReport = vi.fn();
vi.mock('../api/route', () => ({
  fetchRouteReport: (...args: unknown[]) => fetchRouteReport(...args),
}));

function group(over: Partial<RouteGroup> = {}): RouteGroup {
  return {
    surface: 'dispatch',
    n: 43,
    failures: 1,
    failRate: 0.0233,
    costN: 43,
    meanCost: 0.21,
    p90Cost: 0.4,
    agree: 0.1,
    ...over,
  };
}

function report(over: Partial<RouteReport> = {}): RouteReport {
  return {
    surface: '',
    days: 30,
    minSamples: 20,
    rows: 70,
    unsettled: 2,
    byTier: { groups: [group({ tier: 'S' })], hiddenGroups: 2, hiddenRuns: 9 },
    byModel: { groups: [group({ model: 'sonnet', n: 52 })], hiddenGroups: 0, hiddenRuns: 0 },
    divergent: {
      groups: [
        group({ tier: 'S', pick: 'haiku', model: 'sonnet', pickRan: group({ tier: 'S', model: 'haiku', n: 21, meanCost: 0.05 }) }),
      ],
      hiddenGroups: 0,
      hiddenRuns: 0,
    },
    hiddenGroups: 2,
    ...over,
  };
}

const EMPTY = { groups: [], hiddenGroups: 3, hiddenRuns: 11 };

afterEach(() => {
  cleanup();
  fetchRouteReport.mockReset();
});

describe('RoutingTables', () => {
  it('renders the tier and model tables with their rows', () => {
    render(<RoutingTables rep={report()} />);
    const tables = screen.getAllByRole('table');
    expect(tables).toHaveLength(2);
    expect(tables[0]?.textContent).toContain('By tier');
    expect(tables[0]?.textContent).toContain('43');
    expect(tables[0]?.textContent).toContain('$0.21');
    expect(tables[1]?.textContent).toContain('By model that ran');
    expect(tables[1]?.textContent).toContain('sonnet');
    expect(document.body.textContent).toContain('2 group(s) with 9 run(s) hidden');
  });

  it('puts the divergent cell beside the cell where the pick ran', () => {
    render(<RoutingTables rep={report()} />);
    const text = document.body.textContent ?? '';
    expect(text).toContain('tier S, picked haiku, ran sonnet: 43 runs, 2% fail, $0.21 mean');
    expect(text).toContain('when haiku ran: 21 runs, 2% fail, $0.05 mean');
  });

  it('shows the empty state — and no table — when every group is hidden', () => {
    render(<RoutingTables rep={report({ byTier: EMPTY, byModel: EMPTY, divergent: EMPTY })} />);
    expect(screen.queryByRole('table')).toBeNull();
    expect(document.body.textContent).toContain('Not enough runs yet (n < 20 per group)');
  });

  it('never draws a group under the gate even if the API sent one', () => {
    const leaky = report({
      byTier: { groups: [group({ tier: 'XL', n: 3 })], hiddenGroups: 0, hiddenRuns: 0 },
      byModel: EMPTY,
      divergent: EMPTY,
    });
    render(<RoutingTables rep={leaky} />);
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('phrases a divergent cell without a pickRan honestly', () => {
    expect(divergentPhrase(group({ tier: 'M', pick: 'opus', model: 'sonnet', failRate: 0.1 }))).toBe(
      'tier M, picked opus, ran sonnet: 43 runs, 10% fail, $0.21 mean',
    );
  });
});

describe('RoutingReport', () => {
  it('fetches both surfaces for 30 days, then refetches one surface on toggle', async () => {
    fetchRouteReport.mockResolvedValue(report());
    render(<RoutingReport />);
    await waitFor(() => expect(screen.getAllByRole('table')).toHaveLength(2));
    expect(fetchRouteReport).toHaveBeenCalledWith('', 30);

    fireEvent.click(screen.getByRole('button', { name: 'phase runs' }));
    await waitFor(() => expect(fetchRouteReport).toHaveBeenCalledWith('phaserun', 30));
  });

  it('shows a fetch failure as an alert', async () => {
    fetchRouteReport.mockRejectedValue(new Error('GET /api/route/report: 500'));
    render(<RoutingReport />);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('500'));
  });
});
