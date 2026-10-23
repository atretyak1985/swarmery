// @vitest-environment jsdom
//
// The decision → phase → evidence table is mounted INLINE on the plan's Plan tab
// (verify-not-trust phase 12): the real route tree opens `/details/plan`, and the
// table sits in the centre column above the README — no drawer. An epic from an
// older daemon (no `evidence` field) shows the README alone.

import { cleanup, configure, render, screen, within } from '../test/render';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecisionTable, Epic, EpicPhase } from '../api/types';
import { PlansPlace } from './plans/PlansPlace';
import { PLANS_ROUTE_PATHS } from './plans/plansUrl';

configure({ asyncUtilTimeout: 3000 });

beforeAll(async () => {
  await import('./Plans');
});

vi.mock('../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({
    slug: 'swarmery',
    project: { id: 3, slug: 'swarmery', name: 'swarmery' },
    projectId: 3,
    loading: false,
  }),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../lib/markdown', () => ({
  Markdown: ({ text }: { text?: string }) => <div data-testid="markdown">{text}</div>,
}));

const BASE = '/p/swarmery/plans';

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
  id: 11,
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

const TABLE: DecisionTable = {
  planReview: { status: 'none', reason: '', detail: '', at: '' },
  rows: [
    {
      id: 'D-1',
      kind: 'decision',
      text: 'Where does the table live?',
      answer: 'inline',
      status: 'decided',
      chip: 'remaining',
      reason: '',
      phases: [1],
      evidence: [],
    },
  ],
  counts: { evidence: 0, remaining: 1, notImplemented: 0, notVerified: 0 },
};

function epic(over: Partial<Epic> = {}): Epic {
  return {
    taskId: 1,
    externalId: '2026-10-23-plan-alpha',
    projectId: 3,
    projectSlug: 'swarmery',
    title: 'Alpha',
    status: 'active',
    startedAt: null,
    planDir: '/ws/alpha/plan',
    hasSummary: false,
    hasSpec: false,
    spec: null,
    phases: [phase()],
    rollup: { done: 0, total: 3, pct: 0, incompletePhases: 1 },
    planRun: null,
    cardExternalId: null,
    linkedSessions: [],
    ...over,
  };
}

let epics: Epic[] = [];

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const json = (body: unknown): Promise<Response> =>
        Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response);
      if (url.startsWith('/api/epics?')) return json(epics);
      if (url.startsWith('/api/sessions?')) return json({ sessions: [], nextCursor: null });
      if (/\/api\/epics\/\d+\/revisions$/.test(url)) return json({ revisions: [] });
      if (/\/docs\?path=/.test(url)) return json({ path: 'README.md', content: 'README body' });
      return json([]);
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(path: string): void {
  const router = createMemoryRouter(
    [{ path: '/p/:slug', children: PLANS_ROUTE_PATHS.map((p) => ({ path: p, element: <PlansPlace /> })) }],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
}

describe('Plans — decision table on the Plan tab', () => {
  it('renders the table inline above the README', async () => {
    epics = [epic({ evidence: TABLE, planReviewStatus: 'none' })];
    mount(`${BASE}/${epics[0]?.externalId ?? ''}/details/plan`);
    const region = await screen.findByRole('region', { name: 'Decisions and criteria' });
    expect(within(region).getByRole('cell', { name: 'D-1' })).toBeTruthy();
    expect(region.querySelector('[data-chip="remaining"]')).not.toBeNull();
    const readme = await screen.findByText('README body');
    // Same column, table first.
    expect(region.compareDocumentPosition(readme) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('shows the README alone for an epic without evidence (older daemon)', async () => {
    epics = [epic()];
    mount(`${BASE}/${epics[0]?.externalId ?? ''}/details/plan`);
    await screen.findByText('README body');
    expect(screen.queryByRole('region', { name: 'Decisions and criteria' })).toBeNull();
  });
});
