import { lazy, StrictMode, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { App } from './App';
import { Loading } from './components/ui';
import { Approvals } from './pages/Approvals';
import { Overview } from './pages/Overview';
import { Sessions } from './pages/Sessions';
import { SessionDetailPage } from './pages/SessionDetail';
import { Docs } from './pages/Docs';
import { System } from './pages/System';
import './index.css';

// Analytics pulls in Recharts — lazy-load it so that weight stays out of the
// initial bundle (only fetched when the route is visited).
const Analytics = lazy(() => import('./pages/Analytics').then((m) => ({ default: m.Analytics })));

const router = createBrowserRouter([
  {
    path: '/',
    element: <App />,
    children: [
      { index: true, element: <Overview /> },
      { path: 'approvals', element: <Approvals /> },
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
      { path: 'system', element: <System /> },
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
    <RouterProvider router={router} />
  </StrictMode>,
);
