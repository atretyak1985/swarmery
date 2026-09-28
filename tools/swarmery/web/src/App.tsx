// App shell: a full-width top header (SW◆RMERY wordmark at left, and a live
// daemon status at right) with a bottom border. Below it, the one Sidebar
// (components/Sidebar.tsx — Canvas v3: project switcher on top, nine places
// from lib/nav.ts, ⌘K at the bottom) in its All-projects state; <main> owns the
// scroll. Mobile drops the sidebar for a flat fixed bottom nav fed by the same
// nav model.
//
// The Inbox row carries the only nav badge — pending approvals (REST resync +
// WS permission_requested/permission_resolved over the shared connection) plus
// proposed advisor recommendations — and Sessions a green dot while any session
// is live (useSidebarSignals).

import { useState } from 'react';
import { Link, Outlet } from 'react-router-dom';
import { MOCK } from './api';
import { AccountReadyBanner } from './components/AccountReadyBanner';
import { NewProjectButton } from './components/NewProjectButton';
import { ProjectApprovalsSection } from './components/ProjectApprovalsSection';
import { MobileNav, Sidebar, useSidebarSignals } from './components/Sidebar';
import { ThemeToggle } from './components/ThemeToggle';
import { UsageChip } from './components/usage/UsageChip';
import { useFillRoute } from './lib/fillRoute';
import { useHealth, versionLabel, versionTitle } from './lib/health';
import { PluginDriftBadge } from './components/PluginDriftBadge';
import { loadPrefs, useBrowserNotifications, type NotifyPrefs } from './lib/notifications';
import { NotifyPrefsContext } from './lib/notifyPrefsContext';
import { useScope } from './lib/scope';

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
  // Browser notifications (control-plane v2): prefs from localStorage, the
  // hook rides the same shared WS connection as the sidebar signals below.
  const [notifyPrefs, setNotifyPrefs] = useState<NotifyPrefs>(loadPrefs);
  useBrowserNotifications(notifyPrefs);
  const { health, unreachable } = useHealth();
  const { scope, scopeProject } = useScope();
  // Does the active route own its vertical scroll? Declared on the route itself
  // (main.tsx `handle: { fill: true }`), never matched on the pathname here.
  const fill = useFillRoute();
  const { pendingCount, inboxCount, liveSessions } = useSidebarSignals();

  const daemonOk = !unreachable;

  return (
    <NotifyPrefsContext.Provider value={{ prefs: notifyPrefs, setPrefs: setNotifyPrefs }}>
    {/* overflow-hidden: this shell IS the viewport, so the document itself must
        never scroll — the scroller is <main> below (or the page, on fill routes).
        Same guard as workspace/WorkspaceShell.tsx. */}
    <div className="app-shell flex h-dvh flex-col overflow-hidden">
      {/* Full-width top header: wordmark, status. */}
      <header className="header-hairline relative z-20 flex h-14 shrink-0 items-center gap-4 bg-bg px-4 desk:px-6">
        {/* Fixed-width block on desktop (24px pad + 172px + 16px gap = 212px) so
            the header's content edge lines up with the sidebar edge — identical
            to the project shell's header, so nothing shifts between shells. */}
        <Link
          to="/"
          aria-label="swarmery home"
          className="flex min-w-0 items-center font-sans text-[16px] leading-none font-extrabold tracking-[0.09em] text-ink uppercase transition-opacity hover:opacity-80 desk:w-[172px] desk:shrink-0"
        >
          SW<span className="text-brand">◆</span>RMERY
        </Link>
        {/* The project switcher at the sidebar top is the scope control (it
            replaced the Sessions/Projects mode toggle). The page-search box
            lives in each searchable page's body (PageSearchInput); ⌘K is owned
            by the Sidebar; theme + notifications live on /settings. */}
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
        {/* Desktop sidebar (212px, desk and up) in its All-projects state. */}
        <Sidebar slug={null} inboxCount={inboxCount} liveSessions={liveSessions}>
          {scope !== null && (
            <ProjectApprovalsSection
              scope={scope}
              scopeSlug={scopeProject?.slug ?? scope}
              totalPending={pendingCount}
            />
          )}
        </Sidebar>

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
      <MobileNav slug={null} variant="bottom" inboxCount={inboxCount} />
    </div>
    </NotifyPrefsContext.Provider>
  );
}
