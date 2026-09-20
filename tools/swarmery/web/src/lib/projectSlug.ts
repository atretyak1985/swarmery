// Pretty project slugs (URL layer). The DB `slug` is the path-encoded identity
// (`/Volumes/Work/swarmery` → `-Volumes-Work-swarmery`, produced by
// ingest.SlugForPath, which encodes '/' ONLY) and stays the ingest/API identity
// everywhere. It is NOT the ~/.claude/projects directory name: that encoding
// (internal/claudeproj.Slug) also maps '.' → '-', so the two diverge on any path
// with a dot segment. Nothing here may adopt that encoding — project identity and
// every dashboard URL are keyed on the DB slug.
// URLs and ?scope= carry a DISPLAY slug derived from the project NAME instead
// ("My App" → "my-app"), which the server's scope predicate also matches
// (internal/api/scope.go projectMatchExpr — slugifyName must mirror its SQL
// `lower(replace(name, ' ', '-'))`). Legacy path-slug deep links keep working:
// every resolver here accepts both forms.

/** The minimal project shape the slug helpers need (full Project qualifies,
 * as do stats/DTO rows that carry id+slug+name). */
export interface ProjectLike {
  id: number;
  slug: string;
  name: string | null;
}

/** Kebab-case a display name the same way the server predicate does. */
export function slugifyName(name: string): string {
  return name.toLowerCase().replaceAll(' ', '-');
}

/**
 * The slug to put in URLs for a project: the kebab-cased name, falling back to
 * the path slug when the name is empty or ambiguous (another project shares
 * the kebab name, or the kebab name shadows another project's path slug).
 */
export function displaySlug(project: ProjectLike, projects: readonly ProjectLike[]): string {
  const name = project.name ?? '';
  if (name === '') return project.slug;
  const pretty = slugifyName(name);
  const clash = projects.some(
    (p) =>
      p.id !== project.id && (slugifyName(p.name ?? '') === pretty || p.slug === pretty),
  );
  return clash ? project.slug : pretty;
}

/**
 * Resolve a URL slug / scope value (pretty, legacy path slug, or numeric id)
 * to a project. Path slug wins so legacy links stay exact; a name match only
 * resolves when it is unambiguous.
 */
export function findProject<T extends ProjectLike>(
  projects: readonly T[],
  key: string | null,
): T | null {
  if (key === null || key === '') return null;
  const byPath = projects.find((p) => p.slug === key);
  if (byPath !== undefined) return byPath;
  const byId = projects.find((p) => String(p.id) === key);
  if (byId !== undefined) return byId;
  const lower = key.toLowerCase();
  const byName = projects.filter((p) => slugifyName(p.name ?? '') === lower);
  return byName.length === 1 && byName[0] !== undefined ? byName[0] : null;
}
