// Legacy /projects/:id → /p/:slug redirect (fusion phase 4): keeps old deep
// links (and the Projects list's existing row links) working after project mode
// lands. Resolves the numeric id against the shared project store, then
// navigates to the workspace. While the store is still loading it renders the
// old ProjectDetail so the link is never broken and never bounces to
// /p/undefined; an id that matches no known project also falls back to it.

import { useLingui } from '@lingui/react/macro';
import { Navigate, useParams } from 'react-router-dom';
import { displaySlug } from '../lib/projectSlug';
import { useScope } from '../lib/scope';
import { ProjectDetail } from '../pages/ProjectDetail';
import { Loading } from '../components/ui';

export function ProjectDetailRedirect(): JSX.Element {
  const { t } = useLingui();
  const { id } = useParams<{ id: string }>();
  const { projects } = useScope();

  const numericId = id !== undefined ? Number.parseInt(id, 10) : NaN;
  const match = projects.find((p) => p.id === numericId);

  if (match !== undefined) {
    return <Navigate to={`/p/${displaySlug(match, projects)}`} replace />;
  }
  // Not resolved yet (empty store) OR unknown id → keep the old detail view.
  return projects.length === 0 ? <Loading label={t`project…`} /> : <ProjectDetail />;
}
