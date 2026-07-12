import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { App } from './App';
import { Overview } from './pages/Overview';
import { Sessions } from './pages/Sessions';
import { SessionDetailPage } from './pages/SessionDetail';
import './index.css';

const router = createBrowserRouter([
  {
    path: '/',
    element: <App />,
    children: [
      { index: true, element: <Overview /> },
      { path: 'sessions', element: <Sessions /> },
      { path: 'sessions/:id', element: <SessionDetailPage /> },
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
