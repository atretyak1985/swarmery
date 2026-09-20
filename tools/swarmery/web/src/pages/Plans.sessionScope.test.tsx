// @vitest-environment jsdom
//
// The plan-sessions column's phase slice (plan-sessions phase filter, phase 2).
//
// These are claims about the RENDERED column, not about `scopePlanSessions` —
// that function has its own unit suite next door. What only a mounted page can
// prove is the wiring the unit tests cannot see: that opening a phase actually
// feeds `activePhaseId` through, that the operator's pick is keyed to the PLAN
// and therefore survives moving between phases, and that closing the details
// widens the column again without anything resetting the pick by hand.
//
// The fence worth having is SC-6, the negative one: the plan-run controller and
// the task_sessions rows (`linked` / `inferred`) have no phase coordinate in the
// data at all, so if they ever appeared in a phase slice the column would be
// inventing an attribution. Asserting their ABSENCE is what keeps that honest.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run src/pages/Plans.sessionScope.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does NOT type-check
// this file — the runner surfaces type errors as failures instead.

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, LinkedSession, Session, SessionPlanGroup } from '../api/types';
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

const TASK_ID = 77;
const PHASE_1 = 11;
const PHASE_2 = 22;
const PHASE_3 = 33;

const phase = (over: Partial<EpicPhase> = {}): EpicPhase => ({
  id: PHASE_1,
  seq: 1,
  name: 'Scope model',
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

const PHASES: EpicPhase[] = [
  phase(),
  phase({ id: PHASE_2, seq: 2, name: 'UI wiring', docRelPath: 'phase-2.md' }),
  phase({ id: PHASE_3, seq: 3, name: 'Nothing ran here', docRelPath: 'phase-3.md' }),
];

/** The one row the column cannot place on a phase by heuristic OR by field. */
const OPERATOR_LINK: LinkedSession = {
  sessionUuid: 'operator-0000-0000-0000',
  linkSource: 'heuristic',
  confidence: 0.6,
  costUsd: null,
  startedAt: '2026-09-20T08:00:00Z',
  endedAt: null,
};

const epic = (): Epic => ({
  taskId: TASK_ID,
  externalId: '2026-09-20-plan-sessions-phase-filter',
  projectId: 3,
  projectSlug: 'swarmery',
  title: 'plan-sessions phase filter',
  status: 'active',
  startedAt: '2026-09-20T09:00:00Z',
  planDir: '/ws/plan',
  hasSummary: false,
  hasSpec: false,
  spec: null,
  phases: PHASES,
  rollup: { done: 0, total: 9, pct: 0, incompletePhases: 3 },
  planRun: null,
  cardExternalId: null,
  linkedSessions: [OPERATOR_LINK],
});

function group(over: Partial<SessionPlanGroup> = {}): SessionPlanGroup {
  return {
    taskId: TASK_ID,
    title: 'plan-sessions phase filter',
    role: 'phase',
    phaseId: PHASE_1,
    phaseSeq: 1,
    phaseName: 'Scope model',
    ...over,
  };
}

function session(id: number, title: string, planGroup: SessionPlanGroup, startedAt: string): Session {
  return {
    id,
    projectId: 3,
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    sessionUuid: `uuid-${String(id)}`,
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status: 'completed',
    startedAt,
    endedAt: null,
    title,
    source: 'jsonl',
    planGroup,
  };
}

// Phase #1 has a run AND a subagent under it, phase #2 has one run, phase #3 has
// none — so "the pick survived" (SC-4) is distinguishable from "the automatic
// rule would have done the same thing anyway" when we move from #1 to #2.
const SESSIONS: Session[] = [
  session(1, 'controller run', group({ role: 'controller', phaseId: null, phaseSeq: null }), '2026-09-20T09:00:00Z'),
  session(2, 'phase 1 run', group(), '2026-09-20T09:05:00Z'),
  session(3, 'phase 1 subagent', group(), '2026-09-20T09:09:00Z'),
  session(4, 'phase 2 run', group({ phaseId: PHASE_2, phaseSeq: 2, phaseName: 'UI wiring' }), '2026-09-20T09:20:00Z'),
];

/** 4 grouped sessions + 1 linked-only row. */
const TOTAL = SESSIONS.length + 1;

function stubFetch(): void {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const json = (body: unknown): Promise<Response> =>
        Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response);
      if (url.startsWith('/api/epics?')) return json([epic()]);
      if (url.startsWith('/api/sessions?')) return json({ sessions: SESSIONS, nextCursor: null });
      // Opening a phase mounts the detail panel, which loads that phase's doc.
      if (/\/docs\?path=/.test(url)) return json({ path: 'phase.md', content: '# phase\n' });
      return json([]);
    }),
  );
}

/** The column under test. */
function column(): HTMLElement {
  return screen.getByLabelText('plan sessions');
}

/** Titles of the session rows currently rendered, in DOM order. */
function rowTitles(): string[] {
  return within(column())
    .queryAllByRole('listitem')
    .map((li) => li.textContent ?? '');
}

/** Mount the page and wait for the column to have loaded its sessions. */
async function mountPlans(): Promise<void> {
  render(
    <MemoryRouter>
      <Plans />
    </MemoryRouter>,
  );
  await waitFor(() => {
    expect(rowTitles().some((t) => t.includes('controller run'))).toBe(true);
  });
}

function openPhase(seq: number, name: string): void {
  fireEvent.click(screen.getByRole('button', { name: `open Phase ${String(seq)} — ${name} details` }));
}

function backToPhases(): void {
  fireEvent.click(screen.getByRole('button', { name: 'all phases' }));
}

function scopeToggle(): HTMLElement {
  return screen.getByLabelText('session scope');
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

describe('plan sessions column — no phase open', () => {
  it('shows every row of the plan and a bare count, with no scope toggle', async () => {
    await mountPlans();
    const titles = rowTitles();
    expect(titles).toHaveLength(TOTAL);
    expect(titles[0]).toContain('controller run');
    expect(titles.some((t) => t.includes('operator'))).toBe(true);
    expect(screen.queryByLabelText('session scope')).toBeNull();
    expect(within(column()).getByText(String(TOTAL))).toBeTruthy();
  });
});

describe('plan sessions column — a phase with sessions of its own', () => {
  it('narrows to exactly that phase and labels both sides of the toggle (SC-1, SC-3, SC-7)', async () => {
    await mountPlans();
    openPhase(1, 'Scope model');

    await waitFor(() => {
      expect(rowTitles()).toHaveLength(2);
    });
    const titles = rowTitles();
    expect(titles[0]).toContain('phase 1 run');
    expect(titles[1]).toContain('phase 1 subagent');

    const toggle = scopeToggle();
    expect(toggle.textContent).toContain('#1 (2)');
    expect(toggle.textContent).toContain(`all (${String(TOTAL)})`);
  });

  it('keeps the controller and the linked row out of the phase slice (SC-6)', async () => {
    await mountPlans();
    openPhase(1, 'Scope model');

    await waitFor(() => {
      expect(rowTitles()).toHaveLength(2);
    });
    const titles = rowTitles().join('|');
    expect(titles).not.toContain('controller run');
    expect(titles).not.toContain('operator');
    expect(titles).not.toContain('phase 2 run');
  });

  it('the toggle widens the column back to the whole plan (SC-3)', async () => {
    await mountPlans();
    openPhase(1, 'Scope model');
    await waitFor(() => {
      expect(rowTitles()).toHaveLength(2);
    });

    fireEvent.click(scopeToggle());

    await waitFor(() => {
      expect(rowTitles()).toHaveLength(TOTAL);
    });
    // Both numbers are still on screen — a widened column still says what the
    // phase slice would hold.
    expect(scopeToggle().textContent).toContain('#1 (2)');
  });
});

describe('plan sessions column — the pick belongs to the plan, not the phase', () => {
  it('“all” survives moving to another phase that HAS sessions of its own (SC-4)', async () => {
    await mountPlans();
    openPhase(1, 'Scope model');
    await waitFor(() => {
      expect(rowTitles()).toHaveLength(2);
    });
    fireEvent.click(scopeToggle());
    await waitFor(() => {
      expect(rowTitles()).toHaveLength(TOTAL);
    });

    backToPhases();
    openPhase(2, 'UI wiring');

    // The automatic rule would have narrowed to phase #2's single run; the
    // operator's standing choice wins.
    await waitFor(() => {
      expect(scopeToggle().textContent).toContain('#2 (1)');
    });
    expect(rowTitles()).toHaveLength(TOTAL);
  });

  it('closing the details widens the column again (SC-5)', async () => {
    await mountPlans();
    openPhase(1, 'Scope model');
    await waitFor(() => {
      expect(rowTitles()).toHaveLength(2);
    });

    backToPhases();

    await waitFor(() => {
      expect(rowTitles()).toHaveLength(TOTAL);
    });
    expect(screen.queryByLabelText('session scope')).toBeNull();
  });
});

describe('plan sessions column — a phase with no sessions of its own', () => {
  it('falls back to the whole plan and marks the empty slice “#3 (0)” (SC-2)', async () => {
    await mountPlans();
    openPhase(3, 'Nothing ran here');

    await waitFor(() => {
      expect(scopeToggle().textContent).toContain('#3 (0)');
    });
    expect(rowTitles()).toHaveLength(TOTAL);
    expect(scopeToggle().getAttribute('data-tip')).toBe(
      'this phase has no sessions of its own — showing the whole plan',
    );
  });

  it('a deliberate pick of that empty slice is honoured, not overridden', async () => {
    await mountPlans();
    openPhase(3, 'Nothing ran here');
    await waitFor(() => {
      expect(scopeToggle().textContent).toContain('#3 (0)');
    });

    fireEvent.click(scopeToggle());

    await waitFor(() => {
      expect(screen.getByText('no sessions for this phase')).toBeTruthy();
    });
    expect(rowTitles()).toHaveLength(0);
    // The plan's real size is still on screen, so the empty column is legible
    // as "filtered", not as "this plan never ran" (SC-7).
    expect(scopeToggle().textContent).toContain(`all (${String(TOTAL)})`);
  });
});
