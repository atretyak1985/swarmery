// @vitest-environment jsdom
//
// plansUrl — the Plans place's URL scheme (plans-deep-links phase 1). Every row
// of the plan README's URL table is built, matched against the REAL route paths
// with react-router's own matcher (so the router's decoding is in the loop, not
// a hand-rolled split), parsed back and rebuilt: build → parse → build must be
// the identity for every canonical target.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/plans/plansUrl.test.ts`.
// web/tsconfig.json EXCLUDES *.test.ts, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { matchRoutes } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import {
  PLANS_ROUTE_PATHS,
  parsePlansRoute,
  plansHref,
  plansPath,
  plansTaskHref,
  sameHref,
  type ParsedPlansRoute,
  type PlansTarget,
} from './plansUrl';

const SLUG = 'swarmery';
const EXT = '2026-09-30-plans-deep-links';

/** The same route tree main.tsx mounts, for the router's own matcher. */
const ROUTES = [{ path: '/p/:slug', children: PLANS_ROUTE_PATHS.map((path) => ({ path })) }];

/** Match an href against the real child routes under /p/:slug (the router's
 * ranking AND its path decoding) and parse it. */
function parseHref(href: string): ParsedPlansRoute {
  const url = new URL(href, 'http://localhost');
  const matches = matchRoutes(ROUTES, url.pathname);
  expect(matches).not.toBeNull();
  const leaf = matches![matches!.length - 1]!;
  expect(leaf.params.slug).toBe(SLUG);
  return parsePlansRoute(leaf.params, url.search);
}

/** The canonical target a parse describes (what the page would render). */
function targetOf(r: ParsedPlansRoute): PlansTarget {
  const t: PlansTarget = { plan: r.plan };
  if (r.status !== null) t.status = r.status;
  if (r.detail !== null) t.detail = r.detail;
  return t;
}

// The README's URL table, row by row.
const ROWS: { name: string; target: PlansTarget; href: string }[] = [
  { name: 'plan list, Active (default)', target: { plan: null }, href: `/p/${SLUG}/plans` },
  { name: 'filter Done, no plan', target: { plan: null, status: 'done' }, href: `/p/${SLUG}/plans?status=done` },
  {
    name: 'filter Archived, no plan',
    target: { plan: null, status: 'archived' },
    href: `/p/${SLUG}/plans?status=archived`,
  },
  { name: 'plan selected', target: { plan: EXT }, href: `/p/${SLUG}/plans/${EXT}` },
  {
    name: 'phase drawer (Story)',
    target: { plan: EXT, detail: { kind: 'phase', seq: 3, tab: 'story' } },
    href: `/p/${SLUG}/plans/${EXT}/phase/3`,
  },
  ...(['criteria', 'runs', 'report', 'edit'] as const).map((tab) => ({
    name: `phase drawer tab ${tab}`,
    target: { plan: EXT, detail: { kind: 'phase' as const, seq: 3, tab } },
    href: `/p/${SLUG}/plans/${EXT}/phase/3/${tab}`,
  })),
  ...(['plan', 'spec', 'summary', 'revisions', 'edit'] as const).map((tab) => ({
    name: `plan details tab ${tab}`,
    target: { plan: EXT, detail: { kind: 'plan' as const, tab } },
    href: `/p/${SLUG}/plans/${EXT}/details/${tab}`,
  })),
  {
    name: 'one revision',
    target: { plan: EXT, detail: { kind: 'plan', tab: 'revisions', revId: 42 } },
    href: `/p/${SLUG}/plans/${EXT}/details/revisions/42`,
  },
];

describe('plansPath — every URL-table row', () => {
  for (const row of ROWS) {
    it(`builds ${row.name}`, () => {
      expect(plansPath(SLUG, row.target)).toBe(row.href);
    });
    it(`round-trips ${row.name} (build → parse → build)`, () => {
      const parsed = parseHref(plansPath(SLUG, row.target));
      expect(parsed.invalidTab).toBe(false);
      expect(parsed.legacy).toBeNull();
      expect(plansPath(SLUG, targetOf(parsed))).toBe(row.href);
    });
  }

  it('omits ?status= for the default filter and whenever a plan is named', () => {
    expect(plansPath(SLUG, { plan: null, status: 'active' })).toBe(`/p/${SLUG}/plans`);
    expect(plansPath(SLUG, { plan: EXT, status: 'done' })).toBe(`/p/${SLUG}/plans/${EXT}`);
  });

  it('drops a revId on any tab but revisions', () => {
    expect(plansPath(SLUG, { plan: EXT, detail: { kind: 'plan', tab: 'plan', revId: 7 } })).toBe(
      `/p/${SLUG}/plans/${EXT}/details/plan`,
    );
  });
});

describe('externalId encoding', () => {
  const odd = 'plan with spaces/and#hash?q=1&x';

  it('encodes the externalId as one path segment', () => {
    const href = plansPath(SLUG, { plan: odd });
    expect(href).toBe(`/p/${SLUG}/plans/${encodeURIComponent(odd)}`);
    expect(href.split('/')).toHaveLength(5); // '', p, slug, plans, <one segment>
  });

  it('decodes back to the same externalId through the router matcher', () => {
    const href = plansPath(SLUG, { plan: odd, detail: { kind: 'phase', seq: 2, tab: 'runs' } });
    const parsed = parseHref(href);
    expect(parsed.plan).toBe(odd);
    expect(parsed.detail).toEqual({ kind: 'phase', seq: 2, tab: 'runs' });
    expect(plansPath(SLUG, targetOf(parsed))).toBe(href);
  });
});

describe('plansHref — merges the current search', () => {
  it('keeps ?scope= on every navigation', () => {
    expect(plansHref(SLUG, { plan: EXT }, '?scope=swarmery')).toBe(`/p/${SLUG}/plans/${EXT}?scope=swarmery`);
    expect(plansHref(SLUG, { plan: null, status: 'done' }, '?scope=swarmery')).toBe(
      `/p/${SLUG}/plans?scope=swarmery&status=done`,
    );
  });

  it('never touches PlansPlace’s own ?tab=new|board|playbooks', () => {
    for (const tab of ['new', 'board', 'playbooks']) {
      expect(plansHref(SLUG, { plan: null }, `?tab=${tab}`)).toBe(`/p/${SLUG}/plans?tab=${tab}`);
    }
  });

  it('drops every param it owns: status, task, plan, phase and tab=revisions', () => {
    expect(
      plansHref(
        SLUG,
        { plan: EXT, detail: { kind: 'plan', tab: 'revisions' } },
        '?scope=s&task=12&plan=12&phase=3&tab=revisions&status=done&keep=1',
      ),
    ).toBe(`/p/${SLUG}/plans/${EXT}/details/revisions?scope=s&keep=1`);
  });

  it('re-sets ?status= from the target, replacing a stale one', () => {
    expect(plansHref(SLUG, { plan: null, status: 'archived' }, '?status=done')).toBe(
      `/p/${SLUG}/plans?status=archived`,
    );
    expect(plansHref(SLUG, { plan: null, status: 'active' }, '?status=done')).toBe(`/p/${SLUG}/plans`);
  });

  it('with no current search equals plansPath', () => {
    for (const row of ROWS) expect(plansHref(SLUG, row.target)).toBe(plansPath(SLUG, row.target));
  });
});

describe('parsePlansRoute — invalid segments degrade to the nearest valid level', () => {
  it('an unknown phase tab reads as Story and is flagged', () => {
    const r = parseHref(`/p/${SLUG}/plans/${EXT}/phase/3/bogus`);
    expect(r.detail).toEqual({ kind: 'phase', seq: 3, tab: 'story' });
    expect(r.invalidTab).toBe(true);
    expect(plansPath(SLUG, targetOf(r))).toBe(`/p/${SLUG}/plans/${EXT}/phase/3`);
  });

  it('an unknown plan-details tab reads as Plan and is flagged', () => {
    const r = parseHref(`/p/${SLUG}/plans/${EXT}/details/bogus`);
    expect(r.detail).toEqual({ kind: 'plan', tab: 'plan' });
    expect(r.invalidTab).toBe(true);
  });

  it('a non-numeric seq drops the phase level', () => {
    const r = parseHref(`/p/${SLUG}/plans/${EXT}/phase/abc`);
    expect(r.detail).toBeNull();
    expect(r.invalidTab).toBe(true);
    expect(plansPath(SLUG, targetOf(r))).toBe(`/p/${SLUG}/plans/${EXT}`);
  });

  it('a non-numeric revId drops the revision level, keeping Revisions', () => {
    const r = parseHref(`/p/${SLUG}/plans/${EXT}/details/revisions/abc`);
    expect(r.detail).toEqual({ kind: 'plan', tab: 'revisions' });
    expect(r.invalidTab).toBe(true);
  });

  it('an unknown ?status= reads as absent', () => {
    expect(parseHref(`/p/${SLUG}/plans?status=bogus`).status).toBeNull();
  });
});

describe('parsePlansRoute — the legacy numeric hand-off', () => {
  it('?task=<id> alone', () => {
    expect(parseHref(`/p/${SLUG}/plans?task=12`).legacy).toEqual({
      taskId: 12,
      phaseSeq: null,
      wantRevisions: false,
    });
  });

  it('?plan=<id> is read the same way', () => {
    // Built from parts on purpose: no producer may WRITE the legacy `?plan=`
    // form any more (phase 3 greps src for it and expects nothing), but old
    // links and bookmarks still arrive in it.
    const legacyPlanLink = `/p/${SLUG}/plans` + '?plan=12&phase=3';
    expect(parseHref(legacyPlanLink).legacy).toEqual({
      taskId: 12,
      phaseSeq: 3,
      wantRevisions: false,
    });
  });

  it('&tab=revisions asks for the Revisions tab', () => {
    expect(parseHref(`/p/${SLUG}/plans?task=12&tab=revisions&scope=s`).legacy).toEqual({
      taskId: 12,
      phaseSeq: null,
      wantRevisions: true,
    });
  });

  it('a non-numeric id parses as NaN (resolves to nothing)', () => {
    expect(Number.isNaN(parseHref(`/p/${SLUG}/plans?task=abc`).legacy?.taskId)).toBe(true);
  });

  it('is ignored once a plan is named in the path', () => {
    expect(parseHref(`/p/${SLUG}/plans/${EXT}?task=12`).legacy).toBeNull();
  });

  it('the resolved canonical href drops the hand-off and keeps the rest', () => {
    expect(
      plansHref(SLUG, { plan: EXT, detail: { kind: 'phase', seq: 3, tab: 'story' } }, '?task=12&phase=3&scope=s'),
    ).toBe(`/p/${SLUG}/plans/${EXT}/phase/3?scope=s`);
  });
});

describe('plansTaskHref — the numeric hand-off producers write', () => {
  it('builds ?task=<id>, with &phase= and &tab=revisions only when asked', () => {
    expect(plansTaskHref(SLUG, 12)).toBe(`/p/${SLUG}/plans?task=12`);
    expect(plansTaskHref(SLUG, 12, { phase: 3 })).toBe(`/p/${SLUG}/plans?task=12&phase=3`);
    expect(plansTaskHref(SLUG, 12, { revisions: true })).toBe(`/p/${SLUG}/plans?task=12&tab=revisions`);
    expect(plansTaskHref(SLUG, 12, { revisions: false })).toBe(`/p/${SLUG}/plans?task=12`);
  });

  it('parses back to the same legacy hand-off through the router matcher', () => {
    expect(parseHref(plansTaskHref(SLUG, 12)).legacy).toEqual({ taskId: 12, phaseSeq: null, wantRevisions: false });
    expect(parseHref(plansTaskHref(SLUG, 12, { phase: 3, revisions: true })).legacy).toEqual({
      taskId: 12,
      phaseSeq: 3,
      wantRevisions: true,
    });
  });
});

describe('sameHref', () => {
  it('treats encoding differences as the same location', () => {
    expect(sameHref('/p/s/plans/a%7Eb', '/p/s/plans/a~b')).toBe(true);
    expect(sameHref('/p/s/plans/a', '/p/s/plans/b')).toBe(false);
    expect(sameHref('/p/s/plans/%E0%A4%A', '/p/s/plans/%E0%A4%A')).toBe(true); // malformed, equal
  });
});
