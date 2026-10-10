// "Disconnect" — removes the credential swarmery's own store holds for one
// account, from the card that account owns.
//
// Rendered ONLY on a card the daemon marked `connectedVia: "swarmery"`. That is
// the whole gate: the `claude` CLI's credential file and the macOS keychain item
// are the CLI's, the daemon refuses to touch them, and a button that could end
// the operator's terminal login would be a trap. A CLI-backed card therefore has
// no disconnect at all — there is nothing here that is ours to remove.
//
// What it does NOT do, and the copy says so: it does not log the CLI out and it
// does not revoke anything at Anthropic. That holds after a credential handoff
// too — the file the connect flow wrote into the config dir became the CLI's the
// moment it landed (on macOS the CLI consumed it into the Keychain), so it is
// not ours to remove. Disconnecting stops swarmery reading quota; the tokens
// stay valid upstream until they expire, and the terminal login stays intact.
//
// The confirm is a two-step INLINE step, never window.confirm: a native dialog
// blocks the whole tab, cannot be styled or dismissed by the app, and is
// suppressed outright in some embedded browsers — which would silently turn a
// destructive action into a no-op.
//
// Placement: this renders INSIDE the card's header row (top right), not as a
// card footer — idle is a single small button that costs the card no height.
// The wrapper is `display: contents` so the pieces are flex items of the
// header itself: the armed confirm strip and the error line are `w-full`,
// which wraps each onto its own full-width line right under the title.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { disconnectUsageAccount } from '../../api';
import { useUsage } from '../../lib/usageData';

type Phase = 'idle' | 'confirming' | 'working';

const btn =
  'rounded-[6px] border border-line px-1.5 py-0.5 font-mono text-[9.5px] whitespace-nowrap text-ink-dim transition-colors hover:bg-surface2 hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand disabled:opacity-50';

export function UsageDisconnect({ account }: { account: string }): JSX.Element {
  const { t } = useLingui();
  const { refresh } = useUsage();
  const [phase, setPhase] = useState<Phase>('idle');
  const [error, setError] = useState<string | null>(null);

  const run = async (): Promise<void> => {
    setPhase('working');
    setError(null);
    try {
      await disconnectUsageAccount(account);
      setPhase('idle');
      // Fresh, not cached: the daemon dropped its 30s usage cache on the way
      // out, and the operator is watching this card for the change.
      await refresh(true);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
      // Back to the plain button: the confirm was answered, and re-arming it
      // would read as if nothing had been attempted.
      setPhase('idle');
    }
  };

  return (
    <div className="contents" data-disconnect={account}>
      {phase === 'confirming' ? (
        <div className="flex w-full flex-wrap items-center gap-2">
          <span className="font-mono text-[9.5px] text-ink-dim">
            <Trans>
              Disconnect {account}? Removes swarmery&apos;s stored credential only — your
              <code className="px-1">claude</code> login is untouched, and nothing is revoked at
              Anthropic.
            </Trans>
          </span>
          <button type="button" onClick={() => void run()} className={btn}>
            <Trans>yes, disconnect</Trans>
          </button>
          <button type="button" onClick={() => setPhase('idle')} className={btn}>
            <Trans>cancel</Trans>
          </button>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => {
            setError(null);
            setPhase('confirming');
          }}
          disabled={phase === 'working'}
          className={btn}
        >
          {phase === 'working' ? t`disconnecting…` : t`disconnect`}
        </button>
      )}

      {error !== null && (
        <p role="alert" className="w-full font-mono text-[10px] leading-snug break-words text-red">
          {error}
        </p>
      )}
    </div>
  );
}
