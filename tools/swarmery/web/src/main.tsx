import { lazy, StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, Outlet, RouterProvider } from 'react-router-dom';
import { App } from './App';
import { ProjectColorProvider } from './lib/projectColors';
import { ScopeProvider } from './lib/scope';
import { ThemeProvider } from './lib/theme';
import { Loading } from './components/ui';
import { Approvals } from './pages/Approvals';
import { Overview } from './pages/Overview';
import { Projects } from './pages/Projects';
import { Sessions } from './pages/Sessions';
import { SessionDetailPage } from './pages/SessionDetail';
import { Docs } from './pages/Docs';
import { Architecture } from './pages/Architecture';
import { Serena } from './pages/Serena';
import { Graphify } from './pages/Graphify';
import { System } from './pages/System';
import { ProjectDetailRedirect } from './workspace/ProjectDetailRedirect';
import { Routines } from './pages/Routines';
import './index.css';

// Analytics pulls in Recharts — lazy-load it so that weight stays out of the
// initial bundle (only fetched when the route is visited).
const Analytics = lazy(() => import('./pages/Analytics').then((m) => ({ default: m.Analytics })));

// Retro follows the same lazy pattern — fetched only when visited.
const Retro = lazy(() => import('./pages/Retro').then((m) => ({ default: m.Retro })));

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
const PlansPlaceholder = lazy(() =>
  import('./pages/PlansPlaceholder').then((m) => ({ default: m.PlansPlaceholder })),
);
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
 * BOTH the fleet App and the project-workspace shell, so they read one store. */
function RootProviders(): JSX.Element {
  return (
    <ScopeProvider>
      <Outlet />
    </ScopeProvider>
  );
}

/** Suspense boundary for a lazy workspace route element. */
function ws(node: JSX.Element): JSX.Element {
  return <Suspense fallback={<Loading label="workspace…" />}>{node}</Suspense>;
}

const router = createBrowserRouter([
  {
    element: <RootProviders />,
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
          { path: 'system', element: <System /> },
          { path: 'routines', element: <Routines /> },
          { path: 'serena', element: <Serena /> },
          { path: 'graphify', element: <Graphify /> },
          { path: 'architecture', element: <Architecture /> },
          { path: 'docs', element: <Docs /> },
          { path: 'docs/:slug', element: <Docs /> },
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
          { path: 'plans', element: ws(<PlansPlaceholder />) },
          { path: 'sessions', element: <Sessions /> },
          { path: 'sessions/:id', element: <SessionDetailPage /> },
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
          { path: 'architecture', element: ws(<ScopedArchitecture />) },
          { path: 'serena', element: ws(<ScopedSerena />) },
          { path: 'graphify', element: ws(<ScopedGraphify />) },
          { path: 'settings', element: ws(<ProjectSettings />) },
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
