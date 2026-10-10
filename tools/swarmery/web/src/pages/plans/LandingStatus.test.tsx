// @vitest-environment jsdom
//
// The landing status chips and Refresh (phase-landing plan, phase 7, SC-13):
// one chip per normalized CI / review / state value (`none` hidden), the time of
// the last read, and a Refresh that calls the refresh endpoint, shows aria-busy
// while it runs, hands the new landing up, and shows a refusal inline. The API
// module is mocked except LandError, the real class.

import { act, cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PhaseChangeStatus, PhaseLanding } from '../../api/types';
import { LandingStatus } from './LandingStatus';

const api = vi.hoisted(() => ({ refreshPhaseLanding: vi.fn() }));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, refreshPhaseLanding: api.refreshPhaseLanding };
});

const { LandError } = await import('../../api');

afterEach(cleanup);
beforeEach(() => {
  api.refreshPhaseLanding.mockReset();
});

const status = (over: Partial<PhaseChangeStatus> = {}): PhaseChangeStatus => ({
  state: 'open',
  draft: false,
  ci: 'none',
  review: 'none',
  checkedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
  ...over,
});

const landing = (over: Partial<PhaseLanding> = {}): PhaseLanding => ({
  state: 'pr_open',
  prUrl: 'https://host.example/acme/w/77',
  prNumber: 77,
  prProvider: 'github',
  prStatus: status(),
  landedAt: null,
  error: null,
  ...over,
});

function renderStatus(l: PhaseLanding, onRefreshed = vi.fn()) {
  render(<LandingStatus taskId={9} phaseId={207} landing={l} onRefreshed={onRefreshed} />);
  return { onRefreshed };
}

const text = (id: string): string | null => screen.queryByTestId(id)?.textContent ?? null;

describe('LandingStatus chips', () => {
  it.each([
    ['success', '✓CI passed'],
    ['failure', '✗CI failed'],
    ['pending', '◌CI running'],
  ] as const)('CI %s reads "%s"', (ci, want) => {
    renderStatus(landing({ prStatus: status({ ci }) }));
    expect(text('landing-status-ci')).toBe(want);
  });

  it.each([
    ['approved', '✓approved'],
    ['changes_requested', '✗changes requested'],
    ['review_required', 'review required'],
  ] as const)('review %s reads "%s"', (review, want) => {
    renderStatus(landing({ prStatus: status({ review }) }));
    expect(text('landing-status-review')).toBe(want);
  });

  it('hides CI and review when there is none', () => {
    renderStatus(landing({ prStatus: status({ ci: 'none', review: 'none' }) }));
    expect(screen.queryByTestId('landing-status-ci')).toBeNull();
    expect(screen.queryByTestId('landing-status-review')).toBeNull();
  });

  it.each([
    [{ state: 'open', draft: false }, 'open'],
    [{ state: 'open', draft: true }, 'draft'],
    [{ state: 'merged', draft: false }, '✓merged'],
    [{ state: 'closed', draft: false }, 'closed'],
  ] as const)('state %o reads "%s"', (over, want) => {
    renderStatus(landing({ prStatus: status(over) }));
    expect(text('landing-status-state')).toBe(want);
  });

  it('shows when the status was last read, relative', () => {
    renderStatus(landing());
    expect(text('landing-status-checked')).toBe('checked 4 min ago');
  });

  it('says so before the first read', () => {
    renderStatus(landing({ prStatus: null }));
    expect(screen.queryByTestId('landing-status-state')).toBeNull();
    expect(text('landing-status-checked')).toBe('status not read yet');
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeTruthy();
  });

  it('renders nothing before a change request exists', () => {
    for (const state of ['none', 'ready', 'pushed', 'returned'] as const) {
      renderStatus(landing({ state, prStatus: null }));
      expect(screen.queryByTestId('landing-status')).toBeNull();
      cleanup();
    }
  });
});

describe('LandingStatus Refresh', () => {
  it('calls the refresh endpoint, is aria-busy while it runs, and hands the new landing up', async () => {
    let resolve: (l: PhaseLanding) => void = () => {};
    api.refreshPhaseLanding.mockReturnValue(
      new Promise<PhaseLanding>((r) => {
        resolve = r;
      }),
    );
    const { onRefreshed } = renderStatus(landing());

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(api.refreshPhaseLanding).toHaveBeenCalledWith(9, 207);
    const busy = screen.getByRole('button', { name: 'Refreshing…' });
    expect(busy.getAttribute('aria-busy')).toBe('true');
    expect((busy as HTMLButtonElement).disabled).toBe(true);

    const merged = landing({ state: 'merged', landedAt: '2026-10-09T11:00:00Z', prStatus: status({ state: 'merged' }) });
    await act(async () => {
      resolve(merged);
    });
    expect(onRefreshed).toHaveBeenCalledWith(merged);
    const idle = screen.getByRole('button', { name: 'Refresh' });
    expect(idle.getAttribute('aria-busy')).toBe('false');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows a 422 refusal with its manual hint', async () => {
    api.refreshPhaseLanding.mockRejectedValue(
      new LandError(
        422,
        { error: 'not authenticated', code: 'not-authenticated', hint: 'log in:\ncli auth login --hostname host.example' },
        'refresh failed',
      ),
    );
    const { onRefreshed } = renderStatus(landing());
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('not authenticated'));
    expect(screen.getByRole('alert').textContent).toContain('cli auth login --hostname host.example');
    expect(onRefreshed).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Refresh' }).getAttribute('aria-busy')).toBe('false');
  });

  it('words a 409 no-change-request refusal', async () => {
    api.refreshPhaseLanding.mockRejectedValue(
      new LandError(409, { error: 'no change request', code: 'no-change-request' as never }, 'refresh failed'),
    );
    renderStatus(landing());
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('no open change request'));
  });
});
