// @vitest-environment jsdom
//
// The "open in terminal" badge on the Plans page (mods-swarmery-link phase 7):
// GET /api/epics/{taskId}/terminal-sessions → a badge on the phase the session
// picked, a plan-level badge by the title for a pick without a phase.
//
// Claims are about the mounted page: which rows carry a badge, that a 404 / [] /
// failed fetch draws nothing, that a `plan_updated` frame for THIS plan refetches,
// and that a slow response for a plan the operator has already left never lands.
//
// On its own: `npx vitest run src/pages/Plans.terminalBadge.test.tsx`. Tests are
// excluded from tsc and vitest does not type check — types here are documentation.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, TerminalSession, WSMessage } from '../api/types';
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

// A socket stand-in the test can push frames through. Each mounted subscriber
// registers once and always calls its LATEST handler (as the real hook does).
const handlers = new Set<(msg: WSMessage) => void>();
vi.mock('../lib/ws', async () => {
  const { useEffect, useRef } = await import('react');
  return {
    useLiveUpdates: (onMessage: (msg: WSMessage) => void) => {
      const ref = useRef(onMessage);
      ref.current = onMessage;
      useEffect(() => {
        const h = (msg: WSMessage): void => ref.current(msg);
        handlers.add(h);
        return () => {
          handlers.delete(h);
        };
      }, []);
    },
  };
});

vi.mock('../lib/markdown', () => ({
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));

const TASK_A = 77;
const TASK_B = 88;
const PHASE_A1 = 11;
const PHASE_A2 = 22;
const PHASE_B1 = 81;

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
  id: PHASE_A1,
  seq: 1,
  name: 'Scope model',
  docPath: '/ws/plan/phase-1.md',
  docRelPath: 'phase-1.md',
  dependsOn: [],
  checkboxesDone: 0,
  checkboxesTotal: 3,
  landOpen: 0,
  runBranch: '',
  manualOpen: 0,
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

const epic = (over: Partial<Epic> = {}): Epic => ({
  taskId: TASK_A,
  externalId: '2026-10-14-plan-a',
  projectId: 3,
  projectSlug: 'swarmery',
  title: 'plan alpha',
  status: 'active',
  startedAt: '2026-10-14T09:00:00Z',
  planDir: '/ws/plan',
  hasSummary: false,
  hasSpec: false,
  spec: null,
  phases: [
    phase(),
    phase({ id: PHASE_A2, seq: 2, name: 'UI wiring', docRelPath: 'phase-2.md' }),
  ],
  rollup: { done: 0, total: 6, pct: 0, incompletePhases: 2 },
  planRun: null,
  cardExternalId: null,
  linkedSessions: [],
  ...over,
});

const epicB = (): Epic =>
  epic({
    taskId: TASK_B,
    externalId: '2026-10-14-plan-b',
    title: 'plan beta',
    phases: [phase({ id: PHASE_B1, seq: 1, name: 'Beta work' })],
    rollup: { done: 0, total: 3, pct: 0, incompletePhases: 1 },
  });

const ts = (over: Partial<TerminalSession> = {}): TerminalSession => ({
  sessionId: 41,
  sessionUuid: 'efaa9b56-1111-2222-3333-444455556666',
  phaseId: String(PHASE_A1),
  chosenBy: 'terminal',
  at: '2026-10-14T09:12:03.120Z',
  live: true,
  ...over,
});

type Reply = { status: number; body: unknown } | Promise<{ status: number; body: unknown }>;

let epics: Epic[] = [];
/** Per-task reply for terminal-sessions; may be a promise to hold the response. */
let terminalReply: (taskId: number) => Reply = () => ({ status: 200, body: [] });
let terminalCalls: number[] = [];

function stubFetch(): void {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const json = (status: number, body: unknown): Promise<Response> =>
        Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response);
      const terminal = /^\/api\/epics\/(\d+)\/terminal-sessions$/.exec(url);
      if (terminal !== null) {
        const taskId = Number(terminal[1]);
        terminalCalls.push(taskId);
        return Promise.resolve(terminalReply(taskId)).then((r) => json(r.status, r.body));
      }
      if (url.startsWith('/api/epics?')) return json(200, epics);
      if (url.startsWith('/api/sessions?')) return json(200, { sessions: [], nextCursor: null });
      return json(200, []);
    }),
  );
}

function phaseRow(seq: number, name: string): HTMLElement {
  return screen.getByRole('button', { name: `Phase ${String(seq)} — ${name}` });
}

async function mountPlans(): Promise<void> {
  render(<PlansAtRoute />);
  await waitFor(() => {
    expect(screen.getByRole('button', { name: /^Phase 1/ })).toBeTruthy();
  });
}

function push(msg: WSMessage): void {
  for (const h of [...handlers]) h(msg);
}

beforeEach(() => {
  epics = [epic(), epicB()];
  terminalReply = () => ({ status: 200, body: [] });
  terminalCalls = [];
  handlers.clear();
  localStorage.clear();
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('terminal badge on the Plans page', () => {
  it('shows the badge on the matching phase only, with the short id and a live dot', async () => {
    terminalReply = () => ({ status: 200, body: [ts()] });
    await mountPlans();

    const badge = await within(phaseRow(1, 'Scope model')).findByTestId('terminal-badge');
    expect(badge.textContent).toContain('open in terminal');
    expect(badge.textContent).toContain('efaa9b56');
    expect(badge.getAttribute('data-live')).toBe('true');
    expect(badge.getAttribute('data-scope')).toBe('phase');
    expect(badge.getAttribute('href')).toBe('/p/swarmery/sessions/41');
    expect(badge.getAttribute('aria-label')).toMatch(/^open in terminal:/);

    expect(within(phaseRow(2, 'UI wiring')).queryByTestId('terminal-badge')).toBeNull();
    expect(screen.getAllByTestId('terminal-badge')).toHaveLength(1);
  });

  it('shows a plan-level badge by the title for a pick with phaseId null', async () => {
    terminalReply = () => ({
      status: 200,
      body: [ts({ phaseId: null, live: false, sessionId: 42, sessionUuid: 'abcdef12-0000' })],
    });
    await mountPlans();

    const badge = await screen.findByTestId('terminal-badge');
    expect(badge.getAttribute('data-scope')).toBe('plan');
    expect(badge.getAttribute('data-live')).toBe('false');
    expect(badge.textContent).toContain('abcdef12');
    expect(badge.getAttribute('href')).toBe('/p/swarmery/sessions/42');
    expect(within(phaseRow(1, 'Scope model')).queryByTestId('terminal-badge')).toBeNull();
  });

  it('counts several sessions and lists them all in the tip', async () => {
    terminalReply = () => ({
      status: 200,
      body: [
        ts({ sessionId: 43, sessionUuid: 'aaaaaaaa-1' }),
        ts({ sessionId: 42, sessionUuid: 'bbbbbbbb-2', live: false }),
      ],
    });
    await mountPlans();

    const badge = await within(phaseRow(1, 'Scope model')).findByTestId('terminal-badge');
    expect(badge.textContent).toContain('aaaaaaaa +1');
    // Links to the newest.
    expect(badge.getAttribute('href')).toBe('/p/swarmery/sessions/43');
    const tip = badge.getAttribute('data-tip') ?? '';
    expect(tip).toContain('aaaaaaaa');
    expect(tip).toContain('bbbbbbbb');
    expect(tip).toContain('2 sessions');
  });

  it('is keyboard reachable and does not open the phase when clicked', async () => {
    terminalReply = () => ({ status: 200, body: [ts()] });
    await mountPlans();
    const badge = await within(phaseRow(1, 'Scope model')).findByTestId('terminal-badge');
    expect(badge.tagName).toBe('A');
    expect(badge.getAttribute('tabindex')).not.toBe('-1');
    fireEvent.click(badge);
    // The row did not open its phase panel (the list is still showing).
    expect(screen.queryByRole('button', { name: 'all phases' })).toBeNull();
  });

  it('renders nothing for an empty list', async () => {
    await mountPlans();
    await waitFor(() => {
      expect(terminalCalls).toContain(TASK_A);
    });
    expect(screen.queryByTestId('terminal-badge')).toBeNull();
  });

  it('renders nothing when the endpoint answers 404', async () => {
    terminalReply = () => ({ status: 404, body: { error: 'plan not found' } });
    await mountPlans();
    await waitFor(() => {
      expect(terminalCalls).toContain(TASK_A);
    });
    expect(screen.queryByTestId('terminal-badge')).toBeNull();
  });

  it('renders nothing when the endpoint fails', async () => {
    terminalReply = () => ({ status: 500, body: { error: 'boom' } });
    await mountPlans();
    await waitFor(() => {
      expect(terminalCalls).toContain(TASK_A);
    });
    expect(screen.queryByTestId('terminal-badge')).toBeNull();
  });

  it('refetches on a plan_updated frame for this plan, not for another', async () => {
    let rows: TerminalSession[] = [];
    terminalReply = () => ({ status: 200, body: rows });
    await mountPlans();
    await waitFor(() => {
      expect(terminalCalls).toHaveLength(1);
    });
    expect(screen.queryByTestId('terminal-badge')).toBeNull();

    // Another plan's frame must not refetch this plan's sessions.
    push({ type: 'plan_updated', payload: { taskId: TASK_B, projectId: 3 } });
    await Promise.resolve();
    expect(terminalCalls).toHaveLength(1);

    rows = [ts()];
    push({ type: 'plan_updated', payload: { taskId: TASK_A, projectId: 3 } });
    const badge = await within(phaseRow(1, 'Scope model')).findByTestId('terminal-badge');
    expect(badge.textContent).toContain('efaa9b56');
    expect(terminalCalls).toHaveLength(2);
  });

  it('ignores a stale response for a plan that is no longer selected', async () => {
    let releaseA: (r: { status: number; body: unknown }) => void = () => undefined;
    terminalReply = (taskId) =>
      taskId === TASK_A
        ? new Promise((resolve) => {
            releaseA = resolve;
          })
        : { status: 200, body: [] };
    await mountPlans();
    await waitFor(() => {
      expect(terminalCalls).toContain(TASK_A);
    });

    // Leave plan A while its terminal-sessions call is still out.
    fireEvent.click(screen.getByRole('link', { name: /plan beta/ }));
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^Phase 1 — Beta work/ })).toBeTruthy();
    });
    await waitFor(() => {
      expect(terminalCalls).toContain(TASK_B);
    });

    // A's late answer carries a phase id that does not exist on B; the plan-level
    // pick would show up by B's title if it leaked.
    releaseA({ status: 200, body: [ts({ phaseId: null })] });
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId('terminal-badge')).toBeNull();
  });
});
