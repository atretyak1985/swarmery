// @vitest-environment jsdom
//
// The way out of a finished revise wizard. A revise row stays the project's
// newest planning_sessions row after the operator Applies or Rejects the staged
// diff (neither touches planning_sessions), and the page always renders the
// newest row — so before this fix a done/failed/cancelled revise hid the idea
// intake for good and no new plan could be started from the UI.
//
// The claims: the intake stays hidden by default (revise sessions start from the
// Plans page), the revise banner offers "Start another plan" once the wizard is
// no longer running, pressing it shows the intake and POSTs a normal plan start,
// and the button is NOT offered while the revise interview is still running.
//
// Runs with the web suite: `npm test`. On its own:
// `npx vitest run src/pages/PlanningMode.reviseEscape.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PlanningStatus } from '../api/types';
import { PlanningMode } from './PlanningMode';

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

let calls: { url: string; init: RequestInit | undefined }[] = [];

function startCall(): { url: string; init: RequestInit | undefined } | undefined {
  return calls.find((c) => c.init?.method === 'POST' && /\/planning$/.test(c.url));
}

function revise(status: PlanningStatus['status']): PlanningStatus {
  return {
    active: false,
    sessionUuid: 'u-revise-1',
    sessionId: null,
    startedAt: '2026-10-01T10:00:00Z',
    status,
    currentQuestion: null,
    runningPlan: null,
    rawReply: null,
    history: [],
    planDir: null,
    mode: 'revise',
    model: '',
    effort: '',
    reviseTaskId: 42,
    lastError: null,
  };
}

function stubFetch(planning: PlanningStatus): void {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      const json = (body: unknown): Promise<Response> =>
        Promise.resolve({
          ok: true,
          status: init?.method === 'POST' ? 202 : 200,
          json: () => Promise.resolve(body),
        } as Response);
      if (/\/planning$/.test(url) && init?.method === 'POST') {
        return json({ sessionUuid: 'u-plan-1' });
      }
      if (/\/planning$/.test(url)) return json(planning);
      return json([]);
    }),
  );
}

async function mountPlanning(): Promise<void> {
  render(
    <MemoryRouter>
      <PlanningMode />
    </MemoryRouter>,
  );
  await screen.findByText('revising');
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('finished revise wizard', () => {
  it.each(['done', 'failed', 'cancelled'] as const)(
    'offers "Start another plan" when the revise wizard is %s',
    async (status) => {
      stubFetch(revise(status));
      await mountPlanning();
      expect(screen.queryByLabelText('describe what you want to build')).toBeNull();
      expect(screen.getByRole('button', { name: 'Start another plan' })).toBeDefined();
    },
  );

  it('opens the intake and starts a normal plan', async () => {
    stubFetch(revise('done'));
    await mountPlanning();

    fireEvent.click(screen.getByRole('button', { name: 'Start another plan' }));
    const idea = await screen.findByLabelText('describe what you want to build');
    // The revise banner and card give way, so the intake is not read as part of the revision.
    expect(screen.queryByText('revising')).toBeNull();

    fireEvent.change(idea, { target: { value: 'add a bulk export' } });
    fireEvent.click(screen.getByRole('button', { name: 'Start planning' }));
    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });
    expect(startCall()?.url).toBe('/api/projects/3/planning');
    expect(JSON.parse(String(startCall()?.init?.body))).toMatchObject({ idea: 'add a bulk export' });
  });

  it('"keep the plan card" returns to the revision', async () => {
    stubFetch(revise('done'));
    await mountPlanning();

    fireEvent.click(screen.getByRole('button', { name: 'Start another plan' }));
    await screen.findByLabelText('describe what you want to build');
    fireEvent.click(screen.getByRole('button', { name: 'keep the plan card' }));

    await screen.findByText('revising');
    expect(screen.queryByLabelText('describe what you want to build')).toBeNull();
  });

  it('does not offer it while the revise interview is running', async () => {
    stubFetch({ ...revise('generating'), active: true });
    await mountPlanning();
    expect(screen.queryByRole('button', { name: 'Start another plan' })).toBeNull();
  });
});
