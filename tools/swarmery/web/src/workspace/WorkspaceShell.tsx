// Workspace shell (fusion phase 4): the top-level chrome for project mode — a
// slim header (wordmark linking back to the fleet, a theme toggle, daemon
// health) above the ProjectWorkspaceLayout. It is a SIBLING of the fleet <App/>
// (its own header + rescoped sidebar + status bar), not nested inside it, so
// project mode is a distinct full-screen surface rather than a page within the
// fleet frame. Shared providers (ScopeProvider) live one level up in
// RootProviders so both surfaces read the same project store.

import { Link } from 'react-router-dom';
import { MOCK } from '../api';
import { UsagePopover } from '../components/UsagePopover';
import { useHealth, shortVersion } from '../lib/health';
import { useTheme } from '../lib/theme';
import { ProjectWorkspaceLayout } from './ProjectWorkspaceLayout';

function ThemeToggle(): JSX.Element {
  const { theme, toggle } = useTheme();
  const isLight = theme === 'light';
  return (
    <button
      type="button"
      role="switch"
      aria-checked={isLight}
      aria-label={isLight ? 'switch to dark theme' : 'switch to light theme'}
      onClick={toggle}
      className="flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-lg border border-line bg-field text-[13px] leading-none text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
    >
      <span aria-hidden="true">{isLight ? '☾' : '☀'}</span>
    </button>
  );
}

export function WorkspaceShell(): JSX.Element {
  const { health, unreachable } = useHealth();
  const daemonOk = !unreachable;
  return (
    <div className="flex h-dvh flex-col">
      <header className="header-hairline relative z-20 flex h-14 shrink-0 items-center gap-4 bg-bg px-4 desk:px-6">
        <Link
          to="/"
          aria-label="back to all projects"
          className="flex items-center gap-2 font-sans text-[16px] leading-none font-extrabold tracking-[0.09em] text-ink uppercase transition-opacity hover:opacity-80"
        >
          SW<span className="text-brand">◆</span>RMERY
        </Link>
        <Link
          to="/"
          className="font-mono text-[11px] text-ink-dim transition-colors hover:text-ink"
        >
          ← fleet
        </Link>
        <span className="ml-auto flex items-center gap-3">
          <ThemeToggle />
          <UsagePopover />
          <span className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
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
      <ProjectWorkspaceLayout />
    </div>
  );
}
