// App shell: a full-width top header (SW◆RMERY wordmark at left, a mono
// scope filter + contextual search/filters, and a live daemon status at
// right) with a bottom border. Below it, a static labelled sidebar (248px,
// desktop only) carries the glyph nav items grouped into labelled sections
// (work / insights / tools / system) and a live daemon-health footer; <main>
// owns the scroll. Mobile drops the sidebar for a flat fixed bottom nav.
//
// The Docs nav item appears only when /api/docs has entries; the Sessions item
// carries a today-count badge (/api/stats/overview); the Approvals item carries
// a LIVE amber pending-count badge (REST resync + WS permission_requested/
// permission_resolved over the shared connection).

import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';
import type { WSMessage } from './api/types';
import {
  fetchApprovals,
  fetchDocs,
  fetchRecommendations,
  fetchStatsOverview,
  MOCK,
} from './api';
import { fetchSystemSummary } from './api/system';
import { AccountReadyBanner } from './components/AccountReadyBanner';
import { CommandPalette } from './components/CommandPalette';
import { ModeToggle } from './components/ModeToggle';
import { NewProjectButton } from './components/NewProjectButton';
import { ProjectApprovalsSection } from './components/ProjectApprovalsSection';
import { ThemeToggle } from './components/ThemeToggle';
import { UsageChip } from './components/usage/UsageChip';
import { useFillRoute } from './lib/fillRoute';
import { isoDay } from './lib/format';
import { useHealth, versionLabel, versionTitle } from './lib/health';
import { PluginDriftBadge } from './components/PluginDriftBadge';
import { loadPrefs, useBrowserNotifications, type NotifyPrefs } from './lib/notifications';
import { NotifyPrefsContext } from './lib/notifyPrefsContext';
import { useScope } from './lib/scope';
import { useLiveUpdates } from './lib/ws';

interface NavItem {
  to: string;
  glyph: string;
  label: string;
  /** Count badge (approvals pending, sessions today). */
  badge?: string;
  /** Amber attention styling on the badge (pending approvals). */
  alert?: boolean;
}

/** Desktop sidebar group — `label: null` renders items with no header
 * (Command deck). A section whose items are all hidden renders nothing. */
interface NavSection {
  label: string | null;
  items: NavItem[];
}

const DOCS_NAV: NavItem = { to: '/docs', glyph: '❐', label: 'Docs' };

/* The global project-scope dropdown no longer lives in this rail. It was a
 * shell-level control for a page-level filter: it sat above the nav on every
 * screen, including ones it could not filter, and on Sessions it had to be
 * hidden outright. The scope control now renders inside the filter row of the
 * page that uses it (pages/Sessions.tsx → ScopeChip), driving the same
 * useScope() context. */

export function App(): JSX.Element {
  // ScopeProvider + PageSearchProvider now live one level up (RootProviders in
  // main.tsx) so the fleet App and the project-workspace shell share one project
  // store and one page-search context (each searchable page renders its own
  // PageSearchInput now — the header no longer carries the search box).
  return <AppShell />;
}

function AppShell(): JSX.Element {
  const [hasDocs, setHasDocs] = useState(false);
  const [sessionsToday, setSessionsToday] = useState<number | null>(null);
  // Pending approvals as a SET of ids: WS +/- stays idempotent when the same
  // permission_resolved arrives twice (own action + fan-out) or after resync.
  const [pendingIds, setPendingIds] = useState<ReadonlySet<number>>(new Set());
  const [paletteOpen, setPaletteOpen] = useState(false);
  // Browser notifications (control-plane v2): prefs from localStorage, the
  // hook rides the same shared WS connection as the badge below.
  const [notifyPrefs, setNotifyPrefs] = useState<NotifyPrefs>(loadPrefs);
  useBrowserNotifications(notifyPrefs);
  const { health, unreachable } = useHealth();
  const { scope, scopeProject } = useScope();
  // Does the active route own its vertical scroll? Declared on the route itself
  // (main.tsx `handle: { fill: true }`), never matched on the pathname here.
  const fill = useFillRoute();

  // Tool dashboards (Serena / Graphify / Architecture) are project-scoped and
  // live in the project-mode sidebar — the session sidebar no longer carries a
  // global Tools section, so no /api/tools polling is needed here.
  useEffect(() => {
    fetchDocs()
      .then((docs) => setHasDocs(docs.length > 0))
      .catch(() => setHasDocs(false)); // empty/unreachable → hide the Docs item
  }, []);

  // Global Cmd+K / Ctrl+K → command palette. Window-level so it works from
  // any focused element; preventDefault stops the browser's own search-bar
  // focus shortcut.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent): void => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setPaletteOpen((prev) => !prev);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  useEffect(() => {
    // Sessions nav badge: one-shot fetch of today's overview so the count
    // works on every screen (hidden when unavailable).
    fetchStatsOverview(isoDay())
      .then((o) => setSessionsToday(o.sessions))
      .catch(() => setSessionsToday(null));
  }, []);

  // Retro nav badge: count of proposed advisor recommendations — one-shot
  // fetch on mount, same pattern as the sessions badge (hidden when the
  // endpoint is unavailable), plus a refetch whenever navigation crosses the
  // /retro boundary so Accept/Dismiss/Analyze on the page refresh the badge
  // on the way out (and a stale count refreshes on the way in).
  const [proposedRecs, setProposedRecs] = useState<number | null>(null);
  const syncProposed = useCallback((): void => {
    fetchRecommendations('proposed')
      .then((r) => setProposedRecs(r.recommendations.length))
      .catch(() => setProposedRecs(null));
  }, []);
  useEffect(syncProposed, [syncProposed]); // one-shot mount fetch
  const { pathname } = useLocation();
  const onRetro = pathname.startsWith('/retro');
  const prevOnRetro = useRef(onRetro);
  useEffect(() => {
    if (prevOnRetro.current === onRetro) return;
    prevOnRetro.current = onRetro;
    syncProposed();
  }, [onRetro, syncProposed]);

  // System nav badge: promotion + stale-override insight count, fetched on
  // mount and refetched on WS system_item_updated (hidden when the summary is
  // unavailable) — pattern: sessions badge + approvals resync.
  const [insightCount, setInsightCount] = useState<number | null>(null);
  const syncInsights = useCallback((): void => {
    fetchSystemSummary()
      .then((s) => setInsightCount(s.insights.promotions + s.insights.staleOverrides))
      .catch(() => setInsightCount(null));
  }, []);
  useEffect(syncInsights, [syncInsights]);

  // Approvals badge: REST is the source of truth (mount + reconnect resync);
  // the WS stream is the low-latency hint in between (docs/ws-protocol.md).
  const syncPending = useCallback((): void => {
    fetchApprovals('pending')
      .then((list) => setPendingIds(new Set(list.map((r) => r.id))))
      .catch(() => setPendingIds(new Set())); // approvals API absent → no badge
  }, []);
  useEffect(syncPending, [syncPending]);

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'permission_requested') {
        setPendingIds((prev) => new Set(prev).add(msg.payload.id));
      } else if (msg.type === 'permission_resolved') {
        setPendingIds((prev) => {
          if (!prev.has(msg.payload.id)) return prev;
          const next = new Set(prev);
          next.delete(msg.payload.id);
          return next;
        });
      } else if (msg.type === 'system_item_updated') {
        // Registry change → System nav badge resync. The message is rare
        // (scanner/edit events), so no debounce is needed.
        syncInsights();
      }
      // Other message types are the pages' concern — ignore here.
    },
    [syncInsights],
  );
  // Reconnect / 60s reconcile: resync BOTH WS-driven badges — pending
  // approvals and the System insights count — since either may have drifted
  // while the socket was down.
  const resyncBadges = useCallback((): void => {
    syncPending();
    syncInsights();
  }, [syncPending, syncInsights]);
  useLiveUpdates(onMessage, resyncBadges);

  const pendingCount = pendingIds.size;
  // Session-mode sidebar. A scope switcher sits at the top of the rail (mirrors
  // project mode); the Projects item opens the fleet project list. Tool
  // dashboards are project-scoped (project-mode sidebar). System / Docs /
  // Settings are pinned to the bottom of the rail (mirrors the project-mode
  // System / Settings footer), rendered separately below.
  const sections: NavSection[] = [
    { label: null, items: [{ to: '/', glyph: '◉', label: 'Overview' }] },
    {
      label: 'Work',
      items: [
        { to: '/sessions', glyph: '❯', label: 'Sessions', ...badgeFor(sessionsToday) },
        { to: '/projects', glyph: '▤', label: 'Projects' },
        {
          to: '/approvals',
          glyph: '⧗',
          label: 'Approvals',
          ...(pendingCount > 0 ? { badge: String(pendingCount), alert: true } : {}),
        },
        { to: '/routines', glyph: '⟳', label: 'Routines' },
      ],
    },
    {
      label: 'Insights',
      items: [
        { to: '/analytics', glyph: '▦', label: 'Analytics' },
        { to: '/retro', glyph: '↺', label: 'Retro', ...badgeFor(proposedRecs) },
        { to: '/decisions', glyph: '◇', label: 'Decisions' },
        { to: '/lessons', glyph: '✎', label: 'Lessons' },
      ],
    },
  ];
  // Bottom-pinned cluster (session mode): the System destination (tabbed shell,
  // carrying the promotion/drift/lint inbox badge), Docs when present, and the
  // global Settings page. Mirrors the project-mode System/Settings footer and
  // is appended to the flat mobile nav so everything stays reachable.
  const bottomItems: NavItem[] = [
    { to: '/system', glyph: '☷', label: 'System', ...badgeFor(insightCount) },
    ...(hasDocs ? [DOCS_NAV] : []),
    { to: '/settings', glyph: '⚙', label: 'Settings' },
  ];
  // Mobile bottom nav stays flat — sections flattened in order, then the
  // bottom-pinned cluster, no labels.
  const items: NavItem[] = [...sections.flatMap((s) => s.items), ...bottomItems];

  // Shared NavLink className for a desktop sidebar row (section items + the
  // pinned Settings link) — extracted so the long class string is not duplicated.
  const navItemClass = ({ isActive }: { isActive: boolean }): string =>
    `flex h-[38px] items-center gap-3 rounded-[10px] border px-3 transition-colors ${
      isActive
        ? 'border-line-strong bg-surface2 text-brand'
        : 'border-transparent text-ink-dim hover:bg-surface2/50 hover:text-ink'
    }`;

  const daemonOk = !unreachable;

  return (
    <NotifyPrefsContext.Provider value={{ prefs: notifyPrefs, setPrefs: setNotifyPrefs }}>
    {/* overflow-hidden: this shell IS the viewport, so the document itself must
        never scroll — the scroller is <main> below (or the page, on fill routes).
        Same guard as workspace/WorkspaceShell.tsx. */}
    <div className="app-shell flex h-dvh flex-col overflow-hidden">
      {/* Full-width top header: wordmark, scope filter, search/filters, status. */}
      <header className="header-hairline relative z-20 flex h-14 shrink-0 items-center gap-4 bg-bg px-4 desk:px-6">
        {/* Fixed-width block on desktop (24px pad + 208px + 16px gap = 248px) so
            the ModeToggle lines up with the sidebar edge — identical to the
            project shell's header, so nothing shifts when toggling modes. */}
        <Link
          to="/"
          aria-label="swarmery home"
          className="flex min-w-0 items-center font-sans text-[16px] leading-none font-extrabold tracking-[0.09em] text-ink uppercase transition-opacity hover:opacity-80 desk:w-[208px] desk:shrink-0"
        >
          SW<span className="text-brand">◆</span>RMERY
        </Link>
        {/* Mode toggle, switching between the fleet (session) shell and the
            project shell. The project scope switcher moved to the sidebar top
            (mirrors project mode), so header chrome stays identical across
            modes — no shift when toggling. The page-search box now lives in each
            searchable page's body (PageSearchInput), not the header. Cmd+K still
            opens the global search palette; theme + notifications live on
            /settings. */}
        <ModeToggle />
        <span className="ml-auto flex items-center gap-3">
        <ThemeToggle />
        <UsageChip />
        {!MOCK && <NewProjectButton />}
        <span
          className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim"
        >
          {MOCK ? (
            <>
              <span className="inline-block h-[7px] w-[7px] rounded-full bg-amber" />
              mock data
            </>
          ) : (
            <>
              <span
                className={`inline-block h-[7px] w-[7px] rounded-full ${daemonOk ? 'animate-pulse-dot bg-green' : 'bg-red'}`}
              />
              {daemonOk ? 'daemon healthy' : 'daemon unreachable'}
              {health !== null && <span title={versionTitle(health)}>· {versionLabel(health)}</span>}
              <PluginDriftBadge health={health} />
            </>
          )}
        </span>
        </span>
      </header>

      {/* Readiness signal for the scoped project's effective account — renders
          nothing (no wrapper at all) when unscoped or the account is fine, so
          the header rhythm is unchanged in the common case. */}
      <AccountReadyBanner />

      <div className="flex min-h-0 flex-1">
        {/* Desktop sidebar — static labelled panel (248px), no collapse.
            Settings is pinned to the bottom via mt-auto. */}
        <nav className="hidden w-[248px] shrink-0 flex-col border-r border-line px-3 py-4 desk:flex">
          {sections
            .filter((section) => section.items.length > 0)
            .map((section) => (
              <div key={section.label ?? 'top'} className="flex flex-col gap-0.5">
                {section.label !== null && (
                  /* Sidebar group eyebrow — SectionTitle's mono uppercase idiom
                   * scaled down for the rail (10px, faint, tighter rhythm). */
                  <div className="mt-4 mb-1 px-3 font-mono text-[10px] font-medium tracking-[0.14em] text-ink-faint uppercase">
                    {section.label}
                  </div>
                )}
                {section.items.map((item) => (
                  <NavLink key={item.to} to={item.to} end={item.to === '/'} className={navItemClass}>
                    <span
                      className="w-[16px] shrink-0 text-center text-[16px] leading-none"
                      aria-hidden="true"
                    >
                      {item.glyph}
                    </span>
                    <span className="truncate text-[13.5px] font-medium">{item.label}</span>
                    {item.badge !== undefined && (
                      <span
                        className={`ml-auto flex h-[18px] min-w-[18px] items-center justify-center rounded-full px-[5px] font-mono text-[10px] font-bold ${
                          item.alert === true ? 'bg-amber text-bg' : 'bg-line-strong text-ink-dim'
                        }`}
                      >
                        {item.badge}
                      </span>
                    )}
                  </NavLink>
                ))}
              </div>
            ))}
          {scope !== null && (
            <ProjectApprovalsSection
              scope={scope}
              scopeSlug={scopeProject?.slug ?? scope}
              totalPending={pendingCount}
            />
          )}
          {/* System / Docs / Settings, pinned to the bottom of the rail. */}
          <div className="mt-auto flex flex-col gap-0.5 pt-3">
            {bottomItems.map((item) => (
              <NavLink key={item.to} to={item.to} className={navItemClass}>
                <span
                  className="w-[16px] shrink-0 text-center text-[16px] leading-none"
                  aria-hidden="true"
                >
                  {item.glyph}
                </span>
                <span className="truncate text-[13.5px] font-medium">{item.label}</span>
                {item.badge !== undefined && (
                  <span
                    className={`ml-auto flex h-[18px] min-w-[18px] items-center justify-center rounded-full px-[5px] font-mono text-[10px] font-bold ${
                      item.alert === true ? 'bg-amber text-bg' : 'bg-line-strong text-ink-dim'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </NavLink>
            ))}
          </div>
        </nav>

        {/* Fill routes (lib/fillRoute.ts) own their own scroll: this container
            hands it over — overflow-hidden plus the flex/min-h-0 chain the page
            needs to size itself against the leftover height. Every other route
            keeps the byte-identical scroller it has always had. The mobile
            bottom-nav inset (pb-[72px]) applies in both modes.
            [-webkit-overflow-scrolling:touch] belongs to whatever scrolls, so it
            drops here and moves onto the page's own pane in the page phases. */}
        <main
          // Marks the container whose overflow the fill flag flips, in BOTH
          // modes. The two shells hand off scroll at different depths (here it is
          // <main>, in the workspace shell it is a div inside it), so the
          // screenshot harness needs one selector that finds the right node in
          // either — see assertShellScroll in scripts/screenshot.mjs.
          data-shell-scroller=""
          className={
            fill
              ? 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden pb-[72px] desk:pb-0'
              : 'min-w-0 flex-1 overflow-y-auto pb-[72px] [-webkit-overflow-scrolling:touch] desk:pb-0'
          }
        >
          <Outlet />
        </main>
      </div>

      {/* Mobile bottom nav */}
      <nav className="fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-line bg-bg/95 px-1 pt-2 pb-[calc(8px+env(safe-area-inset-bottom))] backdrop-blur-md desk:hidden">
        {items.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === '/'}
            className={({ isActive }) =>
              `flex flex-col items-center gap-[3px] rounded-lg px-2.5 py-1 text-[10.5px] transition-colors ${
                isActive ? 'font-medium text-brand' : 'text-ink-faint hover:text-ink'
              }`
            }
          >
            <span className="relative text-[17px] leading-none" aria-hidden="true">
              {item.glyph}
              {item.alert === true && (
                <span className="absolute -top-0.5 -right-1.5 h-[6px] w-[6px] rounded-full bg-amber" />
              )}
            </span>
            {item.label}
          </NavLink>
        ))}
      </nav>

      {paletteOpen && <CommandPalette onClose={() => setPaletteOpen(false)} />}
    </div>
    </NotifyPrefsContext.Provider>
  );
}

/** Neutral (non-alert) count badge when a positive number is available. */
function badgeFor(n: number | null): { badge?: string } {
  return n !== null && n > 0 ? { badge: String(n) } : {};
}
