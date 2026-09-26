// @vitest-environment jsdom
//
// The planner effort picker in the planning wizard (phase 2, step 2.7) — the
// twin of the Plans page's per-phase one, and modelled on its test.
//
// Every claim here is about the WIRE, not about the widget: `../api` is NOT
// mocked, so the real `startPlanning` runs and the assertions read the actual
// `fetch` init. That matters because the contract this step has to keep is a
// negative one — the picker's `default` must send NO `effort` KEY, not
// `{"effort":"default"}` and not `{"effort":""}`. A mocked api module would
// happily record an argument the real function then dropped on the floor.
//
// Why that one is worth a test at all: the daemon's claudeflags.NormalizeEffort
// folds "default" (and "off", and "none") to "omit --effort", and a `claude -p`
// with no --effort runs at the CLI's xhigh — the DEEPEST, most expensive
// setting. So the word `default` on the wire would not select the default; it
// would silently buy the most expensive interview available. Only the absent
// key reaches the daemon's own ladder.
//
// The second fence is the OPTION SET: the options are the closed set the daemon
// accepts (claudeflags.ValidEfforts), so "the UI cannot send a value the API
// would reject" stays structural rather than a second validation.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run src/pages/PlanningMode.plannerEffort.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does NOT type-check
// this file — the runner surfaces type errors as failures instead.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

// The live socket is not part of any claim here, and opening one in jsdom would
// only add a timer to clean up.
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

// Markdown pulls mermaid/katex; none of it is under test and all of it is slow.
vi.mock('../lib/markdown', () => ({
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));

/** Every fetch the page made, in order. */
let calls: { url: string; init: RequestInit | undefined }[] = [];

/** The POST that started the planner, or undefined when none was made. */
function startCall(): { url: string; init: RequestInit | undefined } | undefined {
  return calls.find((c) => c.init?.method === 'POST' && /\/planning$/.test(c.url));
}

const idle: PlanningStatus = {
  active: false,
  sessionUuid: '',
  sessionId: null,
  startedAt: null,
  status: '',
  currentQuestion: null,
  runningPlan: null,
  rawReply: null,
  history: [],
  planDir: null,
  mode: '',
  model: '',
  effort: '',
  reviseTaskId: null,
  lastError: null,
};

function stubFetch(): void {
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
      if (/\/planning$/.test(url)) return json(idle);
      return json([]);
    }),
  );
}

/** Mount the page and wait for the idea intake. */
async function mountPlanning(): Promise<void> {
  render(
    <MemoryRouter>
      <PlanningMode />
    </MemoryRouter>,
  );
  await screen.findByLabelText('describe what you want to build');
}

function picker(): HTMLSelectElement {
  return screen.getByLabelText('planner effort') as HTMLSelectElement;
}

/** Fill the idea box and press Start — the button is disabled while it is empty. */
function startPlanningRun(): void {
  fireEvent.change(screen.getByLabelText('describe what you want to build'), {
    target: { value: 'add a bulk export' },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Start planning' }));
}

/** The parsed body of the start POST. */
function startBody(): Record<string, unknown> {
  return JSON.parse(String(startCall()?.init?.body)) as Record<string, unknown>;
}

beforeEach(() => {
  localStorage.clear();
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('planner effort picker', () => {
  // The options ARE the closed set (plus the no-op entry). If this list ever
  // drifts from claudeflags.ValidEfforts, the UI starts offering a 400.
  it('offers the planner default first, then exactly the closed set', async () => {
    await mountPlanning();
    const values = Array.from(picker().options).map((o) => o.value);
    expect(values).toEqual(['default', 'low', 'medium', 'high', 'xhigh', 'max']);
  });

  // Empty storage must land on the entry that CHANGES NOTHING. Defaulting to a
  // real rung here would silently re-price every planning run on the machine.
  it('defaults to "planner default" when storage is empty', async () => {
    await mountPlanning();
    expect(picker().value).toBe('default');
  });

  it('defaults to the stored choice when storage has one', async () => {
    localStorage.setItem('swarmery.planning.effort', 'max');
    await mountPlanning();
    expect(picker().value).toBe('max');
  });

  // Its OWN key: the phase runner's choice must not leak into the wizard.
  it('ignores the phase runner\'s key', async () => {
    localStorage.setItem('swarmery.phaserun.effort', 'low');
    await mountPlanning();
    expect(picker().value).toBe('default');
  });

  // A value written by a future (or corrupted) build must not be offered back.
  it('falls back to the default when the stored value is not in the set', async () => {
    localStorage.setItem('swarmery.planning.effort', 'ludicrous');
    await mountPlanning();
    expect(picker().value).toBe('default');
  });

  it('sends the chosen effort alongside the model', async () => {
    await mountPlanning();
    fireEvent.change(picker(), { target: { value: 'low' } });
    startPlanningRun();

    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });
    expect(startCall()?.url).toBe('/api/projects/3/planning');
    expect(startBody()).toEqual({ idea: 'add a bulk export', model: 'opus', effort: 'low' });
  });

  // THE negative contract, and the reason this file exists. `{"effort":"default"}`
  // would be folded by the daemon to "omit --effort", and an omitted --effort is
  // xhigh — so the word must never reach the wire. Only the ABSENT key lets the
  // daemon's ladder pick.
  it('sends NO effort key at all for "planner default"', async () => {
    await mountPlanning();
    expect(picker().value).toBe('default');
    startPlanningRun();

    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });
    const body = startBody();
    expect('effort' in body).toBe(false);
    expect(body).toEqual({ idea: 'add a bulk export', model: 'opus' });
  });

  // The model picker must keep working unchanged — effort is an addition beside
  // it, not a replacement for it.
  it('leaves the model picker\'s own contract alone', async () => {
    await mountPlanning();
    fireEvent.change(screen.getByLabelText('planner model'), { target: { value: 'sonnet' } });
    startPlanningRun();

    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });
    expect(startBody()).toEqual({ idea: 'add a bulk export', model: 'sonnet' });
  });

  // "Survives a remount" is the observable form of "it was written to storage".
  it('remembers the choice across a remount', async () => {
    await mountPlanning();
    fireEvent.change(picker(), { target: { value: 'xhigh' } });
    startPlanningRun();
    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });

    cleanup();
    stubFetch();
    await mountPlanning();
    expect(picker().value).toBe('xhigh');
  });

  // Safari private mode throws on ACCESS, not only on write — both halves have
  // to be survivable or the whole planning page goes blank behind a storage quirk.
  it('renders and still starts when localStorage throws', async () => {
    const boom = (): never => {
      throw new Error('storage disabled');
    };
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(boom);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(boom);

    await mountPlanning();
    expect(picker().value).toBe('default');
    fireEvent.change(picker(), { target: { value: 'medium' } });
    startPlanningRun();

    await waitFor(() => {
      expect(startCall()).toBeDefined();
    });
    expect(startBody()).toEqual({ idea: 'add a bulk export', model: 'opus', effort: 'medium' });
    vi.restoreAllMocks();
  });
});
