// The single navigation model (Canvas v3, artboards 2a/2b): nine places in one
// sidebar, shared by the fleet shell (App.tsx), the project shell
// (workspace/ProjectWorkspaceLayout.tsx) and both mobile navs. It replaces the
// two per-shell nav arrays and the Sessions/Projects mode toggle.
//
// A place is a DESTINATION, not a route: each one claims every page it absorbs
// (Health owns /analytics and /retro, Knowledge owns memory/architecture/serena/
// graphify/docs, …), so the sidebar highlights the right row on every routed
// path. `match` is checked against the path with any `/p/:slug` prefix removed,
// so one table serves both shells. nav.test.ts asserts every path routed in
// main.tsx is claimed by exactly one place — add a route, extend a `segments`.
//
// hrefs point at today's routes; later phases repoint them (Inbox → /inbox,
// Health → /health, Learning → /learning, Knowledge → /p/:slug/knowledge).

export type PlaceId =
  | 'today'
  | 'inbox'
  | 'sessions'
  | 'plans'
  | 'health'
  | 'learning'
  | 'knowledge'
  | 'system'
  | 'settings';

export type PlaceSection = 'main' | 'improve' | 'bottom';

export interface Place {
  id: PlaceId;
  glyph: string;
  label: string;
  section: PlaceSection;
  /** Only meaningful inside one project (Plans, Knowledge): under All projects
   * the row renders dimmed and resolves through the last-visited project. */
  projectOnly: boolean;
  /** Target for a scope — `slug` null = All projects (the fleet shell). */
  href: (slug: string | null) => string;
  /** Does this place own `pathname` (fleet `/x` or project `/p/:slug/x`)? */
  match: (pathname: string) => boolean;
}

/** First path segment after an optional `/p/:slug` prefix; "" for the index. */
export function placeSegment(pathname: string): string {
  const parts = pathname.split('/').filter(Boolean);
  const rest = parts[0] === 'p' ? parts.slice(2) : parts;
  return rest[0] ?? '';
}

interface PlaceDef {
  id: PlaceId;
  glyph: string;
  label: string;
  section: PlaceSection;
  projectOnly: boolean;
  /** Sub-path of the place's landing page ("" = the scope's index). */
  path: string;
  /** The place is fleet-only today: its href ignores the project scope. */
  fleetOnly?: boolean;
  /** First path segments this place owns ("" = the index route). */
  segments: readonly string[];
}

const DEFS: readonly PlaceDef[] = [
  { id: 'today', glyph: '◉', label: 'Today', section: 'main', projectOnly: false, path: '', segments: [''] },
  { id: 'inbox', glyph: '☐', label: 'Inbox', section: 'main', projectOnly: false, path: 'approvals', segments: ['approvals'] },
  { id: 'sessions', glyph: '❯', label: 'Sessions', section: 'main', projectOnly: false, path: 'sessions', segments: ['sessions'] },
  {
    id: 'plans',
    glyph: '❐',
    label: 'Plans',
    section: 'main',
    projectOnly: true,
    path: 'plans',
    segments: ['planning', 'plans', 'board', 'playbooks'],
  },
  { id: 'health', glyph: '♡', label: 'Health', section: 'improve', projectOnly: false, path: 'analytics', segments: ['analytics', 'retro'] },
  {
    id: 'learning',
    glyph: '✎',
    label: 'Learning',
    section: 'improve',
    projectOnly: false,
    path: 'lessons',
    fleetOnly: true,
    segments: ['lessons', 'decisions'],
  },
  {
    id: 'knowledge',
    glyph: '❖',
    label: 'Knowledge',
    section: 'improve',
    projectOnly: true,
    path: 'memory',
    segments: ['memory', 'architecture', 'serena', 'graphify', 'docs'],
  },
  {
    id: 'system',
    glyph: '☷',
    label: 'System',
    section: 'bottom',
    projectOnly: false,
    path: 'system',
    segments: ['system', 'system-hub', 'agents', 'routines'],
  },
  { id: 'settings', glyph: '⚙', label: 'Settings', section: 'bottom', projectOnly: false, path: 'settings', segments: ['settings', 'projects'] },
];

function toPlace(def: PlaceDef): Place {
  return {
    id: def.id,
    glyph: def.glyph,
    label: def.label,
    section: def.section,
    projectOnly: def.projectOnly,
    href: (slug) => {
      // A project-only place has no fleet page: without a project it lands on
      // the project list, where the operator picks one.
      if (slug === null && def.projectOnly) return '/projects';
      if (slug === null || def.fleetOnly === true) return def.path === '' ? '/' : `/${def.path}`;
      return def.path === '' ? `/p/${slug}` : `/p/${slug}/${def.path}`;
    },
    match: (pathname) => def.segments.includes(placeSegment(pathname)),
  };
}

export const PLACES: readonly Place[] = DEFS.map(toPlace);

/** Places of one sidebar section, in table order. */
export function placesIn(section: PlaceSection): readonly Place[] {
  return PLACES.filter((p) => p.section === section);
}

/** The place owning `pathname`, or null for an unrouted path. */
export function activePlace(pathname: string): Place | null {
  return PLACES.find((p) => p.match(pathname)) ?? null;
}

/**
 * Where a sidebar row navigates. Under All projects (`slug` null) a
 * project-only place resolves through the last-visited project, so the row
 * keeps working instead of disappearing; with no project ever opened it falls
 * back to the project list (`place.href(null)`).
 */
export function resolvePlaceHref(place: Place, slug: string | null, lastProject: string | null): string {
  if (slug === null && place.projectOnly && lastProject !== null) return place.href(lastProject);
  return place.href(slug);
}
