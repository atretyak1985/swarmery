// Bottom terminal dock (fusion phase 15). Hangs off ProjectWorkspaceLayout,
// toggled by the StatusBar "Terminal" button (Fusion's bottom-bar position).
// Owns a tab strip (each tab is one live PTY), a toolbar (font − / reset / +,
// Clear), and a height-resizable drag handle. Open state,
// height, and the tab list are persisted per project in localStorage so a
// reload restores the workspace. The heavy xterm bundle is lazy-loaded: the
// XTerm wrapper is only fetched when the dock first renders a tab.

import { Trans, useLingui } from '@lingui/react/macro';
import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { TermStatus, XTermHandle } from './XTerm';
import { TERM_CLOSE_MESSAGE, type TermCloseKind } from './closeReason';
import { ExitFullscreenIcon, FullscreenIcon, MaximizeIcon, MinimizeIcon, RestoreIcon } from './DockIcons';

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
}

/** The command a "Claude Code" dock tab runs in its shell. */
export const CLAUDE_COMMAND = 'claude';
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

/** Dock state the layout owns (so the TaskModal can push a worktree tab). */
export interface DockState {
  open: boolean;
  tabs: TermTab[];
  activeId: string | null;
}

const MIN_HEIGHT = 140;
const MAX_HEIGHT = 720;
const DEFAULT_HEIGHT = 280;
const FONT_MIN = 10;
const FONT_MAX = 20;
const FONT_DEFAULT = 14;
/** A header window-control button: icon only, label on aria-label + title. */
const WINDOW_BUTTON =
  'flex h-6 w-6 items-center justify-center rounded transition-colors hover:bg-bg hover:text-ink';

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

  // Track full screen from the browser, so Esc / F11 / the OS exit all sync.
  useEffect(() => {
    const onFullscreenChange = (): void => {
      const on = rootRef.current !== null && document.fullscreenElement === rootRef.current;
      setIsFullscreen(on);
      if (!on) unlockKeyboard();
    };
    document.addEventListener('fullscreenchange', onFullscreenChange);
    return () => document.removeEventListener('fullscreenchange', onFullscreenChange);
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

  /** Start a fresh PTY in a closed tab (same cwd, same command — run again on purpose). */
  const restartTab = (id: string): void => {
    setClosedById(({ [id]: _gone, ...rest }) => rest);
    setArmed((prev) => withId(prev, id));
    setGenById((prev) => ({ ...prev, [id]: (prev[id] ?? 0) + 1 }));
  };

  /** Type a restored tab's command into its live shell (the Start strip). */
  const startRun = (tab: TermTab): void => {
    if (tab.run === undefined || tab.run === '') return;
    termRefs.current.get(tab.id)?.send(`${tab.run}\r`);
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

  const activeTab = useMemo(
    () => state.tabs.find((t) => t.id === state.activeId) ?? null,
    [state.tabs, state.activeId],
  );

  const openTab = (tab: TermTab): void => {
    onChange({ open: true, tabs: [...state.tabs, tab], activeId: tab.id });
  };
  const newProjectTab = (): void => {
    const id = `t-${Date.now().toString(36)}`;
    // A "+" tab always opens in the project root (worktree tabs come from the
    // TaskModal). Fall back to the first tab's cwd only if the path is unknown.
    const cwd = projectPath !== '' ? projectPath : (state.tabs[0]?.cwd ?? '');
    openTab({ id, label: `shell ${state.tabs.length + 1}`, cwd });
  };
  const newClaudeTab = (): void => {
    const cwd = projectPath !== '' ? projectPath : (state.tabs[0]?.cwd ?? '');
    if (cwd === '') return;
    const next = openClaudeTerminal(state, cwd);
    if (next.activeId !== null) {
      const id = next.activeId;
      setArmed((prev) => withId(prev, id));
    }
    onChange(next);
  };
  const closeTab = (id: string): void => {
    const remaining = state.tabs.filter((t) => t.id !== id);
    termRefs.current.delete(id);
    onChange({
      open: remaining.length > 0 ? state.open : false,
      tabs: remaining,
      activeId: state.activeId === id ? (remaining[remaining.length - 1]?.id ?? null) : state.activeId,
    });
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
            strip + toolbar + window controls. Below desk this matches main's
            plain header (no surface2 fill, no caption) per the review's R1-1 finding. */}
        <div
          data-testid="dock-header"
          className="flex h-9 shrink-0 items-center gap-1 border-b border-line px-2 desk:h-10 desk:gap-1.5 desk:bg-surface2"
        >
          <span className="hidden shrink-0 font-mono text-[10px] text-ink-2 uppercase tracking-[0.12em] desk:inline-flex">
            <Trans>terminal</Trans>
          </span>
          <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
            {state.tabs.map((tab) => {
              const active = tab.id === state.activeId;
              const st = statusById[tab.id];
              return (
                <div
                  key={tab.id}
                  className={`group flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 font-mono text-[10.5px] ${
                    active ? 'border-line-strong bg-surface2 text-ink' : 'border-transparent text-ink-dim'
                  }`}
                >
                  <button
                    type="button"
                    onClick={() => onChange({ ...state, activeId: tab.id })}
                    title={st === 'closed' && closedById[tab.id] !== undefined ? t(TERM_CLOSE_MESSAGE[closedById[tab.id] as TermCloseKind]) : undefined}
                    className="flex items-center gap-1.5"
                  >
                    <span
                      aria-hidden="true"
                      className={`inline-block h-[6px] w-[6px] rounded-full ${
                        st === 'open' ? 'bg-green' : st === 'closed' ? 'bg-red' : 'bg-amber'
                      }`}
                    />
                    <span className="max-w-[140px] truncate">{tab.label}</span>
                  </button>
                  <button
                    type="button"
                    aria-label={`close ${tab.label}`}
                    onClick={() => closeTab(tab.id)}
                    className="text-ink-faint transition-colors hover:text-red"
                  >
                    ×
                  </button>
                </div>
              );
            })}
            <button
              type="button"
              aria-label="new terminal"
              onClick={newProjectTab}
              className="shrink-0 rounded-md border border-line px-1.5 py-0.5 font-mono text-[11px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
            >
              +
            </button>
          </div>

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
              onClick={() => termRefs.current.get(state.activeId ?? '')?.clear()}
              className="rounded border border-line px-2 py-0.5 transition-colors hover:text-ink"
            >
              Clear
            </button>
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

        {/* Panes: every tab is kept mounted (its PTY stays live); only the active
            one is visible so switching tabs doesn't kill the shell. */}
        <div className="relative min-h-0 flex-1 bg-[#0b0d10]">
          <Suspense
            fallback={<div className="p-3 font-mono text-[11px] text-ink-faint">loading terminal…</div>}
          >
            {state.tabs.map((tab) => {
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
                  className="absolute inset-0 p-1.5"
                  style={{ visibility: tab.id === state.activeId ? 'visible' : 'hidden' }}
                  aria-hidden={tab.id !== state.activeId}
                >
                  <XTerm
                    key={genById[tab.id] ?? 0}
                    ref={(h) => {
                      termRefs.current.set(tab.id, h);
                    }}
                    cwd={tab.cwd}
                    initialInput={armed.has(tab.id) ? tab.run : undefined}
                    fontSize={fontSize}
                    onStatus={(s, why) => setStatus(tab.id, s, why)}
                  />
                  {closed !== undefined && (
                    <div
                      role="status"
                      className="absolute inset-x-3 bottom-3 flex items-center gap-3 rounded-md border border-line bg-surface2 px-3 py-2 font-mono text-[11px] text-ink"
                    >
                      <span className="min-w-0 flex-1">{t(TERM_CLOSE_MESSAGE[closed])}</span>
                      <button
                        type="button"
                        onClick={() => restartTab(tab.id)}
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
          </Suspense>
          {activeTab === null && (
            <div className="p-3 font-mono text-[11px] text-ink-faint">no terminal open</div>
          )}
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

/** Build the initial (empty, closed) dock state. */
export function emptyDock(): DockState {
  return { open: false, tabs: [], activeId: null };
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

/** Open a fresh project-root terminal, appending a tab and revealing the dock. */
export function openProjectTerminal(state: DockState, projectPath: string): DockState {
  const id = `t-${Date.now().toString(36)}`;
  const label = `shell ${state.tabs.length + 1}`;
  return { open: true, tabs: [...state.tabs, { id, label, cwd: projectPath }], activeId: id };
}

/**
 * Open a fresh Claude Code tab in `cwd`: a project shell that runs `claude` as
 * soon as it connects (the WebStorm "Claude Code" terminal button). Every click
 * opens a new tab — parallel Claude sessions are a normal workflow.
 */
export function openClaudeTerminal(state: DockState, cwd: string): DockState {
  const id = `t-${Date.now().toString(36)}`;
  const n = state.tabs.filter((t) => t.run === CLAUDE_COMMAND).length;
  const label = n === 0 ? CLAUDE_LABEL : `${CLAUDE_LABEL} (${n + 1})`;
  return { open: true, tabs: [...state.tabs, { id, label, cwd, run: CLAUDE_COMMAND }], activeId: id };
}

/** Open a worktree terminal for a task, deduped by cwd (focus if already open). */
export function openWorktreeTerminal(state: DockState, taskLabel: string, worktreePath: string): DockState {
  const existing = state.tabs.find((t) => t.cwd === worktreePath);
  if (existing !== undefined) {
    return { ...state, open: true, activeId: existing.id };
  }
  const id = `t-${Date.now().toString(36)}`;
  return {
    open: true,
    tabs: [...state.tabs, { id, label: taskLabel, cwd: worktreePath }],
    activeId: id,
  };
}
