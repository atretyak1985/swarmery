// @vitest-environment jsdom
//
// Plans link affordances (plans-deep-links phase 2). Phase 1 made the URL the
// state; these tests pin what makes that URL usable by a person, on the REAL
// route tree main.tsx mounts (PLANS_ROUTE_PATHS under /p/:slug, each rendering
// <PlansPlace/>) in a data router (createMemoryRouter + RouterProvider):
//
//   - Tabs renders <a role="tab"> for an href tab, <button role="tab"> otherwise,
//     and the arrow keys work for both;
//   - every selection is a real anchor carrying its canonical href (SC-9);
//   - the PlansPlace top tabs switch with PUSH, every other useTabParam caller
//     still replaces (SC-10);
//   - a stale link lands on the nearest valid level with a dismissible notice
//     naming what it could not find; a canonicalised tab is silent (SC-8);
//   - document.title names the open state and is restored on leaving (SC-11);
//   - an unsaved Edit cannot be lost to in-app navigation without a confirm, and
//     arms the native beforeunload prompt (SC-12);
//   - a late revisions fetch for a plan already left does not land over the
//     current plan's list (phase-1 review carry-over).
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/Plans.affordances.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { act, cleanup, configure, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PlanRevision } from '../api/types';
import { Tabs, useTabParam } from '../components/Tabs';
import { PlansPlace } from './plans/PlansPlace';
import { PLANS_ROUTE_PATHS } from './plans/plansUrl';

// The Plans page mounts the whole PlansPlace tree, and on a loaded machine its
// first render can outlast Testing Library's 1 s default, so every waitFor and
// findBy* in this file gets a longer budget. Still under vitest's 5 s test
// timeout. Vitest isolates each test file, so this doesn't leak into others.
configure({ asyncUtilTimeout: 3000 });

// The project follows the URL's :slug, as the real ProjectWorkspaceProvider
// does; its display NAME differs from the slug so the title test can tell them apart.
vi.mock('../workspace/ProjectContext', async () => {
  const { useParams } = await import('react-router-dom');
  return {
    useProjectWorkspace: () => {
      const { slug = 'swarmery' } = useParams<{ slug: string }>();
      return { slug, project: { id: 3, slug, name: `${slug} project` }, projectId: 3, loading: false };
    },
  };
});

vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../lib/markdown', () => ({
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));
// The other PlansPlace bodies are not under test — only that their tab switches.
vi.mock('./Board', () => ({ Board: () => <div>board body</div> }));
vi.mock('./Playbooks', () => ({ Playbooks: () => <div>playbooks body</div> }));
vi.mock('./PlanningMode', () => ({ PlanningMode: () => <div>new plan body</div> }));

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

let revisionsByTask: Record<number, PlanRevision[]> = {};
/** Fetches held open until `release(key)`: `revisions:<taskId>`. */
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
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const response = (body: unknown, status = 200): Response =>
        ({ ok: status < 400, status, json: () => Promise.resolve(body) }) as Response;
      const json = (body: unknown): Promise<Response> => Promise.resolve(response(body));
      if (url.startsWith('/api/epics?')) return json([A, B, DONE]);
      if (url.startsWith('/api/sessions?')) return json({ sessions: [], nextCursor: null });
      const revs = /\/api\/epics\/(\d+)\/revisions$/.exec(url);
      if (revs !== null) {
        const key = `revisions:${String(revs[1])}`;
        const body = (): unknown => ({ revisions: revisionsByTask[Number(revs[1])] ?? [] });
        return held.has(key)
          ? new Promise<Response>((res) => {
              waiting.push({ key, resolve: () => res(response(body())) });
            })
          : json(body());
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

function mount(...entries: string[]): Router {
  const router = createMemoryRouter(ROUTES, { initialEntries: entries, initialIndex: entries.length - 1 });
  render(<RouterProvider router={router} />);
  return router;
}

const at = (r: Router): string => `${r.state.location.pathname}${r.state.location.search}`;

async function settled(r: Router, href: string): Promise<void> {
  await waitFor(() => {
    expect(at(r)).toBe(href);
  });
}

async function back(r: Router): Promise<void> {
  await act(async () => {
    await r.navigate(-1);
  });
}

function listItem(title: string): HTMLElement {
  const item = screen
    .getAllByRole('link')
    .find((l) => l.hasAttribute('aria-current') && l.textContent?.includes(title));
  if (item === undefined) throw new Error(`no plan list item "${title}"`);
  return item;
}
function tabIn(tablist: string, name: RegExp): HTMLElement {
  return within(screen.getByRole('tablist', { name: tablist })).getByRole('tab', { name });
}
function notice(): HTMLElement | null {
  return screen.queryByText(/not found/);
}

beforeEach(() => {
  revisionsByTask = { 2: [revision(40, 'applied', 'an older change')] };
  held.clear();
  waiting = [];
  localStorage.clear();
  document.title = 'Swarmery';
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

// ---------------------------------------------------------------------------

type Id = 'one' | 'two' | 'three';
const IDS: Id[] = ['one', 'two', 'three'];

/** Tabs whose items are links to `?t=<id>`, plus the location for assertions. */
function LinkTabsHarness({ onChange }: { onChange: (id: Id) => void }): JSX.Element {
  const { search } = useLocation();
  const value = (new URLSearchParams(search).get('t') ?? 'one') as Id;
  const tabs = IDS.map((id) => ({ id, label: id, href: `/tabs?t=${id}` }));
  return <Tabs tabs={tabs} value={value} onChange={onChange} ariaLabel="link tabs" />;
}

describe('Tabs — an href tab is a link', () => {
  it('renders <a role="tab"> with its href when given, <button role="tab"> otherwise', () => {
    const r = createMemoryRouter([
      {
        path: '/tabs',
        element: (
          <Tabs
            tabs={[
              { id: 'one', label: 'one', href: '/tabs?t=one' },
              { id: 'two', label: 'two' },
            ]}
            value="one"
            onChange={() => undefined}
            ariaLabel="mixed"
          />
        ),
      },
    ], { initialEntries: ['/tabs'] });
    render(<RouterProvider router={r} />);
    const one = screen.getByRole('tab', { name: 'one' });
    const two = screen.getByRole('tab', { name: 'two' });
    expect(one.tagName).toBe('A');
    expect(one.getAttribute('href')).toBe('/tabs?t=one');
    expect(one.getAttribute('aria-selected')).toBe('true');
    expect(one.getAttribute('tabindex')).toBe('0');
    expect(two.tagName).toBe('BUTTON');
    expect(two.getAttribute('tabindex')).toBe('-1');
  });

  it('a plain click navigates (push) through the link and does not also call onChange', async () => {
    const onChange = vi.fn();
    const r = createMemoryRouter([{ path: '/tabs', element: <LinkTabsHarness onChange={onChange} /> }], {
      initialEntries: ['/tabs'],
    });
    render(<RouterProvider router={r} />);
    fireEvent.click(screen.getByRole('tab', { name: 'two' }));
    await settled(r, '/tabs?t=two');
    expect(r.state.historyAction).toBe('PUSH');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('the arrow keys navigate to a link tab and focus it', async () => {
    const onChange = vi.fn();
    const r = createMemoryRouter([{ path: '/tabs', element: <LinkTabsHarness onChange={onChange} /> }], {
      initialEntries: ['/tabs'],
    });
    render(<RouterProvider router={r} />);
    const list = screen.getByRole('tablist', { name: 'link tabs' });
    fireEvent.keyDown(list, { key: 'ArrowRight' });
    await settled(r, '/tabs?t=two');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'two' }));
    fireEvent.keyDown(list, { key: 'ArrowLeft' });
    fireEvent.keyDown(list, { key: 'ArrowLeft' }); // wraps to the last
    await settled(r, '/tabs?t=three');
    expect(screen.getByRole('tab', { name: 'three' }).getAttribute('aria-selected')).toBe('true');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('button tabs still switch through onChange on the arrow keys (no router needed)', () => {
    function Local(): JSX.Element {
      const [v, setV] = useState<Id>('one');
      return <Tabs tabs={IDS.map((id) => ({ id, label: id }))} value={v} onChange={setV} ariaLabel="plain" />;
    }
    render(<Local />);
    fireEvent.keyDown(screen.getByRole('tablist', { name: 'plain' }), { key: 'End' });
    expect(screen.getByRole('tab', { name: 'three' }).getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'three' }));
  });
});

function ParamHarness({ history }: { history?: 'push' | 'replace' }): JSX.Element {
  const [value, setValue] = useTabParam<Id>('tab', IDS, 'one', history !== undefined ? { history } : undefined);
  return <Tabs tabs={IDS.map((id) => ({ id, label: id }))} value={value} onChange={setValue} ariaLabel="param" />;
}

describe('useTabParam — replace by default, push on request (SC-10)', () => {
  it('replaces by default, so other pages keep their Back behaviour', async () => {
    const r = createMemoryRouter([{ path: '/x', element: <ParamHarness /> }], { initialEntries: ['/x?scope=s'] });
    render(<RouterProvider router={r} />);
    fireEvent.click(screen.getByRole('tab', { name: 'two' }));
    await settled(r, '/x?scope=s&tab=two');
    expect(r.state.historyAction).toBe('REPLACE');
  });

  it("pushes with { history: 'push' }", async () => {
    const r = createMemoryRouter([{ path: '/x', element: <ParamHarness history="push" /> }], {
      initialEntries: ['/x?scope=s'],
    });
    render(<RouterProvider router={r} />);
    fireEvent.click(screen.getByRole('tab', { name: 'two' }));
    await settled(r, '/x?scope=s&tab=two');
    expect(r.state.historyAction).toBe('PUSH');
  });
});

// ---------------------------------------------------------------------------

describe('SC-9 — every selection is a real anchor with its canonical href', () => {
  it('plan list items, filter tabs, phase names and the PlansPlace top tabs', async () => {
    mount(`${BASE}/${B.externalId}?scope=swarmery`);
    await waitFor(() => {
      expect(listItem('Beta').getAttribute('aria-current')).toBe('true');
    });
    expect(listItem('Alpha').getAttribute('href')).toBe(`${BASE}/${A.externalId}?scope=swarmery`);
    expect(listItem('Beta').tagName).toBe('A');

    expect(tabIn('plan status filter', /^active/).getAttribute('href')).toBe(`${BASE}?scope=swarmery`);
    expect(tabIn('plan status filter', /^done/).getAttribute('href')).toBe(`${BASE}?scope=swarmery&status=done`);
    expect(tabIn('plan status filter', /^archived/).getAttribute('href')).toBe(
      `${BASE}?scope=swarmery&status=archived`,
    );

    const name = screen.getByRole('link', { name: 'Beta 2' });
    expect(name.getAttribute('href')).toBe(`${BASE}/${B.externalId}/phase/2?scope=swarmery`);
    // The row keeps its button role; the anchor is only the name inside it.
    expect(screen.getByRole('button', { name: 'open Phase 2 — Beta 2 details' }).contains(name)).toBe(true);

    expect(tabIn('Plans', /^Plans$/).getAttribute('href')).toBe(`${BASE}/${B.externalId}?scope=swarmery`);
    // Board and Playbooks are parked (lib/parked.ts): out of the strip.
    const strip = within(screen.getByRole('tablist', { name: 'Plans' }));
    expect(strip.queryByRole('tab', { name: /^Board$/ })).toBeNull();
    expect(strip.queryByRole('tab', { name: /^Playbooks$/ })).toBeNull();
    expect(tabIn('Plans', /^New plan$/).getAttribute('href')).toBe(`${BASE}?scope=swarmery&tab=new`);
  });

  it('a plan row stays a link while its copy-id chip stays a separate control', async () => {
    const realClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    try {
      const r = mount(`${BASE}/${B.externalId}?scope=swarmery`);
      await waitFor(() => {
        expect(listItem('Beta').getAttribute('aria-current')).toBe('true');
      });
      const link = listItem('Alpha');
      const chip = screen.getByRole('button', { name: `copy id: ${A.externalId}` });
      // No <button> inside the <a>: the chip is a sibling, so the link's name is
      // the plan title alone.
      expect(link.contains(chip)).toBe(false);
      expect(link.querySelector('button')).toBeNull();
      expect(link.textContent).toBe('Alpha');

      await act(async () => {
        fireEvent.click(chip);
      });
      expect(writeText).toHaveBeenCalledWith(A.externalId);
      // Copying never selects the row.
      expect(at(r)).toBe(`${BASE}/${B.externalId}?scope=swarmery`);

      fireEvent.click(link);
      await settled(r, `${BASE}/${A.externalId}?scope=swarmery`);
      expect(r.state.historyAction).toBe('PUSH');
    } finally {
      if (realClipboard) Object.defineProperty(navigator, 'clipboard', realClipboard);
      else Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

  it('PhasePanel tabs link to their panel URLs', async () => {
    mount(`${BASE}/${B.externalId}/phase/2/runs?scope=swarmery`);
    const d = await screen.findByRole('region', { name: 'Beta 2' });
    const href = (name: RegExp): string | null => within(d).getByRole('tab', { name }).getAttribute('href');
    expect(href(/^Story/)).toBe(`${BASE}/${B.externalId}/phase/2?scope=swarmery`);
    expect(href(/^Criteria/)).toBe(`${BASE}/${B.externalId}/phase/2/criteria?scope=swarmery`);
    expect(href(/^Runs/)).toBe(`${BASE}/${B.externalId}/phase/2/runs?scope=swarmery`);
    expect(href(/^Report/)).toBe(`${BASE}/${B.externalId}/phase/2/report?scope=swarmery`);
    expect(href(/^Edit/)).toBe(`${BASE}/${B.externalId}/phase/2/edit?scope=swarmery`);
  });

  it('plan-details tabs link to their details URLs', async () => {
    mount(`${BASE}/${B.externalId}/details/plan`);
    await waitFor(() => {
      expect(tabIn('plan details tabs', /^Plan$/).getAttribute('aria-selected')).toBe('true');
    });
    expect(tabIn('plan details tabs', /^Plan$/).getAttribute('href')).toBe(`${BASE}/${B.externalId}/details/plan`);
    expect(tabIn('plan details tabs', /^Revisions/).getAttribute('href')).toBe(
      `${BASE}/${B.externalId}/details/revisions`,
    );
    expect(tabIn('plan details tabs', /^Edit$/).getAttribute('href')).toBe(`${BASE}/${B.externalId}/details/edit`);
  });

  it('clicking a phase name is ONE entry (the row does not navigate a second time)', async () => {
    const r = mount(`${BASE}/${B.externalId}`);
    const name = await screen.findByRole('link', { name: 'Beta 3' });
    fireEvent.click(name);
    await settled(r, `${BASE}/${B.externalId}/phase/3`);
    await screen.findByRole('region', { name: 'Beta 3' });
    await back(r);
    await settled(r, `${BASE}/${B.externalId}`);
  });
});

describe('SC-10 — the PlansPlace top tabs switch with push', () => {
  it('Plans → New plan → Back returns to the Plans tab', async () => {
    const r = mount(`${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(listItem('Beta').getAttribute('aria-current')).toBe('true');
    });
    fireEvent.click(tabIn('Plans', /^New plan$/));
    await settled(r, `${BASE}?tab=new`);
    expect(r.state.historyAction).toBe('PUSH');
    await screen.findByText('new plan body');

    await back(r);
    await settled(r, `${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(listItem('Beta').getAttribute('aria-current')).toBe('true');
    });
    expect(screen.queryByText('new plan body')).toBeNull();
  });

  // Parked tabs (lib/parked.ts) keep their bodies and their URLs: a direct link
  // still opens the board, and the strip then shows the tab you are on, but never
  // the other parked one.
  it('a parked tab opened by a direct link renders and shows only itself in the strip', async () => {
    mount('/elsewhere', `${BASE}?tab=board`);
    await screen.findByText('board body');
    const strip = within(screen.getByRole('tablist', { name: 'Plans' }));
    expect(strip.getByRole('tab', { name: /^Board$/ }).getAttribute('aria-selected')).toBe('true');
    expect(strip.queryByRole('tab', { name: /^Playbooks$/ })).toBeNull();
  });
});

describe('SC-8 — a stale link names what it could not find', () => {
  it('an unknown plan lands on the first Active plan with a notice', async () => {
    const r = mount('/elsewhere', `${BASE}/2026-01-01-gone`);
    await settled(r, `${BASE}/${A.externalId}`);
    expect(r.state.historyAction).toBe('REPLACE');
    await waitFor(() => {
      expect(notice()?.textContent).toBe('Plan 2026-01-01-gone not found — showing the plan list');
    });
  });

  it('an unknown phase lands on its plan with a notice', async () => {
    const r = mount(`${BASE}/${B.externalId}/phase/9/runs`);
    await settled(r, `${BASE}/${B.externalId}`);
    await waitFor(() => {
      expect(notice()?.textContent).toBe('Phase 9 not found in Beta');
    });
  });

  it('an unknown revision lands on the Revisions tab with a notice', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/revisions/999`);
    await settled(r, `${BASE}/${B.externalId}/details/revisions`);
    await waitFor(() => {
      expect(notice()?.textContent).toBe('Revision #999 not found');
    });
  });

  it('an unknown ?task= / ?plan= id names the numeric id', async () => {
    const r = mount(`${BASE}?task=999&scope=swarmery`);
    await settled(r, `${BASE}/${A.externalId}?scope=swarmery`);
    await waitFor(() => {
      expect(notice()?.textContent).toBe('Plan #999 not found');
    });
    cleanup();
    const r2 = mount(`${BASE}?plan=777`);
    await settled(r2, `${BASE}/${A.externalId}`);
    await waitFor(() => {
      expect(notice()?.textContent).toBe('Plan #777 not found');
    });
  });

  it('a canonicalised tab is silent', async () => {
    const r = mount(`${BASE}/${B.externalId}/phase/2/bogus`);
    await settled(r, `${BASE}/${B.externalId}/phase/2`);
    await screen.findByRole('region', { name: 'Beta 2' });
    expect(notice()).toBeNull();
    cleanup();
    const r2 = mount(`${BASE}/${B.externalId}/details/summary`);
    await settled(r2, `${BASE}/${B.externalId}/details/plan`);
    expect(notice()).toBeNull();
    cleanup();
    const r3 = mount(`${BASE}/${DONE.externalId}/phase/1/edit`);
    await settled(r3, `${BASE}/${DONE.externalId}/phase/1/report`);
    expect(notice()).toBeNull();
  });

  it('survives a follow-up replace, is dismissible, and clears on the next push', async () => {
    const r = mount(`${BASE}/${B.externalId}/phase/9`);
    await waitFor(() => {
      expect(notice()).not.toBeNull();
    });
    // A later replace with no state (e.g. ScopeProvider's ?scope= rewrite) keeps it.
    await act(async () => {
      await r.navigate(`${BASE}/${B.externalId}?scope=swarmery`, { replace: true });
    });
    expect(notice()).not.toBeNull();
    // The next operator click (a push) clears it.
    fireEvent.click(listItem('Alpha'));
    await settled(r, `${BASE}/${A.externalId}?scope=swarmery`);
    await waitFor(() => {
      expect(notice()).toBeNull();
    });

    cleanup();
    mount(`${BASE}/2026-01-01-gone`);
    await waitFor(() => {
      expect(notice()).not.toBeNull();
    });
    fireEvent.click(screen.getByRole('button', { name: 'dismiss notice' }));
    expect(notice()).toBeNull();
  });
});

describe('SC-11 — document.title names the open state', () => {
  it('list, plan, details tab, phase tab, Board — and restored on leaving', async () => {
    const r = mount(`${BASE}?status=archived`);
    await waitFor(() => {
      expect(document.title).toBe('Plans · swarmery project — Swarmery');
    });

    await act(async () => {
      await r.navigate(`${BASE}/${B.externalId}`);
    });
    await waitFor(() => {
      expect(document.title).toBe('Beta · Plans · swarmery project — Swarmery');
    });

    await act(async () => {
      await r.navigate(`${BASE}/${B.externalId}/details/revisions`);
    });
    await waitFor(() => {
      expect(document.title).toBe('Revisions — Beta · swarmery project — Swarmery');
    });

    await act(async () => {
      await r.navigate(`${BASE}/${B.externalId}/phase/3/runs`);
    });
    await waitFor(() => {
      expect(document.title).toBe('Runs · Phase 3 — Beta · swarmery project — Swarmery');
    });

    await act(async () => {
      await r.navigate(`${BASE}?tab=board`);
    });
    await screen.findByText('board body');
    expect(document.title).toBe('Board · swarmery project — Swarmery');

    await act(async () => {
      await r.navigate('/elsewhere');
    });
    await screen.findByText('elsewhere');
    expect(document.title).toBe('Swarmery');
  });

  it('restores the title when the Plans place unmounts from a plan', async () => {
    mount(`${BASE}/${B.externalId}/phase/2`);
    await waitFor(() => {
      expect(document.title).toBe('Story · Phase 2 — Beta · swarmery project — Swarmery');
    });
    cleanup();
    expect(document.title).toBe('Swarmery');
  });
});

describe('SC-12 — an unsaved Edit cannot be lost without a prompt', () => {
  async function editPlan(r: Router): Promise<HTMLTextAreaElement> {
    await settled(r, `${BASE}/${B.externalId}/details/edit`);
    return (await screen.findByRole('textbox', { name: 'plan doc source' })) as HTMLTextAreaElement;
  }

  it('dirty: in-app navigation asks, and false cancels it', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const r = mount(`${BASE}/${B.externalId}`, `${BASE}/${B.externalId}/details/edit`);
    const ta = await editPlan(r);
    fireEvent.change(ta, { target: { value: '# doc\nedited\n' } });

    fireEvent.click(listItem('Alpha')); // a link
    await waitFor(() => {
      expect(confirm).toHaveBeenCalledWith('Discard unsaved changes to README.md?');
    });
    expect(at(r)).toBe(`${BASE}/${B.externalId}/details/edit`);

    await back(r); // Back
    await waitFor(() => {
      expect(confirm).toHaveBeenCalledTimes(2);
    });
    expect(at(r)).toBe(`${BASE}/${B.externalId}/details/edit`);
    expect((screen.getByRole('textbox', { name: 'plan doc source' }) as HTMLTextAreaElement).value).toBe(
      '# doc\nedited\n',
    );
  });

  it('dirty: true lets the navigation through', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const r = mount(`${BASE}/${B.externalId}/details/edit`);
    const ta = await editPlan(r);
    fireEvent.change(ta, { target: { value: 'changed' } });
    fireEvent.click(tabIn('plan details tabs', /^Plan$/));
    await settled(r, `${BASE}/${B.externalId}/details/plan`);
    expect(confirm).toHaveBeenCalledTimes(1);
  });

  it('a dirty phase Edit names the phase doc', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const r = mount(`${BASE}/${B.externalId}/phase/2/edit`);
    const ta = (await screen.findByRole('textbox', { name: 'plan doc source' })) as HTMLTextAreaElement;
    fireEvent.change(ta, { target: { value: 'changed' } });
    const d = screen.getByRole('region', { name: 'Beta 2' });
    fireEvent.click(within(d).getByRole('tab', { name: /^Story/ }));
    await waitFor(() => {
      expect(confirm).toHaveBeenCalledWith('Discard unsaved changes to phase-2.md?');
    });
    expect(at(r)).toBe(`${BASE}/${B.externalId}/phase/2/edit`);
  });

  it('clean: navigation does not prompt', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const r = mount(`${BASE}/${B.externalId}/details/edit`);
    await editPlan(r);
    fireEvent.click(listItem('Alpha'));
    await settled(r, `${BASE}/${A.externalId}`);
    expect(confirm).not.toHaveBeenCalled();
  });

  it('beforeunload is cancelled (returnValue set) only while dirty', async () => {
    const r = mount(`${BASE}/${B.externalId}/details/edit`);
    const ta = await editPlan(r);

    const clean = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
    expect(clean.returnValue).toBe(true);

    fireEvent.change(ta, { target: { value: 'changed' } });
    const dirty = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
    expect(dirty.returnValue).toBe(false);

    // Saved-equal again (reverted) → clean.
    fireEvent.click(screen.getByRole('button', { name: 'revert' }));
    const reverted = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(reverted);
    expect(reverted.defaultPrevented).toBe(false);
  });
});

describe('revisions — a late fetch for the plan already left is dropped', () => {
  it("Alpha's slow revisions do not land over Beta's", async () => {
    revisionsByTask = {
      1: [revision(50, 'applied', 'an alpha change')],
      2: [revision(40, 'applied', 'an older change')],
    };
    held.add('revisions:1');
    const r = mount(`${BASE}/${A.externalId}/details/revisions`);
    await waitFor(() => {
      expect(tabIn('plan details tabs', /^Revisions/).getAttribute('aria-selected')).toBe('true');
    });
    await act(async () => {
      await r.navigate(`${BASE}/${B.externalId}/details/revisions`);
    });
    await screen.findByText('an older change');

    await release('revisions:1');
    await new Promise((res) => setTimeout(res, 20));
    expect(screen.queryByText('an alpha change')).toBeNull();
    expect(screen.getByText('an older change')).toBeTruthy();
  });
});
