// Project-workspace layout (fusion phase 4): the frame of project mode. A left
// sidebar rescoped to ONE project (Overview, Board, Plans, Sessions,
// Architecture, Serena*, Graphify*, Retro, Analytics, Settings — *only when the
// tool is provisioned, reusing the /api/tools feed like the global sidebar), a
// ProjectSwitcher on top, a StatusBar at the bottom, and an <Outlet/> for the
// active tab. Later phases (Planning, Epics, Memory, Agent Hub) hang new routes
// into this same frame — see the route table in main.tsx.
//
// One board query lives here (useBoard) and is shared with the Board page and
// the StatusBar through WorkspaceBoardContext, so the card, the counts, and the
// drawer never diverge. Wrapped fleet pages scope to the project via the global
// ScopeContext, which ProjectContext drives from the :slug — no page is forked.

import { createContext, useContext, useEffect, useMemo, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { fetchTools } from '../api';
import { useScope } from '../lib/scope';
import { ProjectWorkspaceProvider, useProjectWorkspace } from './ProjectContext';
import { ProjectSwitcher } from './ProjectSwitcher';
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

interface WorkspaceNavItem {
  /** Sub-path relative to /p/:slug ("" = index/Overview). */
  path: string;
  glyph: string;
  label: string;
}

const BASE_NAV: WorkspaceNavItem[] = [
  { path: '', glyph: '◉', label: 'Overview' },
  { path: 'board', glyph: '▤', label: 'Board' },
  { path: 'planning', glyph: '✦', label: 'Planning' },
  { path: 'plans', glyph: '❐', label: 'Plans' },
  { path: 'playbooks', glyph: '▤', label: 'Playbooks' },
  { path: 'sessions', glyph: '❯', label: 'Sessions' },
  { path: 'architecture', glyph: '▦', label: 'Architecture' },
  { path: 'memory', glyph: '❖', label: 'Memory' },
];
const INSIGHT_NAV: WorkspaceNavItem[] = [
  { path: 'retro', glyph: '↺', label: 'Retro' },
  { path: 'analytics', glyph: '▦', label: 'Analytics' },
];
const SETTINGS_NAV: WorkspaceNavItem = { path: 'settings', glyph: '⚙', label: 'Settings' };
const SERENA_NAV: WorkspaceNavItem = { path: 'serena', glyph: '◎', label: 'Serena' };
const GRAPHIFY_NAV: WorkspaceNavItem = { path: 'graphify', glyph: '⬡', label: 'Graphify' };

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
  const { projects } = useScope();
  const { pathname } = useLocation();
  const board = useBoard(projectId);

  // Tool nav gating: this project has serena / graphify provisioned? Poll the
  // same /api/tools feed the global sidebar uses (60s), matched by slug.
  const [hasSerena, setHasSerena] = useState(false);
  const [hasGraphify, setHasGraphify] = useState(false);
  useEffect(() => {
    let disposed = false;
    const poll = (): void => {
      fetchTools()
        .then((t) => {
          if (disposed) return;
          setHasSerena(t.serena.available && t.serena.projects.some((p) => p.slug === slug));
          setHasGraphify(t.graphify.projects.some((p) => p.slug === slug && p.hasViz));
        })
        .catch(() => {
          if (disposed) return;
          setHasSerena(false);
          setHasGraphify(false);
        });
    };
    poll();
    const timer = setInterval(poll, 60_000);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, [slug]);

  const counts = useMemo(() => boardCounts(board.tasks), [board.tasks]);
  const subPath = activeSubPath(pathname, slug);

  const toolNav: WorkspaceNavItem[] = [
    ...(hasSerena ? [SERENA_NAV] : []),
    ...(hasGraphify ? [GRAPHIFY_NAV] : []),
  ];

  return (
    <WorkspaceBoardContext.Provider value={board}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1">
          <nav className="hidden w-[228px] shrink-0 flex-col border-r border-line px-3 py-3 desk:flex">
            <ProjectSwitcher projects={projects} currentSlug={slug} subPath={subPath} />
            <div className="mt-4 flex flex-col gap-0.5">
              {BASE_NAV.map((item) => (
                <WorkspaceLink key={item.path} slug={slug} item={item} />
              ))}
            </div>
            {toolNav.length > 0 && (
              <>
                <NavGroupLabel>Tools</NavGroupLabel>
                <div className="flex flex-col gap-0.5">
                  {toolNav.map((item) => (
                    <WorkspaceLink key={item.path} slug={slug} item={item} />
                  ))}
                </div>
              </>
            )}
            <NavGroupLabel>Insights</NavGroupLabel>
            <div className="flex flex-col gap-0.5">
              {INSIGHT_NAV.map((item) => (
                <WorkspaceLink key={item.path} slug={slug} item={item} />
              ))}
            </div>
            <div className="mt-auto pt-3">
              <WorkspaceLink slug={slug} item={SETTINGS_NAV} />
            </div>
          </nav>

          <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
            {/* Mobile tab strip (the desktop rail is hidden < desk). */}
            <div className="flex gap-1 overflow-x-auto border-b border-line px-3 py-2 desk:hidden">
              {[...BASE_NAV, ...toolNav, ...INSIGHT_NAV, SETTINGS_NAV].map((item) => (
                <WorkspaceLink key={item.path} slug={slug} item={item} compact />
              ))}
            </div>
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
              <Outlet />
            </div>
          </main>
        </div>
        <StatusBar counts={counts} projectId={project?.id ?? null} />
      </div>
    </WorkspaceBoardContext.Provider>
  );
}

function NavGroupLabel({ children }: { children: string }): JSX.Element {
  return (
    <div className="mt-4 mb-1 px-3 font-mono text-[10px] font-medium tracking-[0.14em] text-ink-faint uppercase">
      {children}
    </div>
  );
}

function WorkspaceLink({
  slug,
  item,
  compact = false,
}: {
  slug: string;
  item: WorkspaceNavItem;
  compact?: boolean;
}): JSX.Element {
  const to = item.path === '' ? `/p/${slug}` : `/p/${slug}/${item.path}`;
  if (compact) {
    return (
      <NavLink
        to={to}
        end={item.path === ''}
        className={({ isActive }) =>
          `flex shrink-0 items-center gap-1.5 rounded-lg border px-2.5 py-1 font-mono text-[11px] whitespace-nowrap transition-colors ${
            isActive ? 'border-line-strong bg-surface2 text-brand' : 'border-transparent text-ink-dim'
          }`
        }
      >
        <span aria-hidden="true">{item.glyph}</span>
        {item.label}
      </NavLink>
    );
  }
  return (
    <NavLink
      to={to}
      end={item.path === ''}
      className={({ isActive }) =>
        `flex h-[34px] items-center gap-3 rounded-[10px] border px-3 transition-colors ${
          isActive
            ? 'border-line-strong bg-surface2 text-brand'
            : 'border-transparent text-ink-dim hover:bg-surface2/50 hover:text-ink'
        }`
      }
    >
      <span className="w-[16px] shrink-0 text-center text-[15px] leading-none" aria-hidden="true">
        {item.glyph}
      </span>
      <span className="truncate text-[13px] font-medium">{item.label}</span>
    </NavLink>
  );
}

export function ProjectWorkspaceLayout(): JSX.Element {
  return (
    <ProjectWorkspaceProvider>
      <WorkspaceInner />
    </ProjectWorkspaceProvider>
  );
}
