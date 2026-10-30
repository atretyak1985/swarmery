// How an instruction can reach a working session, which decides whether `redirect`
// exists at all: a button that focuses a disabled composer does nothing.

import type { ProcState, Session } from '../../api/types';

export function hasLiveProcess(procState: ProcState | null | undefined): boolean {
  return procState === 'running' || procState === 'orphaned';
}

export type RedirectChannel = 'composer' | 'link' | null;

/** The composer while it accepts text, else a link mod that polls for commands. */
export function redirectChannel(session: Pick<Session, 'procState' | 'resumeInFlight' | 'link'>): RedirectChannel {
  if (!hasLiveProcess(session.procState) && session.resumeInFlight !== true) return 'composer';
  return session.link?.controllable === true ? 'link' : null;
}
