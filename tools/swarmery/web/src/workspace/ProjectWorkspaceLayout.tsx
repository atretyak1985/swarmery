// Project-workspace layout (fusion phase 4): the frame of project mode. The one
// Sidebar (components/Sidebar.tsx — Canvas v3) scoped to this project, with
// the ProjectSwitcher on top; a StatusBar at the bottom, and an <Outlet/> for
// the active tab. The nine places come from lib/nav.ts, shared with the fleet
// shell; pages a place absorbs (Serena / Graphify / Architecture under
// Knowledge, Board / Playbooks / Planning under Plans, Retro under Health) stay
// routed and highlight their place — see the route table in main.tsx.
//
// One board query lives here (useBoard) and is shared with the Board page and
// the StatusBar through WorkspaceBoardContext, so the card, the counts, and the
// drawer never diverge. Wrapped fleet pages scope to the project via the global
// ScopeContext, which ProjectContext drives from the :slug — no page is forked.

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { MobileNav, Sidebar, useSidebarSignals } from '../components/Sidebar';
import { useFillRoute } from '../lib/fillRoute';
import {
  TerminalDock,
  emptyDock,
  openWorktreeTerminal,
  toggleDock,
  type DockState,
} from '../terminal/TerminalDock';
import { ProjectWorkspaceProvider, useProjectWorkspace } from './ProjectContext';
import { StatusBar } from './StatusBar';
import { boardCounts } from './boardModel';
import { useBoard, type BoardState } from './useBoard';

const WorkspaceBoardContext = createContext<BoardState | null>(null);

/** The shared board state for the current workspace. Throws if used outside a
 * ProjectWorkspaceLayout (a wiring bug, surfaced loudly). */
export function useWorkspaceBoard(): BoardState {
  const ctx = useContext(WorkspaceBoardContext);
  if (ctx === null) throw new Error('useWorkspaceBoard must be used inside ProjectWorkspaceLayout');
  return ctx;
}

/** Lets deep children (TaskModal) open a terminal in a task's worktree. Null
 * outside a workspace layout — callers guard on it before rendering the action. */
const WorkspaceTerminalContext = createContext<((taskLabel: string, worktreePath: string) => void) | null>(null);

/** The "open a worktree terminal" callback, or null when unavailable. */
export function useWorkspaceTerminal(): ((taskLabel: string, worktreePath: string) => void) | null {
  return useContext(WorkspaceTerminalContext);
}

// Per-project persistence of the dock (open + tabs + active). Height/font live
// in TerminalDock's own keys; this stores the tab set so a reload restores it.
function dockKey(slug: string): string {
  return `swarmery.term.dock.${slug}`;
}
function loadDock(slug: string): DockState {
  try {
    const raw = localStorage.getItem(dockKey(slug));
    if (raw === null) return emptyDock();
    const parsed = JSON.parse(raw) as DockState;
    if (!Array.isArray(parsed.tabs)) return emptyDock();
    return parsed;
  } catch {
    return emptyDock();
  }
}

/** Sub-path of the active workspace tab (e.g. "/board"), for the switcher to
 * preserve across a project switch. "" when on the Overview index. */
function activeSubPath(pathname: string, slug: string): string {
  const prefix = `/p/${slug}`;
  const rest = pathname.startsWith(prefix) ? pathname.slice(prefix.length) : '';
  // Only keep a known first segment; deep ids (e.g. /sessions/123) collapse to
  // the tab root so switching projects lands on a valid scoped list.
  const seg = rest.split('/').filter(Boolean)[0];
  return seg === undefined ? '' : `/${seg}`;
}

function WorkspaceInner(): JSX.Element {
  const { slug, projectId, project } = useProjectWorkspace();
  const { pathname } = useLocation();
  const board = useBoard(projectId);
  // Does the active route own its vertical scroll? Declared on the route itself
  // (main.tsx `handle: { fill: true }`), never matched on the pathname here.
  const fill = useFillRoute();
  const { inboxCount, liveSessions } = useSidebarSignals();

  const counts = useMemo(() => boardCounts(board.tasks), [board.tasks]);
  const subPath = activeSubPath(pathname, slug);

  // Terminal dock state — restored per project from localStorage and re-seeded
  // when the selected project changes.
  const [dock, setDock] = useState<DockState>(() => loadDock(slug));
  useEffect(() => {
    setDock(loadDock(slug));
  }, [slug]);
  useEffect(() => {
    localStorage.setItem(dockKey(slug), JSON.stringify(dock));
  }, [slug, dock]);

  const projectPath = project?.path ?? '';
  // StatusBar toggle: opens a first project-root terminal if none exist, else
  // just flips the dock's visibility.
  const toggleTerminal = useCallback(() => {
    setDock((prev) => toggleDock(prev, projectPath));
  }, [projectPath]);
  const openWorktree = useCallback((taskLabel: string, worktreePath: string) => {
    setDock((prev) => openWorktreeTerminal(prev, taskLabel, worktreePath));
  }, []);

  return (
    <WorkspaceBoardContext.Provider value={board}>
    <WorkspaceTerminalContext.Provider value={openWorktree}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1">
          <Sidebar slug={slug} subPath={subPath} inboxCount={inboxCount} liveSessions={liveSessions} />

          <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
            {/* Mobile tab strip (the desktop rail is hidden < desk). */}
            <MobileNav slug={slug} variant="strip" />
            {/* Fill routes (lib/fillRoute.ts) own their own scroll — this
                container hands it over. Every other route keeps the
                byte-identical scroller it has always had. */}
            <div
              // The scroll-handoff container in THIS shell (the outer <main> is
              // unconditionally overflow-hidden here). Same marker as the global
              // shell so one harness assertion covers both — scripts/screenshot.mjs.
              data-shell-scroller=""
              className={
                fill
                  ? 'flex min-h-0 flex-1 flex-col overflow-hidden'
                  : 'flex min-h-0 flex-1 flex-col overflow-y-auto'
              }
            >
              <Outlet />
            </div>
          </main>
        </div>
        <TerminalDock projectSlug={slug} projectPath={projectPath} state={dock} onChange={setDock} />
        <StatusBar
          counts={counts}
          projectId={project?.id ?? null}
          terminalOpen={dock.open && dock.tabs.length > 0}
          onToggleTerminal={projectPath === '' ? undefined : toggleTerminal}
        />
      </div>
    </WorkspaceTerminalContext.Provider>
    </WorkspaceBoardContext.Provider>
  );
}

export function ProjectWorkspaceLayout(): JSX.Element {
  return (
    <ProjectWorkspaceProvider>
      <WorkspaceInner />
    </ProjectWorkspaceProvider>
  );
}
