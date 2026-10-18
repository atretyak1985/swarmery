// @vitest-environment jsdom
//
// TurnForecastPanel (mods-swarmery-link phase 10). The claims:
//
//   1. Bins and the top surprises render from a fixture: header facts, the
//      bins table, labelled miss components, 2-dp surprise, session links.
//   2. An empty bin (n = 0, null mean / hit rate) is never handed to the chart
//      as 0, and reads "—" in the text alternative.
//   3. tooFew shows the "n is too small" notice and still renders the data.
//   4. n = 0 AND no top rows shows the empty state naming the swarmery link
//      `forecast` option; n = 0 with top rows (scored turns whose confidence
//      did not parse) keeps the surprises table and notes the calibration gap.
//   5. A turn_surprise frame (and the reconnect tick) refetches; other frames
//      do not; a project page ignores another project's frame.
//   6. The window selector refetches with the chosen days; a /p/:slug page
//      passes the numeric project id and scopes the session links.
//   7. Only the latest request lands: a stale response for a window or a
//      project the page has since left is dropped.
//
// On its own: `npx vitest run src/pages/learning/TurnForecastPanel`.

import { Link, MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/turnForecasts';
import type { TurnForecastsResp, WSMessage } from '../../api/types';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { calibrationPoints, TurnForecastPanel } from './TurnForecastPanel';

const ws = vi.hoisted(() => ({
  onMessage: null as ((m: WSMessage) => void) | null,
  onReconnect: null as (() => void) | null,
}));

vi.mock('../../lib/ws', () => ({
  useLiveUpdates: (onMessage: (m: WSMessage) => void, onReconnect: () => void): void => {
    ws.onMessage = onMessage;
    ws.onReconnect = onReconnect;
  },
}));

const PROJECT_IDS: Record<string, string> = { shop: '7', a: '1', b: '2' };

vi.mock('../../lib/scope', () => ({
  useProjectScope: (key: string | null | undefined) =>
    key == null || key === '' ? { id: null, pending: false } : { id: PROJECT_IDS[key] ?? null, pending: false },
}));

vi.mock('../../api/turnForecasts', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/turnForecasts')>();
  return { ...real, fetchTurnForecasts: vi.fn() };
});

const FIXTURE: TurnForecastsResp = {
  days: 14,
  n: 13,
  tooFew: true,
  bins: [
    { lo: 0, hi: 0.2, n: 2, meanConfidence: 0.115, hitRate: 0 },
    { lo: 0.2, hi: 0.4, n: 0, meanConfidence: null, hitRate: null },
    { lo: 0.4, hi: 0.6, n: 4, meanConfidence: 0.52, hitRate: 0.5 },
    { lo: 0.6, hi: 0.8, n: 5, meanConfidence: 0.71, hitRate: 0.6 },
    { lo: 0.8, hi: 1, n: 2, meanConfidence: 0.9, hitRate: 0.5 },
  ],
  top: [
    {
      id: 12,
      sessionId: 1,
      sessionUuid: 's-a',
      projectId: 1,
      runId: 'r-1',
      turnSeq: 12,
      startedAt: '2026-10-13T09:12:00Z',
      surprise: 0.7795,
      top: 'tests_miss',
      model: 'haiku',
    },
    {
      id: 7,
      sessionId: 3,
      sessionUuid: null,
      projectId: null,
      runId: 'r-2',
      turnSeq: 4,
      startedAt: '2026-10-12T16:05:00Z',
      surprise: 0.3,
      top: 'some_future_component',
      model: 'haiku',
    },
  ],
  avgForecastTokens: 115.38,
};

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve: (v: T) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function renderAt(url = '/learning'): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Link to="/p/b/learning">go to b</Link>
      <Routes>
        <Route path="/learning" element={<TurnForecastPanel />} />
        <Route path="/p/:slug/learning" element={<TurnForecastPanel />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.mocked(api.fetchTurnForecasts).mockResolvedValue(FIXTURE);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  ws.onMessage = null;
  ws.onReconnect = null;
});

describe('calibrationPoints', () => {
  it('drops empty bins instead of plotting them as 0', () => {
    const pts = calibrationPoints(FIXTURE.bins);
    expect(pts.map((p) => p.lo)).toEqual([0, 0.4, 0.6, 0.8]);
    expect(pts.find((p) => p.lo === 0.2)).toBeUndefined();
    expect(pts[0]).toEqual({ x: 0.115, y: 0, n: 2, lo: 0, hi: 0.2 });
  });

  it('drops a bin with n > 0 but a null rate', () => {
    expect(calibrationPoints([{ lo: 0, hi: 0.2, n: 3, meanConfidence: 0.1, hitRate: null }])).toEqual([]);
  });
});

describe('TurnForecastPanel', () => {
  it('renders header facts, every bin and the top surprises', async () => {
    renderAt();
    expect(await screen.findByText('13 turns with a confidence')).toBeTruthy();
    expect(screen.getByText('avg forecast cost 115 tokens')).toBeTruthy();
    expect(api.fetchTurnForecasts).toHaveBeenCalledWith(14, null);

    const bins = screen.getByRole('table', { name: 'Calibration by confidence bin' });
    const rows = within(bins).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(5);
    // The empty bin is in the text alternative as "—", not 0%.
    expect(rows[1]?.textContent).toBe('0.2–0.40——');
    expect(rows[2]?.textContent).toBe('0.4–0.6452%50%');

    const top = screen.getByRole('table', { name: 'Most surprising turns' });
    const topRows = within(top).getAllByRole('row').slice(1);
    expect(topRows).toHaveLength(2);
    expect(within(topRows[0] as HTMLElement).getByText('0.78')).toBeTruthy();
    expect(within(topRows[0] as HTMLElement).getByText('tests outcome missed')).toBeTruthy();
    const link = within(topRows[0] as HTMLElement).getByRole('link', { name: 'session 1 · turn 12' });
    expect(link.getAttribute('href')).toBe('/sessions/1');
    // An unknown component name still renders verbatim.
    expect(within(topRows[1] as HTMLElement).getByText('some_future_component')).toBeTruthy();
  });

  it('shows the too-few notice and still renders the data', async () => {
    renderAt();
    expect((await screen.findByRole('status')).textContent).toMatch(/n is too small to read/);
    expect(screen.getByRole('table', { name: 'Most surprising turns' })).toBeTruthy();
  });

  it('omits the notice when tooFew is false', async () => {
    vi.mocked(api.fetchTurnForecasts).mockResolvedValue({ ...FIXTURE, n: 40, tooFew: false });
    renderAt();
    expect(await screen.findByText('40 turns with a confidence')).toBeTruthy();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('renders the empty state when no turn was scored', async () => {
    vi.mocked(api.fetchTurnForecasts).mockResolvedValue({
      days: 14,
      n: 0,
      tooFew: true,
      bins: FIXTURE.bins.map((b) => ({ ...b, n: 0, meanConfidence: null, hitRate: null })),
      top: [],
      avgForecastTokens: null,
    });
    renderAt();
    expect(await screen.findByText('No scored turns in this window.')).toBeTruthy();
    expect(screen.getByText('forecast')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('keeps the surprises when no scored turn carries a confidence', async () => {
    // The daemon's `n` counts only confidence-bearing turns; `top` counts
    // every scored turn (an unparsable model reply scores with a null confidence).
    vi.mocked(api.fetchTurnForecasts).mockResolvedValue({
      ...FIXTURE,
      n: 0,
      tooFew: true,
      bins: FIXTURE.bins.map((b) => ({ ...b, n: 0, meanConfidence: null, hitRate: null })),
    });
    renderAt();
    expect(await screen.findByRole('table', { name: 'Most surprising turns' })).toBeTruthy();
    expect(screen.getByText('0 turns with a confidence')).toBeTruthy();
    expect(screen.getByText(/No turns with a confidence yet/)).toBeTruthy();
    expect(screen.queryByText('No scored turns in this window.')).toBeNull();
    expect(screen.queryByRole('table', { name: 'Calibration by confidence bin' })).toBeNull();
    // The "n too small" notice belongs to calibration, which has nothing to show.
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('refetches on a turn_surprise frame and on reconnect, not on other frames', async () => {
    renderAt();
    await screen.findByText('13 turns with a confidence');
    expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(1);

    act(() => {
      ws.onMessage?.({
        type: 'plan_updated',
        payload: { taskId: 1, projectId: 1 },
      });
    });
    expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(1);

    const payload = FIXTURE.top[0];
    if (payload === undefined) throw new Error('fixture has no top row');
    act(() => {
      ws.onMessage?.({ type: 'turn_surprise', payload });
    });
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(2));

    act(() => {
      ws.onReconnect?.();
    });
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(3));
  });

  it("ignores another project's turn_surprise frame on a project page", async () => {
    renderAt('/p/shop/learning');
    await screen.findByText('13 turns with a confidence');
    expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(1);
    const row = FIXTURE.top[0];
    if (row === undefined) throw new Error('fixture has no top row');

    act(() => {
      ws.onMessage?.({ type: 'turn_surprise', payload: { ...row, projectId: 1 } });
    });
    act(() => {
      ws.onMessage?.({ type: 'turn_surprise', payload: { ...row, projectId: null } });
    });
    expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(1);

    act(() => {
      ws.onMessage?.({ type: 'turn_surprise', payload: { ...row, projectId: 7 } });
    });
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenCalledTimes(2));
    expect(api.fetchTurnForecasts).toHaveBeenLastCalledWith(14, '7');
  });

  it('drops a stale response for a window the page has since left', async () => {
    const d14 = deferred<TurnForecastsResp>();
    const d30 = deferred<TurnForecastsResp>();
    vi.mocked(api.fetchTurnForecasts).mockImplementation((days) => (days === 30 ? d30.promise : d14.promise));
    renderAt();
    fireEvent.click(screen.getByRole('button', { name: '30 days' }));
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenLastCalledWith(30, null));

    await act(async () => {
      d30.resolve({ ...FIXTURE, days: 30, n: 31, tooFew: false });
      await d30.promise;
    });
    await act(async () => {
      d14.resolve(FIXTURE);
      await d14.promise;
    });
    expect(screen.getByText('31 turns with a confidence')).toBeTruthy();
    expect(screen.queryByText('13 turns with a confidence')).toBeNull();
    expect(screen.getByRole('button', { name: '30 days' }).getAttribute('aria-pressed')).toBe('true');
  });

  it('drops a stale response for a project the page has since left', async () => {
    const da = deferred<TurnForecastsResp>();
    const db = deferred<TurnForecastsResp>();
    vi.mocked(api.fetchTurnForecasts).mockImplementation((_days, project) =>
      project === '2' ? db.promise : da.promise,
    );
    renderAt('/p/a/learning');
    expect(api.fetchTurnForecasts).toHaveBeenLastCalledWith(14, '1');
    fireEvent.click(screen.getByRole('link', { name: 'go to b' }));
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenLastCalledWith(14, '2'));

    await act(async () => {
      db.resolve({ ...FIXTURE, n: 21, tooFew: true });
      await db.promise;
    });
    await act(async () => {
      da.resolve(FIXTURE);
      await da.promise;
    });
    expect(screen.getByText('21 turns with a confidence')).toBeTruthy();
    expect(screen.queryByText('13 turns with a confidence')).toBeNull();
    const link = screen.getByRole('link', { name: 'session 1 · turn 12' });
    expect(link.getAttribute('href')).toBe('/p/b/sessions/1');
  });

  it('refetches with the chosen window', async () => {
    renderAt();
    await screen.findByText('13 turns with a confidence');
    fireEvent.click(screen.getByRole('button', { name: '30 days' }));
    await waitFor(() => expect(api.fetchTurnForecasts).toHaveBeenLastCalledWith(30, null));
    expect(screen.getByRole('button', { name: '30 days' }).getAttribute('aria-pressed')).toBe('true');
  });

  it('scopes the request and the session links to /p/:slug', async () => {
    renderAt('/p/shop/learning');
    const link = await screen.findByRole('link', { name: 'session 1 · turn 12' });
    expect(link.getAttribute('href')).toBe('/p/shop/sessions/1');
    expect(api.fetchTurnForecasts).toHaveBeenCalledWith(14, '7');
  });
});
