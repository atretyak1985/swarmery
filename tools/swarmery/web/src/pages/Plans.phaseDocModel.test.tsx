// @vitest-environment jsdom
//
// The phase DOC's own model declaration on the Plans page (phase-model-selection
// phase 3) — the `doc:` chip and its relationship to the plan-wide picker.
//
// The claim under test is the one the whole rung exists for: a declaration the
// operator can SEE, and which is never shown as if it were in effect when it is
// not. internal/dispatch/service.go:979 records the opposite — a `model:` chip
// rendered for several phases while dispatch ignored it, so the chip named a model
// no run ever used. Here the chip has three visually distinct states (in effect,
// overridden by the picker, unknown to the daemon) and the wire is asserted
// alongside it, because "the chip says sonnet" is worthless if the request that
// goes out pins something else.
//
// RESOLUTION IS NOT DUPLICATED HERE. The UI never computes which model wins; it
// sends the picker's choice or nothing at all, and the daemon's four-rung ladder
// (internal/phaserun.resolveModel) decides. So the wire assertions below are about
// ABSENCE — "picker on default ⇒ no body" — rather than about the UI sending the
// doc's value, which would be a second ladder to drift.
//
// Same dev-only runner story as Plans.phaseRunModel.test.tsx:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
//   npx vitest run src/pages/Plans.phaseDocModel.test.tsx
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does not type-check
// this file.

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
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../lib/markdown', () => ({
  Markdown: ({ children }: { children?: string }) => <div>{children}</div>,
}));

let calls: { url: string; init: RequestInit | undefined }[] = [];

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

/** The `doc:` chip, or null when the page renders none. */
function docChip(): HTMLElement | null {
  return screen.queryByText(/^doc:/);
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

describe('phase doc model chip', () => {
  // A doc that declares nothing must add nothing: the overwhelming majority of
  // phases, and any chip there would be noise on every plan on the machine.
  it('renders nothing when the doc declares no model', async () => {
    epics = [epic([phase()])];
    await mountPlans();
    expect(docChip()).toBeNull();
  });

  it('shows the declaration, shortened, when the doc carries one', async () => {
    epics = [epic([phase({ docModel: 'sonnet' })])];
    await mountPlans();
    expect(docChip()?.textContent).toBe('doc: sonnet');
  });

  // A full ID is as legal in the doc as a short name; the chip shortens it for
  // display exactly the way RunModelChip does.
  it('shortens a full model ID', async () => {
    epics = [epic([phase({ docModel: 'claude-fable-5-1' })])];
    await mountPlans();
    expect(docChip()?.textContent).toBe('doc: fable');
  });

  // The load-bearing case: picker on `default` ⇒ the request carries NO body, so
  // the daemon's rung 2 applies the doc's declaration. If this ever starts sending
  // a body, the doc silently stops governing and the chip becomes a lie.
  it('sends no body when the picker is on default, leaving the doc in charge', async () => {
    epics = [epic([phase({ docModel: 'sonnet' })])];
    await mountPlans();
    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));
    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });
    const init = runCall()?.init;
    expect(init?.body).toBeUndefined();
    expect(init?.headers).toBeUndefined();
  });

  // An explicit pick overrides every doc on the plan — and the chip has to SAY so
  // rather than keep reading as if the doc still applied.
  it('marks the declaration overridden when the operator picks a model', async () => {
    epics = [epic([phase({ docModel: 'sonnet' })])];
    await mountPlans();
    fireEvent.change(picker(), { target: { value: 'opus' } });
    await waitFor(() => {
      expect(docChip()?.className).toContain('line-through');
    });
    expect(docChip()?.getAttribute('data-tip')).toContain('overrides it');

    fireEvent.click(screen.getByRole('button', { name: 'Run phase' }));
    await waitFor(() => {
      expect(runCall()).toBeDefined();
    });
    expect(JSON.parse(String(runCall()?.init?.body))).toEqual({ model: 'opus' });
  });

  // The operator's stored choice is NOT discarded by the doc: a machine whose
  // picker was left on `fable` keeps overriding, and the chip reflects that from
  // the first render rather than after a change event.
  it('keeps a stored override in force over a declaring doc', async () => {
    localStorage.setItem('swarmery.phaserun.model', 'fable');
    epics = [epic([phase({ docModel: 'sonnet' })])];
    await mountPlans();
    expect(picker().value).toBe('fable');
    expect(docChip()?.className).toContain('line-through');
  });

  // A value the daemon does not know is shown VERBATIM and flagged, because the
  // run will be refused (409 doc-model-unknown) and the operator has to be able to
  // see the offending text to fix the line.
  it('flags a declaration the daemon does not know', async () => {
    epics = [epic([phase({ docModel: 'gpt-9' })])];
    await mountPlans();
    expect(docChip()?.textContent).toBe('doc: gpt-9');
    expect(docChip()?.className).toContain('text-red');
    expect(docChip()?.getAttribute('data-tip')).toContain('not a model this daemon knows');
  });
});
