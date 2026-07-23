import { lazy, StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { App } from './App';
import { ProjectColorProvider } from './lib/projectColors';
import { ThemeProvider } from './lib/theme';
import { Loading } from './components/ui';
import { Approvals } from './pages/Approvals';
import { Overview } from './pages/Overview';
import { Projects } from './pages/Projects';
import { ProjectDetail } from './pages/ProjectDetail';
import { Sessions } from './pages/Sessions';
import { SessionDetailPage } from './pages/SessionDetail';
import { Docs } from './pages/Docs';
import { Architecture } from './pages/Architecture';
import { Serena } from './pages/Serena';
import { Graphify } from './pages/Graphify';
import { System } from './pages/System';
import './index.css';

// Analytics pulls in Recharts — lazy-load it so that weight stays out of the
// initial bundle (only fetched when the route is visited).
const Analytics = lazy(() => import('./pages/Analytics').then((m) => ({ default: m.Analytics })));

// Retro follows the same lazy pattern — fetched only when visited.
const Retro = lazy(() => import('./pages/Retro').then((m) => ({ default: m.Retro })));

const router = createBrowserRouter([
  {
    path: '/',
    element: <App />,
    children: [
      { index: true, element: <Overview /> },
      { path: 'approvals', element: <Approvals /> },
      { path: 'sessions', element: <Sessions /> },
      { path: 'sessions/:id', element: <SessionDetailPage /> },
      { path: 'projects', element: <Projects /> },
      { path: 'projects/:id', element: <ProjectDetail /> },
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
      { path: 'serena', element: <Serena /> },
      { path: 'graphify', element: <Graphify /> },
      { path: 'architecture', element: <Architecture /> },
      { path: 'docs', element: <Docs /> },
      { path: 'docs/:slug', element: <Docs /> },
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
