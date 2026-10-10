// "Connect account" — the browser half of the daemon's own OAuth session.
//
// Rung 1 of multi-account usage reads each account's <configDir>/.credentials.json,
// which is where the `claude` CLI keeps its credential on Linux and Windows. On
// macOS the CLI uses the login Keychain instead, so a NON-DEFAULT account there
// has no readable credential at all and its card says "not connected" forever,
// no matter how many times the operator runs the suggested login command.
//
// This closes that: the operator authorizes swarmery itself, once. The daemon
// mints the PKCE challenge and holds the verifier; this component only opens the
// authorize URL and posts back the code the callback page displays. It never
// sees a token, and the code it does handle is single-use and useless without
// the verifier that never left the daemon.
//
// One Connect now does the WHOLE job: the daemon completes the exchange, hands
// the credential over to the account's config dir, and verifies with the
// authoritative CLI probe — the complete response reports all three outcomes.
// When verification fails (nextStep:"pty-login"), a third step renders in this
// same card: an ACCOUNT-scoped terminal (the PTY runs under the account's
// CLAUDE_CONFIG_DIR) where the operator runs `claude` and logs in interactively.
// When that session ends — or on the explicit re-check — the account is
// re-probed with source='pty-login' and the card collapses once it is ready.
//
// Rendered ONLY on a card that is waiting on a login, so a healthy connected
// account — the single-account default on every OS — is pixel-identical to what
// it was before this existed.
//
// Two entries, one flow. `connect` is the original: an account the daemon has no
// credential for. `reconnect` is a card whose SWARMERY-owned credential went bad
// (refresh declined, token rejected, scope missing) — for those the daemon sends
// no CLI command, because `claude` writes to a store that credential never came
// from, and this is the only remedy that replaces it. The steps are identical;
// only the wording changes, because the operator's situation differs.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { Suspense, lazy, useState } from 'react';
import { completeUsageLogin, probeAccount, startUsageLogin } from '../../api';
import { useUsage } from '../../lib/usageData';

// The heavy xterm bundle stays in its own chunk (same trick as the dock):
// fetched only if a connect actually falls back to the interactive login.
const XTerm = lazy(() => import('../../terminal/XTerm').then((m) => ({ default: m.XTerm })));

type Phase = 'idle' | 'starting' | 'awaiting-code' | 'submitting' | 'pty-login';

/** `connect` = never connected; `reconnect` = ours, and currently broken. */
export type ConnectVariant = 'connect' | 'reconnect';

const copy: Record<ConnectVariant, { action: MessageDescriptor; blurb: MessageDescriptor }> = {
  connect: {
    action: msg`Connect account`,
    blurb: msg`Authorize swarmery to read this account's quota — no CLI login needed.`,
  },
  reconnect: {
    action: msg`Reconnect`,
    blurb: msg`Authorize swarmery again — this replaces the stored credential for this account.`,
  },
};

const btn =
  'rounded-[6px] border border-line-strong px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim transition-colors hover:bg-surface2 hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand disabled:opacity-50';

export function UsageConnect({
  account,
  variant = 'connect',
  onResolved,
}: {
  account: string;
  variant?: ConnectVariant;
  /** Fires when the account came out of the flow CLI-READY (probe verdict
   * ready, directly or via the pty-login step) — the hosts that embed this
   * flow outside the Usage modal (readiness banner, create-account modal) use
   * it to re-read the account list and show their own resolved state. NOT
   * called on `later`: a deferred pty login is connected, not resolved. */
  onResolved?: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const { refresh } = useUsage();
  const [phase, setPhase] = useState<Phase>('idle');
  const [code, setCode] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [authorizeUrl, setAuthorizeUrl] = useState<string | null>(null);
  // pty-login step state: the fixed-phrase reason the daemon reported, whether
  // the terminal pane is open, and whether a re-probe is in flight.
  const [reason, setReason] = useState<string | null>(null);
  const [termOpen, setTermOpen] = useState(false);
  const [checking, setChecking] = useState(false);

  const busy = phase === 'starting' || phase === 'submitting';

  const begin = async (): Promise<void> => {
    setPhase('starting');
    setError(null);
    try {
      const { authorizeUrl: url } = await startUsageLogin(account);
      setAuthorizeUrl(url);
      // noopener/noreferrer: the authorization page must not get a handle on
      // this window. A blocked popup is not an error — the link below is the
      // fallback, and it is the same URL.
      window.open(url, '_blank', 'noopener,noreferrer');
      setPhase('awaiting-code');
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
      setPhase('idle');
    }
  };

  const submit = async (): Promise<void> => {
    setPhase('submitting');
    setError(null);
    try {
      const outcome = await completeUsageLogin(account, code.trim());
      // Clear the code from component state the moment it is spent: it is
      // single-use, and there is no reason to keep it around.
      setCode('');
      setAuthorizeUrl(null);
      if (outcome.nextStep === 'pty-login') {
        // The quota half succeeded but the CLI is not ready — take over in the
        // same card. Deliberately NO refresh yet: a refresh would flip the
        // card to "connected" and unmount this component mid-step.
        setReason(outcome.reason ?? t`Claude login required for this account`);
        setPhase('pty-login');
        return;
      }
      setPhase('idle');
      await refresh(true);
      if (outcome.runnable === 'ready') onResolved?.();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
      // Stay in the paste step: the usual cause is a partially copied value,
      // and the operator's authorize tab is still open.
      setPhase('awaiting-code');
    }
  };

  // Re-probe after the interactive login — the verdict's source is
  // 'pty-login', so the store says where it came from. Ready collapses the
  // whole card back to connected; anything else re-renders with the verdict's
  // own fixed-phrase reason.
  const recheck = async (): Promise<void> => {
    setChecking(true);
    setError(null);
    try {
      const verdict = await probeAccount(account, 'pty-login');
      if (verdict.runnable === true) {
        setTermOpen(false);
        setReason(null);
        setPhase('idle');
        await refresh(true);
        onResolved?.();
        return;
      }
      setReason(verdict.runnableReason ?? t`Claude login required for this account`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setChecking(false);
    }
  };

  const dismissPty = async (): Promise<void> => {
    setTermOpen(false);
    setReason(null);
    setPhase('idle');
    // The quota half IS connected — the card may now re-render as such.
    await refresh(true);
  };

  return (
    <div
      className="mt-2 border-t border-line pt-2"
      data-connect={account}
      data-variant={variant}
      data-step={phase}
    >
      {phase === 'idle' || phase === 'starting' ? (
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => void begin()}
            disabled={busy}
            className={btn}
          >
            {phase === 'starting' ? t`starting…` : i18n._(copy[variant].action)}
          </button>
          <span className="font-mono text-[9.5px] leading-relaxed text-ink-faint">
            {i18n._(copy[variant].blurb)}
          </span>
        </div>
      ) : phase === 'pty-login' ? (
        <div className="flex flex-col gap-1.5">
          <p className="font-mono text-[10px] leading-relaxed text-ink-dim">
            <span className="text-ink-faint">3 — </span>
            <Trans>
              Quota connected, but the CLI still needs a login:{' '}
              <span className="text-ink-2">{reason}</span>
            </Trans>
          </p>
          {termOpen ? (
            <>
              <p className="font-mono text-[9.5px] leading-relaxed text-ink-faint">
                <Trans>
                  This terminal runs under <span className="text-ink-2">{account}</span>&apos;s
                  config dir. Run <code className="px-1 text-ink-2">claude</code> and complete the
                  login — when you exit the shell, the account is re-checked automatically.
                </Trans>
              </p>
              <div className="h-64 overflow-hidden rounded-lg border border-line bg-[#0b0d10] p-1.5">
                <Suspense
                  fallback={
                    <div className="p-3 font-mono text-[11px] text-ink-faint">
                      <Trans>loading terminal…</Trans>
                    </div>
                  }
                >
                  <XTerm
                    account={account}
                    fontSize={12}
                    onStatus={(s) => {
                      if (s === 'closed') void recheck();
                    }}
                  />
                </Suspense>
              </div>
            </>
          ) : (
            <p className="font-mono text-[9.5px] leading-relaxed text-ink-faint">
              <Trans>
                Finish it here: open a terminal already scoped to this account and log the CLI in.
              </Trans>
            </p>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            {!termOpen && (
              <button type="button" onClick={() => setTermOpen(true)} className={btn}>
                <Trans>open login terminal</Trans>
              </button>
            )}
            <button
              type="button"
              onClick={() => void recheck()}
              disabled={checking}
              className={btn}
            >
              {checking ? t`checking…` : t`re-check now`}
            </button>
            <button type="button" onClick={() => void dismissPty()} disabled={checking} className={btn}>
              <Trans>later</Trans>
            </button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col gap-1.5">
          <p className="font-mono text-[10px] leading-relaxed text-ink-dim">
            <span className="text-ink-faint">1 — </span>
            <Trans>
              Sign in <span className="text-ink-2">as {account}</span>. The browser tab that just
              opened must be logged into THAT account&apos;s claude.ai subscription — if it is
              signed in as someone else, sign out there first or use a private window.
            </Trans>
          </p>
          {authorizeUrl !== null && (
            <a
              href={authorizeUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="font-mono text-[9.5px] break-all text-brand underline-offset-2 hover:underline"
            >
              <Trans>tab didn&apos;t open? authorize here</Trans>
            </a>
          )}
          <p className="font-mono text-[10px] leading-relaxed text-ink-dim">
            <span className="text-ink-faint">2 — </span>
            <Trans>
              Paste the whole code the page shows, including the part after the <code>#</code>.
            </Trans>
          </p>
          <div className="flex items-center gap-1.5">
            <input
              type="text"
              value={code}
              autoComplete="off"
              spellCheck={false}
              aria-label={t`authorization code for ${account}`}
              // i18n-ignore — the shape of the pasted value, not prose
              placeholder="code#state"
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && code.trim() !== '' && !busy) void submit();
              }}
              className="min-w-0 flex-1 rounded-lg border border-line bg-bg/40 px-2 py-1 font-mono text-[10.5px] text-ink-2 outline-none focus-visible:border-brand"
            />
            <button
              type="button"
              onClick={() => void submit()}
              disabled={busy || code.trim() === ''}
              className={btn}
            >
              {phase === 'submitting' ? '…' : t`connect`}
            </button>
            <button
              type="button"
              onClick={() => {
                setCode('');
                setError(null);
                setAuthorizeUrl(null);
                setPhase('idle');
              }}
              disabled={busy}
              className={btn}
            >
              <Trans>cancel</Trans>
            </button>
          </div>
        </div>
      )}

      {error !== null && (
        <p
          role="alert"
          className="mt-1.5 font-mono text-[10px] leading-snug break-words text-red"
        >
          {error}
        </p>
      )}
    </div>
  );
}
