// App shell (Redesign frame): a full-width top header bar — SW◆RMERY wordmark
// at the far left (over the sidebar column), "● control plane · :<port>" at
// the right, border-b spanning the full width — with the sidebar starting
// BELOW the header and <main> as the app's scroll container (so the session
// detail can pin its header and scroll only the tab panel). Mobile keeps the
// bottom nav. The Docs nav item appears only when /api/docs has entries; the
// desktop Sessions item carries a today-count badge (/api/stats/overview);
// the Approvals item carries a LIVE amber pending-count badge (REST resync +
// WS permission_requested/permission_resolved over the shared connection).

import { useCallback, useEffect, useState } from 'react';
import { NavLink, Outlet } from 'react-router-dom';
import type { WSMessage } from './api/types';
import { fetchApprovals, fetchDocs, fetchStatsOverview, MOCK } from './api';
import { isoDay } from './lib/format';
import { useLiveUpdates } from './lib/ws';
import { HealthFooter } from './components/HealthFooter';

interface NavItem {
  to: string;
  icon: string;
  label: string;
  /** Right-aligned desktop count badge. */
  badge?: string;
  badgeClass?: string;
  /** Amber attention dot on the mobile icon (pending approvals). */
  alert?: boolean;
}

const DOCS_NAV: NavItem = { to: '/docs', icon: '❐', label: 'Docs' };

/** The dashboard's own port for the header line (":7777" default). */
function portLabel(): string {
  return window.location.port !== '' ? window.location.port : '7777';
}

export function App(): JSX.Element {
  const [hasDocs, setHasDocs] = useState(false);
  const [sessionsToday, setSessionsToday] = useState<number | null>(null);
  // Pending approvals as a SET of ids: WS +/- stays idempotent when the same
  // permission_resolved arrives twice (own action + fan-out) or after resync.
  const [pendingIds, setPendingIds] = useState<ReadonlySet<number>>(new Set());

  useEffect(() => {
    fetchDocs()
      .then((docs) => setHasDocs(docs.length > 0))
      .catch(() => setHasDocs(false)); // empty/unreachable → hide the Docs item
  }, []);

  useEffect(() => {
    // Sessions nav badge: one-shot fetch of today's overview so the count
    // works on every screen (hidden when unavailable).
    fetchStatsOverview(isoDay())
      .then((o) => setSessionsToday(o.sessions))
      .catch(() => setSessionsToday(null));
  }, []);

  // Approvals badge: REST is the source of truth (mount + reconnect resync);
  // the WS stream is the low-latency hint in between (docs/ws-protocol.md).
  const syncPending = useCallback((): void => {
    fetchApprovals('pending')
      .then((list) => setPendingIds(new Set(list.map((r) => r.id))))
      .catch(() => setPendingIds(new Set())); // approvals API absent → no badge
  }, []);
  useEffect(syncPending, [syncPending]);

  const onMessage = useCallback((msg: WSMessage): void => {
    if (msg.type === 'permission_requested') {
      setPendingIds((prev) => new Set(prev).add(msg.payload.id));
    } else if (msg.type === 'permission_resolved') {
      setPendingIds((prev) => {
        if (!prev.has(msg.payload.id)) return prev;
        const next = new Set(prev);
        next.delete(msg.payload.id);
        return next;
      });
    }
    // Other message types are the pages' concern — ignore here.
  }, []);
  useLiveUpdates(onMessage, syncPending);

  const pendingCount = pendingIds.size;
  const items: NavItem[] = [
    { to: '/', icon: '◉', label: 'Overview' },
    {
      to: '/approvals',
      icon: '⧗',
      label: 'Approvals',
      ...(pendingCount > 0
        ? { badge: String(pendingCount), badgeClass: 'bg-amber/15 text-amber', alert: true }
        : {}),
    },
    {
      to: '/sessions',
      icon: '☰',
      label: 'Sessions',
      ...(sessionsToday !== null && sessionsToday > 0
        ? { badge: String(sessionsToday), badgeClass: 'bg-surface2 text-ink-dim' }
        : {}),
    },
    // System registry (phase 4): the /api/system endpoints ship with the
    // daemon, so the item is always present (empty states live on the page).
    { to: '/system', icon: '⚙', label: 'System' },
    ...(hasDocs ? [DOCS_NAV] : []),
  ];

  return (
    <div className="flex h-dvh flex-col">
      <header className="z-20 flex h-12 shrink-0 items-center gap-2.5 border-b border-line bg-bg px-4 desk:px-5">
        <span className="font-display text-[17px] leading-none font-bold tracking-[0.06em]">
          SW<em className="text-brand not-italic">◆</em>RMERY
        </span>
        <span className="ml-auto flex items-center gap-1.5 font-mono text-[11px] text-ink-dim">
          {MOCK ? (
            <>
              <span className="inline-block h-[7px] w-[7px] rounded-full bg-amber" />
              {`mock data · :${portLabel()}`}
            </>
          ) : (
            <>
              <span className="inline-block h-[7px] w-[7px] animate-pulse-dot rounded-full bg-green" />
              {`control plane · :${portLabel()}`}
            </>
          )}
        </span>
      </header>

      <div className="flex min-h-0 flex-1">
        <nav className="fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-line bg-bg/95 px-1 pt-2 pb-[calc(8px+env(safe-area-inset-bottom))] backdrop-blur-md desk:static desk:inset-auto desk:z-auto desk:w-[210px] desk:shrink-0 desk:flex-col desk:justify-start desk:gap-1 desk:overflow-y-auto desk:border-t-0 desk:border-r desk:bg-bg desk:px-3 desk:py-4 desk:backdrop-blur-none">
          {items.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) =>
                `flex flex-col items-center gap-[3px] rounded-lg px-2.5 py-1 text-[10.5px] transition-colors desk:w-full desk:flex-row desk:justify-start desk:gap-2.5 desk:px-3 desk:py-2 desk:text-[13px] ${
                  isActive ? 'font-medium text-brand desk:bg-surface2' : 'text-ink-dim hover:text-ink'
                }`
              }
            >
              <span className="relative text-[17px] leading-none" aria-hidden="true">
                {item.icon}
                {item.alert === true && (
                  <span className="absolute -top-0.5 -right-1.5 h-[6px] w-[6px] rounded-full bg-amber desk:hidden" />
                )}
              </span>
              {item.label}
              {item.badge !== undefined && (
                <span
                  className={`ml-auto hidden min-w-[18px] rounded-full px-1.5 py-px text-center font-mono text-[10px] leading-[14px] desk:block ${item.badgeClass ?? 'bg-surface2 text-ink-dim'}`}
                >
                  {item.badge}
                </span>
              )}
            </NavLink>
          ))}
          <span className="hidden desk:contents">
            <HealthFooter />
          </span>
        </nav>

        <main className="min-w-0 flex-1 overflow-y-auto [-webkit-overflow-scrolling:touch]">
          <div className="mx-auto h-full max-w-[1360px] p-4 pb-[88px] desk:px-7 desk:py-6">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
