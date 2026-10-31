// The one sidebar (Swarmery v4 handoff §2, 196px), rendered by BOTH shells: the
// fleet shell (App.tsx, scope = All projects) and the project shell
// (workspace/ProjectWorkspaceLayout.tsx, scope = the :slug). The project switcher
// on top IS the scope control. On desktop there is no top header: the wordmark,
// the theme and language switches live on this rail's first row, and usage and
// daemon health in its footer (HealthFooter). Below `desk` the rail is hidden and
// each shell keeps its compact header, so all of that stays reachable.
//
// Rows come from lib/nav.ts — eleven places in three groups (main, Improve, the
// bottom cluster). Project-only places (Plans, Knowledge) read like any other row
// under All projects and resolve through the last-visited project, so muscle
// memory never breaks. Three badges: Inbox (decisions that block nothing), Needs
// you (everything blocked on the operator, sessions and phases) and Sessions (the
// sessions in that state). Sessions shows a live dot while it has no badge.
//
// The sidebar also owns the global keyboard: ⌘K / Ctrl+K and the palette it
// opens, 1–5, t and the panel keys [ ] \, so they work the same in both shells. The
// palette renders OUTSIDE the <nav>: the rail is display:none below `desk`, and a
// fixed overlay inside a hidden parent would never paint.
//
// Collapsed (lib/panels.ts) the rail is 52px of glyphs: every label, the language
// and theme switches, the Improve eyebrow and the footer go. The badge moves onto
// the glyph's corner (D-4): the design clips it there, and the Needs you count must
// never be lost.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { fetchSessions } from '../api';
import type { NeedsYouItem, NeedsYouKind, WSMessage } from '../api/types';
import { ProjectSwitcher } from '../workspace/ProjectSwitcher';
import { loadLastProject } from '../lib/lastProject';
import { PLACES, placesIn, resolvePlaceHref, type Place } from '../lib/nav';
import { PANEL_EASE, setListBadge, togglePanel, usePanels } from '../lib/panels';
import { useScope } from '../lib/scope';
import { useTheme } from '../lib/theme';
import { useGlobalKeys } from '../lib/useGlobalKeys';
import { useInboxItems } from '../pages/inbox/useInboxItems';
import { useLiveUpdates } from '../lib/ws';
import { useNeedsYou } from '../lib/useNeedsYou';
import { CommandPalette } from './CommandPalette';
import { HealthFooter } from './HealthFooter';
import { LanguageToggle } from './LanguageToggle';

export interface SidebarSignals {
  /** The Inbox badge: decisions that block nothing — approvals moved to Needs you. */
  inboxCount: number;
  /** The Needs you badge: everything blocked on the operator (useNeedsYou). */
  needsYouCount: number;
  /** The Sessions badge: distinct sessions waiting on the operator. */
  sessionsNeedCount: number;
  /** At least one session is running or waiting on the operator. */
  liveSessions: boolean;
}

/** The kinds that mean a live session is held up by the operator. A failure, a
 * phase item and a stalled phase are in the queue without being such a session. */
const SESSION_BLOCKERS: ReadonlySet<NeedsYouKind> = new Set([
  'approval',
  'question',
  'prod_deploy_local',
  'awaiting_reply',
]);

/** One session can hold several requests at once, so the count is by session. */
export function sessionsNeedingYou(items: readonly NeedsYouItem[]): number {
  return new Set(items.filter((i) => SESSION_BLOCKERS.has(i.kind)).map((i) => i.sessionId)).size;
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

  return {
    inboxCount: inbox.items.filter((item) => item.kind !== 'approval').length,
    needsYouCount: needsYou.count,
    sessionsNeedCount: sessionsNeedingYou(needsYou.items),
    liveSessions: live,
  };
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
  sessionsNeedCount = 0,
  liveSessions,
  children,
}: {
  /** The project scope (/p/:slug), or null for All projects (the fleet shell). */
  slug: string | null;
  /** Current project sub-tab to preserve across a switch (e.g. "/plans"). */
  subPath?: string;
  inboxCount: number;
  needsYouCount?: number;
  sessionsNeedCount?: number;
  liveSessions: boolean;
  /** Shell-specific extras rendered above the bottom cluster. */
  children?: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  const { projects } = useScope();
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const { theme, toggle: toggleTheme } = useTheme();
  const { nav: collapsed } = usePanels();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const togglePalette = useCallback(() => setPaletteOpen((prev) => !prev), []);
  usePaletteShortcut(togglePalette);
  useGlobalKeys({
    onPlace: (index) => {
      const place = placesIn('main')[index];
      if (place !== undefined) navigate(resolvePlaceHref(place, slug, slug === null ? loadLastProject() : null));
    },
    onTheme: toggleTheme,
    onPanelNav: () => togglePanel('nav'),
    onPanelList: () => togglePanel('list'),
    onPanelRail: () => togglePanel('rail'),
  });

  const activePlace = PLACES.find((place) => place.match(pathname));
  const activeBadge =
    activePlace === undefined ? null : badgeFor(activePlace, inboxCount, needsYouCount, sessionsNeedCount);
  useEffect(() => setListBadge(activeBadge), [activeBadge]);

  const row = (place: Place): JSX.Element => (
    <SidebarRow
      key={place.id}
      place={place}
      slug={slug}
      active={place.match(pathname)}
      badge={badgeFor(place, inboxCount, needsYouCount, sessionsNeedCount)}
      live={place.id === 'sessions' && liveSessions && sessionsNeedCount === 0}
      compact={collapsed}
    />
  );
  const navToggleLabel = collapsed ? t`expand sidebar · [` : t`collapse sidebar · [`;

  return (
    <>
      {/* The rail's first two rows stay outside the scroller: the project menu is
          wider than the rail and an overflow container would clip it. */}
      <nav
        aria-label={t`primary`}
        className={`hidden shrink-0 flex-col border-r border-line bg-surface px-[10px] py-3 transition-[width] desk:flex ${
          collapsed ? 'w-[52px]' : 'w-[196px]'
        } ${PANEL_EASE}`}
      >
        <div className={`flex items-center px-1.5 pt-0.5 ${collapsed ? 'justify-center' : 'justify-between'}`}>
          <Link
            to="/"
            aria-label={slug === null ? t`swarmery home` : t`back to all projects`}
            className="font-mono text-[13px] leading-[normal] font-bold tracking-[-0.02em] text-ink hover:text-brand"
          >
            {/* i18n-ignore — the product's name */}
            {collapsed ? 's' : 'swarmery'}
          </Link>
          <span className="flex items-center gap-0.5">
            {/* The toggle is 26px tall; the row stays the design's 22px. */}
            {!collapsed && (
              <span className="-my-0.5 mr-0.5">
                <LanguageToggle />
              </span>
            )}
            <button
              type="button"
              onClick={() => togglePanel('nav')}
              aria-label={navToggleLabel}
              data-tip={navToggleLabel}
              className="h-[22px] w-[22px] rounded-[5px] font-mono text-[11px] leading-none text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
            >
              {collapsed ? '▸' : '◂'}
            </button>
            {!collapsed && (
              <button
                type="button"
                onClick={toggleTheme}
                aria-label={t`theme`}
                data-tip={t`theme · t`}
                className="h-[22px] w-[22px] rounded-[5px] text-[12px] leading-none text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
              >
                {theme === 'dark' ? '☾' : '☀'}
              </button>
            )}
          </span>
        </div>
        <div className="mt-3">
          <ProjectSwitcher
            projects={projects}
            currentSlug={slug}
            subPath={slug === null ? '' : subPath}
            compact={collapsed}
          />
        </div>
        <div className="mt-4 flex min-h-0 flex-1 flex-col overflow-y-auto">
          <div className="flex flex-col gap-px">{placesIn('main').map(row)}</div>
          <div className="mt-[18px] flex flex-col gap-px">
            {/* Section eyebrow — the mono micro-label idiom, rail-sized. */}
            {!collapsed && (
              <div className="px-2 pb-1.5 font-mono text-[9.5px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase">
                <Trans>Improve</Trans>
              </div>
            )}
            {placesIn('improve').map(row)}
          </div>
          {!collapsed && children}
          <div className="mt-auto flex flex-col gap-px pt-[18px]">
            {placesIn('bottom').map(row)}
            <button
              type="button"
              onClick={togglePalette}
              aria-haspopup="dialog"
              aria-label={collapsed ? t`Search & actions` : undefined}
              data-tip={t`jump to anything, or type a task and dispatch it`}
              className="flex w-full items-center gap-2 rounded-[7px] px-2 py-1.5 text-[13px] leading-[normal] font-medium text-ink-dim transition-colors hover:bg-surface2/50 hover:text-ink"
            >
              {/* i18n-ignore — the shortcut as printed on the keyboard */}
              <span aria-hidden="true" className="w-4 shrink-0 text-center font-mono text-[11px] text-ink-faint">
                ⌘K
              </span>
              {!collapsed && (
                <span className="flex-1 text-left">
                  <Trans>Search &amp; actions</Trans>
                </span>
              )}
            </button>
            {!collapsed && <HealthFooter />}
          </div>
        </div>
      </nav>
      {paletteOpen && <CommandPalette slug={slug} onClose={() => setPaletteOpen(false)} />}
    </>
  );
}

/** The numeric badge of a row, hidden at zero; System never gets one. */
function badgeFor(place: Place, inboxCount: number, needsYouCount: number, sessionsNeedCount: number): number | null {
  const count =
    place.id === 'inbox'
      ? inboxCount
      : place.id === 'needs-you'
        ? needsYouCount
        : place.id === 'sessions'
          ? sessionsNeedCount
          : 0;
  return count > 0 ? count : null;
}

function SidebarRow({
  place,
  slug,
  active,
  badge,
  live,
  compact,
}: {
  place: Place;
  slug: string | null;
  active: boolean;
  badge: number | null;
  live: boolean;
  /** The collapsed rail: glyph only, the badge on its corner. */
  compact: boolean;
}): JSX.Element {
  const { t } = useLingui();
  const viaLastProject = slug === null && place.projectOnly;
  const label = place.label;
  // Amber is for what blocks: Inbox blocks nothing, so its badge stays grey.
  const urgent = place.id === 'needs-you' || place.id === 'sessions';
  const glyphTone = active && place.section !== 'bottom' ? 'text-brand' : 'text-ink-faint';
  const waiting =
    badge === null
      ? null
      : plural(badge, { one: '# waiting', few: '# waiting', many: '# waiting', other: '# waiting' });
  const hint = viaLastProject ? t`${label} — opens in a project` : undefined;
  return (
    <Link
      to={resolvePlaceHref(place, slug, viaLastProject ? loadLastProject() : null)}
      aria-current={active ? 'page' : undefined}
      // The label is gone from the collapsed row, and an aria-label replaces the
      // row's content, so it has to carry the count and the live dot as well.
      aria-label={
        compact ? [label, waiting, live ? t`live sessions` : null].filter((part) => part !== null).join(' · ') : undefined
      }
      data-tip={compact ? (hint ?? label) : undefined}
      title={compact ? undefined : hint}
      className={`relative flex shrink-0 items-center gap-2 rounded-[7px] px-2 py-1.5 text-[13px] leading-[normal] font-medium transition-colors ${
        active ? 'bg-surface2 text-ink' : 'text-ink-dim hover:bg-surface2/50 hover:text-ink'
      }`}
    >
      <span aria-hidden="true" className={`w-4 shrink-0 text-center font-mono text-[12px] ${glyphTone}`}>
        {place.glyph}
      </span>
      {!compact && <span className="min-w-0 flex-1 truncate">{label}</span>}
      {badge !== null && (
        <span
          aria-label={compact ? undefined : (waiting ?? undefined)}
          className={`rounded-full font-mono font-bold ${
            compact ? 'absolute top-0 right-0 px-[3px] text-[9px] leading-3' : 'px-1.5 text-[10px] leading-4'
          } ${urgent ? 'bg-brand-bg text-brand' : 'bg-surface2 text-ink-dim'}`}
        >
          {badge}
        </span>
      )}
      {live && (
        <span
          role="img"
          aria-label={t`live sessions`}
          className={`h-[6px] w-[6px] shrink-0 animate-pulse-dot rounded-full bg-green ${
            compact ? 'absolute top-1 right-1' : 'ml-auto'
          }`}
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
            }`}
          >
            <span className="relative text-[17px] leading-none" aria-hidden="true">
              {place.glyph}
              {badgeFor(place, inboxCount, needsYouCount, 0) !== null && (
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
