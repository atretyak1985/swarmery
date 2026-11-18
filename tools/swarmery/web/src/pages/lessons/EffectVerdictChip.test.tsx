// @vitest-environment jsdom
//
// Effect verdicts on the Lessons page (honest learning phase 1). The claims:
//
//   1. The chip names each of the five verdicts; a row from an older daemon
//      (no verdict field) reads as "not enough data".
//   2. The confounder list names each confounder by kind and id with the
//      detector's sentence; no confounders renders nothing.
//   3. The Lessons page shows the chip and the confounder list on an active
//      lesson's card, and the confounded share of the measured active lessons.
//   4. The lesson detail shows the chip and the confounder list.
//
// `npx vitest run src/pages/lessons/EffectVerdictChip.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as lessons from '../../api/lessons';
import { cleanup, render, screen, within } from '../../test/render';
import { LessonDetail, Lessons } from '../Lessons';
import { ConfounderList, EffectVerdictChip } from './EffectVerdictChip';
import { confoundedShare, effectVerdictOf } from './effectVerdict';

vi.mock('../../api/lessons', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/lessons')>();
  return { ...real, fetchLessons: vi.fn(), fetchRetirements: vi.fn() };
});

function eff(verdict: lessons.EffectVerdict | undefined, confounders: lessons.LessonConfounder[] = []): lessons.LessonEffectiveness {
  const measured = verdict !== 'insufficient';
  return {
    lessonId: 0,
    windowN: 10,
    minRuns: 5,
    beforeN: measured ? 10 : 2,
    afterN: 10,
    medianBefore: measured ? 0.8 : null,
    medianAfter: measured ? 0.3 : null,
    medianDrop: measured ? 0.5 : null,
    uses: 4,
    relied: 1,
    reliedRate: 0.25,
    computedAt: '2026-11-02T00:00:00Z',
    ...(verdict !== undefined ? { verdict } : {}),
    confounders,
  };
}

const otherLesson: lessons.LessonConfounder = {
  kind: 'lesson',
  id: 'L-26',
  at: '2026-10-20T10:00:00Z',
  detail: 'L-26 "Check the ingest cursor" was activated in the same area (internal/ingest/**)',
};
const switchModel: lessons.LessonConfounder = {
  kind: 'model_switch',
  id: 'claude-opus-5→claude-opus-5-5',
  at: '2026-10-21T10:00:00Z',
  detail: 'the runs switched model inside the measured window',
};

function lesson(id: number, title: string, e: lessons.LessonEffectiveness | null): lessons.Lesson {
  return {
    id,
    phaseId: 1,
    phaseName: 'Phase 1',
    planId: 'plan-a',
    sourcePhaseRun: '',
    title,
    normTitle: title.toLowerCase(),
    guidance: `${title} guidance.`,
    areaGlobs: ['internal/ingest/**'],
    evidence: [],
    cause: '',
    sourceParagraph: '',
    surpriseIndex: null,
    status: 'active',
    linkedNormTitle: '',
    mergedIntoId: null,
    recurrences: 1,
    recurrenceRuns: [],
    model: '',
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    activatedAt: '2026-10-02T00:00:00Z',
    retiredAt: null,
    retireReason: null,
    promotedBranch: '',
    matches: [],
    effectiveness: e,
    source: 'surprise',
    reviewType: '',
    reviewArea: '',
    findings: [],
  };
}

const confounded = lesson(24, 'Confounded lesson', eff('confounded', [otherLesson, switchModel]));
const helped = lesson(58, 'Helped lesson', eff('helped'));
const hurt = lesson(59, 'Hurt lesson', eff('hurt'));
const unmeasured = lesson(70, 'Unmeasured lesson', eff('insufficient'));

beforeEach(() => {
  vi.mocked(lessons.fetchLessons).mockResolvedValue([confounded, helped, hurt, unmeasured]);
  vi.mocked(lessons.fetchRetirements).mockResolvedValue([]);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('EffectVerdictChip', () => {
  it('names each verdict', () => {
    const want: Record<lessons.EffectVerdict, string> = {
      helped: 'helped',
      hurt: 'hurt',
      no_effect: 'no effect',
      confounded: 'confounded',
      insufficient: 'not enough data',
    };
    for (const [verdict, label] of Object.entries(want) as [lessons.EffectVerdict, string][]) {
      render(<EffectVerdictChip verdict={verdict} />);
      const chip = screen.getByTestId('effect-verdict');
      expect(chip.textContent).toBe(label);
      expect(chip.getAttribute('data-verdict')).toBe(verdict);
      expect(chip.getAttribute('title')).not.toBe('');
      cleanup();
    }
  });

  it('reads a row without a verdict as insufficient', () => {
    expect(effectVerdictOf(eff(undefined))).toBe('insufficient');
    expect(effectVerdictOf(null)).toBe('insufficient');
    expect(effectVerdictOf(eff('helped'))).toBe('helped');
  });
});

describe('ConfounderList', () => {
  it('names every confounder by kind and id', () => {
    render(<ConfounderList confounders={[otherLesson, switchModel]} />);
    const list = screen.getByRole('list', { name: 'confounders' });
    const items = within(list).getAllByRole('listitem');
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toContain('another lesson');
    expect(items[0]?.textContent).toContain('L-26');
    expect(items[0]?.textContent).toContain('Check the ingest cursor');
    expect(items[1]?.textContent).toContain('model switch');
    expect(items[1]?.textContent).toContain('claude-opus-5→claude-opus-5-5');
  });

  it('renders nothing without confounders', () => {
    const { container } = render(<ConfounderList confounders={[]} />);
    expect(container.textContent).toBe('');
  });
});

describe('confoundedShare', () => {
  it('counts confounded among the measured active lessons only', () => {
    const retired = { ...lesson(80, 'Retired', eff('confounded')), status: 'retired' as const };
    expect(confoundedShare([confounded, helped, hurt, unmeasured, retired])).toEqual({
      measured: 3,
      confounded: 1,
      share: 1 / 3,
    });
    expect(confoundedShare([unmeasured])).toEqual({ measured: 0, confounded: 0, share: 0 });
  });
});

describe('Lessons page', () => {
  it('shows the chip, the confounders and the confounded share', async () => {
    render(
      <MemoryRouter>
        <Lessons embedded />
      </MemoryRouter>,
    );
    await screen.findByText(confounded.title);
    expect(screen.getByTestId('confounded-share').textContent).toContain('1 of 3 measured lessons are confounded (33%)');

    const card = screen.getByText(confounded.title).closest('li');
    if (card === null) throw new Error('no confounded card');
    expect(within(card).getByTestId('effect-verdict').textContent).toBe('confounded');
    const list = within(card).getByRole('list', { name: 'confounders' });
    expect(within(list).getByText('L-26')).toBeTruthy();

    const helpedCard = screen.getByText(helped.title).closest('li');
    if (helpedCard === null) throw new Error('no helped card');
    expect(within(helpedCard).getByTestId('effect-verdict').textContent).toBe('helped');
    expect(within(helpedCard).queryByRole('list', { name: 'confounders' })).toBeNull();
  });

  it('hides the share when nothing is measured', async () => {
    vi.mocked(lessons.fetchLessons).mockResolvedValue([unmeasured]);
    render(
      <MemoryRouter>
        <Lessons embedded />
      </MemoryRouter>,
    );
    await screen.findByText(unmeasured.title);
    expect(screen.queryByTestId('confounded-share')).toBeNull();
  });
});

describe('LessonDetail', () => {
  it('shows the chip and the confounder list', () => {
    render(
      <MemoryRouter>
        <LessonDetail lesson={confounded} busy={false} act={() => undefined} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId('effect-verdict').textContent).toBe('confounded');
    const list = screen.getByRole('list', { name: 'confounders' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(2);
  });
});
