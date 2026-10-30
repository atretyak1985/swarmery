// @vitest-environment jsdom
//
// The sidebar's bottom block. The claims:
//
//   1. It shows today's spend, the plan percentage and the reset clock from real
//      sources, and stays honest (`—`, no plan line) until they answer.
//   2. It opens the Usage modal, so the header chip's detail is still reachable.
//   3. It keeps the daemon's health: version when up, red when unreachable.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { fetchStatsToday } from '../api';
import { useHealth } from '../lib/health';
import { useUsage } from '../lib/usageData';
import { HealthFooter } from './HealthFooter';

vi.mock('../api', () => ({
  MOCK: false,
  fetchStatsToday: vi.fn(async () => ({ cost_usd: 12.4 })),
}));

vi.mock('../lib/health', () => ({
  useHealth: vi.fn(() => ({ health: { version: '0.2.2', build: '0.2.2' }, unreachable: false })),
  versionLabel: () => 'v0.2.2',
  versionTitle: () => 'swarmery 0.2.2',
}));

vi.mock('../lib/usageData', () => ({ useUsage: vi.fn() }));
vi.mock('../lib/activeAccount', () => ({ useActiveUsageAccount: () => null }));
vi.mock('./PluginDriftBadge', () => ({ PluginDriftBadge: () => null }));
vi.mock('./usage/UsageModal', () => ({
  UsageModal: ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="usage modal" /> : null),
}));

const setModalOpen = vi.fn();

function withPlan(percentUsed: number, status = 'ok'): void {
  vi.mocked(useUsage).mockReturnValue({
    providers: [
      {
        status,
        windows: [{ key: 'session', percentUsed, resetAt: new Date(Date.now() + 3_600_000).toISOString() }],
      },
    ],
    setModalOpen,
  } as never);
}

async function renderFooter(): Promise<void> {
  render(
    <MemoryRouter>
      <HealthFooter />
    </MemoryRouter>,
  );
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  withPlan(41);
});

afterEach(cleanup);

describe('HealthFooter', () => {
  it('shows spend, plan and the reset clock', async () => {
    await renderFooter();
    expect(screen.getByText('$12.40 today')).toBeTruthy();
    expect(screen.getByText('plan 41 %')).toBeTruthy();
    expect(screen.getByText(/^resets /)).toBeTruthy();
  });

  it('draws the plan bar at the used percentage, capped at the track', async () => {
    withPlan(120);
    const { container } = render(
      <MemoryRouter>
        <HealthFooter />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    const bar = container.querySelector<HTMLElement>('[style]');
    expect(bar?.style.width).toBe('100%');
    expect(bar?.className).toContain('bg-red');
  });

  it('says it does not know yet instead of showing a zero', async () => {
    vi.mocked(fetchStatsToday).mockResolvedValueOnce({ cost_usd: null } as never);
    vi.mocked(useUsage).mockReturnValue({ providers: [], setModalOpen } as never);
    await renderFooter();
    expect(screen.getByText('— today')).toBeTruthy();
    expect(screen.queryByText(/^plan /)).toBeNull();
  });

  it('opens the Usage modal and registers it with the shared poller', async () => {
    await renderFooter();
    expect(screen.queryByRole('dialog', { name: 'usage modal' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Subscription usage' }));
    expect(screen.getByRole('dialog', { name: 'usage modal' })).toBeTruthy();
    expect(setModalOpen).toHaveBeenCalledWith(true);
  });

  it('keeps the daemon version on the line', async () => {
    await renderFooter();
    expect(screen.getByText('v0.2.2')).toBeTruthy();
  });

  it('goes red when the daemon is unreachable', async () => {
    vi.mocked(useHealth).mockReturnValue({ health: null, unreachable: true });
    await renderFooter();
    expect(screen.getByText('daemon unreachable').className).toContain('text-red');
  });
});
