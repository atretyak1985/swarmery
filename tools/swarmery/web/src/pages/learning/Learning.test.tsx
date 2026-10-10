// @vitest-environment jsdom
//
// Learning (Canvas v3 phase 6). The claims:
//
//   1. /learning renders four tabs, Lessons first with its candidate count,
//      and the embedded Lessons tab carries no page heading or calibration.
//   2. `?tab=classifier` selects the classifier: sentence titles, the words
//      off · watching · acting, and no code-name mode on screen.
//   3. Choosing "watching" calls putDecisionMode with 'shadow'.
//   4. Proof renders its empty state and performs no network call.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/learning`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as decisions from '../../api/decisions';
import * as lessons from '../../api/lessons';
import * as calibration from '../../api/calibration';
import { Learning } from './Learning';
import { Proof } from './Proof';

vi.mock('../../api/decisions', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/decisions')>();
  return { ...real, fetchDecisions: vi.fn(), putDecisionMode: vi.fn() };
});
vi.mock('../../api/lessons', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/lessons')>();
  return { ...real, fetchLessons: vi.fn(), fetchRetirements: vi.fn() };
});
vi.mock('../../api/calibration', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/calibration')>();
  return { ...real, fetchCalibration: vi.fn() };
});

function stats(questionId: string, over: Partial<decisions.QuestionStats> = {}): decisions.QuestionStats {
  return {
    questionId,
    mode: 'shadow',
    threshold: 0.6,
    calls: 503,
    errors: 18,
    acted: 0,
    withTruth: 0,
    agreed: 0,
    agreement: null,
    histogram: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
    ...over,
  };
}

const RESP: decisions.DecisionsResponse = {
  configured: true,
  local: true,
  claude: false,
  questions: [
    stats('d2.task_type'),
    stats('d2.failure_cause', { mode: 'off', calls: 0, errors: 0 }),
  ],
};

function lesson(id: number): lessons.Lesson {
  return {
    id,
    phaseId: 1,
    phaseName: 'phase',
    planId: 'plan',
    sourcePhaseRun: 'run',
    title: `lesson ${String(id)}`,
    normTitle: `lesson ${String(id)}`,
    guidance: 'guidance',
    areaGlobs: [],
    evidence: [],
    cause: '',
    sourceParagraph: '',
    surpriseIndex: null,
    status: 'candidate',
    linkedNormTitle: '',
    mergedIntoId: null,
    recurrences: 1,
    recurrenceRuns: [],
    model: 'm',
    createdAt: '2026-09-27T00:00:00Z',
    updatedAt: '2026-09-27T00:00:00Z',
    activatedAt: null,
    retiredAt: null,
    retireReason: null,
    promotedBranch: '',
    matches: [],
    effectiveness: null,
  };
}

function renderAt(url: string): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/learning" element={<Learning />} />
        <Route path="/p/:slug/learning" element={<Learning />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.mocked(decisions.fetchDecisions).mockResolvedValue(RESP);
  vi.mocked(decisions.putDecisionMode).mockResolvedValue(RESP);
  vi.mocked(lessons.fetchLessons).mockResolvedValue([1, 2, 3].map(lesson));
  vi.mocked(lessons.fetchRetirements).mockResolvedValue([]);
  vi.mocked(calibration.fetchCalibration).mockResolvedValue({
    dims: ['model', 'effort'],
    minSamples: 5,
    groups: [],
    hiddenGroups: 0,
    hiddenRuns: 0,
  } as unknown as calibration.CalibrationReport);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('Learning', () => {
  it('renders four tabs with Lessons first and its candidate count', async () => {
    renderAt('/learning');
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((t) => t.textContent?.replace(/\d+$/, ''))).toEqual([
      'Lessons',
      'The classifier',
      'Forecast honesty',
      'Proof',
    ]);
    expect(tabs[0]?.getAttribute('aria-selected')).toBe('true');
    await waitFor(() => expect(tabs[0]?.textContent).toBe('Lessons3'));
    expect(lessons.fetchLessons).toHaveBeenCalledWith('candidate');
    // Embedded: one page heading (Learning) and no calibration panel.
    expect(screen.getAllByRole('heading', { level: 1 }).map((h) => h.textContent)).toEqual(['Learning']);
    expect(screen.queryByText('How honest forecasts are')).toBeNull();
  });

  it('selects the classifier from ?tab= and speaks in sentences and mode words', async () => {
    renderAt('/learning?tab=classifier');
    expect(screen.getByRole('tab', { name: 'The classifier' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('What kind of task was it?')).toBeTruthy();
    expect(screen.getByText('d2.task_type')).toBeTruthy();
    expect(screen.getByText('Why did it fail?')).toBeTruthy();
    const sw = screen.getByRole('group', { name: 'mode for What kind of task was it?' });
    expect(within(sw).getAllByRole('button').map((b) => b.textContent)).toEqual(['off', 'watching', 'acting']);
    const panel = screen.getByRole('tabpanel');
    expect(panel.textContent).not.toMatch(/\bshadow\b|\bactive\b/);
    const link = screen.getAllByRole('link', { name: 'check in Inbox →' })[0];
    expect(link?.getAttribute('href')).toBe('/inbox?tab=classifier');
  });

  it('scopes the Inbox link to the project', async () => {
    renderAt('/p/shop/learning?tab=classifier');
    const link = (await screen.findAllByRole('link', { name: 'check in Inbox →' }))[0];
    expect(link?.getAttribute('href')).toBe('/p/shop/inbox?tab=classifier');
  });

  it("calls putDecisionMode with 'shadow' when watching is chosen", async () => {
    renderAt('/learning?tab=classifier');
    const sw = await screen.findByRole('group', { name: 'mode for Why did it fail?' });
    fireEvent.click(within(sw).getByRole('button', { name: 'watching' }));
    await waitFor(() =>
      expect(decisions.putDecisionMode).toHaveBeenCalledWith('d2.failure_cause', 'shadow'),
    );
  });

  it('renders the calibration panel under Forecast honesty', async () => {
    renderAt('/learning?tab=honesty');
    expect(await screen.findByText('How honest forecasts are')).toBeTruthy();
    expect(screen.getByText('calibration')).toBeTruthy();
  });

  it('renders Proof as an empty state with no fetch', () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal('fetch', fetchSpy);
    render(
      <MemoryRouter>
        <Proof inboxHref="/inbox" />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'Because of you' })).toBeTruthy();
    expect(screen.getByText('Nothing to show yet.')).toBeTruthy();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('loads no classifier or calibration data on the Proof tab', () => {
    renderAt('/learning?tab=proof');
    expect(screen.getByText('Nothing to show yet.')).toBeTruthy();
    expect(decisions.fetchDecisions).not.toHaveBeenCalled();
    expect(calibration.fetchCalibration).not.toHaveBeenCalled();
  });
});
