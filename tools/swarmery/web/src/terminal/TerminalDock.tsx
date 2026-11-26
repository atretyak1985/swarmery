// Bottom terminal dock (fusion phase 15). Hangs off ProjectWorkspaceLayout,
// toggled by the StatusBar "Terminal" button (Fusion's bottom-bar position).
// Owns the tab groups (each tab is one live PTY), a toolbar (font − / reset / +,
// Clear), and a height-resizable drag handle. Open state, the split layout,
// height, and the tab list are persisted per project in localStorage so a
// reload restores the workspace. The heavy xterm bundle is lazy-loaded: the
// XTerm wrapper is only fetched when the dock first renders a tab.
//
// Splits (terminal-dock-webstorm phase 4): the state is DockStateV2 from
// ./layout, a tree of tab groups. The body renders two layers. The terminal
// layer holds every tab's XTerm wrapper, keyed by tab id under ONE parent in
// `state.tabs` order, and only moves them (left/top/width/height + visibility)
// into their group's rectangle — a split, a move or a merge never re-parents an
// XTerm, so it never unmounts and its PTY lives on. The chrome layer on top
// holds the per-group tab strips, the focus ring and the resize handles; drop
// overlays exist only while a tab is being dragged, so xterm keeps its pointer
// input the rest of the time. A single group keeps the classic look: its strip
// sits in the header and its terminals fill the body.

import { Trans, useLingui } from '@lingui/react/macro';
import { type CSSProperties, Suspense, lazy, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { DockTermSession } from '../api/types';
import { deleteDockTermSession, listDockTermSessions } from '../api';
import type { TermStatus, XTermHandle } from './XTerm';
import { TERM_CLOSE_MESSAGE, type TermCloseKind } from './closeReason';
import { ExitFullscreenIcon, FullscreenIcon, MaximizeIcon, MinimizeIcon, RestoreIcon } from './DockIcons';
import { CLAUDE_COMMAND, claudeCommandFor, newSessionId, shortTitle } from './claudeSession';
import { TermSessionList } from './BackgroundTerminals';
import {
  type DockStateV2,
  type Edge,
  type LayoutGroup,
  type LayoutNode,
  type LayoutSplit,
  type Rect,
  activateTab,
  addTab,
  allGroups,
  closeTab as closeDockTab,
  dropTarget,
  emptyDockV2,
  focusGroup,
  groupOf,
  layoutRects,
  moveTab,
  reorderTab,
  resizeSplit,
  splitWithTab,
} from './layout';
import { SplitHandle } from './SplitHandle';
import type { TabMenuItem } from './TabMenu';
import { type StripTab, TAB_MIME, TabStrip, hasDraggedTab } from './TabStrip';
import { useTabSessionTitles } from './useTabSessionTitles';

export { CLAUDE_COMMAND };

const XTerm = lazy(() => import('./XTerm').then((m) => ({ default: m.XTerm })));

/** One terminal tab: a stable id, a display label, and the cwd its PTY runs in. */
export interface TermTab {
  id: string;
  label: string;
  cwd: string;
  /**
   * A command typed into the shell once its PTY connects (e.g. `claude` for a
   * Claude Code tab). It goes through the shell like a keystroke, so the
   * daemon's PTY protocol is unchanged and exiting the command drops back to
   * the shell. It runs only on an explicit action in this page load (opening
   * the tab, Restart): a tab restored by a reload or a remount opens a plain
   * shell with a Start button instead, because every automatic replay started
   * a new, empty Claude session.
   */
  run?: string;
  /**
   * The UUID of the Claude session a Claude Code tab (`run === CLAUDE_COMMAND`)
   * is bound to. Minted when the tab opens, so the tab types
   * `claude --session-id <uuid>` first and `claude --resume <uuid>` once the
   * daemon has ingested it, and takes its caption from that session's title.
   * Absent on other tabs, and on Claude tabs saved before sessions were bound
   * (those get one on their next Start / Restart).
   */
  sessionId?: string;
  /**
   * The daemon-side session id this tab's PTY is bound to (phase 5), set once
   * its `hello` frame arrives (XTerm's `onHello`). Persisted in the same
   * localStorage dock state as every other tab field, so a page reload opens
   * `?attach=<ptyId>` instead of a brand-new shell — see XTerm.tsx and
   * termSocket.ts's `termWsUrlFor`. Absent until the first hello, and on a
   * tab saved before this phase.
   */
  ptyId?: string | undefined;
}

/** Tab label for a Claude Code tab — a product name, never translated. */
const CLAUDE_LABEL = 'Claude Code';

/** The ✳ glyph + "Claude Code" button content, shared by the dock and the StatusBar. */
export function ClaudeCodeLabel(): JSX.Element {
  return (
    <>
      <span aria-hidden="true" className="text-[#d97757]">
        ✳
      </span>
      {CLAUDE_LABEL}
    </>
  );
}

/**
 * Dock state the layout owns (so the TaskModal can push a worktree tab): the v2
 * split tree. Each group keeps its own active tab; activeTabId() reads the
 * focused group's.
 */
export type DockState = DockStateV2;

const MIN_HEIGHT = 140;
const MAX_HEIGHT = 720;
const DEFAULT_HEIGHT = 280;
const FONT_MIN = 10;
const FONT_MAX = 20;
const FONT_DEFAULT = 14;
/** A header window-control button: icon only, label on aria-label + title. */
const WINDOW_BUTTON =
  'flex h-6 w-6 items-center justify-center rounded transition-colors hover:bg-bg hover:text-ink';
/** Height of a group's own tab strip once the dock is split, in px. */
const STRIP_H = 28;
/** Gap between split neighbours; the resize handle fills it. */
const GUTTER = 4;
/** The whole body: a lone group fills it, exactly as the unsplit dock always did. */
const FILL: CSSProperties = { left: 0, top: 0, right: 0, bottom: 0 };
const NO_RECT: Rect = { x: 0, y: 0, w: 0, h: 0 };
/** The part of a group a drop zone highlights. */
const ZONE_BOX: Record<Edge | 'center', CSSProperties> = {
  center: { left: 0, top: 0, right: 0, bottom: 0 },
  left: { left: 0, top: 0, width: '50%', height: '100%' },
  right: { right: 0, top: 0, width: '50%', height: '100%' },
  top: { left: 0, top: 0, width: '100%', height: '50%' },
  bottom: { left: 0, bottom: 0, width: '100%', height: '50%' },
};

function heightKey(projectSlug: string): string {
  return `swarmery.term.height.${projectSlug}`;
}
function fontKey(projectSlug: string): string {
  return `swarmery.term.font.${projectSlug}`;
}

function readNum(key: string, fallback: number, lo: number, hi: number): number {
  const raw = localStorage.getItem(key);
  if (raw === null) return fallback;
  const n = Number(raw);
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : fallback;
}

/** The Keyboard Lock API surface used here (not in lib.dom for every target). */
interface KeyboardLock {
  lock: (keys?: string[]) => Promise<void>;
  unlock: () => void;
}

/** navigator.keyboard when the browser implements Keyboard Lock, else null. */
function keyboardLock(): KeyboardLock | null {
  if (typeof navigator === 'undefined' || !('keyboard' in navigator)) return null;
  const kb: unknown = (navigator as Navigator & { keyboard?: unknown }).keyboard;
  if (typeof kb !== 'object' || kb === null) return null;
  if (!('lock' in kb) || typeof kb.lock !== 'function') return null;
  if (!('unlock' in kb) || typeof kb.unlock !== 'function') return null;
  return kb as KeyboardLock;
}

/**
 * In full screen, hold Escape for the page so a single Esc reaches Claude Code
 * (the browser then exits full screen on a long Esc press). Best effort: any
 * failure leaves the browser's default Esc behaviour.
 */
function lockEscape(): void {
  const kb = keyboardLock();
  if (kb === null) return;
  try {
    kb.lock(['Escape']).catch(() => undefined);
  } catch {
    /* unsupported in this context — keep the default */
  }
}
function unlockKeyboard(): void {
  try {
    keyboardLock()?.unlock();
  } catch {
    /* nothing was locked */
  }
}

/** Is `el` inside a terminal (xterm.js renders into a `.xterm` element)? */
function inTerminal(el: Element | null): boolean {
  return el !== null && el.closest('.xterm') !== null;
}

/** Every split in the tree, by id. */
function splitsById(node: LayoutNode, out: Map<string, LayoutSplit> = new Map()): Map<string, LayoutSplit> {
  if (node.kind === 'split') {
    out.set(node.id, node);
    for (const c of node.children) splitsById(c, out);
  }
  return out;
}

/**
 * The px length a split's sizes share: the span of its groups' rectangles along
 * its axis, minus the gutters between its children.
 */
function splitExtent(split: LayoutSplit, rects: Record<string, Rect>): number {
  const row = split.dir === 'row';
  const boxes = allGroups(split).map((g) => rects[g.id] ?? NO_RECT);
  if (boxes.length === 0) return 0;
  const lo = Math.min(...boxes.map((r) => (row ? r.x : r.y)));
  const hi = Math.max(...boxes.map((r) => (row ? r.x + r.w : r.y + r.h)));
  return Math.max(0, hi - lo - GUTTER * (split.children.length - 1));
}

export function TerminalDock({
  projectSlug,
  projectPath,
  state,
  onChange,
  maximized = false,
  onMaximizedChange,
}: {
  projectSlug: string;
  /** The project's on-disk root — cwd for a "+" (new project shell) tab. */
  projectPath: string;
  state: DockState;
  onChange: (next: DockState) => void;
  /** Whether the dock covers the page column (the layout hides its <main>). */
  maximized?: boolean;
  /** Asks the layout to maximize / restore the dock. */
  onMaximizedChange?: (next: boolean) => void;
}): JSX.Element | null {
  const [height, setHeight] = useState(() => readNum(heightKey(projectSlug), DEFAULT_HEIGHT, MIN_HEIGHT, MAX_HEIGHT));
  const [fontSize, setFontSize] = useState(() => readNum(fontKey(projectSlug), FONT_DEFAULT, FONT_MIN, FONT_MAX));
  const { t } = useLingui();
  const [statusById, setStatusById] = useState<Record<string, TermStatus>>({});
  // Why each closed tab closed, for its Restart overlay.
  const [closedById, setClosedById] = useState<Record<string, TermCloseKind>>({});
  // Bumped by Restart: a new key remounts the tab's XTerm, which opens a new PTY.
  const [genById, setGenById] = useState<Record<string, number>>({});
  // Tabs whose `run` command types itself on the next PTY connect: armed only
  // by an explicit action here, disarmed once that connect opens.
  const [armed, setArmed] = useState<ReadonlySet<string>>(() => new Set());
  // Tabs whose command has run in this page load, so the Start strip hides.
  const [started, setStarted] = useState<ReadonlySet<string>>(() => new Set());
  // setStatus is a stable callback; it reads the current arms through this.
  const armedRef = useRef(armed);
  armedRef.current = armed;
  const termRefs = useRef<Map<string, XTermHandle | null>>(new Map());
  const dragRef = useRef<{ startY: number; startH: number } | null>(null);
  // The dock root: the element that goes full screen.
  const rootRef = useRef<HTMLDivElement>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  // The body's px size: layoutRects lays the groups out in it.
  const bodyRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  // The tab being dragged (drop overlays exist only meanwhile), and the zone under the pointer.
  const [dragging, setDragging] = useState<string | null>(null);
  const [dropZone, setDropZone] = useState<{ groupId: string; zone: Edge | 'center' } | null>(null);
  // A tab whose button takes focus after the next render (a menu action moved it).
  const pendingFocusRef = useRef<string | null>(null);
  // "Background terminals" menu: PTYs under this project that are not open in
  // any current tab. Fetched on open and every 30s while the dock is open —
  // a REST poll, not a second WS channel (GET /api/term/sessions).
  const [bgMenuOpen, setBgMenuOpen] = useState(false);
  const [bgSessions, setBgSessions] = useState<readonly DockTermSession[]>([]);
  // Every live session, fetched only once a tab's Start overlay actually needs
  // it (closedById has a 'too-many' entry) — the list the limit overlay shows.
  const [tooManySessions, setTooManySessions] = useState<readonly DockTermSession[]>([]);

  // Track full screen from the browser, so Esc / F11 / the OS exit all sync.
  useEffect(() => {
    const onFullscreenChange = (): void => {
      const on = rootRef.current !== null && document.fullscreenElement === rootRef.current;
      setIsFullscreen(on);
      if (!on) unlockKeyboard();
    };
    document.addEventListener('fullscreenchange', onFullscreenChange);
    return () => {
      document.removeEventListener('fullscreenchange', onFullscreenChange);
      // Unmounted in full screen: the browser leaves full screen once the
      // element is gone, but this listener can no longer release the Esc lock.
      unlockKeyboard();
    };
  }, []);

  // Esc restores a maximized dock — but never while focus is in a terminal:
  // Claude Code (and every TUI) needs that key.
  useEffect(() => {
    if (!maximized || !state.open || onMaximizedChange === undefined) return undefined;
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape' || e.defaultPrevented) return;
      if (inTerminal(document.activeElement)) return;
      onMaximizedChange(false);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [maximized, state.open, onMaximizedChange]);

  // Persist height / font per project.
  useEffect(() => {
    localStorage.setItem(heightKey(projectSlug), String(height));
  }, [projectSlug, height]);
  useEffect(() => {
    localStorage.setItem(fontKey(projectSlug), String(fontSize));
  }, [projectSlug, fontSize]);

  // Measure the body before paint, then follow its resizes (dock drag, maximize,
  // full screen). Re-run when the dock (re)mounts its body.
  const mounted = state.tabs.length > 0;
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (el === null) return undefined;
    const measure = (): void => {
      const r = el.getBoundingClientRect();
      // A collapsed dock (display:none) measures 0 × 0. Keep the last real size:
      // laying the groups out in 0 px would shrink every split terminal — and the
      // program in its PTY — to a couple of columns until the dock reopens.
      if (r.width === 0 || r.height === 0) return;
      setSize((p) => (p.w === r.width && p.h === r.height ? p : { w: r.width, h: r.height }));
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return undefined;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [mounted]);

  // A drag that ends anywhere (Esc, a drop outside the dock) drops the overlays.
  useEffect(() => {
    if (dragging === null) return undefined;
    const end = (): void => {
      setDragging(null);
      setDropZone(null);
    };
    window.addEventListener('dragend', end, true);
    return () => window.removeEventListener('dragend', end, true);
  }, [dragging]);

  // Refresh "Background terminals" when the menu opens, and every 30s while
  // the dock itself is open (so the badge count stays roughly live even with
  // the menu closed) — one REST poll, no dedicated WS channel.
  useEffect(() => {
    if (!state.open || projectPath === '') return undefined;
    let cancelled = false;
    const refresh = (): void => {
      listDockTermSessions(projectPath)
        .then((list) => {
          if (!cancelled) setBgSessions(list);
        })
        .catch(() => undefined);
    };
    refresh();
    const iv = window.setInterval(refresh, 30_000);
    return () => {
      cancelled = true;
      window.clearInterval(iv);
    };
  }, [state.open, projectPath]);

  // The too-many overlay's own session list: fetched once some tab actually
  // shows that overlay, refreshed whenever the set of such tabs changes (a
  // Close in the overlay, or a newly full daemon).
  const anyTooMany = Object.values(closedById).includes('too-many');
  useEffect(() => {
    if (!anyTooMany) return undefined;
    let cancelled = false;
    listDockTermSessions(projectPath)
      .then((list) => {
        if (!cancelled) setTooManySessions(list);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [anyTooMany, projectPath]);

  // Hand focus to the tab a menu action just moved (its button may have changed strip).
  useEffect(() => {
    const id = pendingFocusRef.current;
    if (id === null) return;
    pendingFocusRef.current = null;
    const buttons = rootRef.current?.querySelectorAll<HTMLElement>('[data-tab-button]') ?? [];
    Array.from(buttons)
      .find((b) => b.dataset.tabButton === id)
      ?.focus();
  });

  const setStatus = useCallback((id: string, s: TermStatus, closed?: TermCloseKind) => {
    setStatusById((prev) => (prev[id] === s ? prev : { ...prev, [id]: s }));
    if (closed !== undefined) setClosedById((prev) => ({ ...prev, [id]: closed }));
    if (s === 'open') {
      // XTerm captured the armed command before reporting `open`; drop the arm
      // so a later remount of this tab does not type it again.
      if (armedRef.current.has(id)) {
        setArmed((prev) => withoutId(prev, id));
        setStarted((prev) => withId(prev, id));
      }
    }
  }, []);

  // The daemon's title + ingest state for every Claude tab's session.
  const titles = useTabSessionTitles(state.tabs);

  /** The command a tab types: a Claude tab resumes its session once the daemon has it. */
  const commandFor = (tab: TermTab): string | undefined => claudeCommandFor(tab, titles[tab.id]?.known ?? false);

  /** A Claude tab saved before sessions were bound gets its session id now. */
  const bindSession = (tab: TermTab): TermTab => {
    if (tab.run !== CLAUDE_COMMAND || (tab.sessionId !== undefined && tab.sessionId !== '')) return tab;
    const bound: TermTab = { ...tab, sessionId: newSessionId() };
    onChange({ ...state, tabs: state.tabs.map((t) => (t.id === tab.id ? bound : t)) });
    return bound;
  };

  /**
   * Start a fresh PTY in a closed tab (same cwd, same command — run again on
   * purpose). Binds a Claude tab's session id and drops its stale `ptyId` (a
   * fresh PTY gets a fresh one on its next hello) in ONE onChange — bindSession's
   * own onChange would otherwise be built from the same pre-update `state` and
   * get clobbered by a second, separate call.
   */
  const restartTab = (tab: TermTab): void => {
    const { id } = tab;
    const bound =
      tab.run === CLAUDE_COMMAND && (tab.sessionId === undefined || tab.sessionId === '')
        ? { ...tab, sessionId: newSessionId() }
        : tab;
    const next: TermTab = { ...bound, ptyId: undefined };
    onChange({ ...state, tabs: state.tabs.map((t) => (t.id === id ? next : t)) });
    setClosedById(({ [id]: _gone, ...rest }) => rest);
    setArmed((prev) => withId(prev, id));
    setGenById((prev) => ({ ...prev, [id]: (prev[id] ?? 0) + 1 }));
  };

  /**
   * Reconnect a closed tab to the SAME PTY (`attached-elsewhere`'s "Take over
   * here", or a `not-found`/`detach-expired` retry) — a plain remount, which
   * reattaches via the tab's existing `ptyId` (XTerm.tsx) rather than typing
   * `run` into a new shell. A no-op once the id is truly gone: the remount
   * just reports `not-found` again.
   */
  const reattachTab = (tab: TermTab): void => {
    const { id } = tab;
    setClosedById(({ [id]: _gone, ...rest }) => rest);
    setGenById((prev) => ({ ...prev, [id]: (prev[id] ?? 0) + 1 }));
  };

  /** This tab's PTY id, once its `hello` frame named one. */
  const handleHello = (id: string, ptyId: string): void => {
    onChange({ ...state, tabs: state.tabs.map((t) => (t.id === id ? { ...t, ptyId } : t)) });
  };

  /** Type a restored tab's command into its live shell (the Start strip). */
  const startRun = (tab: TermTab): void => {
    const command = commandFor(bindSession(tab));
    if (command === undefined || command === '') return;
    termRefs.current.get(tab.id)?.send(`${command}\r`);
    setStarted((prev) => withId(prev, tab.id));
  };

  // Drag-to-resize: track the pointer on the handle; taller = drag up.
  const onHandleDown = (e: React.PointerEvent): void => {
    dragRef.current = { startY: e.clientY, startH: height };
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onHandleMove = (e: React.PointerEvent): void => {
    const d = dragRef.current;
    if (d === null) return;
    const next = Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, d.startH + (d.startY - e.clientY)));
    setHeight(next);
  };
  const onHandleUp = (e: React.PointerEvent): void => {
    dragRef.current = null;
    (e.target as HTMLElement).releasePointerCapture(e.pointerId);
  };

  /** cwd for a new project tab: the project root, else the first tab's cwd. */
  const newTabCwd = (): string => (projectPath !== '' ? projectPath : (state.tabs[0]?.cwd ?? ''));
  // "+" always opens in the project root (worktree tabs come from the
  // TaskModal), in the focused group.
  const newProjectTab = (): void => {
    onChange(openProjectTerminal(state, newTabCwd()));
  };
  const newClaudeTab = (): void => {
    const cwd = newTabCwd();
    if (cwd === '') return;
    const next = openClaudeTerminal(state, cwd);
    const id = activeTabId(next);
    if (id !== null) setArmed((prev) => withId(prev, id));
    onChange(next);
  };
  /**
   * Close a tab; returns the next state so a menu can move focus on. The ×
   * ends the PTY itself (DELETE /api/term/sessions/{ptyId}) — a plain unmount
   * (reload, navigation, a remount for Restart/attach) must NOT do this, or
   * every reload would kill the very session it is trying to reattach to; see
   * XTerm.tsx's unmount cleanup, which only closes the socket (a detach, not a
   * close). Fire-and-forget: a 404 (already gone) or a network hiccup is not
   * this tab's problem any more, it is already removed.
   */
  const closeTab = (id: string): DockState => {
    const ptyId = state.tabs.find((t) => t.id === id)?.ptyId;
    if (ptyId !== undefined) void deleteDockTermSession(ptyId).catch(() => undefined);
    termRefs.current.delete(id);
    const next = closeDockTab(state, id);
    onChange(next);
    return next;
  };

  /** Attach a background PTY as a new tab, then close the menu. */
  const attachBackground = (s: DockTermSession): void => {
    const tab: TermTab = { id: newTabId(state.tabs), label: s.label ?? s.foreground ?? 'shell', cwd: s.cwd, ptyId: s.id };
    onChange(addTab(state, tab));
    setBgMenuOpen(false);
  };
  /** Close a background PTY from the menu, without touching any open tab. */
  const closeBackground = (s: DockTermSession): void => {
    void deleteDockTermSession(s.id).catch(() => undefined);
    setBgSessions((prev) => prev.filter((x) => x.id !== s.id));
  };
  /** Close a session from the too-many overlay's own list. */
  const closeTooMany = (s: DockTermSession): void => {
    void deleteDockTermSession(s.id).catch(() => undefined);
    setTooManySessions((prev) => prev.filter((x) => x.id !== s.id));
  };

  const enterFullscreen = (): void => {
    const el = rootRef.current;
    if (el === null || typeof el.requestFullscreen !== 'function') return;
    try {
      Promise.resolve(el.requestFullscreen()).then(lockEscape, () => undefined);
    } catch {
      /* refused (no user gesture, iframe policy) — stay in the page */
    }
  };
  const exitFullscreenIfAny = (): void => {
    if (rootRef.current === null || document.fullscreenElement !== rootRef.current) return;
    if (typeof document.exitFullscreen !== 'function') return;
    try {
      Promise.resolve(document.exitFullscreen()).catch(() => undefined);
    } catch {
      /* already left */
    }
  };
  const toggleFullscreen = (): void => {
    if (isFullscreen) exitFullscreenIfAny();
    else enterFullscreen();
  };

  /** "—": hide the dock without closing a tab, so every PTY keeps running (D-5). */
  const minimize = (): void => {
    exitFullscreenIfAny();
    onMaximizedChange?.(false);
    onChange({ ...state, open: false });
  };

  // Keep the dock mounted whenever there are tabs, even while collapsed, so
  // collapsing the footer "Terminal" toggle hides the panel via CSS instead of
  // unmounting XTerm (which would close the PTY WebSocket and kill the
  // session — see XTerm.tsx's unmount cleanup). Only closeTab should
  // actually tear the dock down. See dockView() for the pure decision.
  const view = dockView(state);
  if (!view.mount) return null;

  // ---- Layout: the groups' rectangles inside the measured body ----
  const groups = allGroups(state.layout);
  const split = groups.length > 1;
  const { groups: rects, handles } = layoutRects(state.layout, { x: 0, y: 0, w: size.w, h: size.h }, GUTTER);
  const splits = splitsById(state.layout);
  const groupIdOf = new Map<string, string>();
  for (const g of groups) for (const id of g.tabIds) groupIdOf.set(id, g.id);
  const shownIds = new Set(groups.map((g) => g.activeId));
  const focusedTabId = activeTabId(state);
  const tabById = new Map(state.tabs.map((tab) => [tab.id, tab]));
  // "Background terminals": sessions under this project that no open tab's
  // ptyId already names — a tab yet to receive its first hello (ptyId still
  // undefined) is simply not excluded, same as any other PTY not ours.
  const openPtyIds = new Set(state.tabs.flatMap((tab) => (tab.ptyId !== undefined ? [tab.ptyId] : [])));
  const backgroundSessions = bgSessions.filter((s) => !openPtyIds.has(s.id));

  /** A group's whole box (strip included), positioned in the body. */
  const groupBox = (groupId: string): CSSProperties => {
    if (!split) return FILL;
    const r = rects[groupId] ?? NO_RECT;
    return { left: r.x, top: r.y, width: r.w, height: r.h };
  };
  /** A group's terminal area: its box minus its own strip once the dock is split. */
  const contentBox = (groupId: string): CSSProperties => {
    if (!split) return FILL;
    const r = rects[groupId] ?? NO_RECT;
    return { left: r.x, top: r.y + STRIP_H, width: r.w, height: Math.max(0, r.h - STRIP_H) };
  };

  // ---- Tabs as the strips show them ----
  const stripTab = (tab: TermTab): StripTab => {
    const st = statusById[tab.id];
    // A Claude tab shows its session's title, read from the daemon on every
    // render so a dashboard rename shows at once; the saved label is the
    // fallback until the session has a title.
    const fullTitle = titles[tab.id]?.title ?? null;
    const closedKind = closedById[tab.id];
    return {
      id: tab.id,
      caption: shortTitle(fullTitle) ?? tab.label,
      fullTitle,
      status: st,
      closedWhy: st === 'closed' && closedKind !== undefined ? t(TERM_CLOSE_MESSAGE[closedKind]) : undefined,
    };
  };
  const stripTabs = (g: LayoutGroup): StripTab[] =>
    g.tabIds.flatMap((id) => {
      const tab = tabById.get(id);
      return tab === undefined ? [] : [stripTab(tab)];
    });

  // ---- Focus, drag and drop ----
  /** Pointer or focus inside a tab's terminal focuses that tab's group. */
  const focusGroupOf = (tabId: string): void => {
    const gid = groupIdOf.get(tabId);
    if (gid !== undefined && gid !== state.focusedGroupId) onChange(focusGroup(state, gid));
  };
  const endDrag = (): void => {
    setDragging(null);
    setDropZone(null);
  };
  const accepts = (e: React.DragEvent): boolean => dragging !== null || hasDraggedTab(e.dataTransfer);
  const draggedId = (e: React.DragEvent): string | null => {
    const id = e.dataTransfer.getData(TAB_MIME) || dragging;
    return id !== null && tabById.has(id) ? id : null;
  };
  /** A tab dropped on a strip before its tab at `index` (counted with the dragged tab in place). */
  const dropOnStrip = (groupId: string, tabId: string, index: number): void => {
    endDrag();
    const home = groupOf(state, tabId);
    if (home === null || !tabById.has(tabId)) return;
    if (home.id !== groupId) {
      onChange(moveTab(state, tabId, groupId, index));
      return;
    }
    // reorderTab takes the final position, i.e. the index once the tab is lifted out.
    const from = home.tabIds.indexOf(tabId);
    const to = index > from ? index - 1 : index;
    if (to !== from) onChange(reorderTab(state, tabId, to));
  };
  /** A tab dropped on a group's terminal area: the centre moves it in, an edge splits. */
  const dropOnGroup = (groupId: string, tabId: string, zone: Edge | 'center'): void => {
    endDrag();
    if (zone !== 'center') {
      onChange(splitWithTab(state, tabId, groupId, zone));
      return;
    }
    const home = groupOf(state, tabId);
    onChange(home?.id === groupId ? activateTab(state, tabId) : moveTab(state, tabId, groupId));
  };
  const zoneAt = (e: React.DragEvent<HTMLElement>): Edge | 'center' => {
    const r = e.currentTarget.getBoundingClientRect();
    return dropTarget({ x: r.left, y: r.top, w: r.width, h: r.height }, { x: e.clientX, y: e.clientY });
  };

  // ---- The tab menu: every drag action, by keyboard ----
  /** Apply a menu action, then focus `focusId`'s tab button wherever it now lives. */
  const menuAct = (focusId: string | null, next: DockState): void => {
    pendingFocusRef.current = focusId;
    onChange(next);
  };
  const menuItems = (tabId: string): TabMenuItem[] => {
    const home = groupOf(state, tabId);
    if (home === null) return [];
    const at = home.tabIds.indexOf(tabId);
    const alone = home.tabIds.length < 2;
    const items: TabMenuItem[] = [
      {
        key: 'split-right',
        label: t`Split right`,
        disabled: alone,
        onSelect: () => menuAct(tabId, splitWithTab(state, tabId, home.id, 'right')),
      },
      {
        key: 'split-down',
        label: t`Split down`,
        disabled: alone,
        onSelect: () => menuAct(tabId, splitWithTab(state, tabId, home.id, 'bottom')),
      },
      {
        key: 'move-left',
        label: t`Move left`,
        disabled: at <= 0,
        onSelect: () => menuAct(tabId, reorderTab(state, tabId, at - 1)),
      },
      {
        key: 'move-right',
        label: t`Move right`,
        disabled: at >= home.tabIds.length - 1,
        onSelect: () => menuAct(tabId, reorderTab(state, tabId, at + 1)),
      },
    ];
    groups.forEach((g, i) => {
      if (g.id === home.id) return;
      const n = i + 1;
      items.push({
        key: `move-to-${g.id}`,
        label: t`Move to group ${n}`,
        onSelect: () => menuAct(tabId, moveTab(state, tabId, g.id)),
      });
    });
    items.push({
      key: 'close',
      label: t`Close`,
      onSelect: () => {
        pendingFocusRef.current = activeTabId(closeTab(tabId));
      },
    });
    return items;
  };

  /** One group's strip; `className`/`style` place it in the header or on top of the group. */
  const renderStrip = (
    g: LayoutGroup,
    className: string,
    style?: CSSProperties,
    trailing?: JSX.Element,
  ): JSX.Element => (
    <TabStrip
      key={g.id}
      groupId={g.id}
      tabs={stripTabs(g)}
      activeId={g.activeId}
      className={className}
      style={style}
      dragging={dragging}
      onActivate={(id) => onChange(activateTab(state, id))}
      onClose={(id) => {
        closeTab(id);
      }}
      onDragStart={setDragging}
      onDragEnd={endDrag}
      onDropTab={(id, index) => dropOnStrip(g.id, id, index)}
      menuItems={menuItems}
    >
      {trailing}
    </TabStrip>
  );

  const newTabButton = (
    <button
      type="button"
      aria-label="new terminal"
      onClick={newProjectTab}
      className="shrink-0 rounded-md border border-line px-1.5 py-0.5 font-mono text-[11px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
    >
      +
    </button>
  );

  // Clamp against the viewport too: the shell roots are `h-dvh overflow-hidden`
  // (App.tsx / workspace/WorkspaceShell.tsx), so a dock taller than the window
  // would push the StatusBar — which carries the toggle that closes it — out of
  // a frame that no longer scrolls.
  const effHeight = Math.min(height, Math.round(window.innerHeight * 0.75));
  // Maximized or full screen, the island fills its box: no fixed height and no
  // resize handle. Only classes change — the XTerm subtree is never remounted.
  const fills = maximized || isFullscreen;
  const rootClass = isFullscreen
    ? 'flex h-full flex-col bg-bg'
    : maximized
      ? 'flex min-h-0 flex-1 flex-col'
      : 'flex shrink-0 flex-col';
  const islandClass = fills
    ? `flex min-h-0 flex-1 flex-col overflow-hidden border-t border-line bg-bg${isFullscreen ? ' h-full' : ' desk:rounded-xl desk:border'}`
    : 'flex shrink-0 flex-col overflow-hidden border-t border-line bg-bg desk:rounded-xl desk:border';
  const loneGroup = groups[0];

  return (
    <div ref={rootRef} className={rootClass} hidden={view.hidden} role="region" aria-label="terminal dock">
      {/* Resize handle: the 8px gap above the island at desk. Below desk
          (D-9/D-10/D-11 gate at --breakpoint-desk, index.css:592) the dock
          isn't an island yet, so this stays the thin h-1 strip main always had.
          The whole gap stays the hit area; hover only paints a 1px centred line. */}
      {!fills && (
        <div
          onPointerDown={onHandleDown}
          onPointerMove={onHandleMove}
          onPointerUp={onHandleUp}
          role="separator"
          aria-orientation="horizontal"
          aria-label="resize terminal"
          className="relative h-1 shrink-0 cursor-row-resize before:absolute before:inset-x-0 before:top-1/2 before:h-px before:-translate-y-1/2 before:bg-transparent hover:before:bg-brand/60 desk:h-2"
        />
      )}

      <div className={islandClass} style={fills ? undefined : { height: effHeight }}>
        {/* Header: the dock's own tone, a mono "terminal" caption, then the tab
            strip (a lone group's — split groups carry their own) + toolbar +
            window controls. Below desk this matches main's plain header (no
            surface2 fill, no caption) per the review's R1-1 finding. */}
        <div
          data-testid="dock-header"
          className="flex h-9 shrink-0 items-center gap-1 border-b border-line px-2 desk:h-10 desk:gap-1.5 desk:bg-surface2"
        >
          <span className="hidden shrink-0 font-mono text-[10px] text-ink-2 uppercase tracking-[0.12em] desk:inline-flex">
            <Trans>terminal</Trans>
          </span>
          {!split && loneGroup !== undefined ? (
            renderStrip(loneGroup, 'flex min-w-0 flex-1 items-center gap-1 overflow-x-auto', undefined, newTabButton)
          ) : (
            <div className="flex min-w-0 flex-1 items-center gap-1">{newTabButton}</div>
          )}

          {/* Toolbar: Claude Code, font − / size / +, Clear. */}
          <div className="flex shrink-0 items-center gap-1 font-mono text-[10.5px] text-ink-dim">
            <button
              type="button"
              aria-label="open Claude Code in a new terminal"
              title="Open Claude Code in a new terminal tab"
              onClick={newClaudeTab}
              className="flex items-center gap-1 rounded border border-line px-2 py-0.5 text-ink transition-colors hover:border-line-strong"
            >
              <ClaudeCodeLabel />
            </button>
            <button
              type="button"
              aria-label="decrease font size"
              disabled={fontSize <= FONT_MIN}
              onClick={() => setFontSize((f) => Math.max(FONT_MIN, f - 1))}
              className="rounded border border-line px-1.5 py-0.5 transition-colors hover:text-ink disabled:opacity-40"
            >
              A−
            </button>
            <span className="w-7 text-center tabular-nums">{fontSize}px</span>
            <button
              type="button"
              aria-label="increase font size"
              disabled={fontSize >= FONT_MAX}
              onClick={() => setFontSize((f) => Math.min(FONT_MAX, f + 1))}
              className="rounded border border-line px-1.5 py-0.5 transition-colors hover:text-ink disabled:opacity-40"
            >
              A+
            </button>
            <button
              type="button"
              onClick={() => termRefs.current.get(focusedTabId ?? '')?.clear()}
              className="rounded border border-line px-2 py-0.5 transition-colors hover:text-ink"
            >
              Clear
            </button>
            <div className="relative">
              <button
                type="button"
                aria-label={t`background terminals`}
                aria-expanded={bgMenuOpen}
                onClick={() => setBgMenuOpen((o) => !o)}
                className="flex items-center gap-1 rounded border border-line px-2 py-0.5 transition-colors hover:text-ink"
              >
                <Trans>Background</Trans>
                {backgroundSessions.length > 0 && (
                  <span className="tabular-nums">{backgroundSessions.length}</span>
                )}
              </button>
              {bgMenuOpen && (
                <div
                  role="dialog"
                  aria-label={t`background terminals`}
                  className="absolute right-0 top-full z-30 mt-1 max-h-72 w-72 overflow-y-auto rounded-md border border-line-strong bg-surface2 p-1.5 font-mono text-[11px] text-ink shadow-lg"
                >
                  <TermSessionList
                    sessions={backgroundSessions}
                    projectPath={projectPath}
                    onAttach={attachBackground}
                    onClose={closeBackground}
                  />
                </div>
              )}
            </div>
          </div>

          {/* Window controls, last in the header (the WebStorm order):
              maximize / restore, full screen / exit, minimize "—". */}
          <div
            role="group"
            aria-label={t`window controls`}
            className="flex shrink-0 items-center gap-0.5 border-l border-line pl-1 text-ink-dim"
          >
            {onMaximizedChange !== undefined && (
              <button
                type="button"
                aria-label={maximized ? t`restore terminal size` : t`maximize terminal`}
                title={maximized ? t`Restore the terminal size (Esc)` : t`Maximize the terminal over the page`}
                aria-pressed={maximized}
                onClick={() => onMaximizedChange(!maximized)}
                className={WINDOW_BUTTON}
              >
                {maximized ? <RestoreIcon /> : <MaximizeIcon />}
              </button>
            )}
            <button
              type="button"
              aria-label={isFullscreen ? t`exit full screen` : t`full screen`}
              title={isFullscreen ? t`Hold Esc to exit full screen` : t`Show the terminal full screen`}
              aria-pressed={isFullscreen}
              onClick={toggleFullscreen}
              className={WINDOW_BUTTON}
            >
              {isFullscreen ? <ExitFullscreenIcon /> : <FullscreenIcon />}
            </button>
            <button
              type="button"
              aria-label={t`minimize terminal`}
              title={t`Hide the terminal — its programs keep running`}
              onClick={minimize}
              className={WINDOW_BUTTON}
            >
              <MinimizeIcon />
            </button>
          </div>
        </div>

        {/* Body: the terminal layer, then the chrome layer, then (mid-drag) the drop overlays. */}
        <div ref={bodyRef} data-testid="dock-body" className="relative min-h-0 flex-1 bg-[#0b0d10]">
          <Suspense
            fallback={<div className="p-3 font-mono text-[11px] text-ink-faint">loading terminal…</div>}
          >
            {/* Terminal layer: every tab stays mounted (its PTY stays live) under
                this one parent, in `state.tabs` order; only each wrapper's box and
                visibility follow the layout, so no split, move or merge remounts it. */}
            <div className="absolute inset-0">
              {state.tabs.map((tab) => {
                const groupId = groupIdOf.get(tab.id) ?? '';
                const shown = shownIds.has(tab.id);
                const closed = statusById[tab.id] === 'closed' ? closedById[tab.id] : undefined;
                const awaitingStart =
                  statusById[tab.id] === 'open' &&
                  tab.run !== undefined &&
                  tab.run !== '' &&
                  !armed.has(tab.id) &&
                  !started.has(tab.id);
                return (
                  <div
                    key={tab.id}
                    data-term-tab={tab.id}
                    className="absolute p-1.5"
                    style={{ ...contentBox(groupId), visibility: shown ? 'visible' : 'hidden' }}
                    aria-hidden={!shown}
                    onPointerDown={() => focusGroupOf(tab.id)}
                    onFocus={() => focusGroupOf(tab.id)}
                  >
                    <XTerm
                      key={genById[tab.id] ?? 0}
                      ref={(h) => {
                        termRefs.current.set(tab.id, h);
                      }}
                      cwd={tab.cwd}
                      ptyId={tab.ptyId}
                      initialInput={armed.has(tab.id) ? commandFor(tab) : undefined}
                      fontSize={fontSize}
                      onStatus={(s, why) => setStatus(tab.id, s, why)}
                      onHello={(id) => handleHello(tab.id, id)}
                    />
                    {closed === 'attached-elsewhere' && (
                      <div
                        role="status"
                        className="absolute inset-x-3 bottom-3 flex items-center gap-3 rounded-md border border-line bg-surface2 px-3 py-2 font-mono text-[11px] text-ink"
                      >
                        <span className="min-w-0 flex-1">{t(TERM_CLOSE_MESSAGE[closed])}</span>
                        <button
                          type="button"
                          onClick={() => reattachTab(tab)}
                          className="shrink-0 rounded border border-line-strong px-2 py-0.5 text-ink transition-colors hover:border-brand hover:text-brand"
                        >
                          <Trans>Take over here</Trans>
                        </button>
                      </div>
                    )}
                    {closed === 'too-many' && (
                      <div
                        role="status"
                        className="absolute inset-x-3 bottom-3 flex max-h-56 flex-col gap-2 overflow-y-auto rounded-md border border-line bg-surface2 px-3 py-2 font-mono text-[11px] text-ink"
                      >
                        <span className="min-w-0">{t(TERM_CLOSE_MESSAGE[closed])}</span>
                        <TermSessionList sessions={tooManySessions} projectPath={projectPath} onClose={closeTooMany} />
                        <button
                          type="button"
                          onClick={() => restartTab(tab)}
                          className="shrink-0 self-end rounded border border-line-strong px-2 py-0.5 text-ink transition-colors hover:border-brand hover:text-brand"
                        >
                          <Trans>Try again</Trans>
                        </button>
                      </div>
                    )}
                    {closed !== undefined && closed !== 'attached-elsewhere' && closed !== 'too-many' && (
                      <div
                        role="status"
                        className="absolute inset-x-3 bottom-3 flex items-center gap-3 rounded-md border border-line bg-surface2 px-3 py-2 font-mono text-[11px] text-ink"
                      >
                        <span className="min-w-0 flex-1">{t(TERM_CLOSE_MESSAGE[closed])}</span>
                        <button
                          type="button"
                          onClick={() => restartTab(tab)}
                          className="shrink-0 rounded border border-line-strong px-2 py-0.5 text-ink transition-colors hover:border-brand hover:text-brand"
                        >
                          {tab.run === CLAUDE_COMMAND ? <Trans>Restart Claude Code</Trans> : <Trans>Restart</Trans>}
                        </button>
                      </div>
                    )}
                    {awaitingStart && (
                      <div
                        role="status"
                        className="absolute inset-x-3 bottom-3 flex items-center gap-3 rounded-md border border-line bg-surface2 px-3 py-2 font-mono text-[11px] text-ink"
                      >
                        <span className="min-w-0 flex-1">
                          <Trans>Restored after a reload — the command is not started, so no empty session is created.</Trans>
                        </span>
                        <button
                          type="button"
                          onClick={() => startRun(tab)}
                          className="shrink-0 rounded border border-line-strong px-2 py-0.5 text-ink transition-colors hover:border-brand hover:text-brand"
                        >
                          {tab.run === CLAUDE_COMMAND ? <Trans>Start Claude Code</Trans> : <Trans>Run</Trans>}
                        </button>
                        <button
                          type="button"
                          aria-label={t`dismiss`}
                          onClick={() => setStarted((prev) => withId(prev, tab.id))}
                          className="shrink-0 text-ink-faint transition-colors hover:text-ink"
                        >
                          ×
                        </button>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </Suspense>

          {/* Chrome layer (split only): a strip on top of each group, a ring on the
              focused one, a handle in every gutter. Only its children take the pointer. */}
          {split && (
            <div className="pointer-events-none absolute inset-0">
              {groups.map((g) => {
                const r = rects[g.id] ?? NO_RECT;
                return renderStrip(
                  g,
                  'pointer-events-auto absolute flex items-center gap-1 overflow-x-auto border-b border-line bg-surface2 px-1',
                  { left: r.x, top: r.y, width: r.w, height: STRIP_H },
                );
              })}
              <div
                data-testid="group-focus"
                data-group-id={state.focusedGroupId}
                className="pointer-events-none absolute ring-1 ring-brand/60 ring-inset"
                style={groupBox(state.focusedGroupId)}
              />
              {handles.map((h) => {
                const node = splits.get(h.splitId);
                if (node === undefined) return null;
                return (
                  <SplitHandle
                    key={`${h.splitId}:${h.index}`}
                    dir={h.dir}
                    rect={h.rect}
                    index={h.index}
                    sizes={node.sizes}
                    extent={splitExtent(node, rects)}
                    label={t`Resize split`}
                    onResize={(sizes) => onChange(resizeSplit(state, h.splitId, sizes))}
                  />
                );
              })}
            </div>
          )}

          {/* Drop overlays, only mid-drag (xterm owns the pointer otherwise): the
              centre of a group's terminal area moves the tab in, an edge splits. */}
          {dragging !== null &&
            groups.map((g) => (
              <div
                key={g.id}
                data-testid="drop-zone"
                data-group-id={g.id}
                className="absolute z-20"
                style={contentBox(g.id)}
                onDragOver={(e) => {
                  if (!accepts(e)) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = 'move';
                  const zone = zoneAt(e);
                  setDropZone((p) => (p?.groupId === g.id && p.zone === zone ? p : { groupId: g.id, zone }));
                }}
                onDragLeave={(e) => {
                  const next = e.relatedTarget;
                  if (next instanceof Node && e.currentTarget.contains(next)) return;
                  setDropZone((p) => (p?.groupId === g.id ? null : p));
                }}
                onDrop={(e) => {
                  if (!accepts(e)) return;
                  e.preventDefault();
                  const id = draggedId(e);
                  if (id === null) {
                    endDrag();
                    return;
                  }
                  dropOnGroup(g.id, id, zoneAt(e));
                }}
              >
                {dropZone?.groupId === g.id && (
                  <div
                    data-testid="drop-highlight"
                    data-zone={dropZone.zone}
                    className="pointer-events-none absolute rounded-sm border border-brand bg-brand/15"
                    style={ZONE_BOX[dropZone.zone]}
                  />
                )}
              </div>
            ))}

          {focusedTabId === null && <div className="p-3 font-mono text-[11px] text-ink-faint">no terminal open</div>}
        </div>
      </div>
    </div>
  );
}

function withId(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
  return set.has(id) ? set : new Set([...set, id]);
}
function withoutId(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
  const next = new Set(set);
  next.delete(id);
  return next;
}

/** A fresh tab id, unique among `tabs` even when two open within one millisecond. */
function newTabId(tabs: readonly TermTab[]): string {
  const base = `t-${Date.now().toString(36)}`;
  const taken = new Set(tabs.map((tab) => tab.id));
  if (!taken.has(base)) return base;
  let n = 1;
  while (taken.has(`${base}-${n}`)) n += 1;
  return `${base}-${n}`;
}

/** Build the initial (empty, closed) dock state. */
export function emptyDock(): DockState {
  return emptyDockV2();
}

/** The active tab of the focused group — the tab the toolbar acts on — or null. */
export function activeTabId(state: DockState): string | null {
  const groups = allGroups(state.layout);
  return (groups.find((g) => g.id === state.focusedGroupId) ?? groups[0])?.activeId ?? null;
}

/**
 * StatusBar "Terminal" toggle. Opens a first project-root terminal when none
 * exist; otherwise just flips visibility. Collapsing NEVER drops tabs, so the
 * live PTYs (and their scrollback) survive an expand/collapse cycle — the bug
 * this guards against was clearing tabs on the second click, which unmounted
 * XTerm and killed the shell. `projectPath === ''` (project not resolved yet)
 * is a no-op.
 */
export function toggleDock(state: DockState, projectPath: string): DockState {
  if (state.tabs.length === 0) {
    if (projectPath === '') return state;
    return openProjectTerminal(state, projectPath);
  }
  return { ...state, open: !state.open };
}

/**
 * Render decision for the dock: keep it mounted (every PTY stays live) whenever
 * tabs exist, and hide via CSS while collapsed. Unmounting would close the
 * WebSocket and SIGHUP the shell — see XTerm.tsx — so `mount` must stay true
 * across collapse.
 */
export function dockView(state: DockState): { mount: boolean; hidden: boolean } {
  return { mount: state.tabs.length > 0, hidden: !state.open };
}

/**
 * Does a maximized dock cover the page column? Only while it is visible: a
 * minimized or empty dock gives the page back. The layout sets `hidden` on its
 * <main> from this (display:none — the Outlet stays mounted, pages keep state).
 */
export function dockCoversPage(state: DockState, maximized: boolean): boolean {
  const view = dockView(state);
  return maximized && view.mount && !view.hidden;
}

/** Open a fresh project-root terminal in the focused group, revealing the dock. */
export function openProjectTerminal(state: DockState, projectPath: string): DockState {
  const label = `shell ${state.tabs.length + 1}`;
  return addTab(state, { id: newTabId(state.tabs), label, cwd: projectPath });
}

/**
 * Open a fresh Claude Code tab in `cwd`, in the focused group: a project shell
 * that runs `claude` as soon as it connects (the WebStorm "Claude Code"
 * terminal button), bound to a newly minted session id. Every click opens a new
 * tab — parallel Claude sessions are a normal workflow.
 */
export function openClaudeTerminal(state: DockState, cwd: string): DockState {
  const n = state.tabs.filter((t) => t.run === CLAUDE_COMMAND).length;
  const label = n === 0 ? CLAUDE_LABEL : `${CLAUDE_LABEL} (${n + 1})`;
  const tab: TermTab = { id: newTabId(state.tabs), label, cwd, run: CLAUDE_COMMAND, sessionId: newSessionId() };
  return addTab(state, tab);
}

/** Open a worktree terminal for a task, deduped by cwd (focus if already open). */
export function openWorktreeTerminal(state: DockState, taskLabel: string, worktreePath: string): DockState {
  const existing = state.tabs.find((t) => t.cwd === worktreePath);
  if (existing !== undefined) {
    return { ...activateTab(state, existing.id), open: true };
  }
  return addTab(state, { id: newTabId(state.tabs), label: taskLabel, cwd: worktreePath });
}
