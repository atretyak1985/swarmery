// The global project-scope control, as a filter chip ("● all projects ▾").
//
// It used to be a shell-level dropdown pinned above the sidebar nav: one
// control, rendered on every screen including the ones it could not filter.
// Now it lives in the filter row of each page that actually reads
// `useScope().scope`, beside that page's own chips — same context, same
// behaviour, just placed where its effect is visible.
//
// FLEET ROUTES ONLY. Inside the project workspace (/p/<slug>/…) the URL IS the
// project filter and ProjectWorkspaceProvider pins the scope to it, so a chip
// there could only desync the page from its own route. The `slug` route param
// is the discriminator — undefined on every fleet route — and the guard lives
// HERE rather than at each call site so seven pages cannot get it wrong
// seven different ways.
//
// Because ProjectWorkspaceProvider pins the scope on every /p/<slug> visit and
// never unpins it, this chip is also the only way back to "all projects" — which
// is exactly why it belongs on every scope-filtered page, not just one.

import { useLingui } from '@lingui/react/macro';
import { useParams } from 'react-router-dom';
import { useScope } from '../lib/scope';
import { ProjectDropdown } from './ProjectDropdown';

export function ScopeChip(): JSX.Element | null {
  const { slug } = useParams<{ slug?: string }>();
  const { scope, setScope, projects } = useScope();
  const { t } = useLingui();
  if (slug !== undefined) return null;
  return (
    <ProjectDropdown
      projects={projects}
      value={scope}
      onChange={setScope}
      allLabel={t`All projects`}
      groupByTag
    />
  );
}
