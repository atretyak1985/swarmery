// @vitest-environment jsdom
//
// The global locked banner: hidden until a write is answered 401
// dashboard_locked, then names both ways to unlock; dismissable, and the next
// refusal brings it back.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { apiFetch, resetDashboardSessionForTests } from '../lib/dashboardSession';
import { DashboardLockedBanner } from './DashboardLockedBanner';

const locked = (): Response =>
  new Response(JSON.stringify({ error: 'dashboard locked', code: 'dashboard_locked' }), {
    status: 401,
    headers: { 'Content-Type': 'application/json' },
  });

beforeEach(() => {
  window.localStorage.clear();
  resetDashboardSessionForTests();
  vi.stubGlobal('fetch', vi.fn(async () => locked()));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const banner = (): HTMLElement | null => screen.queryByTestId('dashboard-locked-banner');

describe('DashboardLockedBanner', () => {
  it('renders nothing until a write is refused', () => {
    render(<DashboardLockedBanner />);
    expect(banner()).toBeNull();
  });

  it('a 401 dashboard_locked shows both ways to unlock; dismiss hides it until the next one', async () => {
    render(<DashboardLockedBanner />);
    await act(async () => {
      await apiFetch('/api/sessions/1/stop', { method: 'POST' });
    });
    await waitFor(() => expect(banner()).not.toBeNull());
    expect(screen.getByRole('alert').textContent).toContain('dashboard locked');
    expect(screen.getByText('swarmery open')).toBeTruthy();
    expect(screen.getByText('Open dashboard')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss the dashboard locked notice' }));
    expect(banner()).toBeNull();

    await act(async () => {
      await apiFetch('/api/sessions/1/stop', { method: 'POST' });
    });
    await waitFor(() => expect(banner()).not.toBeNull());
  });

  it('a read answered 401 is not a lock', async () => {
    render(<DashboardLockedBanner />);
    await act(async () => {
      await apiFetch('/api/sessions');
    });
    expect(banner()).toBeNull();
  });
});
