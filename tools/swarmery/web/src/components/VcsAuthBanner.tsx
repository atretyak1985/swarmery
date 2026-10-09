// The project's code-host sign-in banner (Phase 5, SC-12), mounted under the
// project header above every workspace page. It reads GET /api/projects/{id}/vcs
// and says one thing: the daemon cannot act on this repository's host as the
// operator — not signed in, or the sign-in expired — with a way out. When the
// daemon could not tell (status unknown: the provider CLI is missing or the
// host did not answer) it says only that, in a softer tone: a signed-in
// operator who is offline must not be told they are signed out.
//
// Every word comes from the response: the provider's name from `terms`, the
// sign-in command from `cliLogin` (chosen by the daemon per provider). Nothing
// here branches on which provider a project uses (SC-11; enforced by
// web/scripts/check-no-provider-branching.sh).
//
// When the daemon could not classify the origin's host (`askProvider`), the
// banner asks which service hosts it instead (VcsProviderAsk), once.
//
// "Sign in" opens VcsSignInDialog (Phase 8): a device code or a pasted token,
// with the terminal command as its footer fallback. Closing the dialog after a
// sign-in re-asks the daemon past its 60s cache, as "Re-check" does. A caller
// may pass `onSignIn` to open a sign-in surface of its own instead.

import { useEffect, useState } from 'react';
import type { VcsInfo } from '../api/types';
import { useProjectVcs } from '../lib/useProjectVcs';
import { VcsProviderAsk } from './VcsProviderAsk';
import { VcsSignInDialog } from './VcsSignInDialog';

export interface VcsAuthBannerProps {
  projectId: number | null;
  /** Replaces the built-in sign-in dialog. Absent: "Sign in" opens VcsSignInDialog. */
  onSignIn?: () => void;
}

/** Whether the banner has anything to say: a remote the daemon cannot act on,
 *  and a sign-in path to offer (an unknown host with no CLI has none). */
function needsSignIn(vcs: VcsInfo): boolean {
  if (!vcs.remote.present || vcs.auth.status === 'ok') return false;
  return !(vcs.auth.status === 'unknown' && vcs.cliLogin === '');
}

const BTN_BASE =
  'rounded-md border px-2 py-0.5 font-mono text-[10.5px] transition-colors focus-visible:outline focus-visible:outline-2 disabled:cursor-not-allowed disabled:opacity-50';
const BTN_WARN = `${BTN_BASE} border-amber/50 text-amber hover:bg-amber/15 focus-visible:outline-amber`;
const BTN_SOFT = `${BTN_BASE} border-line text-ink-2 hover:bg-surface2 focus-visible:outline-ink-dim`;
const SECTION_WARN = 'border-amber/40 bg-amber/10 text-amber';
const SECTION_SOFT = 'border-line bg-surface text-ink-2';

/** The banner's one-line statement for an auth status it shows. */
function headlineFor(status: VcsInfo['auth']['status'], provider: string): string {
  switch (status) {
    case 'expired':
      return `${provider} repository · sign-in expired`;
    case 'unknown':
      return `${provider} repository · sign-in status unknown`;
    default:
      return `${provider} repository · not signed in`;
  }
}

export function VcsAuthBanner({ projectId, onSignIn }: VcsAuthBannerProps): JSX.Element | null {
  const { vcs, reload } = useProjectVcs(projectId);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [checking, setChecking] = useState(false);

  // A new answer (or a project switch) ends the Re-check in flight.
  useEffect(() => {
    setChecking(false);
  }, [vcs]);

  if (vcs === null) return null;
  // An origin on a host the daemon could not classify: ask which service
  // hosts it (once — the answer is stored) instead of a sign-in it cannot name.
  if (vcs.askProvider === true && projectId !== null) {
    return <VcsProviderAsk projectId={projectId} host={vcs.host} onSaved={reload} />;
  }
  if (!needsSignIn(vcs)) return null;

  const { terms } = vcs;
  const unknown = vcs.auth.status === 'unknown';
  const headline = headlineFor(vcs.auth.status, terms.provider);
  const btn = unknown ? BTN_SOFT : BTN_WARN;
  const sshNote =
    vcs.remote.protocol === 'ssh' && vcs.auth.status === 'missing'
      ? `Push works over SSH; opening a ${terms.change} needs a ${terms.provider} token.`
      : null;

  const recheck = (): void => {
    setChecking(true);
    reload();
  };

  return (
    <>
      <section
        aria-label={`${terms.provider} sign-in`}
        data-testid="vcs-auth-banner"
        data-status={vcs.auth.status}
        className={`border-b px-4 py-2 font-mono text-[11px] desk:px-6 ${unknown ? SECTION_SOFT : SECTION_WARN}`}
      >
        <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span className="font-semibold">{headline}</span>
          {sshNote !== null && <span className="text-ink-3">{sshNote}</span>}
          <span className="ml-auto flex items-center gap-2">
            <button
              type="button"
              className={btn}
              onClick={onSignIn ?? (() => setDialogOpen(true))}
              {...(onSignIn === undefined ? { 'aria-haspopup': 'dialog' as const } : {})}
            >
              Sign in
            </button>
            <button type="button" className={btn} onClick={recheck} disabled={checking} aria-busy={checking}>
              Re-check
            </button>
          </span>
        </div>
      </section>
      {dialogOpen && projectId !== null && (
        <VcsSignInDialog
          projectId={projectId}
          vcs={vcs}
          onClose={(signedIn) => {
            setDialogOpen(false);
            if (signedIn) recheck();
          }}
        />
      )}
    </>
  );
}
