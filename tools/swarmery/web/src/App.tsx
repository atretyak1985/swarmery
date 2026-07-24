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
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import type { WSMessage } from './api/types';
import {
  fetchApprovals,
  fetchDocs,
  fetchRecommendations,
  fetchStatsOverview,
  fetchTools,
  MOCK,
} from './api';
import { fetchSystemSummary } from './api/system';
import { CommandPalette } from './components/CommandPalette';
import { NewProjectButton } from './components/NewProjectButton';
import { NotifySettings } from './components/NotifySettings';
import { ProjectDropdown } from './components/ProjectDropdown';
import { isoDay } from './lib/format';
import { useHealth, shortVersion } from './lib/health';
import { loadPrefs, useBrowserNotifications, type NotifyPrefs } from './lib/notifications';
import {
  PageSearchProvider,
  pageSearchPlaceholder,
  usePageSearchControl,
} from './lib/pageSearch';
import { useScope } from './lib/scope';
import { useTheme } from './lib/theme';
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
const SERENA_NAV: NavItem = { to: '/serena', glyph: '◎', label: 'Serena' };
const GRAPHIFY_NAV: NavItem = { to: '/graphify', glyph: '⬡', label: 'Graphify' };
const ARCHITECTURE_NAV: NavItem = { to: '/architecture', glyph: '▦', label: 'Architecture' };

/** Global project scope switcher (header) — GitHub-org-switcher pattern.
 * Projects come from the ScopeProvider's shared fetch. */
function ScopeSwitcher(): JSX.Element {
  const { scope, setScope, projects } = useScope();
  return (
    <ProjectDropdown
      projects={projects}
      value={scope}
      onChange={setScope}
      allLabel="All projects"
      groupByTag
    />
  );
}

export function App(): JSX.Element {
  // ScopeProvider now lives one level up (RootProviders in main.tsx) so the
  // fleet App and the project-workspace shell share one project store; App only
  // adds the page-search context the fleet header needs.
  return (
    <PageSearchProvider>
      <AppShell />
    </PageSearchProvider>
  );
}

/** Light/dark toggle — a sun/moon pill matching the header control language. */
function ThemeToggle(): JSX.Element {
  const { theme, toggle } = useTheme();
  const isLight = theme === 'light';
  return (
    <button
      type="button"
      role="switch"
      aria-checked={isLight}
      aria-label={isLight ? 'switch to dark theme' : 'switch to light theme'}
      title={isLight ? 'switch to dark theme' : 'switch to light theme'}
      onClick={toggle}
      className="flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-lg border border-line bg-field text-[13px] leading-none text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
    >
      <span aria-hidden="true">{isLight ? '☾' : '☀'}</span>
    </button>
  );
}

/** Contextual header search — one input, filters the current page's list.
 * Hidden on pages with no searchable list (placeholder === null). */
function HeaderSearch(): JSX.Element | null {
  const { pathname } = useLocation();
  const { query, setQuery } = usePageSearchControl();
  const placeholder = pageSearchPlaceholder(pathname);
  if (placeholder === null) return null;
  return (
    <div className="relative hidden w-[220px] sm:block">
      <span
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-[13px] leading-none text-ink-faint"
      >
        ⌕
      </span>
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
        className="w-full rounded-[9px] border border-line-strong bg-field py-[6px] pr-8 pl-7 font-mono text-[12px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-ink-dim"
      />
      {query !== '' && (
        <button
          type="button"
          onClick={() => setQuery('')}
          aria-label="clear filter"
          className="absolute top-1/2 right-2 -translate-y-1/2 font-mono text-[13px] leading-none text-ink-dim transition-colors hover:text-ink"
        >
          ×
        </button>
      )}
    </div>
  );
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

  // Tool-dashboard nav items (serena / graphify): each is visible only while
  // /api/tools reports at least one project for that tool. Polled on the same
  // 60s cadence as daemon health (lib/health.ts) so items appear/disappear as
  // lsp-pack gets toggled or graphify builds land, without a reload.
  const [hasSerena, setHasSerena] = useState(false);
  const [hasGraphify, setHasGraphify] = useState(false);
  const [hasArchitecture, setHasArchitecture] = useState(false);
  useEffect(() => {
    let disposed = false;
    const poll = (): void => {
      fetchTools()
        .then((t) => {
          if (disposed) return;
          setHasSerena(t.serena.projects.length > 0);
          setHasGraphify(t.graphify.projects.length > 0);
          setHasArchitecture(t.architecture.projects.length > 0);
        })
        .catch(() => {
          // endpoint absent / daemon unreachable → hide all tool items
          if (disposed) return;
          setHasSerena(false);
          setHasGraphify(false);
          setHasArchitecture(false);
        });
    };
    poll();
    const timer = setInterval(poll, 60_000);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, []);
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
  const sections: NavSection[] = [
    { label: null, items: [{ to: '/', glyph: '◉', label: 'Command deck' }] },
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
      ],
    },
    {
      // Tool dashboards are conditional — when none is available the section
      // has no items and the label is skipped with it.
      label: 'Tools',
      items: [
        ...(hasSerena ? [SERENA_NAV] : []),
        ...(hasGraphify ? [GRAPHIFY_NAV] : []),
        ...(hasArchitecture ? [ARCHITECTURE_NAV] : []),
      ],
    },
    {
      label: 'System',
      items: [
        { to: '/system', glyph: '⚙', label: 'System', ...badgeFor(insightCount) },
        ...(hasDocs ? [DOCS_NAV] : []),
      ],
    },
  ];
  // Mobile bottom nav stays flat — sections flattened in order, no labels.
  const items: NavItem[] = sections.flatMap((s) => s.items);

  const daemonOk = !unreachable;

  return (
    <div className="app-shell flex h-dvh flex-col">
      {/* Full-width top header: wordmark, scope filter, search/filters, status. */}
      <header className="header-hairline relative z-20 flex h-14 shrink-0 items-center gap-4 bg-bg px-4 desk:px-6">
        {/* Fixed-width block on desktop: 24px header padding + 208px + 16px gap
            = 248px, so the scope switcher starts exactly where the sidebar ends. */}
        <span className="flex min-w-0 items-center desk:w-[208px] desk:shrink-0">
          <span className="font-sans text-[16px] leading-none font-extrabold tracking-[0.09em] text-ink uppercase">
            SW<span className="text-brand">◆</span>RMERY
          </span>
        </span>
        <ScopeSwitcher />
        {/* One contextual search input right after the scope filter — filters
            the current page's list. Section chips (status/scope/sort) live in
            the page body. Cmd+K still opens the global search palette. */}
        <HeaderSearch />
        <span className="ml-auto flex items-center gap-3">
        <ThemeToggle />
        {!MOCK && (
          <span className="flex items-center gap-2">
            <NotifySettings prefs={notifyPrefs} onChange={setNotifyPrefs} />
            <NewProjectButton />
          </span>
        )}
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
              {health !== null ? ` · ${shortVersion(health.version)}` : ''}
            </>
          )}
        </span>
        </span>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* Desktop sidebar — static labelled panel (248px), no collapse. */}
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
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.to === '/'}
                    className={({ isActive }) =>
                      `flex h-[38px] items-center gap-3 rounded-[10px] border px-3 transition-colors ${
                        isActive
                          ? 'border-line-strong bg-surface2 text-brand'
                          : 'border-transparent text-ink-dim hover:bg-surface2/50 hover:text-ink'
                      }`
                    }
                  >
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
        </nav>

        <main className="min-w-0 flex-1 overflow-y-auto pb-[72px] [-webkit-overflow-scrolling:touch] desk:pb-0">
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
  );
}

/** Neutral (non-alert) count badge when a positive number is available. */
function badgeFor(n: number | null): { badge?: string } {
  return n !== null && n > 0 ? { badge: String(n) } : {};
}
