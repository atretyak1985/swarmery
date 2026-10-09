// Sign the daemon in to a project's code host from the dashboard (Phase 8,
// SC-14), opened by VcsAuthBanner's "Sign in". Two ways in, both ending in the
// daemon's own credential store:
//
//   - Device code: the daemon opens an OAuth device flow; the human enters the
//     short code at the host's verification page — in any browser, on any
//     machine, so it works for a remote daemon too. The dialog polls once per
//     `interval` seconds, honouring a raised interval (the host's slow_down),
//     and stops on ok / expired / denied, on close and on unmount.
//   - Token: a pasted personal access token, validated by the daemon before it
//     is stored; a rejected token (422) stores nothing.
//
// Every word about the host comes from the API (`terms`, `host`, `cliLogin`,
// the server's own `hint`); nothing here branches on which provider a project
// uses (SC-11; enforced by web/scripts/check-no-provider-branching.sh).
//
// Keyboard: focus moves into the dialog on open and back to the opener on
// close, Tab is contained (containTab, the shared overlay trap), Escape closes.
// The status line is aria-live, so a screen reader hears "Waiting…", "Signed
// in as …" and the failure states as they happen.

import { type FormEvent, useEffect, useId, useRef, useState } from 'react';
import { pollVcsLogin, startVcsLogin, submitVcsToken, VcsLoginError } from '../api';
import type { VcsInfo, VcsLoginStart } from '../api/types';
import { containTab } from './ui';

export interface VcsSignInDialogProps {
  projectId: number;
  /** The project's GET …/vcs answer: host, terms and the terminal fallback. */
  vcs: VcsInfo;
  /** Called on Escape, the close button or a backdrop click; `signedIn` tells
   *  the caller to re-ask the daemon (the banner's fresh reload). */
  onClose: (signedIn: boolean) => void;
}

type Tab = 'device' | 'token';

type DeviceState =
  | { kind: 'idle' }
  | { kind: 'starting' }
  | { kind: 'waiting'; start: VcsLoginStart; interval: number }
  | { kind: 'ok'; login: string }
  | { kind: 'expired' }
  | { kind: 'denied' }
  | { kind: 'error'; message: string; hint: string };

type TokenState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'ok'; login: string }
  | { kind: 'error'; message: string; hint: string };

function errorState(e: unknown): { kind: 'error'; message: string; hint: string } {
  if (e instanceof VcsLoginError) return { kind: 'error', message: e.message, hint: e.hint };
  return { kind: 'error', message: e instanceof Error ? e.message : String(e), hint: '' };
}

function signedInLine(login: string): string {
  return login === '' ? 'Signed in.' : `Signed in as ${login}.`;
}

const BTN =
  'rounded-lg border px-3 py-1.5 font-mono text-[11.5px] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-ink-dim disabled:cursor-not-allowed disabled:opacity-50';
const BTN_PRIMARY = `${BTN} border-green/40 bg-green/10 font-semibold text-green hover:bg-green/20`;
const BTN_PLAIN = `${BTN} border-line bg-surface text-ink-2 hover:bg-surface2`;
const TAB =
  'rounded-md px-2.5 py-1 font-mono text-[11px] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-ink-dim';

export function VcsSignInDialog({ projectId, vcs, onClose }: VcsSignInDialogProps): JSX.Element {
  const [tab, setTab] = useState<Tab>('device');
  const [device, setDevice] = useState<DeviceState>({ kind: 'idle' });
  const [tokenState, setTokenState] = useState<TokenState>({ kind: 'idle' });
  const [token, setToken] = useState('');
  const [copied, setCopied] = useState(false);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const firstRef = useRef<HTMLButtonElement | null>(null);
  const titleId = useId();
  const deviceTabId = useId();
  const tokenTabId = useId();
  const devicePanelId = useId();
  const tokenPanelId = useId();
  const tokenInputId = useId();

  const signedIn = device.kind === 'ok' || tokenState.kind === 'ok';
  // Latest values for the mount-once keyboard effect.
  const closeRef = useRef<() => void>(() => onClose(false));
  useEffect(() => {
    closeRef.current = () => onClose(signedIn);
  });

  // Focus in on open, back to the opener on close; Escape closes; Tab stays in.
  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    firstRef.current?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopImmediatePropagation();
        closeRef.current();
        return;
      }
      const box = boxRef.current;
      if (box !== null) containTab(e, box);
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      if (opener?.isConnected === true) opener.focus();
    };
  }, []);

  // One poll per `interval` seconds while a code is waiting. Every pending
  // answer is a new state object, which schedules the next step; leaving the
  // waiting state, closing or unmounting cancels the timer and drops any
  // answer still in flight.
  useEffect(() => {
    if (device.kind !== 'waiting') return;
    let cancelled = false;
    const { start, interval } = device;
    const timer = window.setTimeout(() => {
      pollVcsLogin(projectId, start.loginId)
        .then((r) => {
          if (cancelled) return;
          switch (r.status) {
            case 'pending':
              setDevice({ kind: 'waiting', start, interval: r.interval > 0 ? r.interval : interval });
              return;
            case 'ok':
              setDevice({ kind: 'ok', login: r.login ?? '' });
              return;
            case 'expired':
              setDevice({ kind: 'expired' });
              return;
            case 'denied':
              setDevice({ kind: 'denied' });
              return;
          }
        })
        .catch((e: unknown) => {
          if (!cancelled) setDevice(errorState(e));
        });
    }, interval * 1000);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [device, projectId]);

  const startDevice = async (): Promise<void> => {
    setDevice({ kind: 'starting' });
    setCopied(false);
    try {
      const start = await startVcsLogin(projectId);
      setDevice({ kind: 'waiting', start, interval: start.interval > 0 ? start.interval : 5 });
    } catch (e: unknown) {
      setDevice(errorState(e));
    }
  };

  const copyCode = (code: string): void => {
    const clip = typeof navigator === 'undefined' ? undefined : navigator.clipboard;
    if (clip === undefined) return;
    clip.writeText(code).then(
      () => setCopied(true),
      () => setCopied(false),
    );
  };

  const submitToken = async (e: FormEvent<HTMLFormElement>): Promise<void> => {
    e.preventDefault();
    const value = token.trim();
    if (value === '') return;
    setTokenState({ kind: 'submitting' });
    // The token leaves the page's state as soon as it is sent.
    setToken('');
    try {
      const r = await submitVcsToken(projectId, value);
      setTokenState({ kind: 'ok', login: r.login });
    } catch (err: unknown) {
      setTokenState(errorState(err));
    }
  };

  const host = vcs.host === '' ? 'this host' : vcs.host;
  const provider = vcs.terms.provider;

  let status: string;
  if (tab === 'device') {
    switch (device.kind) {
      case 'idle':
        status = `Get a one-time code, then enter it on ${host}.`;
        break;
      case 'starting':
        status = 'Asking for a code…';
        break;
      case 'waiting':
        status = `Waiting for you to enter the code on ${host}…`;
        break;
      case 'ok':
        status = signedInLine(device.login);
        break;
      case 'expired':
        status = 'The code expired before it was entered. Get a new one.';
        break;
      case 'denied':
        status = 'The sign-in was declined.';
        break;
      case 'error':
        status = device.message;
        break;
    }
  } else {
    switch (tokenState.kind) {
      case 'idle':
        status = `Paste a ${provider} personal access token. It is checked with ${host} before it is stored.`;
        break;
      case 'submitting':
        status = 'Checking the token…';
        break;
      case 'ok':
        status = signedInLine(tokenState.login);
        break;
      case 'error':
        status = tokenState.message;
        break;
    }
  }
  const failed =
    (tab === 'device' && (device.kind === 'error' || device.kind === 'expired' || device.kind === 'denied')) ||
    (tab === 'token' && tokenState.kind === 'error');
  const hint =
    tab === 'device' ? (device.kind === 'error' ? device.hint : '') : tokenState.kind === 'error' ? tokenState.hint : '';

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="presentation"
      onClick={(e) => {
        e.stopPropagation();
        onClose(signedIn);
      }}
    >
      <div
        ref={boxRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="vcs-sign-in-dialog"
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4 font-mono text-[12px] text-ink-2"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <h2 id={titleId} className="font-display text-[14px] font-bold text-ink">
            Sign in to {provider}
            <span className="ml-1.5 font-mono text-[11px] font-normal text-ink-3">{vcs.host}</span>
          </h2>
          <button type="button" className={BTN_PLAIN} onClick={() => onClose(signedIn)} aria-label="Close">
            ×
          </button>
        </div>

        <div role="tablist" aria-label="Sign-in method" className="mt-3 flex gap-1.5">
          <button
            ref={firstRef}
            type="button"
            role="tab"
            id={deviceTabId}
            aria-selected={tab === 'device'}
            aria-controls={devicePanelId}
            className={`${TAB} ${tab === 'device' ? 'bg-surface2 text-ink' : 'text-ink-3 hover:text-ink-2'}`}
            onClick={() => setTab('device')}
          >
            Device code
          </button>
          <button
            type="button"
            role="tab"
            id={tokenTabId}
            aria-selected={tab === 'token'}
            aria-controls={tokenPanelId}
            className={`${TAB} ${tab === 'token' ? 'bg-surface2 text-ink' : 'text-ink-3 hover:text-ink-2'}`}
            onClick={() => setTab('token')}
          >
            Token
          </button>
        </div>

        {tab === 'device' ? (
          <div role="tabpanel" id={devicePanelId} aria-labelledby={deviceTabId} className="mt-3">
            {device.kind === 'waiting' ? (
              <>
                <div className="flex items-center gap-2">
                  <output
                    data-testid="vcs-user-code"
                    className="rounded-lg border border-line bg-bg px-3 py-2 font-mono text-[22px] font-bold tracking-[0.2em] text-ink"
                  >
                    {device.start.userCode}
                  </output>
                  <button type="button" className={BTN_PLAIN} onClick={() => copyCode(device.start.userCode)}>
                    {copied ? 'Copied' : 'Copy'}
                  </button>
                </div>
                <a
                  href={device.start.verificationUriComplete ?? device.start.verificationUri}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mt-2.5 inline-block text-ink underline underline-offset-2 hover:text-green"
                >
                  Open {device.start.verificationUri}
                </a>
              </>
            ) : device.kind === 'ok' ? null : (
              <button
                type="button"
                className={BTN_PRIMARY}
                disabled={device.kind === 'starting'}
                aria-busy={device.kind === 'starting'}
                onClick={() => {
                  void startDevice();
                }}
              >
                {device.kind === 'idle' || device.kind === 'starting' ? 'Get a code' : 'Get a new code'}
              </button>
            )}
          </div>
        ) : (
          <div role="tabpanel" id={tokenPanelId} aria-labelledby={tokenTabId} className="mt-3">
            {tokenState.kind === 'ok' ? null : (
              <form
                className="flex flex-col gap-2"
                onSubmit={(e) => {
                  void submitToken(e);
                }}
              >
                <label htmlFor={tokenInputId} className="text-ink-3">
                  Personal access token
                </label>
                <input
                  id={tokenInputId}
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  className="rounded-lg border border-line bg-bg px-2.5 py-1.5 font-mono text-[12px] text-ink"
                />
                <div>
                  <button
                    type="submit"
                    className={BTN_PRIMARY}
                    disabled={tokenState.kind === 'submitting' || token.trim() === ''}
                    aria-busy={tokenState.kind === 'submitting'}
                  >
                    Save token
                  </button>
                </div>
              </form>
            )}
          </div>
        )}

        <p
          role="status"
          aria-live="polite"
          data-testid="vcs-sign-in-status"
          className={`mt-3 ${failed ? 'text-red' : signedIn ? 'text-green' : 'text-ink-2'}`}
        >
          {status}
        </p>
        {hint !== '' && <p className="mt-1.5 whitespace-pre-wrap text-ink-3">{hint}</p>}

        {vcs.cliLogin !== '' && (
          <p className="mt-3 border-t border-line pt-2.5 text-ink-3">
            or run in a terminal: <code className="text-ink">{vcs.cliLogin}</code>
          </p>
        )}
        {signedIn && (
          <div className="mt-3 flex justify-end">
            <button type="button" className={BTN_PRIMARY} onClick={() => onClose(true)}>
              Done
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
