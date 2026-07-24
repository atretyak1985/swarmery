// Workspace status bar (fusion phase 4): the fixed strip at the bottom of the
// project workspace. Shows the live Waiting/Running/Blocked counts for the
// current project (derived client-side from the board query + task_updated WS,
// so it updates without a refresh), the dispatcher state chip (paused/active
// from GET /api/dispatch), a per-project pause toggle, and the daemon version.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { DispatchStatus } from '../api/types';
import { fetchDispatchStatus, pauseDispatch } from '../api';
import { useHealth, shortVersion } from '../lib/health';
import { projectScopeKey } from './scopeKey';
import type { BoardCounts } from './boardModel';

const POLL_MS = 15_000;

function Dot({ className }: { className: string }): JSX.Element {
  return <span aria-hidden="true" className={`inline-block h-[7px] w-[7px] rounded-full ${className}`} />;
}

export function StatusBar({
  counts,
  projectId,
  terminalOpen = false,
  onToggleTerminal,
}: {
  counts: BoardCounts;
  projectId: number | null;
  /** Whether the terminal dock is currently visible (drives the chevron). */
  terminalOpen?: boolean;
  /** Toggles the bottom terminal dock; omitted (button hidden) until the
   * project path resolves. */
  onToggleTerminal?: (() => void) | undefined;
}): JSX.Element {
  const { health } = useHealth();
  const [dispatch, setDispatch] = useState<DispatchStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const aliveRef = useRef(true);

  const load = useCallback((): void => {
    fetchDispatchStatus()
      .then((d) => {
        if (aliveRef.current) setDispatch(d);
      })
      .catch(() => {
        if (aliveRef.current) setDispatch(null); // 503 (not attached) → hide the chip
      });
  }, []);

  useEffect(() => {
    aliveRef.current = true;
    load();
    const timer = setInterval(load, POLL_MS);
    return () => {
      aliveRef.current = false;
      clearInterval(timer);
    };
  }, [load]);

  const projectPaused =
    dispatch !== null && projectId !== null && dispatch.pausedScopes.includes(projectScopeKey(projectId));
  const globalPaused = dispatch?.globalPaused ?? false;

  const togglePause = (): void => {
    if (projectId === null || dispatch === null) return;
    setBusy(true);
    pauseDispatch('project', !projectPaused, projectId)
      .then(() => {
        if (aliveRef.current) load();
      })
      .catch(() => {
        /* surfaced by the next poll; keep the bar quiet */
      })
      .finally(() => {
        if (aliveRef.current) setBusy(false);
      });
  };

  return (
    <div className="flex h-9 shrink-0 items-center gap-4 border-t border-line bg-bg px-4 font-mono text-[10.5px] text-ink-dim">
      <span className="flex items-center gap-1.5">
        <Dot className="bg-ink-faint" />
        Waiting {counts.waiting}
      </span>
      <span className="flex items-center gap-1.5">
        <Dot className={counts.running > 0 ? 'animate-pulse-dot bg-green' : 'bg-ink-faint'} />
        Running {counts.running}
      </span>
      <span className="flex items-center gap-1.5">
        <Dot className={counts.blocked > 0 ? 'bg-amber' : 'bg-ink-faint'} />
        Blocked {counts.blocked}
      </span>

      <span className="ml-auto flex items-center gap-3">
        {onToggleTerminal !== undefined && (
          <button
            type="button"
            onClick={onToggleTerminal}
            aria-label={terminalOpen ? 'hide terminal' : 'show terminal'}
            aria-pressed={terminalOpen}
            className={`flex items-center gap-1 rounded-md border px-2 py-0.5 text-[10px] transition-colors ${
              terminalOpen
                ? 'border-line-strong bg-surface2 text-ink'
                : 'border-line text-ink-dim hover:border-line-strong hover:text-ink'
            }`}
          >
            <span aria-hidden="true">❯_</span>
            Terminal
            <span aria-hidden="true">{terminalOpen ? '▾' : '▸'}</span>
          </button>
        )}
        {dispatch !== null && (
          <>
            <span
              className="flex items-center gap-1.5"
              title={
                globalPaused
                  ? 'dispatcher globally paused'
                  : `${dispatch.freeSlots} of ${dispatch.maxConcurrent} slots free`
              }
            >
              <Dot className={globalPaused || projectPaused ? 'bg-amber' : 'bg-green'} />
              {globalPaused ? 'dispatcher paused' : projectPaused ? 'project paused' : 'dispatcher active'}
            </span>
            {projectId !== null && !globalPaused && (
              <button
                type="button"
                disabled={busy}
                onClick={togglePause}
                aria-label={projectPaused ? 'resume this project' : 'pause this project'}
                className="rounded-md border border-line px-2 py-0.5 text-[10px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink disabled:opacity-50"
              >
                {projectPaused ? 'resume' : 'pause'}
              </button>
            )}
          </>
        )}
        {health !== null && <span className="text-ink-faint">{shortVersion(health.version)}</span>}
      </span>
    </div>
  );
}
