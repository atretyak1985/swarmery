// @vitest-environment jsdom
//
// State-machine tests for the page-level improver card. The invariant worth
// pinning is the GATE: in `proposed` the card must offer accept/dismiss and
// NOTHING that starts planning, and a planning conflict must render as a
// sentence with a link rather than a status code.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/RetroImproveCard.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, render, screen, waitFor } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import type { Project, RetroAnalysis, RetroAnalysisStatus } from '../api/types';

// vi.mock is hoisted above every const in this file, so everything the factory
// closes over has to be hoisted with it.
const mocks = vi.hoisted(() => {
  class FakeConflict extends Error {
    readonly sessionUuid: string;
    readonly projectSlug: string;
    constructor(message: string, sessionUuid: string, projectSlug: string) {
      super(message);
      this.name = 'RetroPlanConflictError';
      this.sessionUuid = sessionUuid;
      this.projectSlug = projectSlug;
    }
  }
  return {
    FakeConflict,
    fetchRetroAnalysis: vi.fn(),
    startRetroAnalysis: vi.fn(),
    decideRetroAnalysis: vi.fn(),
    planFromRetroAnalysis: vi.fn(),
  };
});
const { FakeConflict, fetchRetroAnalysis, planFromRetroAnalysis } = mocks;

vi.mock('../api', () => ({
  fetchRetroAnalysis: mocks.fetchRetroAnalysis,
  startRetroAnalysis: mocks.startRetroAnalysis,
  decideRetroAnalysis: mocks.decideRetroAnalysis,
  planFromRetroAnalysis: mocks.planFromRetroAnalysis,
  RetroPlanConflictError: mocks.FakeConflict,
}));

const PROJECTS: Project[] = [
  { id: 1, slug: '-work-alpha', name: 'Alpha' } as Project,
  { id: 2, slug: '-work-beta', name: 'Beta' } as Project,
];

const scopeState = vi.hoisted(() => ({ project: null as { id: number; slug: string; name: string | null } | null }));

vi.mock('../lib/scope', () => ({
  useScope: () => ({
    scope: scopeState.project === null ? null : scopeState.project.slug,
    projects: PROJECTS,
    scopeName: scopeState.project?.name ?? null,
    scopeProject: scopeState.project,
    setScope: vi.fn(),
  }),
}));

import { RetroImproveCard } from './RetroImproveCard';

function analysis(status: RetroAnalysisStatus, over: Partial<RetroAnalysis> = {}): RetroAnalysis {
  return {
    id: 7,
    windowFrom: '2026-08-10',
    windowTo: '2026-08-24',
    scope: '',
    digestSha256: 'abc',
    markdown: '## Що болить\nБолить [E:agent:tech-lead]\n',
    citations: 3,
    status,
    error: '',
    planningSessionUuid: '',
    createdAt: '2026-08-24T09:00:00Z',
    decidedAt: null,
    ...over,
  };
}

function renderCard(): void {
  render(
    <MemoryRouter>
      <RetroImproveCard range={{ from: '2026-08-10', to: '2026-08-24' }} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  scopeState.project = null;
  vi.clearAllMocks();
});
afterEach(cleanup);

describe('RetroImproveCard', () => {
  it('offers Improve and says what it will do when there is no analysis', async () => {
    fetchRetroAnalysis.mockResolvedValue({ analysis: null });
    renderCard();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^improve$/i })).toBeTruthy();
    });
    expect(screen.getByText(/until you accept it/i)).toBeTruthy();
  });

  it('shows an elapsed timer and disables the button while running', async () => {
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('running') });
    renderCard();
    await waitFor(() => {
      expect(screen.getByText(/the improver is reading the report/i)).toBeTruthy();
    });
    const btn = screen.getByRole('button', { name: /analysing/i }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it('shows the row error VERBATIM on failure, plus a retry', async () => {
    const reason = 'the analysis cites no evidence: every claim must end in an [E:kind:id] marker';
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('failed', { error: reason }) });
    renderCard();
    await waitFor(() => {
      expect(screen.getByText(reason)).toBeTruthy();
    });
    expect(screen.getByRole('button', { name: /try again/i })).toBeTruthy();
    expect(screen.queryByText(/something went wrong/i)).toBeNull();
  });

  // The gate: proposed offers a decision and nothing else.
  it('offers only accept/dismiss in `proposed` — never a plan button', async () => {
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('proposed') });
    renderCard();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^accept$/i })).toBeTruthy();
    });
    expect(screen.getByRole('button', { name: /^dismiss$/i })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /create a plan/i })).toBeNull();
    expect(screen.queryByLabelText(/plan in/i)).toBeNull();
    expect(screen.getByText(/3 evidence citations/i)).toBeTruthy();
  });

  it('disables planning with a hint while no project is chosen', async () => {
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('accepted') });
    renderCard(); // unscoped ⇒ no pre-filled target
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /create a plan/i })).toBeTruthy();
    });
    const btn = screen.getByRole('button', { name: /create a plan/i }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.getByText(/the changes land in the agent system/i)).toBeTruthy();
  });

  it('pre-fills the target from the page scope', async () => {
    scopeState.project = PROJECTS[0] ?? null;
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('accepted') });
    renderCard();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /create a plan/i })).toBeTruthy();
    });
    const select = screen.getByLabelText(/plan in/i) as HTMLSelectElement;
    expect(select.value).toBe('1');
    const btn = screen.getByRole('button', { name: /create a plan/i }) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
  });

  it('links to the planning session once planned', async () => {
    scopeState.project = PROJECTS[0] ?? null;
    fetchRetroAnalysis.mockResolvedValue({
      analysis: analysis('planned', { planningSessionUuid: 'uuid-plan' }),
    });
    renderCard();
    await waitFor(() => {
      expect(screen.getByRole('link', { name: /open the planning session/i })).toBeTruthy();
    });
    const link = screen.getByRole('link', { name: /open the planning session/i });
    expect(link.getAttribute('href')).toBe('/p/-work-alpha/planning');
  });

  // SC-7: a conflict is a sentence and a link, never a bare status code.
  it('renders a planning conflict in words with a link to the active session', async () => {
    scopeState.project = PROJECTS[0] ?? null;
    fetchRetroAnalysis.mockResolvedValue({ analysis: analysis('accepted') });
    planFromRetroAnalysis.mockRejectedValue(
      new FakeConflict('a planning run is already active for this project', 'uuid-other', '-work-alpha'),
    );
    renderCard();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /create a plan/i })).toBeTruthy();
    });
    (screen.getByRole('button', { name: /create a plan/i }) as HTMLButtonElement).click();
    await waitFor(() => {
      expect(screen.getByText(/already active for this project/i)).toBeTruthy();
    });
    expect(screen.getByRole('link', { name: /open the active session/i })).toBeTruthy();
    expect(screen.queryByText(/409/)).toBeNull();
  });
});
