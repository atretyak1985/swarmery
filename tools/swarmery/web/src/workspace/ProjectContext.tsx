// Project-workspace context (fusion phase 4): the currently-selected project
// for the /p/:slug/… workspace mode. Resolves the URL :slug against the shared
// ScopeContext project list (no extra fetch) and exposes {slug, project,
// projectId} to every workspace surface. The layout ALSO pushes the slug into
// the global scope via useScope().setScope, so wrapped fleet pages
// (Sessions/Analytics/Retro) that read useScope().scope transparently filter
// to this project without being forked.

import { createContext, useContext, useEffect, useMemo, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import type { Project } from '../api/types';
import { saveLastProject } from '../lib/lastProject';
import { findProject } from '../lib/projectSlug';
import { useScope } from '../lib/scope';

interface ProjectWorkspaceValue {
  /** The :slug route param — the pretty name slug, or a legacy path slug on
   * old deep links. Reuse it verbatim when building sibling /p/ links. */
  slug: string;
  /** Resolved project row, or null while the scope store is still loading /
   * the slug matches no known project. */
  project: Project | null;
  /** Numeric project id for the board/dispatch APIs; null until resolved. */
  projectId: number | null;
  /** True while the shared project list is still being fetched (so callers can
   * distinguish "loading" from "unknown slug"). */
  loading: boolean;
}

const ProjectWorkspaceContext = createContext<ProjectWorkspaceValue>({
  slug: '',
  project: null,
  projectId: null,
  loading: true,
});

export function ProjectWorkspaceProvider({ children }: { children: ReactNode }): JSX.Element {
  const { slug = '' } = useParams<{ slug: string }>();
  const { projects, setScope, scope } = useScope();

  const project = useMemo(() => findProject(projects, slug), [projects, slug]);

  // Drive the global scope to this project so wrapped fleet pages filter to it.
  // Only writes when it actually differs to avoid a setState/URL loop with the
  // ScopeProvider's own ?scope= sync.
  useEffect(() => {
    if (slug !== '' && scope !== slug) setScope(slug);
  }, [slug, scope, setScope]);

  // Remember the last-visited project so the sidebar's project-only places
  // (Plans, Knowledge) reopen it under All projects. Only the raw :slug is
  // persisted (never a placeholder).
  useEffect(() => {
    if (slug !== '') saveLastProject(slug);
  }, [slug]);

  const value = useMemo<ProjectWorkspaceValue>(
    () => ({
      slug,
      project,
      projectId: project?.id ?? null,
      loading: projects.length === 0,
    }),
    [slug, project, projects.length],
  );

  return (
    <ProjectWorkspaceContext.Provider value={value}>{children}</ProjectWorkspaceContext.Provider>
  );
}

export function useProjectWorkspace(): ProjectWorkspaceValue {
  return useContext(ProjectWorkspaceContext);
}
