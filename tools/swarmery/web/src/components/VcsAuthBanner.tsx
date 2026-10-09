// The project's code-host sign-in banner (Phase 5, SC-12), mounted under the
// project header above every workspace page. It reads GET /api/projects/{id}/vcs
// and says one thing: the daemon cannot act on this repository's host as the
// operator — not signed in, or the sign-in expired — with a way out.
//
// Every word comes from the response: the provider's name from `terms`, the
// sign-in command from `cliLogin` (chosen by the daemon per provider). Nothing
// here branches on which provider a project uses (SC-11; enforced by
// web/scripts/check-no-provider-branching.sh).
//
// `onSignIn` is optional: Phase 8 provides the sign-in dialog. Until a caller
// passes one, "Sign in" expands the terminal command plus "Re-check", which
// re-asks the daemon past its 60s cache.

import { useEffect, useId, useState } from 'react';
import type { VcsInfo } from '../api/types';
import { useProjectVcs } from '../lib/useProjectVcs';

export interface VcsAuthBannerProps {
  projectId: number | null;
  /** Opens the sign-in dialog. Absent: "Sign in" expands the terminal command. */
  onSignIn?: () => void;
}

/** Whether the banner has anything to say: a remote the daemon cannot act on,
 *  and a sign-in path to offer (an unknown host with no CLI has none). */
function needsSignIn(vcs: VcsInfo): boolean {
  if (!vcs.remote.present || vcs.auth.status === 'ok') return false;
  return !(vcs.auth.status === 'unknown' && vcs.cliLogin === '');
}

const BTN =
  'rounded-md border border-amber/50 px-2 py-0.5 font-mono text-[10.5px] text-amber transition-colors hover:bg-amber/15 focus-visible:outline focus-visible:outline-2 focus-visible:outline-amber disabled:cursor-not-allowed disabled:opacity-50';

export function VcsAuthBanner({ projectId, onSignIn }: VcsAuthBannerProps): JSX.Element | null {
  const { vcs, reload } = useProjectVcs(projectId);
  const [showHelp, setShowHelp] = useState(false);
  const [checking, setChecking] = useState(false);
  const helpId = useId();

  // A new answer (or a project switch) ends the Re-check in flight.
  useEffect(() => {
    setChecking(false);
  }, [vcs]);

  if (vcs === null || !needsSignIn(vcs)) return null;

  const { terms } = vcs;
  const headline = `${terms.provider} repository · ${vcs.auth.status === 'expired' ? 'sign-in expired' : 'not signed in'}`;
  const sshNote =
    vcs.remote.protocol === 'ssh' && vcs.auth.status === 'missing'
      ? `Push works over SSH; opening a ${terms.change} needs a ${terms.provider} token.`
      : null;

  const recheck = (): void => {
    setChecking(true);
    reload();
  };

  return (
    <section
      aria-label={`${terms.provider} sign-in`}
      data-testid="vcs-auth-banner"
      className="border-b border-amber/40 bg-amber/10 px-4 py-2 font-mono text-[11px] text-amber desk:px-6"
    >
      <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="font-semibold">{headline}</span>
        {sshNote !== null && <span className="text-ink-3">{sshNote}</span>}
        <span className="ml-auto flex items-center gap-2">
          <button
            type="button"
            className={BTN}
            onClick={onSignIn ?? (() => setShowHelp((v) => !v))}
            {...(onSignIn === undefined ? { 'aria-expanded': showHelp, 'aria-controls': helpId } : {})}
          >
            Sign in
          </button>
          <button type="button" className={BTN} onClick={recheck} disabled={checking} aria-busy={checking}>
            Re-check
          </button>
        </span>
      </div>
      {onSignIn === undefined && showHelp && (
        <details id={helpId} open onToggle={(e) => setShowHelp(e.currentTarget.open)} className="mt-2 text-ink-2">
          <summary className="cursor-pointer text-ink-3">Sign in from a terminal</summary>
          {vcs.cliLogin !== '' ? (
            <>
              <pre className="mt-1.5 overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-1.5 text-[11px] text-ink">
                {vcs.cliLogin}
              </pre>
              <p className="mt-1.5 text-ink-3">Run it on the machine the daemon runs on, then Re-check.</p>
            </>
          ) : (
            <p className="mt-1.5 text-ink-3">
              Sign the daemon in to {vcs.host === '' ? 'this host' : vcs.host}, then Re-check.
            </p>
          )}
        </details>
      )}
    </section>
  );
}
