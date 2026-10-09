// @vitest-environment jsdom
//
// The project sign-in banner (Phase 5, SC-12): hidden when the daemon is
// signed in or there is no remote; "not signed in" / "sign-in expired" with a
// Sign in action otherwise; the SSH + missing-token sentence; the terminal
// command fallback and a Re-check that bypasses the daemon's cache. The API is
// mocked at getProjectVcs, so the real useProjectVcs hook runs.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { VcsInfo } from '../api/types';
import { VcsAuthBanner } from './VcsAuthBanner';

const api = vi.hoisted(() => ({ getProjectVcs: vi.fn(), putProjectVcsProvider: vi.fn() }));

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>();
  return { ...real, getProjectVcs: api.getProjectVcs, putProjectVcsProvider: api.putProjectVcsProvider };
});

afterEach(cleanup);

// A neutral vocabulary and CLI: the banner may only ever read them.
const vcs = (over: Partial<VcsInfo> = {}, auth: Partial<VcsInfo['auth']> = {}): VcsInfo => ({
  provider: 'unknown',
  host: 'code.example.com',
  terms: { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' },
  remote: { url: 'https://code.example.com/acme/widgets.git', present: true, protocol: 'https' },
  auth: { status: 'missing', login: '', source: 'none', ...auth },
  baseBranch: '',
  allowPushToBase: false,
  source: 'host',
  cliLogin: 'hostcli auth login --hostname code.example.com',
  ...over,
});

beforeEach(() => {
  api.getProjectVcs.mockReset();
  api.putProjectVcsProvider.mockReset();
});

async function settle(): Promise<void> {
  await waitFor(() => expect(api.getProjectVcs).toHaveBeenCalled());
  await act(async () => {
    await Promise.resolve();
  });
}

const banner = (): HTMLElement | null => screen.queryByTestId('vcs-auth-banner');

describe('VcsAuthBanner', () => {
  it('is hidden when the daemon is signed in', async () => {
    api.getProjectVcs.mockResolvedValue(vcs({}, { status: 'ok', login: 'octo', source: 'cli' }));
    render(<VcsAuthBanner projectId={3} />);
    await settle();
    expect(banner()).toBeNull();
    expect(api.getProjectVcs).toHaveBeenCalledWith(3, false);
  });

  it('is hidden without a remote, and for a host with no sign-in path', async () => {
    api.getProjectVcs.mockResolvedValue(vcs({ remote: { url: '', present: false, protocol: '' } }));
    render(<VcsAuthBanner projectId={3} />);
    await settle();
    expect(banner()).toBeNull();
    cleanup();

    api.getProjectVcs.mockResolvedValue(vcs({ cliLogin: '' }, { status: 'unknown' }));
    render(<VcsAuthBanner projectId={4} />);
    await settle();
    expect(banner()).toBeNull();
  });

  it('says "not signed in" with a Sign in button when the token is missing', async () => {
    api.getProjectVcs.mockResolvedValue(vcs());
    render(<VcsAuthBanner projectId={3} />);
    expect(await screen.findByText('Host A repository · not signed in')).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Host A sign-in' })).toBeTruthy();
    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
    // https remote: the SSH sentence does not apply.
    expect(screen.queryByText(/Push works over SSH/)).toBeNull();
  });

  it('says "sign-in expired" when the host rejected the stored credentials', async () => {
    api.getProjectVcs.mockResolvedValue(vcs({}, { status: 'expired', login: 'octo', source: 'store' }));
    render(<VcsAuthBanner projectId={3} />);
    expect(await screen.findByText('Host A repository · sign-in expired')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
  });

  it('an unknown status (CLI missing, host unreachable) never claims "not signed in"', async () => {
    api.getProjectVcs.mockResolvedValue(vcs({}, { status: 'unknown' }));
    render(<VcsAuthBanner projectId={3} />);
    expect(await screen.findByText('Host A repository · sign-in status unknown')).toBeTruthy();
    expect(screen.queryByText(/not signed in/)).toBeNull();
    expect(screen.queryByText(/sign-in expired/)).toBeNull();
    expect(banner()?.getAttribute('data-status')).toBe('unknown');
    expect(screen.getByRole('button', { name: 'Re-check' })).toBeTruthy();
  });

  it('explains the SSH remote + missing token case with terms', async () => {
    api.getProjectVcs.mockResolvedValue(
      vcs({
        terms: { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' },
        remote: { url: 'git@code.example.com:acme/widgets.git', present: true, protocol: 'ssh' },
      }),
    );
    render(<VcsAuthBanner projectId={3} />);
    expect(
      await screen.findByText('Push works over SSH; opening a Merge Request needs a Host B token.'),
    ).toBeTruthy();
  });

  it('calls onSignIn when the caller provides the dialog', async () => {
    api.getProjectVcs.mockResolvedValue(vcs());
    const onSignIn = vi.fn();
    render(<VcsAuthBanner projectId={3} onSignIn={onSignIn} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in' }));
    expect(onSignIn).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/auth login/)).toBeNull();
  });

  it('asks which service hosts an unclassified origin instead of offering a sign-in', async () => {
    api.getProjectVcs.mockResolvedValue(vcs({ askProvider: true, cliLogin: '' }, { status: 'unknown' }));
    api.putProjectVcsProvider.mockResolvedValue(undefined);
    render(<VcsAuthBanner projectId={3} />);
    expect(await screen.findByRole('group', { name: 'Which service hosts code.example.com?' })).toBeTruthy();
    expect(banner()).toBeNull();
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull();

    api.getProjectVcs.mockResolvedValue(
      vcs({ askProvider: false, terms: { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' } }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'GitLab' }));
    expect(api.putProjectVcsProvider).toHaveBeenCalledWith(3, 'gitlab');
    await waitFor(() => expect(api.getProjectVcs).toHaveBeenLastCalledWith(3, true));
    expect(await screen.findByText('Host B repository · not signed in')).toBeTruthy();
    expect(screen.queryByTestId('vcs-provider-ask')).toBeNull();
  });

  it('without onSignIn, expands the daemon-chosen command; Re-check bypasses the cache', async () => {
    api.getProjectVcs.mockResolvedValue(vcs());
    render(<VcsAuthBanner projectId={3} />);
    const signIn = await screen.findByRole('button', { name: 'Sign in' });
    expect(signIn.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(signIn);
    expect(signIn.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByText('hostcli auth login --hostname code.example.com')).toBeTruthy();

    api.getProjectVcs.mockResolvedValue(vcs({}, { status: 'ok', login: 'octo', source: 'cli' }));
    fireEvent.click(screen.getByRole('button', { name: 'Re-check' }));
    await waitFor(() => expect(api.getProjectVcs).toHaveBeenLastCalledWith(3, true));
    await waitFor(() => expect(banner()).toBeNull());
  });
});
