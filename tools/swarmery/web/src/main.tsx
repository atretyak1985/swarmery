import { lazy, StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import {
  createBrowserRouter,
  isRouteErrorResponse,
  Link,
  Outlet,
  RouterProvider,
  useRouteError,
} from 'react-router-dom';
import { App } from './App';
import { TooltipLayer } from './components/Tooltip';
import { PageSearchProvider } from './lib/pageSearch';
import { ProjectColorProvider } from './lib/projectColors';
import { ScopeProvider } from './lib/scope';
import { ThemeProvider } from './lib/theme';
import { UsageDataProvider } from './lib/usageData';
import { Loading } from './components/ui';
import { Approvals } from './pages/Approvals';
import { Decisions } from './pages/Decisions';
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
import './index.css';

// Analytics pulls in Recharts — lazy-load it so that weight stays out of the
// initial bundle (only fetched when the route is visited).
const Analytics = lazy(() => import('./pages/Analytics').then((m) => ({ default: m.Analytics })));

// Retro follows the same lazy pattern — fetched only when visited.
const Retro = lazy(() => import('./pages/Retro').then((m) => ({ default: m.Retro })));

// Agent Hub (fusion phase 17) — lazy like Analytics/Retro so the fleet initial
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
const Board = lazy(() => import('./pages/Board').then((m) => ({ default: m.Board })));
const ProjectOverview = lazy(() =>
  import('./pages/ProjectOverview').then((m) => ({ default: m.ProjectOverview })),
);
const ProjectSettings = lazy(() =>
  import('./pages/ProjectSettings').then((m) => ({ default: m.ProjectSettings })),
);
const Plans = lazy(() => import('./pages/Plans').then((m) => ({ default: m.Plans })));
const PlanningMode = lazy(() =>
  import('./pages/PlanningMode').then((m) => ({ default: m.PlanningMode })),
);
const Playbooks = lazy(() => import('./pages/Playbooks').then((m) => ({ default: m.Playbooks })));
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
          { index: true, element: <Overview /> },
          { path: 'approvals', element: <Approvals /> },
          { path: 'sessions', element: <Sessions /> },
          { path: 'sessions/:id', element: <SessionDetailPage /> },
          { path: 'projects', element: <Projects /> },
          // Legacy detail route → redirect into project-workspace mode.
          { path: 'projects/:id', element: <ProjectDetailRedirect /> },
          {
            path: 'analytics',
            element: (
              <Suspense fallback={<Loading label="analytics…" />}>
                <Analytics />
              </Suspense>
            ),
          },
          {
            path: 'retro',
            element: (
              <Suspense fallback={<Loading label="retro…" />}>
                <Retro />
              </Suspense>
            ),
          },
          // Decision classifier (learning-loop phase 9) — per-question stats.
          { path: 'decisions', element: <Decisions /> },
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
          { index: true, element: ws(<ProjectOverview />) },
          { path: 'board', element: ws(<Board />) },
          { path: 'planning', element: ws(<PlanningMode />) },
          { path: 'plans', element: ws(<Plans />) },
          { path: 'playbooks', element: ws(<Playbooks />) },
          { path: 'sessions', element: <Sessions /> },
          { path: 'sessions/:id', element: <SessionDetailPage /> },
          { path: 'approvals', element: ws(<Approvals />) },
          {
            path: 'analytics',
            element: (
              <Suspense fallback={<Loading label="analytics…" />}>
                <Analytics />
              </Suspense>
            ),
          },
          {
            path: 'retro',
            element: (
              <Suspense fallback={<Loading label="retro…" />}>
                <Retro />
              </Suspense>
            ),
          },
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
