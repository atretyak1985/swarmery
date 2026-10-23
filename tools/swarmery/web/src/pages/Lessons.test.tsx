// @vitest-environment jsdom
//
// Lessons — review-sourced candidates (verify-not-trust phase 8). The claims:
//
//   1. A lesson proposed from a repeated review finding carries a «review»
//      badge; a surprise lesson does not.
//   2. Its `finding:<id>` evidence links to each finding's phase Review tab;
//      an id the API could not resolve, and non-finding evidence, stay text.
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
  return { ...real, fetchLessons: vi.fn(), fetchRetirements: vi.fn() };
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
