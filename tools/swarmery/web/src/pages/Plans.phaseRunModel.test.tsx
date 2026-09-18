// @vitest-environment jsdom
//
// The per-phase model picker on the Plans page (phase-model-selection phase 2).
//
// Every claim here is about the WIRE, not about the widget: `../api` is NOT
// mocked, so the real `runEpicPhase` runs and the assertions read the actual
// `fetch` init. That matters because the contract this phase has to keep is a
// negative one — "daemon default" must send NO BODY AT ALL, not `{}` and not
// `{"model":""}`. A mocked api module would happily record an argument that the
// real function then dropped on the floor, and the one regression that would
// silently re-break a working machine (every phase run quietly stops honouring
// SWARMERY_PHASERUN_MODEL) is exactly that one.
//
// The second fence is the OPTION SET. The picker's options are the closed set
// the daemon accepts, so "the UI cannot send a value the API would reject" is
// structural rather than a second validation — asserting the rendered option
// values is what keeps it structural.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run src/pages/Plans.phaseRunModel.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does NOT type-check
// this file — the runner surfaces type errors as failures instead.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase } from '../api/types';
import { Plans } from './Plans';

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

/** The POST that started a phase run, or undefined when none was made. */
function runCall(): { url: string; init: RequestInit | undefined } | undefined {
  return calls.find((c) => /\/phases\/\d+\/run$/.test(c.url));
}

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
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

const epic = (phases: EpicPhase[]): Epic => ({
  taskId: 77,
  externalId: '2026-09-18-phase-model-selection',
  projectId: 3,
  projectSlug: 'swarmery',
  title: 'Per-phase model selection',
  status: 'active',
  startedAt: '2026-09-18T09:00:00Z',
  planDir: '/ws/plan',
  hasSummary: false,
  hasSpec: false,
  spec: null,
  phases,
  rollup: { done: 0, total: 3, pct: 0, incompletePhases: 1 },
  planRun: null,
  cardExternalId: null,
  linkedSessions: [],
});

let epics: Epic[] = [];

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
          status: url.includes('/run') ? 202 : 200,
          json: () => Promise.resolve(body),
        } as Response);
      if (url.startsWith('/api/epics?')) return json(epics);
      if (/\/phases\/\d+\/run$/.test(url)) return json({ status: 'running', sessionUuid: 'u-1' });
      return json([]);
    }),
  );
}

/** Mount the page and wait for the phase row's Run button. */
async function mountPlans(): Promise<void> {
  render(
    <MemoryRouter>
      <Plans />
    </MemoryRouter>,
  );
  await screen.findByRole('button', { name: 'Run phase' });
}

function picker(): HTMLSelectElement {
  return screen.getByLabelText('phase run model') as HTMLSelectElement;
}

beforeEach(() => {
  epics = [epic([phase()])];
  localStorage.clear();
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('phase run model picker', () => {
  // The options ARE the closed set (plus the no-op entry). If this list ever
  // drifts from planning.Models, the UI starts offering a 400.
  it('offers the daemon default first, then exactly the closed set', async () => {
    await mountPlans();
    const values = Array.from(picker().options).map((o) => o.value);
    expect(values).toEqual(['default', 'opus', 'sonnet', 'fable']);
  });

  // Empty storage must land on the entry that CHANGES NOTHING. Defaulting to a
  // real model here would silently re-point every phase run on the machine.
  it('defaults to "daemon default" when storage is empty', async () => {
    await mountPlans();
    expect(picker().value).toBe('default');
  });

  it('defaults to the stored choice when storage has one', async () => {
    localStorage.setItem('swarmery.phaserun.model', 'fable');
    await mountPlans();
    expect(picker().value).toBe('fable');
  });

  // Its OWN key: the planner's choice must not leak into phase runs.
  it('ignores the planner\'s key', async () => {
    localStorage.setItem('swarmery.planning.model', 'sonnet');
    await mountPlans();
    expect(picker().value).toBe('default');
  });

  // A value written by a future (or corrupted) build must not be offered back.
  it('falls back to the default when the stored value is not in the set', async () => {
    localStorage.setItem('swarmery.phaserun.model', 'claude-opus-5[1m]');
    await mountPlans();
    expect(picker().value).toBe('default');
  });

  it('sends the chosen model as a JSON body', async () => {
    await mountPlans();
    fireEvent.change(picker(), { target: { value: 'sonnet' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));

    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });
    const call = runCall();
    expect(call?.url).toBe('/api/epics/77/phases/11/run');
    expect(call?.init?.method).toBe('POST');
    expect(call?.init?.body).toBe('{"model":"sonnet"}');
    expect(call?.init?.headers).toEqual({ 'Content-Type': 'application/json' });
  });

  // The negative contract. `{}` or `{"model":""}` would also be accepted by the
  // daemon today, which is precisely why only "no body at all" can be asserted:
  // anything else is a choice the operator did not make, written into the wire.
  it('sends NO body at all for "daemon default"', async () => {
    await mountPlans();
    expect(picker().value).toBe('default');
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));

    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });
    const call = runCall();
    expect(call?.init?.method).toBe('POST');
    expect(call?.init?.body).toBeUndefined();
    expect(call?.init?.headers).toBeUndefined();
  });

  // "Survives a remount" is the observable form of "it was written to storage".
  it('remembers the choice across a remount', async () => {
    await mountPlans();
    fireEvent.change(picker(), { target: { value: 'opus' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));
    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });

    cleanup();
    stubFetch();
    await mountPlans();
    expect(picker().value).toBe('opus');
  });

  // Safari private mode throws on ACCESS, not only on write — both halves have
  // to be survivable or the whole Plans page goes blank behind a storage quirk.
  it('renders and still runs when localStorage throws', async () => {
    const boom = (): never => {
      throw new Error('storage disabled');
    };
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(boom);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(boom);

    await mountPlans();
    expect(picker().value).toBe('default');
    fireEvent.change(picker(), { target: { value: 'fable' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));

    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });
    expect(runCall()?.init?.body).toBe('{"model":"fable"}');
    vi.restoreAllMocks();
  });
});

describe('which model a finished run used (SC-6)', () => {
  // Read from the phase DTO's runModel, which the daemon fills from the linked
  // session — never from what the picker was set to when the button was pressed.
  it('labels a finished run with the session\'s model, shortened', async () => {
    epics = [
      epic([
        phase({
          runState: 'done',
          runOutcome: 'completed',
          checkboxesDone: 3,
          runSessionUuid: 'u-1',
          runModel: 'claude-fable-5-1',
          runEndedAt: '2026-09-18T10:00:00Z',
          completionState: 'complete',
        }),
      ]),
    ];
    render(
      <MemoryRouter>
        <Plans />
      </MemoryRouter>,
    );
    const chip = await screen.findByText('fable');
    // The full pinned ID stays reachable — the label shortens, it does not erase.
    expect(chip.getAttribute('data-tip')).toContain('claude-fable-5-1');
  });

  // The operator's env knob carries a context-window suffix that is not in the
  // closed set. It must shorten for the label and survive verbatim in the tip.
  it('shortens a context-window-suffixed id without rewriting it', async () => {
    epics = [
      epic([
        phase({
          runState: 'failed',
          runOutcome: 'failed',
          runSessionUuid: 'u-1',
          runModel: 'claude-opus-5[1m]',
          runEndedAt: '2026-09-18T10:00:00Z',
        }),
      ]),
    ];
    render(
      <MemoryRouter>
        <Plans />
      </MemoryRouter>,
    );
    const chip = await screen.findByText('opus');
    expect(chip.getAttribute('data-tip')).toContain('claude-opus-5[1m]');
  });

  // A live run has not used anything yet — the label would be a claim about a
  // process still in flight.
  it('says nothing while the run is still going', async () => {
    epics = [
      epic([
        phase({
          runState: 'running',
          runOutcome: 'running',
          runSessionUuid: 'u-1',
          runModel: 'claude-opus-5',
          runStartedAt: '2026-09-18T10:00:00Z',
        }),
      ]),
    ];
    render(
      <MemoryRouter>
        <Plans />
      </MemoryRouter>,
    );
    await screen.findByText(/Running/);
    expect(screen.queryByText('opus')).toBeNull();
  });

  // A phase that never ran has nothing to say.
  it('says nothing for a phase that never ran', async () => {
    await mountPlans();
    expect(screen.queryByText('opus')).toBeNull();
    expect(screen.queryByText('sonnet')).toBeNull();
  });
});
