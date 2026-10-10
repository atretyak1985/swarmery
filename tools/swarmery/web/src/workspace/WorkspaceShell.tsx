// Workspace shell (fusion phase 4): the top-level chrome for project mode — a
// slim header (wordmark linking back to the fleet, a theme toggle, daemon
// health) above the ProjectWorkspaceLayout. It is a SIBLING of the fleet <App/>
// (its own header + the same Sidebar scoped to the project + status bar), not
// nested inside it, so project mode is a distinct full-screen surface rather
// than a page within the fleet frame. Shared providers (ScopeProvider) live one
// level up in RootProviders so both surfaces read the same project store.
//
// The way back to the fleet is the sidebar's project switcher ("All projects")
// or the wordmark — the Sessions/Projects mode toggle was retired (Canvas v3).

import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import { MOCK } from '../api';
import { AccountReadyBanner } from '../components/AccountReadyBanner';
import { LanguageToggle } from '../components/LanguageToggle';
import { ThemeToggle } from '../components/ThemeToggle';
import { UsageChip } from '../components/usage/UsageChip';
import { useHealth, versionLabel, versionTitle } from '../lib/health';
import { PluginDriftBadge } from '../components/PluginDriftBadge';
import { ProjectWorkspaceLayout } from './ProjectWorkspaceLayout';
import { Wordmark } from '../components/Wordmark';

export function WorkspaceShell(): JSX.Element {
  const { health, unreachable } = useHealth();
  const { t } = useLingui();
  const daemonOk = !unreachable;
  return (
    // overflow-hidden: a shell that IS the viewport must never scroll the
    // document — the scroller lives inside (ProjectWorkspaceLayout's
    // [data-shell-scroller], or the page itself on fill routes). Without it a
    // sub-pixel row height (the mobile tab strip lands on a .5 boundary) rounds
    // up and the whole app gains a 1px document scroll.
    <div className="flex h-dvh flex-col overflow-hidden">
      <header className="header-hairline relative z-20 flex h-14 shrink-0 items-center gap-4 bg-bg px-4 desk:px-6">
        {/* Same fixed-width wordmark block as the fleet header (desk:w-[172px])
            so the header chrome sits at the identical x-position in both
            shells — no shift when switching scope. */}
        <Link
          to="/"
          aria-label={t`back to all projects`}
          className="flex min-w-0 items-center font-sans text-[16px] leading-none font-extrabold tracking-[0.09em] text-ink uppercase transition-opacity hover:opacity-80 desk:w-[172px] desk:shrink-0"
        >
          <Wordmark />
        </Link>
        <span className="ml-auto flex items-center gap-3">
          <LanguageToggle />
          <ThemeToggle />
          <UsageChip />
          <span className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
            {MOCK ? (
              <>
                <span className="inline-block h-[7px] w-[7px] rounded-full bg-amber" />
                <Trans>mock data</Trans>
              </>
            ) : (
              <>
                <span
                  className={`inline-block h-[7px] w-[7px] rounded-full ${daemonOk ? 'animate-pulse-dot bg-green' : 'bg-red'}`}
                />
                {daemonOk ? t`daemon healthy` : t`daemon unreachable`}
                {health !== null && <span title={versionTitle(health)}>· {versionLabel(health)}</span>}
                <PluginDriftBadge health={health} />
              </>
            )}
          </span>
        </span>
      </header>
      {/* Same component, same placement as the fleet shell (App.tsx), so a
          route change between shells never loses the readiness signal. */}
      <AccountReadyBanner />
      <ProjectWorkspaceLayout />
    </div>
  );
}
