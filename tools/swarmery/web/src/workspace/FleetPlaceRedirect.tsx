// Plans and Knowledge belong to a project, so All projects has no page of its own
// for them. /plans(/…) and /knowledge are still real addresses (a typed URL, a link
// written by a model, a bookmark from before the project scope), and they land on
// the last-visited project's place, keeping the rest of the path and the query. With
// no project ever opened they land on the project list, where one gets picked.

import { Navigate, useLocation } from 'react-router-dom';
import { loadLastProject } from '../lib/lastProject';

export function FleetPlaceRedirect(): JSX.Element {
  const { pathname, search } = useLocation();
  const last = loadLastProject();
  if (last === null) return <Navigate to="/projects" replace />;
  return <Navigate to={`/p/${encodeURIComponent(last)}${pathname}${search}`} replace />;
}
