// @vitest-environment jsdom
//
// "Verify again" on the Plans page (phase-reverify plan, phase 2, D8). The
// claims, all on the REAL page with `../api` unmocked so the assertions read
// the actual POST:
//
//   - the phase row offers it exactly when the server says `canReverify`, and
//     on a `fail` phase too — that one reads `done` in the status pill (D5)
//     and is still re-gradable (D7), so a status gate would hide it there;
//   - it reads `verifying…` and is disabled while the POST is in flight and
//     while the server reports `verifyRunning`;
//   - a refusal's message lands in the page's run-error strip, verbatim;
//   - the detail panel's Story card and Runs tab offer the same action.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/Plans.reverify.test.tsx`. web/tsconfig.json
// EXCLUDES *.test.tsx — treat the types here as documentation.

import { cleanup, configure, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase } from '../api/types';
import { Plans } from './Plans';
import { PLANS_ROUTE_PATHS } from './plans/plansUrl';

// The whole Plans tree on a loaded machine can outlast the 1 s default.
configure({ asyncUtilTimeout: 3000 });

const PLAN = '2026-10-18-phase-reverify';

function PlansAt({ path }: { path: string }): JSX.Element {
  return (
    <MemoryRouter initialEntries={[path]}>
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

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
  id: 11,
  seq: 1,
  name: 'Backend',
  docPath: '/ws/plan/phase-1.md',
  docRelPath: 'phase-1.md',
  dependsOn: [],
  checkboxesDone: 3,
  checkboxesTotal: 3,
  docStatus: null,
  docUpdatedAt: null,
  completionReport: null,
  activatedAt: null,
  boardTaskExternalId: null,
  boardTaskId: null,
  boardColumn: null,
  runState: 'done',
  runSessionUuid: 'u-1',
  runModel: null,
  runModels: [],
  runModelFellBack: false,
  docModel: null,
  runStartedAt: '2026-10-18T10:00:00Z',
  runError: null,
  runEvents: [],
  runOutcome: 'completed',
  runEndedAt: '2026-10-18T10:20:00Z',
  runCheckboxesBefore: 0,
  verifyMode: 'normal',
  verifyVerdict: 'inconclusive',
  verifyDetail: 'timeout',
  completionState: 'unverified',
  completionBlockers: ['verification could not conclude'],
  forecasts: [],
  forecastLints: [],
  surprise: null,
  reopens: [],
  canReverify: true,
  verifyRunning: false,
  ...over,
});

const epic = (phases: EpicPhase[]): Epic => ({
  taskId: 77,
  externalId: PLAN,
  projectId: 3,
  projectSlug: 'swarmery',
  title: 'Phase re-verification',
  status: 'active',
  startedAt: '2026-10-18T09:00:00Z',
  planDir: '/ws/plan',
  hasSummary: false,
  hasSpec: false,
  spec: null,
  phases,
  rollup: { done: 3, total: 3, pct: 100, incompletePhases: 1 },
  planRun: null,
  cardExternalId: null,
  linkedSessions: [],
});

let epics: Epic[] = [];
let calls: { url: string; init: RequestInit | undefined }[] = [];
/** The next verify POST's reply; null holds it open until `releaseVerify`. */
let verifyReply: { status: number; body: unknown } | null = { status: 202, body: { status: 'verifying' } };
let releaseVerify: (() => void) | null = null;

const verifyCalls = (): { url: string; init: RequestInit | undefined }[] =>
  calls.filter((c) => /\/phases\/\d+\/verify$/.test(c.url));

function stubFetch(): void {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      const response = (body: unknown, status = 200): Response =>
        ({ ok: status < 400, status, json: () => Promise.resolve(body) }) as Response;
      if (url.startsWith('/api/epics?')) return Promise.resolve(response(epics));
      if (/\/phases\/\d+\/verify$/.test(url)) {
        if (verifyReply === null) {
          return new Promise<Response>((res) => {
            releaseVerify = () => res(response({ status: 'verifying' }, 202));
          });
        }
        return Promise.resolve(response(verifyReply.body, verifyReply.status));
      }
      if (/\/docs\?path=/.test(url)) return Promise.resolve(response({ path: 'phase-1.md', content: '# doc\n' }));
      if (/\/phases\/\d+\/diagnosis$/.test(url))
        return Promise.resolve(
          response({
            phaseId: 11,
            seq: 1,
            name: 'Backend',
            runOutcome: 'partial',
            criteriaTotal: 3,
            criteriaBefore: 0,
            criteriaAfter: 3,
            runStartedAt: null,
            runEndedAt: null,
            runError: null,
            verifyVerdict: 'inconclusive',
            verifyDetail: 'timeout',
            blockers: [{ kind: 'unverified', summary: 'the grade never landed', detail: '' }],
            agentMessage: null,
          }),
        );
      return Promise.resolve(response([]));
    }),
  );
}

beforeEach(() => {
  epics = [epic([phase()])];
  verifyReply = { status: 202, body: { status: 'verifying' } };
  releaseVerify = null;
  localStorage.clear();
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

const LIST = `/p/swarmery/plans/${PLAN}`;

describe('Verify again — phase row', () => {
  it('is offered when the server says canReverify, and POSTs …/verify', async () => {
    render(<PlansAt path={LIST} />);
    const btn = await screen.findByRole('button', { name: 'Verify again' });
    expect(btn.getAttribute('data-tip')).toContain('read-only verifier');
    fireEvent.click(btn);
    await waitFor(() => {
      expect(verifyCalls()).toHaveLength(1);
    });
    expect(verifyCalls()[0]?.url).toBe('/api/epics/77/phases/11/verify');
    expect(verifyCalls()[0]?.init?.method).toBe('POST');
  });

  it('is not offered when canReverify is false or absent', async () => {
    epics = [
      epic([
        phase({ id: 11, seq: 1, name: 'Refused', canReverify: false }),
        phase({ id: 12, seq: 2, name: 'Old daemon', docRelPath: 'phase-2.md', canReverify: undefined }),
      ]),
    ];
    render(<PlansAt path={LIST} />);
    await screen.findByText('Old daemon');
    expect(screen.queryByRole('button', { name: 'Verify again' })).toBeNull();
  });

  // D5 + D7: the fail verdict is `complete` to the gate, so the pill reads done —
  // and the row's done branch must still offer the re-grade.
  it('is offered on a fail-verdict phase that reads done', async () => {
    epics = [
      epic([phase({ verifyVerdict: 'fail', completionState: 'complete', completionBlockers: [], canReverify: true })]),
    ];
    render(<PlansAt path={LIST} />);
    // The row's done branch (✓ summary) is what renders — not Run/Retry.
    await screen.findAllByRole('button', { name: '✓ summary' });
    expect(screen.queryByRole('button', { name: /Run phase|Retry run/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Verify again' })).toBeTruthy();
  });

  it('reads verifying… and is disabled while the server reports verifyRunning', async () => {
    epics = [epic([phase({ verifyRunning: true })])];
    render(<PlansAt path={LIST} />);
    const btn = (await screen.findByRole('button', { name: 'verifying…' })) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    fireEvent.click(btn);
    expect(verifyCalls()).toHaveLength(0);
  });

  it('is disabled while its own request is in flight', async () => {
    verifyReply = null;
    render(<PlansAt path={LIST} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Verify again' }));
    const busy = (await screen.findByRole('button', { name: 'verifying…' })) as HTMLButtonElement;
    expect(busy.disabled).toBe(true);
    releaseVerify?.();
    await screen.findByRole('button', { name: 'Verify again' });
  });

  it('shows the refusal message in the run-error strip', async () => {
    verifyReply = {
      status: 409,
      body: { error: 'a whole-plan run is executing this plan', code: 'plan-running' },
    };
    render(<PlansAt path={LIST} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Verify again' }));
    await screen.findByText('a whole-plan run is executing this plan');
  });
});

describe('Verify again — run diagnosis modal', () => {
  // The modal gets the page's action, not a second implementation: pressing it
  // there is the same POST the row makes.
  it('the unverified blocker fires the page hook and the modal closes', async () => {
    epics = [epic([phase({ runOutcome: 'partial' })])];
    render(<PlansAt path={LIST} />);
    fireEvent.click(await screen.findByRole('button', { name: 'ran · 3/3' }));
    const dialog = await screen.findByRole('dialog', { name: 'Phase run diagnosis' });
    await within(dialog).findByText('the grade never landed');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Verify again' }));
    await waitFor(() => {
      expect(verifyCalls()).toHaveLength(1);
    });
    expect(verifyCalls()[0]?.url).toBe('/api/epics/77/phases/11/verify');
    expect(screen.queryByRole('dialog', { name: 'Phase run diagnosis' })).toBeNull();
  });
});

describe('Verify again — detail panel', () => {
  it('Runs tab offers it beside the primary and POSTs …/verify', async () => {
    render(<PlansAt path={`${LIST}/phase/1/runs`} />);
    const btn = await screen.findByRole('button', { name: 'Verify again' });
    fireEvent.click(btn);
    await waitFor(() => {
      expect(verifyCalls()).toHaveLength(1);
    });
    expect(verifyCalls()[0]?.url).toBe('/api/epics/77/phases/11/verify');
  });

  it('Story card offers it as a secondary action', async () => {
    render(<PlansAt path={`${LIST}/phase/1/story`} />);
    expect(await screen.findByRole('button', { name: 'Verify again' })).toBeTruthy();
  });

  it('Runs tab offers nothing when canReverify is false', async () => {
    epics = [epic([phase({ canReverify: false })])];
    render(<PlansAt path={`${LIST}/phase/1/runs`} />);
    await screen.findByRole('button', { name: /Run phase|Retry run/ });
    expect(screen.queryByRole('button', { name: 'Verify again' })).toBeNull();
  });
});
