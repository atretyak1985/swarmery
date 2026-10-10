// @vitest-environment jsdom
//
// The dashboard sign-in dialog (Phase 8, SC-14): the device-code tab's polling
// states (pending → ok, slow_down raising the interval, expired, denied), the
// 409 unconfigured hint, the token tab's submit and 422, the terminal footer,
// and the dialog's keyboard contract (focus in, Tab contained, Escape closes)
// and timer cleanup. The API is mocked at the three sign-in calls; the real
// VcsLoginError class is kept.

import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { VcsLoginError } from '../api';
import type { VcsInfo, VcsLoginStart } from '../api/types';
import { VcsSignInDialog } from './VcsSignInDialog';

const api = vi.hoisted(() => ({ startVcsLogin: vi.fn(), pollVcsLogin: vi.fn(), submitVcsToken: vi.fn() }));

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>();
  return {
    ...real,
    startVcsLogin: api.startVcsLogin,
    pollVcsLogin: api.pollVcsLogin,
    submitVcsToken: api.submitVcsToken,
  };
});

// A neutral vocabulary and CLI: the dialog may only ever read them.
const vcs = (over: Partial<VcsInfo> = {}): VcsInfo => ({
  provider: 'unknown',
  host: 'code.example.com',
  terms: { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' },
  remote: { url: 'https://code.example.com/acme/widgets.git', present: true, protocol: 'https' },
  auth: { status: 'missing', login: '', source: 'none' },
  baseBranch: '',
  allowPushToBase: false,
  source: 'host',
  cliLogin: 'hostcli auth login --hostname code.example.com',
  ...over,
});

const START: VcsLoginStart = {
  loginId: 'L1',
  userCode: 'WDJB-MJHT',
  verificationUri: 'https://code.example.com/device',
  expiresIn: 900,
  interval: 5,
};

beforeEach(() => {
  api.startVcsLogin.mockReset();
  api.pollVcsLogin.mockReset();
  api.submitVcsToken.mockReset();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** Advance fake time and let the resolved promises it released settle. */
async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const status = (): string => screen.getByTestId('vcs-sign-in-status').textContent ?? '';

function open(onClose = vi.fn(), info = vcs()): ReturnType<typeof vi.fn> {
  render(<VcsSignInDialog projectId={3} vcs={info} onClose={onClose} />);
  return onClose;
}

async function getCode(): Promise<void> {
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }));
  await advance(0);
}

describe('VcsSignInDialog — device code', () => {
  it('shows the code and the verification link, polls every interval honouring slow_down, and ends signed in', async () => {
    vi.useFakeTimers();
    api.startVcsLogin.mockResolvedValue(START);
    api.pollVcsLogin
      .mockResolvedValueOnce({ status: 'pending', interval: 10 })
      .mockResolvedValueOnce({ status: 'ok', login: 'octocat', interval: 10 });
    const onClose = open();
    await getCode();

    expect(api.startVcsLogin).toHaveBeenCalledWith(3);
    expect(screen.getByTestId('vcs-user-code').textContent).toBe('WDJB-MJHT');
    const link = screen.getByRole('link', { name: 'Open https://code.example.com/device' });
    expect(link.getAttribute('href')).toBe('https://code.example.com/device');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(status()).toBe('Waiting for you to enter the code on code.example.com…');

    await advance(4999);
    expect(api.pollVcsLogin).not.toHaveBeenCalled();
    await advance(1);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(1);
    expect(api.pollVcsLogin).toHaveBeenCalledWith(3, 'L1');

    // slow_down raised the interval to 10 s: nothing at +5 s, the next poll at +10 s.
    await advance(5000);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(1);
    await advance(5000);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(2);
    expect(status()).toBe('Signed in as octocat.');

    // Polling stopped.
    await advance(60_000);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(onClose).toHaveBeenCalledWith(true);
  });

  it('prefers the pre-filled verification URI for the link when the host sends one', async () => {
    vi.useFakeTimers();
    api.startVcsLogin.mockResolvedValue({ ...START, verificationUriComplete: 'https://code.example.com/device?code=WDJB' });
    open();
    await getCode();
    const link = screen.getByRole('link', { name: 'Open https://code.example.com/device' });
    expect(link.getAttribute('href')).toBe('https://code.example.com/device?code=WDJB');
  });

  it('an expired code stops polling and offers a new one', async () => {
    vi.useFakeTimers();
    api.startVcsLogin.mockResolvedValue(START);
    api.pollVcsLogin.mockResolvedValueOnce({ status: 'expired', interval: 5 });
    open();
    await getCode();
    await advance(5000);
    expect(status()).toBe('The code expired before it was entered. Get a new one.');
    expect(screen.queryByTestId('vcs-user-code')).toBeNull();
    await advance(30_000);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(1);

    api.startVcsLogin.mockResolvedValue({ ...START, loginId: 'L2', userCode: 'NEW-CODE' });
    fireEvent.click(screen.getByRole('button', { name: 'Get a new code' }));
    await advance(0);
    expect(screen.getByTestId('vcs-user-code').textContent).toBe('NEW-CODE');
  });

  it('a declined sign-in says so and stops polling', async () => {
    vi.useFakeTimers();
    api.startVcsLogin.mockResolvedValue(START);
    api.pollVcsLogin.mockResolvedValueOnce({ status: 'denied', interval: 5 });
    open();
    await getCode();
    await advance(5000);
    expect(status()).toBe('The sign-in was declined.');
    await advance(30_000);
    expect(api.pollVcsLogin).toHaveBeenCalledTimes(1);
  });

  it('shows the server hint when the device flow is not configured (409)', async () => {
    api.startVcsLogin.mockRejectedValue(
      new VcsLoginError(
        409,
        {
          error: 'no OAuth client id is configured for code.example.com, so the device flow cannot start',
          code: 'device-flow-unconfigured',
          hint: 'Set SWARMERY_GITHUB_CLIENT_ID=<client id> in the daemon environment. See docs/vcs-login.md.',
        },
        'sign-in failed',
      ),
    );
    open();
    fireEvent.click(screen.getByRole('button', { name: 'Get a code' }));
    expect(await screen.findByText(/SWARMERY_GITHUB_CLIENT_ID=<client id>/)).toBeTruthy();
    expect(status()).toBe('no OAuth client id is configured for code.example.com, so the device flow cannot start');
    expect(screen.getByRole('button', { name: 'Get a new code' })).toBeTruthy();
  });

  it('closing (unmounting) mid-flow cancels the poll timer', async () => {
    vi.useFakeTimers();
    api.startVcsLogin.mockResolvedValue(START);
    api.pollVcsLogin.mockResolvedValue({ status: 'pending', interval: 5 });
    open();
    await getCode();
    cleanup();
    await advance(60_000);
    expect(api.pollVcsLogin).not.toHaveBeenCalled();
  });
});

describe('VcsSignInDialog — token', () => {
  it('a rejected token (422) shows the server message, clears the field and stores nothing', async () => {
    api.submitVcsToken.mockRejectedValue(
      new VcsLoginError(
        422,
        {
          error: 'code.example.com rejected this token',
          code: 'not-authenticated',
          hint: 'check the token and its scopes, then try again — nothing was stored',
        },
        'token sign-in failed',
      ),
    );
    const onClose = open();
    fireEvent.click(screen.getByRole('tab', { name: 'Token' }));
    expect(screen.getByRole('tab', { name: 'Token' }).getAttribute('aria-selected')).toBe('true');
    const input = screen.getByLabelText('Personal access token') as HTMLInputElement;
    expect(input.type).toBe('password');
    const save = screen.getByRole('button', { name: 'Save token' }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    fireEvent.change(input, { target: { value: '  tok-123  ' } });
    fireEvent.click(save);
    expect(await screen.findByText('code.example.com rejected this token')).toBeTruthy();
    expect(screen.getByText(/nothing was stored/)).toBeTruthy();
    expect(api.submitVcsToken).toHaveBeenCalledWith(3, 'tok-123');
    expect(input.value).toBe('');

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledWith(false);
  });

  it('an accepted token ends signed in', async () => {
    api.submitVcsToken.mockResolvedValue({ status: 'ok', login: 'octocat' });
    const onClose = open();
    fireEvent.click(screen.getByRole('tab', { name: 'Token' }));
    fireEvent.change(screen.getByLabelText('Personal access token'), { target: { value: 'tok-123' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save token' }));
    expect(await screen.findByText('Signed in as octocat.')).toBeTruthy();
    expect(screen.queryByLabelText('Personal access token')).toBeNull();
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledWith(true);
  });
});

describe('VcsSignInDialog — chrome', () => {
  it('names the host from terms, offers the terminal command, and has a live status line', () => {
    open();
    expect(screen.getByRole('dialog', { name: /Sign in to Host A/ }).getAttribute('aria-modal')).toBe('true');
    expect(screen.getByText('hostcli auth login --hostname code.example.com')).toBeTruthy();
    expect(screen.getByText(/or run in a terminal:/)).toBeTruthy();
    expect(screen.getByTestId('vcs-sign-in-status').getAttribute('aria-live')).toBe('polite');
  });

  it('omits the terminal footer when there is no CLI command', () => {
    open(vi.fn(), vcs({ cliLogin: '' }));
    expect(screen.queryByText(/or run in a terminal:/)).toBeNull();
  });

  it('moves focus in, contains Tab, and closes on Escape and on the close button', () => {
    const onClose = open();
    const dialog = screen.getByRole('dialog');
    expect(dialog.contains(document.activeElement)).toBe(true);

    const last = screen.getByRole('button', { name: 'Get a code' });
    last.focus();
    fireEvent.keyDown(window, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));
    fireEvent.keyDown(window, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledWith(false);
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
