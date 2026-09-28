// The nav model's two contracts:
//
//   1. hrefs per scope — fleet (slug null) vs project, including the
//      project-only places' All-projects fallback through the last project.
//   2. coverage — every path routed in src/main.tsx, in BOTH shells, is claimed
//      by exactly one place. The route table is read from the source file
//      itself, so adding a route without extending a place's segments fails
//      here instead of rendering a page with no highlighted sidebar row.
//
// Dev-only suite (web/tsconfig.json excludes *.test.ts). Run with
//   npx vitest run src/lib/nav.test.ts
// after `npm i --no-save vitest`.

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { PLACES, activePlace, placeSegment, placesIn, resolvePlaceHref, type PlaceId } from './nav';

function place(id: PlaceId) {
  const found = PLACES.find((p) => p.id === id);
  if (found === undefined) throw new Error(`no place ${id}`);
  return found;
}

describe('nav model', () => {
  it('defines exactly the ten places, grouped main / improve / bottom', () => {
    expect(PLACES.map((p) => p.id)).toEqual([
      'today',
      'inbox',
      'sessions',
      'plans',
      'health',
      'learning',
      'knowledge',
      'docs',
      'system',
      'settings',
    ]);
    expect(placesIn('main').map((p) => p.label)).toEqual(['Today', 'Inbox', 'Sessions', 'Plans']);
    expect(placesIn('improve').map((p) => p.label)).toEqual(['Health', 'Learning', 'Knowledge']);
    expect(placesIn('bottom').map((p) => p.label)).toEqual(['Docs', 'System', 'Settings']);
    expect(PLACES.filter((p) => p.projectOnly).map((p) => p.id)).toEqual(['plans', 'knowledge']);
  });

  it('builds hrefs per scope', () => {
    const cases: [PlaceId, string, string][] = [
      ['today', '/', '/p/shop'],
      ['inbox', '/inbox', '/p/shop/inbox'],
      ['sessions', '/sessions', '/p/shop/sessions'],
      ['plans', '/projects', '/p/shop/plans'],
      ['health', '/health', '/p/shop/health'],
      ['learning', '/learning', '/p/shop/learning'],
      ['knowledge', '/projects', '/p/shop/knowledge'],
      // Docs is fleet-wide: a project scope does not change where it goes.
      ['docs', '/docs', '/docs'],
      ['system', '/system', '/p/shop/system'],
      ['settings', '/settings', '/p/shop/settings'],
    ];
    for (const [id, fleet, project] of cases) {
      expect(place(id).href(null), `${id} fleet`).toBe(fleet);
      expect(place(id).href('shop'), `${id} project`).toBe(project);
    }
  });

  it('resolves project-only places through the last project under All projects', () => {
    expect(resolvePlaceHref(place('plans'), null, 'shop')).toBe('/p/shop/plans');
    expect(resolvePlaceHref(place('knowledge'), null, 'shop')).toBe('/p/shop/knowledge');
    expect(resolvePlaceHref(place('plans'), null, null)).toBe('/projects');
    // In a project, the last project is irrelevant.
    expect(resolvePlaceHref(place('plans'), 'cart', 'shop')).toBe('/p/cart/plans');
    // Fleet places never detour through the last project.
    expect(resolvePlaceHref(place('sessions'), null, 'shop')).toBe('/sessions');
  });

  it('reads the first segment after an optional /p/:slug prefix', () => {
    expect(placeSegment('/')).toBe('');
    expect(placeSegment('/p/shop')).toBe('');
    expect(placeSegment('/p/shop/plans/12')).toBe('plans');
    expect(placeSegment('/system-hub/skills/x')).toBe('system-hub');
  });
});

/** Every `path: '…'` literal in the router table, as concrete pathnames. */
function routedPaths(): string[] {
  const source = readFileSync(new URL('../main.tsx', import.meta.url), 'utf8');
  const literals = [...source.matchAll(/path: '([^']+)'/g)].map((m) => m[1] ?? '');
  expect(literals.length).toBeGreaterThan(30); // the regex still sees the table
  const concrete = (p: string): string => p.replace(/:[a-zA-Z]+/g, 'x').replace(/\*/g, 'x');
  const out = new Set<string>(['/', '/p/shop']); // the two index routes
  for (const lit of literals) {
    if (lit === '/' || lit === '/p/:slug') continue;
    // Child paths are relative; check each under both shells.
    out.add(`/${concrete(lit)}`);
    out.add(`/p/shop/${concrete(lit)}`);
  }
  return [...out];
}

describe('nav coverage of src/main.tsx', () => {
  it('claims every routed path with exactly one place', () => {
    for (const pathname of routedPaths()) {
      const owners = PLACES.filter((p) => p.match(pathname)).map((p) => p.id);
      expect(owners, pathname).toHaveLength(1);
    }
  });

  it('maps absorbed pages onto their place', () => {
    const cases: [string, PlaceId][] = [
      ['/retro', 'health'],
      ['/decisions', 'learning'],
      ['/p/shop/board', 'plans'],
      ['/p/shop/playbooks', 'plans'],
      ['/p/shop/planning', 'plans'],
      ['/p/shop/serena', 'knowledge'],
      ['/docs/guide', 'docs'],
      ['/routines', 'system'],
      ['/agents/7', 'system'],
      ['/projects', 'settings'],
      ['/sessions/42', 'sessions'],
    ];
    for (const [pathname, id] of cases) expect(activePlace(pathname)?.id, pathname).toBe(id);
  });
});
