// Bottom terminal dock (fusion phase 15). Hangs off ProjectWorkspaceLayout,
// toggled by the StatusBar "Terminal" button (Fusion's bottom-bar position).
// Owns a tab strip (each tab is one live PTY), a toolbar (font − / reset / +,
// Clear, fullscreen, close-all), and a height-resizable drag handle. Open state,
// height, and the tab list are persisted per project in localStorage so a
// reload restores the workspace. The heavy xterm bundle is lazy-loaded: the
// XTerm wrapper is only fetched when the dock first renders a tab.

import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { TermStatus, XTermHandle } from './XTerm';

const XTerm = lazy(() => import('./XTerm').then((m) => ({ default: m.XTerm })));

/** One terminal tab: a stable id, a display label, and the cwd its PTY runs in. */
export interface TermTab {
  id: string;
  label: string;
  cwd: string;
}

/** Dock state the layout owns (so the TaskDrawer can push a worktree tab). */
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

export function TerminalDock({
  projectSlug,
  projectPath,
  state,
  onChange,
}: {
  projectSlug: string;
  /** The project's on-disk root — cwd for a "+" (new project shell) tab. */
  projectPath: string;
  state: DockState;
  onChange: (next: DockState) => void;
}): JSX.Element | null {
  const [height, setHeight] = useState(() => readNum(heightKey(projectSlug), DEFAULT_HEIGHT, MIN_HEIGHT, MAX_HEIGHT));
  const [fontSize, setFontSize] = useState(() => readNum(fontKey(projectSlug), FONT_DEFAULT, FONT_MIN, FONT_MAX));
  const [fullscreen, setFullscreen] = useState(false);
  const [statusById, setStatusById] = useState<Record<string, TermStatus>>({});
  const termRefs = useRef<Map<string, XTermHandle | null>>(new Map());
  const dragRef = useRef<{ startY: number; startH: number } | null>(null);

  // Persist height / font per project.
  useEffect(() => {
    localStorage.setItem(heightKey(projectSlug), String(height));
  }, [projectSlug, height]);
  useEffect(() => {
    localStorage.setItem(fontKey(projectSlug), String(fontSize));
  }, [projectSlug, fontSize]);

  const setStatus = useCallback((id: string, s: TermStatus) => {
    setStatusById((prev) => (prev[id] === s ? prev : { ...prev, [id]: s }));
  }, []);

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
    // TaskDrawer). Fall back to the first tab's cwd only if the path is unknown.
    const cwd = projectPath !== '' ? projectPath : (state.tabs[0]?.cwd ?? '');
    openTab({ id, label: `shell ${state.tabs.length + 1}`, cwd });
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
  const closeAll = (): void => {
    termRefs.current.clear();
    onChange({ open: false, tabs: [], activeId: null });
    setFullscreen(false);
  };

  if (!state.open || state.tabs.length === 0) return null;

  const effHeight = fullscreen ? Math.max(height, MAX_HEIGHT) : height;

  return (
    <div
      className="flex shrink-0 flex-col border-t border-line bg-bg"
      style={{ height: fullscreen ? '75vh' : effHeight }}
      role="region"
      aria-label="terminal dock"
    >
      {/* Resize handle (hidden in fullscreen). */}
      {!fullscreen && (
        <div
          onPointerDown={onHandleDown}
          onPointerMove={onHandleMove}
          onPointerUp={onHandleUp}
          role="separator"
          aria-orientation="horizontal"
          aria-label="resize terminal"
          className="h-1 cursor-row-resize bg-transparent hover:bg-brand/40"
        />
      )}

      {/* Tab strip + toolbar. */}
      <div className="flex items-center gap-1 border-b border-line px-2 py-1">
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          {state.tabs.map((tab) => {
            const active = tab.id === state.activeId;
            const st = statusById[tab.id];
            return (
              <div
                key={tab.id}
                className={`group flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-[10.5px] ${
                  active ? 'border-line-strong bg-surface2 text-ink' : 'border-transparent text-ink-dim'
                }`}
              >
                <button
                  type="button"
                  onClick={() => onChange({ ...state, activeId: tab.id })}
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

        {/* Toolbar: font − / size / +, Clear, fullscreen, close-all. */}
        <div className="flex shrink-0 items-center gap-1 font-mono text-[10.5px] text-ink-dim">
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
          <button
            type="button"
            aria-label={fullscreen ? 'restore terminal' : 'maximize terminal'}
            onClick={() => setFullscreen((v) => !v)}
            className="rounded border border-line px-1.5 py-0.5 transition-colors hover:text-ink"
          >
            {fullscreen ? '▽' : '△'}
          </button>
          <button
            type="button"
            aria-label="close all terminals"
            onClick={closeAll}
            className="rounded border border-line px-2 py-0.5 transition-colors hover:text-ink"
          >
            Close
          </button>
        </div>
      </div>

      {/* Panes: every tab is kept mounted (its PTY stays live); only the active
          one is visible so switching tabs doesn't kill the shell. */}
      <div className="relative min-h-0 flex-1 bg-[#0b0d10]">
        <Suspense
          fallback={<div className="p-3 font-mono text-[11px] text-ink-faint">loading terminal…</div>}
        >
          {state.tabs.map((tab) => (
            <div
              key={tab.id}
              className="absolute inset-0 p-1.5"
              style={{ visibility: tab.id === state.activeId ? 'visible' : 'hidden' }}
              aria-hidden={tab.id !== state.activeId}
            >
              <XTerm
                ref={(h) => {
                  termRefs.current.set(tab.id, h);
                }}
                cwd={tab.cwd}
                fontSize={fontSize}
                onStatus={(s) => setStatus(tab.id, s)}
              />
            </div>
          ))}
        </Suspense>
        {activeTab === null && (
          <div className="p-3 font-mono text-[11px] text-ink-faint">no terminal open</div>
        )}
      </div>
    </div>
  );
}

/** Build the initial (empty, closed) dock state. */
export function emptyDock(): DockState {
  return { open: false, tabs: [], activeId: null };
}

/** Open a fresh project-root terminal, appending a tab and revealing the dock. */
export function openProjectTerminal(state: DockState, projectPath: string): DockState {
  const id = `t-${Date.now().toString(36)}`;
  const label = `shell ${state.tabs.length + 1}`;
  return { open: true, tabs: [...state.tabs, { id, label, cwd: projectPath }], activeId: id };
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
