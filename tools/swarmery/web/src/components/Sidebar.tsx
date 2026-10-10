// The one sidebar (Canvas v3, artboard 2b — its geometry, 212px rail and 34px
// rows, is taken from the artboard), rendered by BOTH shells: the fleet
// shell (App.tsx, scope = All projects) and the project shell
// (workspace/ProjectWorkspaceLayout.tsx, scope = the :slug). It replaces the two
// per-shell rails and the Sessions/Projects mode toggle: the project switcher on
// top IS the scope control now.
//
// Rows come from lib/nav.ts — ten places in three groups (main, Improve, the
// bottom cluster). Project-only places (Plans, Knowledge) stay visible under All
// projects, dimmed, and resolve through the last-visited project so muscle
// memory never breaks. Two numeric badges — Inbox (every waiting decision) and
// Needs you (every blocked session) — and one live dot (Sessions); every other
// nav badge was retired with the old rails.
//
// The sidebar also owns the global ⌘K / Ctrl+K listener and the palette it
// opens, so the shortcut works the same in both shells. The palette renders
// OUTSIDE the <nav>: the rail is display:none below `desk`, and a fixed overlay
// inside a hidden parent would never paint.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { fetchSessions } from '../api';
import type { WSMessage } from '../api/types';
import { ProjectSwitcher } from '../workspace/ProjectSwitcher';
import { loadLastProject } from '../lib/lastProject';
import { PLACES, placesIn, resolvePlaceHref, type Place } from '../lib/nav';
import { useScope } from '../lib/scope';
import { useInboxItems } from '../pages/inbox/useInboxItems';
import { useLiveUpdates } from '../lib/ws';
import { useNeedsYou } from '../lib/useNeedsYou';
import { CommandPalette } from './CommandPalette';

export interface SidebarSignals {
  /** The Inbox badge: every waiting decision, all six sources (useInboxItems). */
  inboxCount: number;
  /** The Needs you badge: undismissed session blockers (useNeedsYou). */
  needsYouCount: number;
  /** At least one session is running or waiting on the operator. */
  liveSessions: boolean;
}

/**
 * The sidebar's live signals. REST is the source of truth (mount + reconnect
 * resync); the shared WS stream is the low-latency hint in between
 * (docs/ws-protocol.md).
 *
 * The Inbox badge is the Inbox's own count — the same six-source aggregate the
 * page renders (pages/inbox/useInboxItems.ts), for `scope` (null = fleet). That
 * hook already refetches on permission_* frames, so approvals need no set here.
 */
export function useSidebarSignals(scope: string | null = null): SidebarSignals {
  const [live, setLive] = useState(false);
  const inbox = useInboxItems(scope);
  const needsYou = useNeedsYou(scope);
  const reloadInbox = inbox.reload;

  const syncLive = useCallback((): void => {
    // One active session lights the dot, so a one-row page of active sessions
    // answers it (same filter as stats.go activeSessions: status 'active',
    // archived projects excluded, plus soft-hidden rows). NOT stats/overview:
    // its 14-day series runs ~60 queries and, on the daemon's single DB
    // connection, stalled every other page while it ran — and this fires on
    // every page load and 2 s after every session update. Its
    // `waiting_approval` was a constant 0, so nothing is lost.
    fetchSessions({ status: 'active' }, { limit: 1 })
      .then((r) => setLive(r.sessions.length > 0))
      .catch(() => setLive(false));
  }, []);
  useEffect(syncLive, [syncLive]);

  // Accept/Dismiss/Analyze on Retro change the waiting count: refetch whenever
  // navigation crosses a /retro boundary (fleet or project), as the old badge did.
  const { pathname } = useLocation();
  const onRetro = /\/retro(\/|$)/.test(pathname);
  const prevOnRetro = useRef(onRetro);
  useEffect(() => {
    if (prevOnRetro.current === onRetro) return;
    prevOnRetro.current = onRetro;
    reloadInbox();
  }, [onRetro, reloadInbox]);

  // Session lifecycle messages are frequent (every turn updates the row), so
  // the live-dot recount is debounced rather than run per message.
  const liveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (liveTimer.current !== null) clearTimeout(liveTimer.current);
    },
    [],
  );
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'session_started' || msg.type === 'session_updated') {
        if (liveTimer.current !== null) clearTimeout(liveTimer.current);
        liveTimer.current = setTimeout(syncLive, 2000);
      }
      // Other message types are the pages' concern — ignore here.
    },
    [syncLive],
  );
  // Reconnect / 60s reconcile: every WS-driven signal may have drifted.
  useLiveUpdates(onMessage, syncLive);

  return { inboxCount: inbox.count, needsYouCount: needsYou.count, liveSessions: live };
}

/** Global ⌘K / Ctrl+K → command palette. Window-level so it works from any
 * focused element; preventDefault stops the browser's own search shortcut. */
function usePaletteShortcut(toggle: () => void): void {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent): void => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        toggle();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [toggle]);
}

export function Sidebar({
  slug,
  subPath = '',
  inboxCount,
  needsYouCount = 0,
  liveSessions,
  children,
}: {
  /** The project scope (/p/:slug), or null for All projects (the fleet shell). */
  slug: string | null;
  /** Current project sub-tab to preserve across a switch (e.g. "/plans"). */
  subPath?: string;
  inboxCount: number;
  needsYouCount?: number;
  liveSessions: boolean;
  /** Shell-specific extras rendered above the bottom cluster. */
  children?: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  const { projects } = useScope();
  const { pathname } = useLocation();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const togglePalette = useCallback(() => setPaletteOpen((prev) => !prev), []);
  usePaletteShortcut(togglePalette);

  const row = (place: Place): JSX.Element => (
    <SidebarRow
      key={place.id}
      place={place}
      slug={slug}
      active={place.match(pathname)}
      badge={badgeFor(place, inboxCount, needsYouCount)}
      live={place.id === 'sessions' && liveSessions}
    />
  );

  return (
    <>
      {/* Geometry is artboard 2b's rail: 212px wide, 12px/10px padding, 2px
          row gap, 34px rows. */}
      <nav
        aria-label={t`primary`}
        className="hidden w-[212px] shrink-0 flex-col gap-[2px] border-r border-line px-[10px] py-3 desk:flex"
      >
        <div className="mb-[10px]">
          <ProjectSwitcher projects={projects} currentSlug={slug} subPath={slug === null ? '' : subPath} />
        </div>
        {placesIn('main').map(row)}
        {/* Section eyebrow — the mono micro-label idiom, rail-sized. */}
        <div className="mt-3 mb-[3px] px-[10px] font-mono text-[10px] leading-[normal] font-medium tracking-[0.14em] text-ink-faint uppercase">
          <Trans>Improve</Trans>
        </div>
        {placesIn('improve').map(row)}
        {children}
        <div className="mt-auto flex flex-col gap-[2px]">
          {placesIn('bottom').map(row)}
          <button
            type="button"
            onClick={togglePalette}
            aria-haspopup="dialog"
            className="mt-1.5 border-t border-line px-[10px] py-2 text-left font-mono text-[10px] leading-[normal] text-ink-faint transition-colors hover:text-ink-dim"
          >
            <Trans>⌘K search &amp; actions</Trans>
          </button>
        </div>
      </nav>
      {paletteOpen && <CommandPalette onClose={() => setPaletteOpen(false)} />}
    </>
  );
}

/** The numeric badge of a row: Inbox and Needs you only, hidden at zero. */
function badgeFor(place: Place, inboxCount: number, needsYouCount: number): number | null {
  if (place.id === 'inbox') return inboxCount > 0 ? inboxCount : null;
  if (place.id === 'needs-you') return needsYouCount > 0 ? needsYouCount : null;
  return null;
}

function SidebarRow({
  place,
  slug,
  active,
  badge,
  live,
}: {
  place: Place;
  slug: string | null;
  active: boolean;
  badge: number | null;
  live: boolean;
}): JSX.Element {
  const { t } = useLingui();
  const dimmed = slug === null && place.projectOnly;
  const label = place.label;
  return (
    <Link
      to={resolvePlaceHref(place, slug, dimmed ? loadLastProject() : null)}
      aria-current={active ? 'page' : undefined}
      title={dimmed ? t`${label} — opens in a project` : undefined}
      // Artboard 2b: an idle row is a borderless 34px line; the active row adds
      // a 1px hairline AROUND that box (36px outer), so its text sits 1px in.
      className={`flex shrink-0 items-center gap-[10px] rounded-[10px] px-[10px] text-[13px] font-medium transition-colors ${
        active
          ? 'h-[36px] border border-line-stronger bg-surface2 text-brand'
          : 'h-[34px] text-ink-3 hover:bg-surface2/50 hover:text-ink'
      } ${dimmed ? 'opacity-60' : ''}`}
    >
      <span className="w-[16px] shrink-0 text-center" aria-hidden="true">
        {place.glyph}
      </span>
      <span className="truncate">{label}</span>
      {badge !== null && (
        <span
          aria-label={plural(badge, { one: '# waiting', few: '# waiting', many: '# waiting', other: '# waiting' })}
          className="ml-auto h-[17px] min-w-[18px] rounded-full bg-amber px-[6px] text-center font-mono text-[10px] leading-[17px] font-bold text-bg"
        >
          {badge}
        </span>
      )}
      {live && (
        <span
          role="img"
          aria-label={t`live sessions`}
          className="ml-auto h-[6px] w-[6px] shrink-0 animate-pulse-dot rounded-full bg-green"
        />
      )}
    </Link>
  );
}

/**
 * The flat mobile nav (below `desk`), fed by the same nav.ts model: `bottom`
 * is the fleet shell's fixed bottom bar, `strip` the project shell's scrolling
 * tab strip at the top of its main column.
 */
export function MobileNav({
  slug,
  variant,
  inboxCount = 0,
  needsYouCount = 0,
}: {
  slug: string | null;
  variant: 'bottom' | 'strip';
  inboxCount?: number;
  needsYouCount?: number;
}): JSX.Element {
  const { t } = useLingui();
  const { pathname } = useLocation();
  const last = slug === null ? loadLastProject() : null;
  if (variant === 'strip') {
    return (
      <div className="flex gap-1 overflow-x-auto border-b border-line px-3 py-2 desk:hidden">
        {PLACES.map((place) => {
          const active = place.match(pathname);
          return (
            <Link
              key={place.id}
              to={resolvePlaceHref(place, slug, last)}
              aria-current={active ? 'page' : undefined}
              className={`flex shrink-0 items-center gap-1.5 rounded-lg border px-2.5 py-1 font-mono text-[11px] whitespace-nowrap transition-colors ${
                active ? 'border-line-strong bg-surface2 text-brand' : 'border-transparent text-ink-dim'
              }`}
            >
              <span aria-hidden="true">{place.glyph}</span>
              {place.label}
            </Link>
          );
        })}
      </div>
    );
  }
  return (
    <nav
      aria-label={t`primary (mobile)`}
      className="fixed inset-x-0 bottom-0 z-20 flex justify-around border-t border-line bg-bg/95 px-1 pt-2 pb-[calc(8px+env(safe-area-inset-bottom))] backdrop-blur-md desk:hidden"
    >
      {PLACES.map((place) => {
        const active = place.match(pathname);
        return (
          <Link
            key={place.id}
            to={resolvePlaceHref(place, slug, last)}
            aria-current={active ? 'page' : undefined}
            className={`flex flex-col items-center gap-[3px] rounded-lg px-1.5 py-1 text-[10.5px] transition-colors ${
              active ? 'font-medium text-brand' : 'text-ink-faint hover:text-ink'
            } ${slug === null && place.projectOnly ? 'opacity-60' : ''}`}
          >
            <span className="relative text-[17px] leading-none" aria-hidden="true">
              {place.glyph}
              {badgeFor(place, inboxCount, needsYouCount) !== null && (
                <span className="absolute -top-0.5 -right-1.5 h-[6px] w-[6px] rounded-full bg-amber" />
              )}
            </span>
            {place.label}
          </Link>
        );
      })}
    </nav>
  );
}
