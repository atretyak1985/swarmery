// @vitest-environment jsdom
//
// The classifier copy model (Canvas v3 phase 6, artboard 1e): one status
// sentence per question — no data, unchecked, a percentage, or suspicious.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/learning
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { describe, expect, it } from 'vitest';
import type { QuestionStats } from '../../api/decisions';
import {
  CHECKS_FOR_TRUST,
  MODE_WORD,
  MODES,
  isSuspicious,
  questionSentence,
  statusSentence,
} from './classifierModel';

function q(over: Partial<QuestionStats> = {}): QuestionStats {
  return {
    questionId: 'd2.task_type',
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

describe('classifier model', () => {
  it('names modes in the dictionary words, in switch order', () => {
    expect(MODES.map((m) => MODE_WORD[m])).toEqual(['off', 'watching', 'acting']);
  });

  it('turns question ids into sentences and leaves unknown ids alone', () => {
    expect(questionSentence('d2.task_type')).toBe('What kind of task was it?');
    expect(questionSentence('d3.divergence_cause')).toBe('Why did the run leave the plan?');
    expect(questionSentence('d9.new')).toBe('d9.new');
  });

  it('explains when data will appear for a question never asked', () => {
    const s = statusSentence(q({ questionId: 'd1.run_end', calls: 0, errors: 0 }));
    expect(s.empty).toBe(true);
    expect(s.text).toBe(
      "No data yet — this question is asked when a plan phase run ends and the rules can't tell. Your first scored phase run will populate it.",
    );
  });

  it('says nothing was checked yet when there is no ground truth', () => {
    const s = statusSentence(q());
    expect(s.empty).toBe(false);
    expect(s.text).toBe(
      `Guessed 503 times, 18 times it couldn't answer. You haven't checked any yet, so we can't say how often it's right. Check ~${String(CHECKS_FOR_TRUST)} in the Inbox and this line becomes a percentage.`,
    );
    expect(s.segments.find((seg) => seg.mark === 'amber')?.text).toBe("You haven't checked any yet");
  });

  it('becomes a percentage once answers are checked', () => {
    const s = statusSentence(q({ errors: 0, withTruth: 40, agreed: 34, agreement: 0.85 }));
    expect(s.text).toBe('Guessed 503 times. Matches you in 85 % of 40 checked.');
    expect(s.tone).toBe('neutral');
  });

  it('asks for more checks below the trust bar', () => {
    const s = statusSentence(q({ errors: 0, withTruth: 12, agreed: 9, agreement: 0.75 }));
    expect(s.text).toContain('Matches you in 75 % of 12 checked.');
    expect(s.text).toContain(`Check ~${String(CHECKS_FOR_TRUST - 12)} more`);
  });

  it('flags an over-confident, mostly-wrong question as suspicious', () => {
    const sure = [0, 0, 0, 0, 0, 0, 0, 0, 5, 95];
    const bad = q({ calls: 485, errors: 0, withTruth: 20, agreed: 4, agreement: 0.2, histogram: sure });
    expect(isSuspicious(bad)).toBe(true);
    const s = statusSentence(bad);
    expect(s.tone).toBe('amber');
    expect(s.text).toMatch(/^Guessed 485 times\. Suspicious: .*Start checking here\.$/);
    expect(s.segments.find((seg) => seg.mark === 'red')?.text).toBe('Suspicious:');
  });

  it('does not flag a confident question that matches, nor an unchecked one', () => {
    const sure = [0, 0, 0, 0, 0, 0, 0, 0, 5, 95];
    expect(isSuspicious(q({ withTruth: 20, agreed: 18, agreement: 0.9, histogram: sure }))).toBe(false);
    expect(isSuspicious(q({ histogram: sure }))).toBe(false);
    const spread = [10, 10, 10, 10, 10, 10, 10, 10, 10, 10];
    expect(isSuspicious(q({ withTruth: 20, agreed: 4, agreement: 0.2, histogram: spread }))).toBe(false);
  });
});
