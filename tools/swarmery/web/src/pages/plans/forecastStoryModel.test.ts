// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import type { EpicPhase, PhaseSurprise } from '../../api/types';
import { forecastStory, NO_LESSON } from './forecastStoryModel';

function surprise(over: Partial<PhaseSurprise> = {}, detail: Partial<PhaseSurprise['detail']> = {}): PhaseSurprise {
  return {
    index: 0.72,
    top: 'size_miss',
    components: { size_miss: 1, duration_miss: 1 },
    weights: { size_miss: 0.15, duration_miss: 0.1 },
    detail: {
      forecastKind: 'posterior',
      forecastPostHoc: false,
      forecastAreas: ['apps/api/src/modules/bids'],
      actualAreas: null,
      unexpectedAreas: [],
      missedAreas: [],
      matchedAreas: [],
      forecastSize: 'L',
      actualSize: 'XS',
      sizeDistance: 3,
      forecastDuration: '90m-4h',
      actualDuration: '<30m',
      actualDurationS: 30,
      durationDistance: 2,
      forecastOutcome: 'done_with_concerns',
      actualOutcome: 'noop',
      testFailuresUnexpected: null,
      confidence: 0.55,
      majorMiss: true,
      ...detail,
    },
    revision: {
      index: 0.11,
      areasAdded: [
        'apps/api/src/database',
        'apps/api/src/modules/bids',
        'apps/api/src/modules/notify-dispatch',
        'apps/api/src/modules/contracts',
      ],
      areasDropped: [],
      sizeShift: null,
      durationShift: null,
      priorOutcome: 'done',
      posteriorOutcome: 'done_with_concerns',
      confidenceDelta: null,
    },
    summary: '',
    phaseId: 3,
    sessionUuid: 's-1',
    forecastDocHash: '',
    actualsSource: 'transcript',
    notifiedAt: null,
    autoVerifyAt: null,
    computedAt: '2026-09-27T10:00:00Z',
    ...over,
  };
}

function phase(over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    seq: 3,
    checkboxesDone: 0,
    checkboxesTotal: 8,
    runCheckboxesBefore: 0,
    completionReport: 'Shipped nothing.',
    runError: null,
    forecasts: [],
    surprise: surprise(),
    ...over,
  } as EpicPhase;
}

describe('forecastStory', () => {
  it('is null for an unscored run — explicit null or a DTO without the field', () => {
    expect(forecastStory(phase({ surprise: null }))).toBeNull();
    const { surprise: _omit, ...rest } = phase();
    expect(forecastStory(rest as EpicPhase)).toBeNull();
  });

  it('tells a large phase that ended in 30 seconds doing nothing', () => {
    const story = forecastStory(phase());
    expect(story?.headline).toBe('Planned as a large phase, it ended in 30 seconds doing nothing.');
    expect(story?.band).toBe('far off plan');
    expect(story?.expected).toBe(
      'A large phase, 90 min to 4 h, finishing with some concerns. The planner was 55 % sure — after reading the code it added 4 more areas (database, bids, notify-dispatch, contracts).',
    );
    expect(story?.happened).toContain('none of the 8 criteria ticked');
  });

  it('says the diff was not measured when actualAreas is null, and "no code touched" when it is empty', () => {
    expect(forecastStory(phase())?.happened).toBe(
      "30 seconds, none of the 8 criteria ticked. The diff was not measured, so we can't say which areas it touched.",
    );
    const empty = forecastStory(phase({ surprise: surprise({}, { actualAreas: [] }) }));
    expect(empty?.happened).toBe('30 seconds, no code touched, none of the 8 criteria ticked.');
    expect(empty?.happened).not.toContain('not measured');
  });

  it('falls back to "no lesson" when the report has no Where reality diverged paragraph', () => {
    const story = forecastStory(phase());
    expect(story?.why).toBeNull();
    expect(story?.next).toBe(NO_LESSON);
    expect(NO_LESSON).toBe('No lesson was proposed: the report has no Where reality diverged paragraph.');
  });

  it('takes "why" from the first sentence of the divergence paragraph, else from runError', () => {
    const withParagraph = forecastStory(
      phase({
        completionReport:
          'Done.\n\n**Where reality diverged:** Another run already owned the phase. It stopped on purpose.\n',
      }),
    );
    expect(withParagraph?.why).toBe('Another run already owned the phase.');
    expect(withParagraph?.next).not.toBe(NO_LESSON);

    const withError = forecastStory(phase({ runError: 'timeout after 4h. killed' }));
    expect(withError?.why).toBe('timeout after 4h.');
  });

  it('counts only the criteria this run ticked and reads the band from lib/offPlan', () => {
    const story = forecastStory(
      phase({
        checkboxesDone: 6,
        runCheckboxesBefore: 2,
        surprise: surprise({ index: 0.12 }, { actualOutcome: 'completed', actualAreas: ['a/b'] }),
      }),
    );
    expect(story?.band).toBe('on plan');
    expect(story?.happened).toBe('30 seconds, touched 1 area, 4 of the 8 criteria ticked.');
  });
});
