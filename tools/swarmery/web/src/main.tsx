import { lazy, StrictMode, Suspense, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  createBrowserRouter,
  isRouteErrorResponse,
  Link,
  Navigate,
  Outlet,
  RouterProvider,
  useLocation,
  useParams,
  useRouteError,
} from 'react-router-dom';
import { App } from './App';
import { TooltipLayer } from './components/Tooltip';
import { PageSearchProvider } from './lib/pageSearch';
import { ProjectColorProvider } from './lib/projectColors';
import { ScopeProvider, useScope } from './lib/scope';
import { ThemeProvider } from './lib/theme';
import { UsageDataProvider } from './lib/usageData';
import { Loading } from './components/ui';
import { Approvals } from './pages/Approvals';
import { Inbox } from './pages/inbox/Inbox';
import { Overview } from './pages/Overview';
import { Projects } from './pages/Projects';
import { Sessions } from './pages/Sessions';
import { SessionDetailPage } from './pages/SessionDetail';
import { Settings } from './pages/Settings';
import { Docs } from './pages/Docs';
import { Architecture } from './pages/Architecture';
import { Serena } from './pages/Serena';
import { Graphify } from './pages/Graphify';
import { ProjectDetailRedirect } from './workspace/ProjectDetailRedirect';
import { Routines } from './pages/Routines';
import { Today } from './pages/today/Today';
import './index.css';

// Health (Canvas v3 phase 5) absorbs /analytics and /retro as tabs. Lazy, and
// it lazy-loads those two pages itself (Analytics pulls in Recharts), so their
// weight stays out of the initial bundle.
const Health = lazy(() => import('./pages/health/Health').then((m) => ({ default: m.Health })));

// Learning (Canvas v3 phase 6) absorbs /lessons and /decisions as tabs. Lazy
// like Health; one component serves /learning and /p/:slug/learning.
const Learning = lazy(() =>
  import('./pages/learning/Learning').then((m) => ({ default: m.Learning })),
);

// Agent Hub (fusion phase 17) — lazy like Health so the fleet initial
// bundle stays unchanged. Serves both /agents (fleet) and /p/:slug/agents.
const AgentHub = lazy(() => import('./pages/AgentHub').then((m) => ({ default: m.AgentHub })));

// System Hub (fusion phase 18) — the catalog grouped by ROLE on the same
// HubShell. Lazy like the Agent Hub; serves /system-hub and /p/:slug/system-hub.
const SystemHub = lazy(() => import('./pages/SystemHub').then((m) => ({ default: m.SystemHub })));

// System shell — the single "System" destination hosting Agents/Toolkit/Hooks/
// Insights as tabs (embeds AgentHub + SystemHub). Lazy like the hubs it wraps;
// serves /system(/:tab) and /p/:slug/system(/:tab).
const SystemShell = lazy(() =>
  import('./pages/SystemShell').then((m) => ({ default: m.SystemShell })),
);

// Project-workspace mode (/p/:slug/…) is a whole subtree — lazy-load it so the
// fleet-mode initial bundle is unchanged (board/drawer weight loads on demand).
const WorkspaceShell = lazy(() =>
  import('./workspace/WorkspaceShell').then((m) => ({ default: m.WorkspaceShell })),
);
const ProjectOverview = lazy(() =>
  import('./pages/ProjectOverview').then((m) => ({ default: m.ProjectOverview })),
);
const ProjectSettings = lazy(() =>
  import('./pages/ProjectSettings').then((m) => ({ default: m.ProjectSettings })),
);
// Plans place: New plan · Plans · Board · Playbooks (each tab body lazy inside).
const PlansPlace = lazy(() =>
  import('./pages/plans/PlansPlace').then((m) => ({ default: m.PlansPlace })),
);
const Memory = lazy(() => import('./pages/Memory').then((m) => ({ default: m.Memory })));
const ScopedSerena = lazy(() =>
  import('./workspace/ScopedPages').then((m) => ({ default: m.ScopedSerena })),
);
const ScopedGraphify = lazy(() =>
  import('./workspace/ScopedPages').then((m) => ({ default: m.ScopedGraphify })),
);
const ScopedArchitecture = lazy(() =>
  import('./workspace/ScopedPages').then((m) => ({ default: m.ScopedArchitecture })),
);

/** Pathless root layout: shared providers (project scope + palette colors) for
 * BOTH the fleet App and the project-workspace shell, so they read one store.
 *
 * UsageDataProvider belongs HERE, not in App.tsx: the fleet shell and the
 * project-workspace shell are SIBLING routes under this layout, so a provider
 * mounted inside App.tsx would leave the workspace header's chip without a
 * poller (and re-poll from scratch on every mode switch). One mount here is what
 * makes "exactly one /api/usage poller app-wide" true. */
function RootProviders(): JSX.Element {
  return (
    <ScopeProvider>
      <PageSearchProvider>
        <UsageDataProvider>
          <Outlet />
        </UsageDataProvider>
        {/* One themed tooltip layer for the whole app — every `data-tip=` in
            any route draws through this node (see components/Tooltip.tsx). */}
        <TooltipLayer />
      </PageSearchProvider>
    </ScopeProvider>
  );
}

/** Suspense boundary for a lazy workspace route element. */
function ws(node: JSX.Element): JSX.Element {
  return <Suspense fallback={<Loading label="workspace…" />}>{node}</Suspense>;
}

/** /p/:slug/approvals → the project Inbox's approvals tab. Waits for the global
 * scope to settle on this project first: on a cold visit ProjectWorkspaceProvider
 * calls setScope(slug) in the same commit, and its setSearchParams (built from the
 * pre-redirect query) would otherwise overwrite ?tab=approvals with ?scope=. */
function ProjectApprovalsRedirect(): JSX.Element | null {
  const { slug = '' } = useParams<{ slug: string }>();
  const { scope } = useScope();
  if (scope !== slug) return null;
  return <Navigate to={`/p/${slug}/inbox?tab=approvals`} replace />;
}

/** /p/:slug/{analytics,retro} → a Health tab. Same scope wait as
 * ProjectApprovalsRedirect, or the provider's ?scope= write drops ?tab=. */
function ProjectHealthRedirect({ tab }: { tab: 'cost' | 'agents' }): JSX.Element | null {
  const { slug = '' } = useParams<{ slug: string }>();
  const { scope } = useScope();
  if (scope !== slug) return null;
  return <Navigate to={`/p/${slug}/health?tab=${tab}`} replace />;
}

/** /p/:slug/{planning,board,playbooks} → a Plans tab, keeping the rest of the
 * query (PlanningMode consumes ?idea=). Same scope wait as ProjectHealthRedirect. */
function ProjectPlansRedirect({ tab }: { tab: 'new' | 'board' | 'playbooks' }): JSX.Element | null {
  const { slug = '' } = useParams<{ slug: string }>();
  const { search } = useLocation();
  // Snapshot the query of the first render: the workspace provider's
  // setScope rewrites the URL to ?scope=<slug> before scope settles, which
  // would drop ?idea= (PlanningMode's hand-off) from a later `search`.
  const [initialSearch] = useState(search);
  const { scope } = useScope();
  if (scope !== slug) return null;
  const q = new URLSearchParams(initialSearch);
  q.delete('scope');
  q.set('tab', tab);
  return <Navigate to={`/p/${slug}/plans?${q.toString()}`} replace />;
}

/** Route-level error boundary. Without one, react-router replaces the whole SPA
 * with its default error screen — recoverable only by pressing Back — for any
 * unmatched path. That is reachable from ordinary content: lib/markdown.tsx
 * renders model-written text (chat, handoff briefs, plan docs, memory), and a
 * model can emit a link to a path this app does not route. Keep the shell. */
function RouteError(): JSX.Element {
  const error = useRouteError();
  const status = isRouteErrorResponse(error) ? error.status : null;
  return (
    <div className="px-6 py-16 text-center">
      <div className="font-mono text-[11px] tracking-[0.14em] text-ink-faint uppercase">
        {status === 404 ? 'not found' : 'something broke'}
      </div>
      <p className="mt-2 text-[13px] text-ink-dim">
        {status === 404
          ? 'That link does not point anywhere in this dashboard.'
          : 'This view failed to render.'}
      </p>
      <Link to="/" className="mt-4 inline-block font-mono text-[11px] text-brand hover:underline">
        ← back to the overview
      </Link>
    </div>
  );
}

const router = createBrowserRouter([
  {
    element: <RootProviders />,
    errorElement: <RouteError />,
    children: [
      {
        path: '/',
        element: <App />,
        children: [
          // Today (Canvas v3 phase 4): the loop map over the old command deck.
          { index: true, element: <Today detail={<Overview />} /> },
          // Inbox (Canvas v3 phase 3) replaces /approvals as the place; the old
          // page keeps rules + history at approvals/manage.
          { path: 'inbox', element: <Inbox />, handle: { fill: true } },
          { path: 'approvals', element: <Navigate to="/inbox?tab=approvals" replace /> },
          { path: 'approvals/manage', element: <Approvals /> },
          { path: 'sessions', element: <Sessions /> },
          { path: 'sessions/:id', element: <SessionDetailPage /> },
          { path: 'projects', element: <Projects /> },
          // Legacy detail route → redirect into project-workspace mode.
          { path: 'projects/:id', element: <ProjectDetailRedirect /> },
          // Health (Canvas v3 phase 5): the retired pages land on their tab.
          {
            path: 'health',
            element: (
              <Suspense fallback={<Loading label="health…" />}>
                <Health />
              </Suspense>
            ),
          },
          { path: 'analytics', element: <Navigate to="/health?tab=cost" replace /> },
          { path: 'retro', element: <Navigate to="/health?tab=agents" replace /> },
          // Learning (Canvas v3 phase 6): the retired pages land on their tab.
          {
            path: 'learning',
            element: (
              <Suspense fallback={<Loading label="learning…" />}>
                <Learning />
              </Suspense>
            ),
          },
          { path: 'lessons', element: <Navigate to="/learning?tab=lessons" replace /> },
          { path: 'decisions', element: <Navigate to="/learning?tab=classifier" replace /> },
          // Agent Hub — roster (/agents) + selected agent (/agents/:id). One
          // component serves both; the :id is the selected registry agent.
          {
            path: 'agents',
            element: (
              <Suspense fallback={<Loading label="agents…" />}>
                <AgentHub />
              </Suspense>
            ),
          },
          {
            path: 'agents/:id',
            element: (
              <Suspense fallback={<Loading label="agents…" />}>
                <AgentHub />
              </Suspense>
            ),
          },
          // System Hub — catalog roster (/system-hub), a category
          // (/system-hub/:category) and a selected item
          // (/system-hub/:category/:id). One component serves all three.
          {
            path: 'system-hub',
            element: (
              <Suspense fallback={<Loading label="system…" />}>
                <SystemHub />
              </Suspense>
            ),
          },
          {
            path: 'system-hub/:category',
            element: (
              <Suspense fallback={<Loading label="system…" />}>
                <SystemHub />
              </Suspense>
            ),
          },
          {
            path: 'system-hub/:category/:id',
            element: (
              <Suspense fallback={<Loading label="system…" />}>
                <SystemHub />
              </Suspense>
            ),
          },
          // System — single destination, tabs Agents/Toolkit/Hooks/Insights.
          // Splat so the shell can own /system/:tab (+ /system/agents/:id).
          {
            path: 'system/*',
            element: (
              <Suspense fallback={<Loading label="system…" />}>
                <SystemShell />
              </Suspense>
            ),
          },
          {
            path: 'system',
            element: (
              <Suspense fallback={<Loading label="system…" />}>
                <SystemShell />
              </Suspense>
            ),
          },
          { path: 'routines', element: <Routines /> },
          // The fill handle below marks embedded pages: the shell stops
          // scrolling and the page fills the leftover height, scrolling inside
          // its own pane instead of under a second scrollbar (lib/fillRoute.ts).
          // Document-shaped routes (Planning included) stay unmarked.
          { path: 'serena', element: <Serena />, handle: { fill: true } },
          { path: 'graphify', element: <Graphify />, handle: { fill: true } },
          { path: 'architecture', element: <Architecture />, handle: { fill: true } },
          // Global settings (session mode): appearance + notifications +
          // auto-approve note + daemon/health. Project settings stays scoped at
          // /p/:slug/settings — do NOT add this to the project subtree.
          { path: 'settings', element: <Settings /> },
          { path: 'docs', element: <Docs />, handle: { fill: true } },
          { path: 'docs/:slug', element: <Docs />, handle: { fill: true } },
        ],
      },
      {
        // Project-workspace mode: its own shell (header + rescoped sidebar +
        // status bar), lazy-loaded. Nothing moves OUT of fleet mode — these
        // routes WRAP the same APIs scoped to :slug.
        path: '/p/:slug',
        element: ws(<WorkspaceShell />),
        children: [
          { index: true, element: ws(<Today detail={ws(<ProjectOverview />)} />) },
          { path: 'plans', element: ws(<PlansPlace />) },
          { path: 'planning', element: <ProjectPlansRedirect tab="new" /> },
          { path: 'board', element: <ProjectPlansRedirect tab="board" /> },
          { path: 'playbooks', element: <ProjectPlansRedirect tab="playbooks" /> },
          { path: 'sessions', element: <Sessions /> },
          { path: 'sessions/:id', element: <SessionDetailPage /> },
          { path: 'inbox', element: ws(<Inbox />), handle: { fill: true } },
          { path: 'approvals', element: <ProjectApprovalsRedirect /> },
          { path: 'approvals/manage', element: ws(<Approvals />) },
          { path: 'health', element: ws(<Health />) },
          { path: 'analytics', element: <ProjectHealthRedirect tab="cost" /> },
          { path: 'retro', element: <ProjectHealthRedirect tab="agents" /> },
          { path: 'learning', element: ws(<Learning />) },
          // Agent Hub, project-scoped (rollups narrowed to :slug via the route).
          { path: 'agents', element: ws(<AgentHub />) },
          { path: 'agents/:id', element: ws(<AgentHub />) },
          // System Hub, project-scoped (EFFECTIVE view: enabled packs + project
          // overrides; template resolution + rollups narrowed to :slug).
          { path: 'system-hub', element: ws(<SystemHub />) },
          { path: 'system-hub/:category', element: ws(<SystemHub />) },
          { path: 'system-hub/:category/:id', element: ws(<SystemHub />) },
          // System shell (tabs), project-scoped — the workspace "System" item.
          { path: 'system', element: ws(<SystemShell />) },
          { path: 'system/*', element: ws(<SystemShell />) },
          // Fill mode, project-scoped — same contract as the global trio above.
          { path: 'architecture', element: ws(<ScopedArchitecture />), handle: { fill: true } },
          { path: 'serena', element: ws(<ScopedSerena />), handle: { fill: true } },
          { path: 'graphify', element: ws(<ScopedGraphify />), handle: { fill: true } },
          { path: 'settings', element: ws(<ProjectSettings />) },
          { path: 'memory', element: ws(<Memory />) },
        ],
      },
    ],
  },
]);

const rootEl = document.getElementById('root');
if (!rootEl) {
  throw new Error('missing #root element');
}

createRoot(rootEl).render(
  <StrictMode>
    <ThemeProvider>
      <ProjectColorProvider>
        <RouterProvider router={router} />
      </ProjectColorProvider>
    </ThemeProvider>
  </StrictMode>,
);
