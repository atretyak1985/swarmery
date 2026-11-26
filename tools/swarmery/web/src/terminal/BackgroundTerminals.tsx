// Dock terminal session list (terminal-dock-webstorm phase 5): the shared
// list + row used by two surfaces — the toolbar's "Background terminals" menu
// (PTYs that are running but not open in any current tab, from
// GET /api/term/sessions) and the `too-many` Start overlay (every PTY that
// counts against the daemon's cap, so the operator can free a slot without
// leaving the dock). Both read the same DockTermSession DTO (api/types.ts,
// mirrors termSessionDTO in internal/api/term.go) and the same DELETE action.

import { Trans, useLingui } from '@lingui/react/macro';
import type { DockTermSession } from '../api/types';

/** `cwd` relative to the project root, or the absolute path when it isn't under it. */
export function relativeCwd(cwd: string, projectPath: string): string {
  if (projectPath === '') return cwd;
  if (cwd === projectPath) return '.';
  const prefix = projectPath.endsWith('/') ? projectPath : `${projectPath}/`;
  return cwd.startsWith(prefix) ? cwd.slice(prefix.length) : cwd;
}

/** Whole minutes between `iso` and `now` (never negative — clock skew clamps to 0). */
function minutesSince(iso: string, now: number): number {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return 0;
  return Math.max(0, Math.round((now - then) / 60_000));
}

/** How long a detached session has left before the reaper closes it (DetachGrace). */
const DETACH_GRACE_MINUTES = 15;

/** One row's status line: detached N min ago, and how long until the reaper closes it. */
function DetachedMeta({ detachedAt, now }: { detachedAt: string; now: number }): JSX.Element {
  const since = minutesSince(detachedAt, now);
  const left = Math.max(0, DETACH_GRACE_MINUTES - since);
  return (
    <Trans>
      detached {since} min ago · closes in {left} min
    </Trans>
  );
}

/**
 * One session row's label: the run DTO carries no human title, so this is the
 * best the dock can show without guessing — the daemon's own label (set by
 * whichever surface started the PTY), else the foreground process, else a
 * generic "shell".
 */
function rowLabel(s: DockTermSession): string | JSX.Element {
  if (s.label !== undefined && s.label !== '') return s.label;
  if (s.foreground !== undefined && s.foreground !== '') return s.foreground;
  return <Trans>shell</Trans>;
}

/**
 * The shared list: `role="list"` with one row per session, an "Attach" button
 * when `onAttach` is given (the background-terminals menu; the too-many
 * overlay omits it — those PTYs are already open elsewhere, in a tab this
 * dock already lost its slot for), and a "Close" button always.
 */
export function TermSessionList({
  sessions,
  projectPath,
  onAttach,
  onClose,
  now = Date.now(),
}: {
  sessions: readonly DockTermSession[];
  projectPath: string;
  onAttach?: (s: DockTermSession) => void;
  onClose: (s: DockTermSession) => void;
  now?: number;
}): JSX.Element {
  const { t } = useLingui();
  if (sessions.length === 0) {
    return <p className="px-2 py-1.5 text-ink-faint">{t`No background terminals.`}</p>;
  }
  return (
    <ul role="list" className="flex flex-col gap-1">
      {sessions.map((s) => (
        <li key={s.id} className="flex items-center gap-2 rounded px-2 py-1 hover:bg-surface2">
          <span className="min-w-0 flex-1 truncate">
            <span className="text-ink">{rowLabel(s)}</span>
            <span className="text-ink-faint"> · {relativeCwd(s.cwd, projectPath)}</span>
            {s.foreground !== undefined && s.foreground !== '' && s.label !== undefined && s.label !== '' && (
              <span className="text-ink-faint"> · {s.foreground}</span>
            )}
            {!s.attached && s.detachedAt !== undefined && s.detachedAt !== '' && (
              <span className="text-ink-faint">
                {' · '}
                <DetachedMeta detachedAt={s.detachedAt} now={now} />
              </span>
            )}
          </span>
          {onAttach !== undefined && (
            <button
              type="button"
              aria-label={t`attach to ${rowLabelText(s)}`}
              onClick={() => onAttach(s)}
              className="shrink-0 rounded border border-line px-1.5 py-0.5 text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
            >
              <Trans>Attach</Trans>
            </button>
          )}
          <button
            type="button"
            aria-label={t`close ${rowLabelText(s)}`}
            onClick={() => onClose(s)}
            className="shrink-0 rounded border border-line px-1.5 py-0.5 text-ink-dim transition-colors hover:border-brand hover:text-brand"
          >
            <Trans>Close</Trans>
          </button>
        </li>
      ))}
    </ul>
  );
}

/** Plain-text row label for an aria-label (which can't hold the `<Trans>` JSX above). */
function rowLabelText(s: DockTermSession): string {
  if (s.label !== undefined && s.label !== '') return s.label;
  if (s.foreground !== undefined && s.foreground !== '') return s.foreground;
  return 'shell';
}
