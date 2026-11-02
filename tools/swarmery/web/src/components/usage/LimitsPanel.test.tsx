// @vitest-environment jsdom
//
// The limits panel. The claims:
//
//   1. A tab per account, opening on the one at its limit, showing its real windows.
//   2. A failed usage request keeps the provider card and its reason.
//   3. The projects an account runs, the default taking every unbound one, and a move
//      that writes the binding: '' for the default account.
//   4. An account that cannot run is no target. Escape closes.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import type { UsageAccount, UsageProvider } from '../../api/types';
import { useUsage } from '../../lib/usageData';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { LimitsPanel } from './LimitsPanel';

vi.mock('../../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api')>()),
  fetchAccounts: vi.fn(),
  fetchProjects: vi.fn(),
  putProjectAccount: vi.fn(async () => ({})),
}));
vi.mock('../../lib/usageData', () => ({ useUsage: vi.fn() }));

const provider = (account: string, percentUsed: number): UsageProvider =>
  ({
    account,
    name: 'Claude',
    source: 'oauth',
    status: 'ok',
    windows: [{ key: 'session', label: '5-hour window', percentUsed, percentLeft: 100 - percentUsed, source: 'oauth' }],
  }) as UsageProvider;

const ACCOUNTS: UsageAccount[] = [
  { account: 'default', providers: [provider('default', 41)] },
  { account: 'na', providers: [provider('na', 100)] },
];

const account = (key: string, isDefault: boolean, projects: string[], runnable: boolean | null = true): unknown => ({
  key,
  isDefault,
  projects,
  runnable,
});

function mount(accounts: UsageAccount[] = ACCOUNTS, activeAccount: string | null = null): { onClose: () => void } {
  vi.mocked(useUsage).mockReturnValue({
    accounts,
    error: null,
    loading: false,
    lastUpdated: 1,
    refresh: vi.fn(),
  } as never);
  const onClose = vi.fn();
  render(
    <MemoryRouter>
      <LimitsPanel onClose={onClose} activeAccount={activeAccount} spentToday={12.4} />
    </MemoryRouter>,
  );
  return { onClose };
}

beforeEach(() => {
  vi.mocked(api.fetchAccounts).mockResolvedValue({
    accounts: [account('default', true, []), account('na', false, ['/work/cars'])],
  } as never);
  vi.mocked(api.fetchProjects).mockResolvedValue([
    { id: 1, path: '/work/cars', slug: 'cars', name: 'Cars' },
    { id: 2, path: '/work/orders', slug: 'orders', name: 'Orders' },
  ] as never);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LimitsPanel', () => {
  it('opens on the account at its limit, with its windows', async () => {
    mount();
    expect(screen.getByRole('tab', { name: /^na/ }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('at limit')).toBeTruthy();
    expect(screen.getByText('5-hour window')).toBeTruthy();
    expect(screen.getByText('na hit a limit')).toBeTruthy();
    await screen.findByText('Cars');
  });

  it('has no tabs for a single account', () => {
    mount([ACCOUNTS[0] as UsageAccount]);
    expect(screen.queryByRole('tablist')).toBeNull();
  });

  it('keeps the provider’s card and reason when the usage request failed', () => {
    const failed = { ...provider('default', 0), status: 'error', error: 'rate-limited', windows: [] } as UsageProvider;
    mount([{ account: 'default', providers: [failed] }]);
    expect(screen.getByText('rate-limited')).toBeTruthy();
    expect(screen.queryByText('at limit')).toBeNull();
  });

  it('gives the default account every unbound project', async () => {
    mount();
    fireEvent.click(screen.getByRole('tab', { name: /^default/ }));
    expect(await screen.findByText('Orders')).toBeTruthy();
    expect(screen.queryByText('Cars')).toBeNull();
    expect(screen.getByText('projects running as default')).toBeTruthy();
    expect(screen.getByText('1 of 2')).toBeTruthy();
  });

  it('moves a project by writing its binding, clearing it for the default', async () => {
    mount();
    await screen.findByText('Cars');
    fireEvent.click(screen.getByRole('button', { name: 'move ▾' }));
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: /to default/ }));
    await waitFor(() => expect(api.putProjectAccount).toHaveBeenCalledWith(1, ''));
    await waitFor(() => expect(api.fetchProjects).toHaveBeenCalledTimes(2));
  });

  it('offers no account that cannot run', async () => {
    vi.mocked(api.fetchAccounts).mockResolvedValue({
      accounts: [account('default', true, [], false), account('na', false, ['/work/cars'])],
    } as never);
    mount();
    await screen.findByText('Cars');
    fireEvent.click(screen.getByRole('button', { name: 'move ▾' }));
    expect((screen.getByRole('menuitem', { name: /to default/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('says a failed move, and stays put', async () => {
    vi.mocked(api.putProjectAccount).mockRejectedValueOnce(new Error('locked'));
    mount();
    await screen.findByText('Cars');
    fireEvent.click(screen.getByRole('button', { name: 'move ▾' }));
    fireEvent.click(screen.getByRole('menuitem', { name: /to default/ }));
    expect((await screen.findByRole('alert')).textContent).toBe('locked');
  });

  it('closes on Escape', () => {
    const { onClose } = mount();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
