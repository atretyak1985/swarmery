// @vitest-environment jsdom
//
// Learning (Canvas v3 phase 6). The claims:
//
//   1. /learning renders three accordion groups, Lessons first and open with its
//      lessons as rows, one page heading and no calibration panel.
//   2. `?tab=classifier` opens Classifier: sentence titles, the words
//      off · watching · acting, and no code-name mode on screen. `?tab=honesty`
//      opens the same group on its Forecast honesty row.
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
    source: 'surprise',
    reviewType: '',
    reviewArea: '',
    findings: [],
    effectiveness: null,
  };
}

/** A group header of the accordion: its label, then its count. */
function group(name: string): HTMLElement {
  return screen.getByRole('tab', { name: new RegExp(`^${name.toLowerCase()}\\s*\\d*$`) });
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
  it('renders three groups with Lessons open first and its lessons as rows', async () => {
    renderAt('/learning');
    const list = within(screen.getByRole('region', { name: 'Learning' }));
    expect(list.getAllByRole('tab')).toHaveLength(3);
    expect(list.getAllByRole('tab', { selected: true })).toHaveLength(1);
    await waitFor(() => expect(group('Lessons').textContent).toMatch(/3/));
    expect(group('Lessons').getAttribute('aria-selected')).toBe('true');
    expect(lessons.fetchLessons).toHaveBeenCalledWith();
    expect(await screen.findByRole('button', { name: /lesson 1/ })).toBeTruthy();
    // One page heading (Learning) and no calibration panel.
    expect(screen.getAllByRole('heading', { level: 1 }).map((h) => h.textContent)).toEqual(['Learning']);
    expect(screen.queryByText('How honest forecasts are')).toBeNull();
  });

  it('opens a lesson as a page with its actions', async () => {
    renderAt('/learning');
    expect(await screen.findByRole('heading', { level: 2, name: 'lesson 1' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'accept' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'dismiss' })).toBeTruthy();
  });

  it('selects the classifier from ?tab= and speaks in sentences and mode words', async () => {
    renderAt('/learning?tab=classifier');
    expect(group('Classifier').getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByText('What kind of task was it?')).toBeTruthy();
    expect(screen.getByText('d2.task_type')).toBeTruthy();
    expect(screen.getByText('Why did it fail?')).toBeTruthy();
    const sw = screen.getByRole('group', { name: 'mode for What kind of task was it?' });
    expect(within(sw).getAllByRole('button').map((b) => b.textContent)).toEqual(['off', 'watching', 'acting']);
    expect(document.body.textContent).not.toMatch(/\bshadow\b|\bactive\b/);
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

  it('renders the calibration panel under Forecast honesty, inside Classifier', async () => {
    renderAt('/learning?tab=honesty');
    expect(group('Classifier').getAttribute('aria-selected')).toBe('true');
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
