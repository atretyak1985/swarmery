// @vitest-environment jsdom
//
// Lessons — review-sourced candidates (verify-not-trust phase 8). The claims:
//
//   1. A lesson proposed from a repeated review finding carries a «review»
//      badge; a surprise lesson does not.
//   2. Its `finding:<id>` evidence links to each finding's phase Review tab;
//      an id the API could not resolve, and non-finding evidence, stay text.
//   3. Every lesson shows its confidence badge; Accept on a low one takes a
//      second click (the confirm step) and only then sends the confirm flag.
//   4. A run that produced no lesson shows its no_entry_reason.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/Lessons.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as lessons from '../api/lessons';
import { cleanup, render, screen, within } from '../test/render';
import { findingHref, Lessons } from './Lessons';

vi.mock('../api/lessons', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api/lessons')>();
  return {
    ...real,
    fetchLessons: vi.fn(),
    fetchRetirements: vi.fn(),
    fetchLessonGenerations: vi.fn(),
    acceptLesson: vi.fn(),
  };
});

const base: lessons.Lesson = {
  id: 0,
  phaseId: 906,
  phaseName: 'Phase 6 — GitLab provider',
  planId: '2026-10-09-phase-landing-repo-provider',
  sourcePhaseRun: '',
  title: '',
  normTitle: '',
  guidance: 'guidance',
  areaGlobs: ['tools/swarmery/internal/api/**'],
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
  createdAt: '2026-10-23T00:00:00Z',
  updatedAt: '2026-10-23T00:00:00Z',
  activatedAt: null,
  retiredAt: null,
  retireReason: null,
  promotedBranch: '',
  matches: [],
  effectiveness: null,
  source: 'surprise',
  reviewType: '',
  reviewArea: '',
  findings: [],
  confidence: 'medium',
  confidenceReason: '',
};

const finding18: lessons.LessonFinding = {
  id: 18,
  taskId: 900,
  projectSlug: 'swarmery',
  planId: '2026-10-09-phase-landing-repo-provider',
  phaseId: 905,
  phaseSeq: 5,
  phaseName: 'Phase 5 — Plans review UI',
  round: '1',
  findingId: 'R1-3',
  type: 'quality',
  file: 'tools/swarmery/internal/api/vcs.go',
  line: 84,
};

const reviewLesson: lessons.Lesson = {
  ...base,
  id: 4,
  title: 'Single-flight cold reads in internal/api',
  sourcePhaseRun: 'review:quality:tools/swarmery/internal/api',
  evidence: ['finding:18', 'finding:99'],
  recurrences: 2,
  source: 'review',
  reviewType: 'quality',
  reviewArea: 'tools/swarmery/internal/api',
  findings: [finding18],
};

const surpriseLesson: lessons.Lesson = {
  ...base,
  id: 3,
  title: 'Cache usage lives under usage.cache_creation',
  evidence: ['divergence:41'],
  surpriseIndex: 0.74,
};

beforeEach(() => {
  vi.mocked(lessons.fetchLessons).mockResolvedValue([reviewLesson, surpriseLesson]);
  vi.mocked(lessons.fetchRetirements).mockResolvedValue([]);
  vi.mocked(lessons.fetchLessonGenerations).mockResolvedValue([]);
  vi.mocked(lessons.acceptLesson).mockImplementation((id) =>
    Promise.resolve({ ...base, id, status: 'active' }),
  );
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function card(title: string): HTMLElement {
  const li = screen.getByText(title).closest('li');
  if (li === null) throw new Error(`no card for ${title}`);
  return li;
}

describe('Lessons — review-sourced candidates', () => {
  it('badges a review lesson and links its findings', async () => {
    render(
      <MemoryRouter>
        <Lessons embedded />
      </MemoryRouter>,
    );
    await screen.findByText(reviewLesson.title);

    const review = card(reviewLesson.title);
    expect(within(review).getByText('review')).toBeTruthy();
    const link = within(review).getByRole('link', { name: 'R1-3 · Phase 5 — Plans review UI' });
    expect(link.getAttribute('href')).toBe('/p/swarmery/plans/2026-10-09-phase-landing-repo-provider/phase/5/review');
    expect(link.getAttribute('title')).toContain('tools/swarmery/internal/api/vcs.go:84');
    // An id the API could not resolve stays text.
    expect(within(review).getByText('finding:99', { exact: false })).toBeTruthy();
    expect(within(review).getAllByRole('link')).toHaveLength(1);

    const surprise = card(surpriseLesson.title);
    expect(within(surprise).queryByText('review')).toBeNull();
    expect(within(surprise).queryAllByRole('link')).toHaveLength(0);
    expect(within(surprise).getByText('divergence:41')).toBeTruthy();
  });

  it('falls back to the plan, then to no link, when the phase is unknown', () => {
    expect(findingHref({ ...finding18, phaseSeq: 0 })).toBe('/p/swarmery/plans?task=900');
    expect(findingHref({ ...finding18, planId: '' })).toBe('/p/swarmery/plans?task=900');
    expect(findingHref({ ...finding18, projectSlug: '' })).toBeNull();
  });
});

const lowLesson: lessons.Lesson = {
  ...base,
  id: 7,
  title: 'A lesson from one run',
  confidence: 'low',
  confidenceReason: 'It rests on a single run.',
};
const highLesson: lessons.Lesson = { ...base, id: 8, title: 'A well-supported lesson', confidence: 'high' };

describe('Lessons — confidence', () => {
  function renderQueue(): void {
    render(
      <MemoryRouter>
        <Lessons embedded />
      </MemoryRouter>,
    );
  }

  it('badges every confidence', async () => {
    vi.mocked(lessons.fetchLessons).mockResolvedValue([lowLesson, highLesson, surpriseLesson]);
    renderQueue();
    await screen.findByText(lowLesson.title);
    expect(within(card(lowLesson.title)).getByTestId('confidence-badge').textContent).toBe('low confidence');
    expect(within(card(lowLesson.title)).getByTestId('confidence-badge').getAttribute('title')).toBe(
      'It rests on a single run.',
    );
    expect(within(card(highLesson.title)).getByTestId('confidence-badge').textContent).toBe('high confidence');
    expect(within(card(surpriseLesson.title)).getByTestId('confidence-badge').textContent).toBe('medium confidence');
  });

  it('asks for a second click before accepting a low lesson', async () => {
    vi.mocked(lessons.fetchLessons).mockResolvedValue([lowLesson]);
    renderQueue();
    await screen.findByText(lowLesson.title);
    within(card(lowLesson.title)).getByRole('button', { name: 'accept' }).click();
    const confirm = await screen.findByRole('group', { name: 'confirm accepting a low-confidence lesson' });
    expect(lessons.acceptLesson).not.toHaveBeenCalled();
    within(confirm).getByRole('button', { name: 'cancel' }).click();
    await screen.findByRole('button', { name: 'accept' });
    expect(lessons.acceptLesson).not.toHaveBeenCalled();

    screen.getByRole('button', { name: 'accept' }).click();
    (await screen.findByRole('button', { name: 'accept anyway' })).click();
    await vi.waitFor(() => expect(lessons.acceptLesson).toHaveBeenCalledWith(lowLesson.id, true));
  });

  it('accepts a medium or high lesson in one click, without the flag', async () => {
    vi.mocked(lessons.fetchLessons).mockResolvedValue([highLesson]);
    renderQueue();
    await screen.findByText(highLesson.title);
    within(card(highLesson.title)).getByRole('button', { name: 'accept' }).click();
    await vi.waitFor(() => expect(lessons.acceptLesson).toHaveBeenCalledWith(highLesson.id));
    expect(screen.queryByRole('button', { name: 'accept anyway' })).toBeNull();
  });

  it('shows why a run produced no lesson', async () => {
    vi.mocked(lessons.fetchLessonGenerations).mockResolvedValue([
      {
        sourcePhaseRun: 'run-1',
        phaseId: 1,
        phaseName: 'Phase 1',
        planId: 'plan-a',
        state: 'done',
        inserted: 0,
        linked: 0,
        rejected: [],
        error: '',
        noEntryReason: 'A one-off network outage.',
        model: 'm',
        createdAt: '2026-11-02T00:00:00Z',
        finishedAt: null,
      },
    ]);
    renderQueue();
    const log = await screen.findByTestId('generation-log');
    expect(within(log).getByText('A one-off network outage.')).toBeTruthy();
  });
});
