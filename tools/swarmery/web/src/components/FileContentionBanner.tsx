// File contention banner: files that two or more LIVE sessions touched in the
// last few hours — the shared-checkout collision parallel sessions keep
// running into when they edit one repo. Visibility only, nothing is locked.
//
// Silence rule: no contended path → null, no zero-height wrapper, so the
// sessions page's rhythm is identical to before this component existed. A
// failed fetch is also silent: this is an advisory, and the page already has
// its own error box for the list it owns.
//
// Freshness rides the page's existing live socket (useLiveUpdates) — no poller
// of its own. A file_change event, a session start/update (a status change can
// end a contention) or a reconnect schedules one trailing refetch, coalesced so
// a burst of edits costs a single request — and capped by a max wait so a
// steady stream of updates still refetches at least every 10 s.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { getFileContention } from '../api';
import type { ContentionPath, WSMessage } from '../api/types';
import { fmtAgo } from '../lib/format';
import { useSessionHref } from '../lib/sessionHref';
import { useLiveUpdates } from '../lib/ws';

/** Trailing coalesce window for WS-triggered refetches. */
const REFETCH_DEBOUNCE_MS = 2000;
/** Upper bound between the first WS trigger and the refetch it causes. */
const REFETCH_MAX_WAIT_MS = 10_000;

export function FileContentionBanner({ project }: { project: string | null }): JSX.Element | null {
  const [paths, setPaths] = useState<ContentionPath[]>([]);
  const [open, setOpen] = useState(false);
  const sessionHref = useSessionHref();
  // Drops a response that belongs to a scope the page has since left.
  const genRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // When the pending refetch was first armed (null = nothing pending).
  const firstArmRef = useRef<number | null>(null);

  const load = useCallback((): void => {
    const gen = genRef.current;
    getFileContention(project ?? undefined)
      .then((r) => {
        if (gen === genRef.current) setPaths(r.paths);
      })
      .catch(() => {
        if (gen === genRef.current) setPaths([]);
      });
  }, [project]);

  useEffect(() => {
    genRef.current += 1;
    // A refetch armed under the old scope must not fire into the new one.
    if (timerRef.current !== null) clearTimeout(timerRef.current);
    timerRef.current = null;
    firstArmRef.current = null;
    setPaths([]);
    load();
  }, [load]);

  // Trailing debounce WITH a max wait: ingest publishes session_updated per
  // tailed batch, so with two streaming sessions a pure trailing timer would be
  // re-armed forever and never fire. The first arm starts a REFETCH_MAX_WAIT_MS
  // clock; no timer ever lands past it.
  const scheduleLoad = useCallback((): void => {
    const fire = (): void => {
      timerRef.current = null;
      firstArmRef.current = null;
      load();
    };
    const now = Date.now();
    if (firstArmRef.current === null) firstArmRef.current = now;
    if (timerRef.current !== null) clearTimeout(timerRef.current);
    const left = firstArmRef.current + REFETCH_MAX_WAIT_MS - now;
    if (left <= 0) {
      fire();
      return;
    }
    timerRef.current = setTimeout(fire, Math.min(REFETCH_DEBOUNCE_MS, left));
  }, [load]);

  useEffect(
    () => () => {
      if (timerRef.current !== null) clearTimeout(timerRef.current);
    },
    [],
  );

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'event_appended') {
        if (msg.payload.event.type === 'file_change') scheduleLoad();
        return;
      }
      if (msg.type === 'session_started' || msg.type === 'session_updated') scheduleLoad();
    },
    [scheduleLoad],
  );
  useLiveUpdates(onMessage, scheduleLoad);

  if (paths.length === 0) return null;

  const n = paths.length;
  const headline =
    n === 1
      ? '1 file is being edited by more than one live session'
      : `${String(n)} files are being edited by more than one live session`;

  return (
    <div
      className="mt-4 rounded-[8px] border border-amber/40 bg-amber/5 px-3 py-2.5"
      data-file-contention-banner=""
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls="file-contention-list"
        className="flex w-full items-center gap-2 rounded-[6px] text-left font-mono text-[11px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber"
      >
        <span aria-hidden="true" className="text-amber">
          {open ? '▾' : '▸'}
        </span>
        <span className="font-semibold text-amber">shared files</span>
        {/* The live region is the headline alone — the button and the list
            are controls, not announcements. */}
        <span aria-live="polite" className="text-ink-2">
          {headline}
        </span>
      </button>
      {open && (
        <ul id="file-contention-list" className="mt-2 space-y-2">
          {paths.map((p) => (
            <li key={p.path} className="border-t border-amber/25 pt-2">
              <div className="font-mono text-[11px] break-all text-ink">{p.path}</div>
              <ul className="mt-1 space-y-0.5">
                {p.sessions.map((s) => (
                  <li
                    key={s.sessionId}
                    className="flex flex-wrap items-baseline gap-x-2 font-mono text-[10.5px]"
                  >
                    <Link
                      to={sessionHref(s.sessionId)}
                      className="rounded-[4px] text-brand hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
                    >
                      {s.title ?? `session ${String(s.sessionId)}`}
                    </Link>
                    <span className="text-ink-dim">{s.status}</span>
                    <span className="text-ink-faint">
                      {s.changes === 1 ? '1 change' : `${String(s.changes)} changes`} · touched{' '}
                      {fmtAgo(s.lastTouched)}
                    </span>
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
