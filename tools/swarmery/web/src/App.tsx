// App shell: sticky app bar, bottom nav (mobile) / sidebar (≥900px), routed
// screens in <Outlet>. Bloombum editorial language: cream paper, hairlines,
// Fraunces wordmark with the burgundy diamond.

import { NavLink, Outlet } from 'react-router-dom';
import { MOCK } from './api';

const NAV_ITEMS = [
  { to: '/', icon: '◉', label: 'Overview' },
  { to: '/sessions', icon: '☰', label: 'Sessions' },
] as const;

export function App(): JSX.Element {
  return (
    <div className="min-h-dvh pb-[76px] desk:pb-0 desk:pl-[210px]">
      <header className="sticky top-0 z-20 flex items-center gap-2.5 border-b border-line bg-bg/90 px-4 py-3 backdrop-blur-md">
        <span className="font-display text-[19px] leading-none font-semibold tracking-[-0.02em] lowercase">
          sw<em className="text-brand not-italic">◆</em>rmery
        </span>
        <span className="ml-auto flex items-center gap-1.5 font-mono text-[11px] text-ink-dim">
          {MOCK ? (
            <>
              <span className="inline-block h-[7px] w-[7px] rounded-full bg-amber" />
              mock data
            </>
          ) : (
            <>
              <span className="inline-block h-[7px] w-[7px] animate-pulse-dot rounded-full bg-green" />
              control plane
            </>
          )}
        </span>
      </header>

      <nav className="fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-line bg-bg/95 px-1 pt-2 pb-[calc(8px+env(safe-area-inset-bottom))] backdrop-blur-md desk:top-[53px] desk:right-auto desk:bottom-0 desk:left-0 desk:w-[210px] desk:flex-col desk:justify-start desk:gap-1 desk:border-t-0 desk:border-r desk:px-3 desk:py-4">
        {NAV_ITEMS.map((item) => (
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
            <span className="text-[17px] leading-none" aria-hidden="true">
              {item.icon}
            </span>
            {item.label}
          </NavLink>
        ))}
      </nav>

      <main className="mx-auto max-w-[820px] p-4 desk:px-7 desk:py-6">
        <Outlet />
      </main>
    </div>
  );
}
