// Why a terminal tab's socket closed, in words the operator can act on.
//
// The daemon puts a reason in the close frame when the PTY side ends the socket
// (internal/api/term.go: termCloseShellExited, termCloseIdle,
// termCloseDaemonStopping, termCloseDetachExpired — keep the strings in step).
// termCloseAttachedElsewhere and termCloseNotFound are close reasons from the
// ATTACH layer itself (phase 4), not the PTY: 'attached-elsewhere' when a
// newer tab/window reattached to the same session, 'not-found' when the id
// named by ?attach= is no longer live (closed already, or never existed).
// Everything else is inferred from the close code and whether the socket ever
// opened: a socket that drops without a close frame (1006) almost always
// means the daemon went away mid-session, e.g. a `make install` restart.

import { msg } from '@lingui/core/macro';
import type { MessageDescriptor } from '@lingui/core';

export type TermCloseKind =
  | 'shell-exited'
  | 'idle'
  | 'daemon-stopping'
  | 'detach-expired'
  | 'attached-elsewhere'
  | 'not-found'
  | 'connection-lost'
  | 'refused'
  | 'too-many'
  | 'start-failed';

/** Close reasons the daemon sends (internal/api/term.go). */
const DAEMON_REASONS: Readonly<Record<string, TermCloseKind>> = {
  'shell-exited': 'shell-exited',
  idle: 'idle',
  'daemon-stopping': 'daemon-stopping',
  'detach-expired': 'detach-expired',
  'attached-elsewhere': 'attached-elsewhere',
  'not-found': 'not-found',
};

/** 1013 Try Again Later: the daemon's terminal cap. */
const CODE_TRY_AGAIN_LATER = 1013;
/** 1011 Internal Error: the daemon accepted the socket but could not start a shell. */
const CODE_INTERNAL_ERROR = 1011;

/** Classify a close event. `opened` is whether the socket ever reached OPEN. */
export function classifyTermClose(opened: boolean, code: number, reason: string): TermCloseKind {
  const named = DAEMON_REASONS[reason];
  if (named !== undefined) return named;
  if (code === CODE_TRY_AGAIN_LATER) return 'too-many';
  if (code === CODE_INTERNAL_ERROR) return 'start-failed';
  if (!opened) return 'refused';
  return 'connection-lost';
}

/** The sentence a closed tab shows for each kind. */
export const TERM_CLOSE_MESSAGE: Readonly<Record<TermCloseKind, MessageDescriptor>> = {
  'shell-exited': msg`The shell in this tab exited.`,
  idle: msg`Closed after 4 hours with no activity.`,
  'daemon-stopping': msg`The swarmery daemon stopped or restarted, and that closed this terminal.`,
  'detach-expired': msg`Nobody reattached to this terminal in time, so it was closed.`,
  'attached-elsewhere': msg`This terminal was reattached from another tab or window.`,
  'not-found': msg`This terminal is already closed.`,
  'connection-lost': msg`Lost the connection to the swarmery daemon. It was probably restarted.`,
  refused: msg`Could not connect: the swarmery daemon is not running, or the dashboard is locked.`,
  'too-many': msg`Too many terminals are open (5 at most). Close one, then restart this tab.`,
  'start-failed': msg`The daemon could not start a shell here.`,
};
