// Single source of truth for project accent colors across the whole app.
//
// `projectColor(slug)` alone can only hash one slug at a time, so two unrelated
// projects can still land on similar hues. This provider fetches the full
// project list once and hands every screen the same guaranteed-distinct map
// (evenly-spaced hues by index, see `projectColorMap`). Any slug not in the
// list — a deep-linked or freshly-created project — falls back to the per-slug
// hash so it still gets a stable color.

import { createContext, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { fetchProjects } from '../api';
import { projectColor, projectColorMap } from './colors';

export type ColorForSlug = (slug: string) => string;

// Default (no provider / before the fetch resolves): per-slug hash.
const ProjectColorContext = createContext<ColorForSlug>(projectColor);

export function ProjectColorProvider({ children }: { children: ReactNode }): JSX.Element {
  const [slugs, setSlugs] = useState<readonly string[]>([]);

  useEffect(() => {
    // Include archived so their colors stay distinct too, and so the set — and
    // therefore every project's assigned color — is stable regardless of which
    // screen is open.
    fetchProjects(true)
      .then((list) => setSlugs(list.map((p) => p.slug)))
      .catch(() => setSlugs([])); // unreachable → components fall back to hash
  }, []);

  const colorFor = useMemo<ColorForSlug>(() => {
    const map = projectColorMap(slugs);
    return (slug) => map.get(slug) ?? projectColor(slug);
  }, [slugs]);

  return <ProjectColorContext.Provider value={colorFor}>{children}</ProjectColorContext.Provider>;
}

export function useProjectColor(): ColorForSlug {
  return useContext(ProjectColorContext);
}
