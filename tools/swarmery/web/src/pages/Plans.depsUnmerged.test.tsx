// @vitest-environment jsdom
//
// The deps-unmerged run refusal on the Plans page (landing Phase 5, SC-13),
// end to end over the WIRE: `../api` is NOT mocked, so the real runEpicPhase
// parses the 409 body's `branches`, the real getProjectVcs supplies the terms,
// and the real landPhase sends the POST the assertions read. The refusal strip
// offers "Open <changeShort>" for each branch that is one of this plan's phase
// branches; a click opens the change request and the strip shows its link.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/Plans.depsUnmerged.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, VcsInfo } from '../api/types';
import { Plans } from './Plans';
import { PLANS_ROUTE_PATHS } from './plans/plansUrl';

function PlansAtRoute(): JSX.Element {
  return (
    <MemoryRouter initialEntries={['/p/swarmery/plans']}>
      <Routes>
        {PLANS_ROUTE_PATHS.map((p) => (
          <Route key={p} path={`/p/:slug/${p}`} element={<Plans />} />
        ))}
      </Routes>
    </MemoryRouter>
  );
}

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
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));

const phase = (over: Partial<EpicPhase> = {}): EpicPhase =>
  ({
    id: 11,
    seq: 1,
    name: 'Backend',
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
  }) as EpicPhase;

const EPIC: Epic = {
  taskId: 77,
  externalId: '2026-10-09-landing',
  projectId: 3,
  projectSlug: 'swarmery',
  title: 'Landing',
  status: 'active',
  startedAt: '2026-10-09T09:00:00Z',
  planDir: '/ws/plan',
  hasSummary: false,
  hasSpec: false,
  spec: null,
  // Phase 1 finished on its own run branch (never merged); phase 2 depends on it.
  phases: [
    phase({
      checkboxesDone: 3,
      completionState: 'complete',
      completionReport: 'done',
      runState: 'done',
      runOutcome: 'completed',
      runSessionUuid: 'u-11',
    }),
    phase({ id: 12, seq: 2, name: 'Frontend', docPath: '/ws/plan/phase-2.md', docRelPath: 'phase-2.md', dependsOn: [1] }),
  ],
  rollup: { done: 3, total: 6, pct: 50, incompletePhases: 1 },
  planRun: null,
  cardExternalId: null,
  linkedSessions: [],
};

const VCS: VcsInfo = {
  provider: 'unknown',
  host: 'code.example.com',
  terms: { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' },
  remote: { url: 'https://code.example.com/acme/w.git', present: true, protocol: 'https' },
  auth: { status: 'ok', login: 'octo', source: 'cli' },
  baseBranch: '',
  allowPushToBase: false,
  source: 'host',
  cliLogin: '',
};

const DEPS_UNMERGED = {
  error: 'deps-unmerged',
  code: 'deps-unmerged',
  message: 'merge swarm/phase-11 and swarm/phase-404 into main first',
  branches: ['swarm/phase-11', 'swarm/phase-404'],
  base: 'main',
};

let calls: { url: string; init: RequestInit | undefined }[] = [];

function stubFetch(): void {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      const json = (body: unknown, status = 200): Promise<Response> =>
        Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response);
      if (url.startsWith('/api/epics?')) return json([EPIC]);
      if (url.startsWith('/api/projects/3/vcs')) return json(VCS);
      if (/\/phases\/\d+\/run$/.test(url)) return json(DEPS_UNMERGED, 409);
      if (/\/phases\/\d+\/land$/.test(url))
        return json({
          branch: 'swarm/phase-11',
          base: 'main',
          action: 'pr',
          landing: {
            state: 'pr_open',
            prUrl: 'https://code.example.com/acme/w/-/merge_requests/9',
            prNumber: 9,
            prProvider: null,
            prStatus: null,
            landedAt: null,
            error: null,
          },
        });
      return json([]);
    }),
  );
}

beforeEach(stubFetch);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('the deps-unmerged refusal', () => {
  it('offers Open <changeShort> per mapped branch; a click opens the change request and shows its link', async () => {
    render(<PlansAtRoute />);
    const runs = await screen.findAllByRole('button', { name: 'Run phase' });
    fireEvent.click(runs[runs.length - 1] as HTMLElement);
    await waitFor(() => expect(calls.some((c) => c.url === '/api/epics/77/phases/12/run')).toBe(true));

    await screen.findByText(DEPS_UNMERGED.message);
    const open = await screen.findByRole('button', { name: 'Open MR for swarm/phase-11' });
    // swarm/phase-404 is no phase of this plan: named, not actionable.
    expect(screen.queryByRole('button', { name: 'Open MR for swarm/phase-404' })).toBeNull();

    fireEvent.click(open);
    const link = await screen.findByRole('link', { name: 'MR #9' });
    expect(link.getAttribute('href')).toBe('https://code.example.com/acme/w/-/merge_requests/9');

    const land = calls.find((c) => /\/phases\/\d+\/land$/.test(c.url));
    expect(land?.url).toBe('/api/epics/77/phases/11/land');
    expect(land?.init?.method).toBe('POST');
    expect(land?.init?.body).toBe('{"action":"pr"}');
    // The refusal sentence stays: the link joins it, it does not replace it.
    await waitFor(() => expect(screen.getByText(DEPS_UNMERGED.message)).toBeTruthy());
  });
});
