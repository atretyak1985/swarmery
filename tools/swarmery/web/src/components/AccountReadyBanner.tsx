// Global account-readiness banner (SC-4): whenever the scoped project's
// effective account is not CLI-ready, say so where the operator already is —
// under the header, on every route, in both shells — with the cause and the
// fix inline. The silence rules matter as much as the speech:
//
//   · no project scope            → nothing: no single account to speak for;
//   · runnable === true           → nothing;
//   · runnable === null           → nothing: never probed is not a failure;
//   · connected === null          → nothing: SWARMERY_USAGE_OAUTH=0 means the
//     question could not be asked, and an unknown is not a failure (SC-6);
//   · runnable === false          → the amber banner below.
//
// Every silent state returns null — no zero-height wrapper, so the header's
// rhythm is byte-identical to before this component existed.
//
// Not dismissible while the state persists: the state IS the dismissal
// condition. Connect (the Usage card's own flow, embedded — including its
// pty-login step) and "check now" both end in a re-read of the account list,
// so the banner clears itself with no page reload (SC-5). A connect that ends
// CLI-ready swaps the alert for a dismissible resolved note carrying the
// terminal path (SC-9) — the one thing a fresh connect does NOT fix.
//
// The cause is never invented here: it is the daemon's own fixed phrase
// (`runnableReason` from the probe), falling back to the usage payload's
// Hint.title (e.g. "Claude login required") when the provider carried one.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { probeAccount } from '../api';
import type { Account } from '../api/types';
import { useActiveUsageAccount } from '../lib/activeAccount';
import { patchReadiness, refreshReadiness, useAccountReadiness } from '../lib/accountReadiness';
import { fmtAgo } from '../lib/format';
import { useUsage } from '../lib/usageData';
import { UsageConnect } from './usage/UsageConnect';
import { TerminalPathNote } from './TerminalPathNote';

const btn =
  'rounded-[6px] border border-amber/40 px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-amber transition-colors hover:bg-amber/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-amber disabled:opacity-50';

export function AccountReadyBanner(): JSX.Element | null {
  const { t } = useLingui();
  const active = useActiveUsageAccount(false);
  const accounts = useAccountReadiness();
  const { accounts: usageAccounts, refresh } = useUsage();
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState<string | null>(null);
  // The account key a Connect just resolved — keeps the terminal-path note on
  // screen (dismissible) after the alert itself has cleared.
  const [resolvedKey, setResolvedKey] = useState<string | null>(null);

  const acct: Account | undefined =
    active === null ? undefined : accounts?.find((a) => a.key === active.account);

  const onResolved = (): void => {
    void refreshReadiness();
    setResolvedKey(active?.account ?? null);
  };

  const checkNow = async (): Promise<void> => {
    if (acct === undefined) return;
    setChecking(true);
    setCheckError(null);
    try {
      const verdict = await probeAccount(acct.key);
      patchReadiness(acct.key, verdict);
    } catch (e: unknown) {
      setCheckError(e instanceof Error ? e.message : String(e));
    } finally {
      setChecking(false);
    }
  };

  // The resolved note: shown after a Connect ended CLI-ready, until dismissed
  // or the scope moves on. Takes precedence only when the alert has nothing
  // left to say (a later demotion re-raises the alert over it).
  const showResolved =
    active !== null && resolvedKey === active.account && acct?.runnable !== false;

  // The silence rules, in order (see the header note).
  const showAlert =
    active !== null &&
    acct !== undefined &&
    acct.connected !== null &&
    acct.runnable === false;

  if (!showAlert) {
    if (!showResolved || active === null) return null;
    const { project, account } = active;
    return (
      <div
        role="status"
        className="border-b border-green/25 bg-green/5 px-4 py-2.5 desk:px-6"
        data-account-ready-banner="resolved"
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[11px]">
          <span className="font-semibold text-green">
            <Trans>account ready</Trans>
          </span>
          <span className="text-ink-2">
            <Trans>
              <span className="text-ink">{project}</span> runs as{' '}
              <span className="text-ink">{account}</span> — connected and CLI-ready.
            </Trans>
          </span>
          <button
            type="button"
            onClick={() => {
              setResolvedKey(null);
              void refresh(true);
            }}
            className="ml-auto rounded-[6px] border border-line px-2 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
          >
            <Trans>dismiss</Trans>
          </button>
        </div>
        <TerminalPathNote />
      </div>
    );
  }

  // Cause: the probe's own fixed phrase, else the usage Hint headline for this
  // account. Never composed here — when the daemon said nothing, say nothing.
  const hintTitle = usageAccounts
    .find((row) => row.account === acct.key)
    ?.providers.find((p) => p.hint !== undefined)?.hint?.title;
  const reason = acct.runnableReason ?? hintTitle;
  const project = active.project;
  const accountKey = acct.key;
  const checkedAgo = acct.runnableCheckedAt === undefined ? '' : fmtAgo(acct.runnableCheckedAt);

  return (
    <div
      role="alert"
      className="border-b border-amber/25 bg-amber/5 px-4 py-2.5 desk:px-6"
      data-account-ready-banner="alert"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[11px]">
        <span className="font-semibold text-amber">
          <Trans>account not ready</Trans>
        </span>
        <span className="text-ink-2">
          <Trans>
            <span className="text-ink">{project}</span> runs as{' '}
            <span className="text-ink">{accountKey}</span>
          </Trans>
          {reason !== undefined && reason !== '' && (
            <>
              {' — '}
              <span className="text-amber">{reason}</span>
            </>
          )}
        </span>
        {acct.runnableCheckedAt !== undefined && (
          <span className="text-ink-faint">
            <Trans>checked {checkedAgo}</Trans>
          </span>
        )}
        <button
          type="button"
          onClick={() => void checkNow()}
          disabled={checking}
          className={`ml-auto ${btn}`}
        >
          {checking ? t`checking…` : t`check now`}
        </button>
      </div>
      {checkError !== null && (
        <p className="mt-1 font-mono text-[10px] break-words text-red">{checkError}</p>
      )}
      {/* The fix, inline: the Usage card's own connect flow (OAuth → credential
          handoff → probe, with the pty-login terminal as its third step). Its
          idle state IS the banner's Connect action. */}
      <UsageConnect account={acct.key} onResolved={onResolved} />
    </div>
  );
}
