// @vitest-environment jsdom
//
// Plans deep links (plans-deep-links phase 1): the URL is the Plans place's
// selection. These tests mount the REAL route tree main.tsx mounts —
// PLANS_ROUTE_PATHS under /p/:slug, each rendering <PlansPlace/> — in a data
// router (createMemoryRouter + RouterProvider), and drive it the way an
// operator does: open a URL, click, press Back (`router.navigate(-1)`).
//
// What only a mounted router can prove, and so what is asserted here:
//   - a URL opens exactly the state it names (SC-1, SC-2, SC-3);
//   - every click is ONE history entry and Back retraces them (SC-4);
//   - every automatic correction is a REPLACE — Back from its result leaves the
//     Plans place instead of bouncing (SC-5), including the legacy numeric
//     `?task=`/`?plan=` hand-off, which must also keep `?scope=` (SC-7);
//   - the Active/Done/Archived filter lives in the URL (SC-6);
//   - moving between the child routes re-renders the page, it does not remount
//     it (the epics are fetched once for a whole click-and-Back sequence).
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/Plans.deepLink.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { act, cleanup, configure, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createBrowserRouter, createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PlanRevision } from '../api/types';
import { PlansPlace } from './plans/PlansPlace';
import { PLANS_ROUTE_PATHS } from './plans/plansUrl';

// The Plans page mounts the whole PlansPlace tree, and on a loaded machine its
// first render can outlast Testing Library's 1 s default, so every waitFor and
// findBy* in this file gets a longer budget. Still under vitest's 5 s test
// timeout. Vitest isolates each test file, so this doesn't leak into others.
configure({ asyncUtilTimeout: 3000 });

// The project follows the URL's :slug, as the real ProjectWorkspaceProvider
// does — so a /p/<slug> change switches projectId on the very next render while
// Plans stays mounted. `a` is an alias of project 3 (the default), `b` is 4.
const PROJECT_IDS = vi.hoisted((): Record<string, number> => ({ swarmery: 3, a: 3, b: 4 }));
vi.mock('../workspace/ProjectContext', async () => {
  const { useParams } = await import('react-router-dom');
  return {
    useProjectWorkspace: () => {
      const { slug = 'swarmery' } = useParams<{ slug: string }>();
      const id = PROJECT_IDS[slug] ?? 3;
      return { slug, project: { id, slug, name: slug }, projectId: id, loading: false };
    },
  };
});

// The live socket is not part of any claim here.
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

// Markdown pulls mermaid/katex; none of it is under test and all of it is slow.
vi.mock('../lib/markdown', () => ({
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));

const BASE = '/p/swarmery/plans';

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
  id: 1,
  seq: 1,
  name: 'phase',
  docPath: '/ws/plan/phase-1.md',
  docRelPath: 'phase-1.md',
  dependsOn: [],
  checkboxesDone: 0,
  checkboxesTotal: 3,
  docStatus: null,
  docUpdatedAt: null,
  completionReport: null,
  activatedAt: null,
  boardTaskExternalId: null,
  boardTaskId: null,
  boardColumn: null,
  runState: 'idle',
  runSessionUuid: null,
  runModel: null,
  runModels: [],
  runModelFellBack: false,
  docModel: null,
  runStartedAt: null,
  runError: null,
  runOutcome: 'idle',
  runEndedAt: null,
  runCheckboxesBefore: null,
  verifyMode: 'off',
  verifyVerdict: null,
  verifyDetail: null,
  completionState: 'incomplete',
  completionBlockers: [],
  ...over,
});

function epic(taskId: number, externalId: string, title: string, over: Partial<Epic> = {}): Epic {
  return {
    taskId,
    externalId,
    projectId: 3,
    projectSlug: 'swarmery',
    title,
    status: 'active',
    startedAt: null,
    planDir: `/ws/${externalId}/plan`,
    hasSummary: false,
    hasSpec: false,
    spec: null,
    phases: [1, 2, 3].map((seq) =>
      phase({ id: taskId * 10 + seq, seq, name: `${title} ${String(seq)}`, docRelPath: `phase-${String(seq)}.md` }),
    ),
    rollup: { done: 0, total: 9, pct: 0, incompletePhases: 3 },
    planRun: null,
    cardExternalId: null,
    linkedSessions: [],
    ...over,
  };
}

const A = epic(1, '2026-09-01-plan-alpha', 'Alpha');
const B = epic(2, '2026-09-02-plan-beta', 'Beta');
const DONE = epic(3, '2026-08-01-done-plan', 'Shipped', {
  status: 'done',
  phases: [phase({ id: 31, seq: 1, name: 'Shipped 1', checkboxesDone: 3, completionState: 'complete' })],
  rollup: { done: 3, total: 3, pct: 100, incompletePhases: 0 },
});

const revision = (id: number, status: PlanRevision['status'], reason: string): PlanRevision => ({
  id,
  status,
  origin: 'operator_revise',
  reason,
  createdAt: '2026-09-02T10:00:00Z',
  files: [],
});
/** Beta's revisions: one staged (the "open" one) and one decided. */
const BETA_REVISIONS = [revision(41, 'staged', 'tighten phase 2'), revision(40, 'applied', 'an older change')];

let epics: Epic[] = [];
let epicFetches = 0;
/** Per-project epic lists; a project absent here gets `epics`. */
let epicsByProject: Record<number, Epic[]> = {};
/** Per-plan revision lists (by taskId); Beta (2) defaults to BETA_REVISIONS, others to []. */
let revisionsByTask: Record<number, PlanRevision[]> = {};
/** A POST /api/epics/<id>/revisions answers 409 naming this open revision. */
let openRevisionOnStart: number | null = null;
/** Fetches held open until `release(key)`: `epics:<projectId>` or `revisions:<taskId>`. */
const held = new Set<string>();
let waiting: { key: string; resolve: () => void }[] = [];

async function release(key: string): Promise<void> {
  held.delete(key);
  const go = waiting.filter((w) => w.key === key);
  waiting = waiting.filter((w) => w.key !== key);
  await act(async () => {
    for (const w of go) w.resolve();
  });
}

function stubFetch(): void {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const response = (body: unknown, status = 200): Response =>
        ({ ok: status < 400, status, json: () => Promise.resolve(body) }) as Response;
      const json = (body: unknown): Promise<Response> => Promise.resolve(response(body));
      /** Answer now, or — when `key` is held — at release, with the body as of then. */
      const gated = (key: string, body: () => unknown): Promise<Response> =>
        held.has(key)
          ? new Promise<Response>((res) => {
              waiting.push({ key, resolve: () => res(response(body())) });
            })
          : json(body());
      if (url.startsWith('/api/epics?')) {
        epicFetches += 1;
        const projectId = Number(new URLSearchParams(url.slice(url.indexOf('?'))).get('projectId'));
        return gated(`epics:${String(projectId)}`, () => epicsByProject[projectId] ?? epics);
      }
      if (url.startsWith('/api/sessions?')) return json({ sessions: [], nextCursor: null });
      const revs = /\/api\/epics\/(\d+)\/revisions$/.exec(url);
      if (revs !== null) {
        const taskId = Number(revs[1]);
        if (init?.method === 'POST') {
          return Promise.resolve(
            response({ error: 'a revision is already open', revisionId: openRevisionOnStart ?? undefined }, 409),
          );
        }
        return gated(`revisions:${String(taskId)}`, () => ({
          revisions: revisionsByTask[taskId] ?? (taskId === 2 ? BETA_REVISIONS : []),
        }));
      }
      const rev = /\/api\/revisions\/(\d+)$/.exec(url);
      if (rev !== null) {
        const all = [...BETA_REVISIONS, ...Object.values(revisionsByTask).flat()];
        const r = all.find((x) => x.id === Number(rev[1]));
        return json({ revision: r, files: [] });
      }
      if (/\/docs\?path=/.test(url)) return json({ path: 'doc.md', content: '# doc\n' });
      return json([]);
    }),
  );
}

const ROUTES = [
  { path: '/p/:slug', children: PLANS_ROUTE_PATHS.map((path) => ({ path, element: <PlansPlace /> })) },
  { path: '/elsewhere', element: <div>elsewhere</div> },
];

type Router = ReturnType<typeof createMemoryRouter>;

/** Mount at the last of `entries` (earlier ones are history to go Back into). */
function mount(...entries: string[]): Router {
  const router = createMemoryRouter(ROUTES, { initialEntries: entries, initialIndex: entries.length - 1 });
  render(<RouterProvider router={router} />);
  return router;
}

const at = (r: Router): string => `${r.state.location.pathname}${r.state.location.search}`;

async function back(r: Router): Promise<void> {
  await act(async () => {
    await r.navigate(-1);
  });
}

/** The plan-list items (the only links carrying aria-current — phase 2 made
 * them anchors). */
function listItems(): HTMLElement[] {
  return screen.getAllByRole('link').filter((b) => b.hasAttribute('aria-current'));
}
function listItem(title: string): HTMLElement {
  const item = listItems().find((b) => b.textContent?.includes(title));
  if (item === undefined) throw new Error(`no plan list item "${title}"`);
  return item;
}
function selectedPlan(): string | null {
  return listItems().find((b) => b.getAttribute('aria-current') === 'true')?.textContent ?? null;
}
function selectedTab(tablist: string): string | null {
  const list = screen.getByRole('tablist', { name: tablist });
  return within(list)
    .getAllByRole('tab')
    .find((t) => t.getAttribute('aria-selected') === 'true')?.textContent ?? null;
}
function filterTab(name: 'active' | 'done' | 'archived'): HTMLElement {
  const list = screen.getByRole('tablist', { name: 'plan status filter' });
  const tab = within(list)
    .getAllByRole('tab')
    .find((t) => t.textContent?.startsWith(name));
  if (tab === undefined) throw new Error(`no filter tab ${name}`);
  return tab;
}
function drawer(): HTMLElement | null {
  return screen.queryByRole('dialog');
}

async function settled(r: Router, href: string): Promise<void> {
  await waitFor(() => {
    expect(at(r)).toBe(href);
  });
}

beforeEach(() => {
  epics = [A, B, DONE];
  epicFetches = 0;
  epicsByProject = {};
  revisionsByTask = {};
  openRevisionOnStart = null;
  held.clear();
  waiting = [];
  localStorage.clear();
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('SC-1 — a plan URL selects that plan and its filter', () => {
  it('opens a non-first Active plan directly, with no correction', async () => {
    const r = mount(`${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    expect(selectedTab('plan status filter')).toMatch(/^active/);
    expect(at(r)).toBe(`${BASE}/${B.externalId}`);
    expect(drawer()).toBeNull();
  });

  it('opens a Done plan under the Done filter', async () => {
    const r = mount(`${BASE}/${DONE.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Shipped');
    });
    expect(selectedTab('plan status filter')).toMatch(/^done/);
    expect(at(r)).toBe(`${BASE}/${DONE.externalId}`);
  });

  it('an unknown plan falls back (replace) to the first Active plan', async () => {
    const r = mount('/elsewhere', `${BASE}/2026-01-01-gone`);
    await settled(r, `${BASE}/${A.externalId}`);
    expect(r.state.historyAction).toBe('REPLACE');
  });
});

describe('SC-2 — a phase URL opens the drawer on that tab', () => {
  it('/phase/<seq>/<tab> opens that phase on that tab', async () => {
    const r = mount(`${BASE}/${B.externalId}/phase/2/runs`);
    const d = await screen.findByRole('dialog', { name: 'Beta 2' });
    expect(within(d).getByRole('tab', { name: 'Runs' }).getAttribute('aria-selected')).toBe('true');
    expect(at(r)).toBe(`${BASE}/${B.externalId}/phase/2/runs`);
  });

  it('/phase/<seq> alone is Story', async () => {
    mount(`${BASE}/${B.externalId}/phase/3`);
    const d = await screen.findByRole('dialog', { name: 'Beta 3' });
    expect(within(d).getByRole('tab', { name: 'Story' }).getAttribute('aria-selected')).toBe('true');
  });

  it('an invalid tab string is canonicalised to Story by replace', async () => {
    const r = mount('/elsewhere', `${BASE}/${B.externalId}/phase/2/bogus`);
    await settled(r, `${BASE}/${B.externalId}/phase/2`);
    expect(r.state.historyAction).toBe('REPLACE');
    await screen.findByRole('dialog', { name: 'Beta 2' });
  });

  it('Edit on a done phase canonicalises to Report (the tab its panel falls back to)', async () => {
    const r = mount(`${BASE}/${DONE.externalId}/phase/1/edit`);
    await settled(r, `${BASE}/${DONE.externalId}/phase/1/report`);
    const d = await screen.findByRole('dialog', { name: 'Shipped 1' });
    expect(within(d).getByRole('tab', { name: 'Report' }).getAttribute('aria-selected')).toBe('true');
  });

  it('an unknown seq drops to the plan (replace)', async () => {
    const r = mount('/elsewhere', `${BASE}/${B.externalId}/phase/9`);
    await settled(r, `${BASE}/${B.externalId}`);
    expect(r.state.historyAction).toBe('REPLACE');
    expect(drawer()).toBeNull();
  });
});

describe('SC-3 — a details URL opens plan details on that tab / that revision', () => {
  it('/details/<tab> opens plan details on that tab', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/edit`);
    await waitFor(() => {
      expect(selectedTab('plan details tabs')).toBe('Edit');
    });
    expect(at(r)).toBe(`${BASE}/${B.externalId}/details/edit`);
  });

  it('/details/revisions opens the Revisions tab', async () => {
    mount(`${BASE}/${B.externalId}/details/revisions`);
    await waitFor(() => {
      expect(selectedTab('plan details tabs')).toMatch(/^Revisions/);
    });
    await screen.findByText('an older change');
    expect(document.querySelector('[aria-current="true"][data-revision-id]')).toBeNull();
  });

  it('/details/revisions/<revId> expands that decided revision', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/revisions/40`);
    await waitFor(() => {
      expect(document.querySelector('[data-revision-id="40"]')?.getAttribute('aria-current')).toBe('true');
    });
    expect(selectedTab('plan details tabs')).toMatch(/^Revisions/);
    // Expanded: its reason is no longer clamped to three lines.
    expect(screen.getByText('an older change').className).not.toContain('line-clamp-3');
    expect(document.querySelector('[data-revision-id="41"]')?.getAttribute('aria-current')).toBeNull();
    expect(at(r)).toBe(`${BASE}/${B.externalId}/details/revisions/40`);
  });

  it('/details/revisions/<revId> marks the staged (open) revision', async () => {
    mount(`${BASE}/${B.externalId}/details/revisions/41`);
    await waitFor(() => {
      expect(document.querySelector('[data-revision-id="41"]')?.getAttribute('aria-current')).toBe('true');
    });
  });

  it('an unknown revId drops to the Revisions tab (replace)', async () => {
    const r = mount('/elsewhere', `${BASE}/${B.externalId}/details/revisions/999`);
    await settled(r, `${BASE}/${B.externalId}/details/revisions`);
    expect(r.state.historyAction).toBe('REPLACE');
  });

  it('Summary on an incomplete plan canonicalises to Plan (replace)', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/summary`);
    await settled(r, `${BASE}/${B.externalId}/details/plan`);
    expect(selectedTab('plan details tabs')).toBe('Plan');
  });
});

describe('SC-4 — every click is one history entry; Back retraces them', () => {
  it('A → B → phase 3 → Runs, then Back ×3 = Story → drawer closed → A', async () => {
    const r = mount('/elsewhere', BASE);
    await settled(r, `${BASE}/${A.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Alpha');
    });

    fireEvent.click(listItem('Beta'));
    await settled(r, `${BASE}/${B.externalId}`);

    fireEvent.click(screen.getByRole('button', { name: 'open Phase 3 — Beta 3 details' }));
    await settled(r, `${BASE}/${B.externalId}/phase/3`);
    const d = await screen.findByRole('dialog', { name: 'Beta 3' });

    fireEvent.click(within(d).getByRole('tab', { name: 'Runs' }));
    await settled(r, `${BASE}/${B.externalId}/phase/3/runs`);
    expect(r.state.historyAction).toBe('PUSH');

    await back(r);
    await settled(r, `${BASE}/${B.externalId}/phase/3`);
    await waitFor(() => {
      expect(within(screen.getByRole('dialog', { name: 'Beta 3' })).getByRole('tab', { name: 'Story' }).getAttribute('aria-selected')).toBe('true');
    });

    await back(r);
    await settled(r, `${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(drawer()).toBeNull();
    });
    expect(selectedPlan()).toContain('Beta');

    await back(r);
    await settled(r, `${BASE}/${A.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Alpha');
    });

    // One more Back leaves the Plans place: the /plans → A correction replaced,
    // it did not push.
    await back(r);
    await settled(r, '/elsewhere');
  });

  it('closing the drawer and ↑/↓ stepping are one entry each', async () => {
    const r = mount(`${BASE}/${B.externalId}/phase/2/criteria`);
    const d = await screen.findByRole('dialog', { name: 'Beta 2' });
    fireEvent.keyDown(document, { key: 'ArrowDown' }); // Drawer listens on document
    await settled(r, `${BASE}/${B.externalId}/phase/3/criteria`);
    expect(d).toBeTruthy();
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Beta 3' })).getByRole('button', { name: 'close' }));
    await settled(r, `${BASE}/${B.externalId}`);
    await back(r);
    await settled(r, `${BASE}/${B.externalId}/phase/3/criteria`);
    await back(r);
    await settled(r, `${BASE}/${B.externalId}/phase/2/criteria`);
  });

  it('re-clicking the selected plan adds no entry and keeps its open details', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/plan`);
    await waitFor(() => {
      expect(selectedTab('plan details tabs')).toBe('Plan');
    });
    const key = r.state.location.key;
    fireEvent.click(listItem('Beta'));
    expect(at(r)).toBe(`${BASE}/${B.externalId}/details/plan`);
    expect(r.state.location.key).toBe(key);
  });

  it('the child-route moves re-render the page — they do not remount it', async () => {
    const r = mount(BASE);
    await settled(r, `${BASE}/${A.externalId}`);
    fireEvent.click(listItem('Beta'));
    await settled(r, `${BASE}/${B.externalId}`);
    fireEvent.click(screen.getByRole('button', { name: 'open Phase 1 — Beta 1 details' }));
    const d = await screen.findByRole('dialog', { name: 'Beta 1' });
    fireEvent.click(within(d).getByRole('tab', { name: 'Report' }));
    await settled(r, `${BASE}/${B.externalId}/phase/1/report`);
    await back(r);
    await back(r);
    await settled(r, `${BASE}/${B.externalId}`);
    // One fetch for the whole sequence: a remount would refetch the epics.
    expect(epicFetches).toBe(1);
  });
});

describe('SC-5 — corrections replace; Back never bounces', () => {
  it('/plans → first Active plan by replace; Back leaves the place', async () => {
    const r = mount('/elsewhere', BASE);
    await settled(r, `${BASE}/${A.externalId}`);
    expect(r.state.historyAction).toBe('REPLACE');
    await back(r);
    await settled(r, '/elsewhere');
    // No bounce: nothing navigates us back into the Plans place.
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe('/elsewhere');
  });

  it('the correction does not grow the browser history (window.history.length)', async () => {
    window.history.replaceState(null, '', `${BASE}?scope=swarmery`);
    const before = window.history.length;
    const router = createBrowserRouter(ROUTES);
    render(<RouterProvider router={router} />);
    await waitFor(() => {
      expect(window.location.pathname).toBe(`${BASE}/${A.externalId}`);
    });
    expect(window.location.search).toBe('?scope=swarmery');
    expect(window.history.length).toBe(before);

    // An operator click, by contrast, is exactly one entry.
    await waitFor(() => {
      expect(listItems().length).toBeGreaterThan(0);
    });
    fireEvent.click(listItem('Beta'));
    await waitFor(() => {
      expect(window.location.pathname).toBe(`${BASE}/${B.externalId}`);
    });
    expect(window.history.length).toBe(before + 1);
    router.dispose();
    window.history.replaceState(null, '', '/');
  });
});

describe('SC-6 — the status filter is in the URL', () => {
  it('Done: one push, lands on the first Done plan; Back returns to the Active plan in one step', async () => {
    const r = mount(`${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    fireEvent.click(filterTab('done'));
    await settled(r, `${BASE}/${DONE.externalId}`);
    expect(selectedTab('plan status filter')).toMatch(/^done/);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Shipped');
    });

    await back(r);
    await settled(r, `${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    expect(selectedTab('plan status filter')).toMatch(/^active/);
  });

  it('an empty ?status=archived shows the empty state and stays put', async () => {
    const r = mount(`${BASE}?status=archived`);
    await screen.findByText('no archived plans');
    expect(selectedTab('plan status filter')).toMatch(/^archived/);
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe(`${BASE}?status=archived`);
  });

  it('clicking an empty filter is one entry and stays on the empty state', async () => {
    const r = mount(`${BASE}/${A.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Alpha');
    });
    fireEvent.click(filterTab('archived'));
    await settled(r, `${BASE}?status=archived`);
    await screen.findByText('no archived plans');
    await back(r);
    await settled(r, `${BASE}/${A.externalId}`);
  });
});

describe('SC-7 — the numeric ?task= / ?plan= hand-off resolves by replace', () => {
  it('?task=<id> → the plan, keeping ?scope=', async () => {
    const r = mount('/elsewhere', `${BASE}?task=2&scope=swarmery`);
    await settled(r, `${BASE}/${B.externalId}?scope=swarmery`);
    expect(r.state.historyAction).toBe('REPLACE');
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    await back(r);
    await settled(r, '/elsewhere');
  });

  it('?plan=<id>&phase=<seq> → that phase on Story, keeping ?scope=', async () => {
    const r = mount(`${BASE}?plan=2&phase=3&scope=swarmery`);
    await settled(r, `${BASE}/${B.externalId}/phase/3?scope=swarmery`);
    const d = await screen.findByRole('dialog', { name: 'Beta 3' });
    expect(within(d).getByRole('tab', { name: 'Story' }).getAttribute('aria-selected')).toBe('true');
  });

  it('?task=<id>&tab=revisions → the Revisions tab, keeping ?scope=', async () => {
    const r = mount(`${BASE}?task=2&tab=revisions&scope=swarmery`);
    await settled(r, `${BASE}/${B.externalId}/details/revisions?scope=swarmery`);
    await waitFor(() => {
      expect(selectedTab('plan details tabs')).toMatch(/^Revisions/);
    });
  });

  it('a Done plan’s id lands under the Done filter', async () => {
    const r = mount(`${BASE}?task=3`);
    await settled(r, `${BASE}/${DONE.externalId}`);
    await waitFor(() => {
      expect(selectedTab('plan status filter')).toMatch(/^done/);
    });
  });

  it('an unknown id falls back to the first Active plan', async () => {
    const r = mount(`${BASE}?task=999&scope=swarmery`);
    await settled(r, `${BASE}/${A.externalId}?scope=swarmery`);
  });
});

describe('PlansPlace — a plan path is the Plans tab', () => {
  it('drops a stray ?tab= on a plan path (replace) and shows the Plans body', async () => {
    const r = mount(`${BASE}/${B.externalId}?tab=board&scope=swarmery`);
    await settled(r, `${BASE}/${B.externalId}?scope=swarmery`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    expect(selectedTab('Plans')).toBe('Plans');
  });
});

describe('a project switch never judges the new URL against the old project’s epics', () => {
  // Plans stays mounted across /p/:slug changes and projectId follows the slug at
  // once, while the loaded epics are still the previous project's until the new
  // fetch lands. Nothing — correction, ?task= resolver, selection — may act in
  // that window, or it rewrites the new URL and, on Back, loses the old entry.
  it('/p/a/plans/<A2> → /p/b/plans → Back lands on /p/a/plans/<A2> again', async () => {
    const GAMMA = epic(5, '2026-09-05-plan-gamma', 'Gamma', { projectId: 4, projectSlug: 'b' });
    const DELTA = epic(6, '2026-09-06-plan-delta', 'Delta', { projectId: 4, projectSlug: 'b' });
    epicsByProject = { 3: [A, B], 4: [GAMMA, DELTA] };
    const A2 = `/p/a/plans/${B.externalId}`;

    const r = mount(A2);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });

    // The ProjectSwitcher's push; project b's epics are still in flight.
    held.add('epics:4');
    await act(async () => {
      await r.navigate('/p/b/plans');
    });
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe('/p/b/plans');

    await release('epics:4');
    await settled(r, `/p/b/plans/${GAMMA.externalId}`);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Gamma');
    });

    // Back: now project b's list is the stale one while a's refetch is out.
    held.add('epics:3');
    await back(r);
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe(A2);

    await release('epics:3');
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    expect(at(r)).toBe(A2);
  });

  it('a slower fetch for a project already left does not land over the current one', async () => {
    const GAMMA = epic(5, '2026-09-05-plan-gamma', 'Gamma', { projectId: 4, projectSlug: 'b' });
    epicsByProject = { 3: [A, B], 4: [GAMMA] };
    const A2 = `/p/a/plans/${B.externalId}`;
    const r = mount(A2);
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });

    held.add('epics:4');
    await act(async () => {
      await r.navigate('/p/b/plans');
    });
    await back(r); // leave b before its fetch lands
    await waitFor(() => {
      expect(selectedPlan()).toContain('Beta');
    });
    await release('epics:4'); // b's rows arrive late — they must be dropped
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe(A2);
    expect(selectedPlan()).toContain('Beta');
    expect(screen.queryByText('Gamma')).toBeNull();
  });
});

describe('"review the open revision →" links a revision the loaded list does not have yet', () => {
  it('keeps /details/revisions/<id> while the reload is out, then marks it', async () => {
    // Alpha's list loads empty; the 409 then names revision 77, staged meanwhile.
    openRevisionOnStart = 77;
    const r = mount(`${BASE}/${A.externalId}`);
    fireEvent.click(await screen.findByRole('button', { name: 'Revise plan' }));
    const modal = screen.getByRole('dialog', { name: 'Revise this plan' });
    fireEvent.change(within(modal).getByLabelText('what should change, and why'), {
      target: { value: 'tighten phase 2' },
    });
    fireEvent.click(within(modal).getByRole('button', { name: 'Start revision' }));
    const link = await within(modal).findByRole('button', { name: 'review the open revision →' });

    revisionsByTask = { 1: [revision(77, 'staged', 'tighten phase 2')] };
    held.add('revisions:1');
    fireEvent.click(link);
    await settled(r, `${BASE}/${A.externalId}/details/revisions/77`);
    // The stale (empty) list must not correct the new id away.
    await new Promise((res) => setTimeout(res, 50));
    expect(at(r)).toBe(`${BASE}/${A.externalId}/details/revisions/77`);

    await release('revisions:1');
    await waitFor(() => {
      expect(document.querySelector('[data-revision-id="77"]')?.getAttribute('aria-current')).toBe('true');
    });
    expect(at(r)).toBe(`${BASE}/${A.externalId}/details/revisions/77`);
  });
});
