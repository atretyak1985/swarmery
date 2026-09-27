// The classifier tab's copy model (Canvas v3 phase 6, artboard 1e). Pure: each
// question becomes a sentence, each mode a word from the code→UI dictionary,
// and each card one status sentence built from its stats. The component only
// lays these out, so every wording rule is unit-tested here.

import type { DecideMode, QuestionStats } from '../../api/decisions';
import { UI_TERMS } from '../../lib/glossary';

/** Checked answers before "matches you" is a number worth trusting. */
export const CHECKS_FOR_TRUST = 30;

/** Share of answers in the top confidence bucket that reads as over-confident. */
const SUSPICIOUS_TOP_SHARE = 0.8;

/** Below this agreement an over-confident question is suspicious. */
const SUSPICIOUS_AGREEMENT = 0.5;

/** The mode switch's order, left to right. */
export const MODES: readonly DecideMode[] = ['off', 'shadow', 'active'];

export const MODE_WORD: Record<DecideMode, string> = {
  off: 'off',
  shadow: UI_TERMS.shadow.ui,
  active: UI_TERMS.active.ui,
};

export const QUESTION_SENTENCE: Record<string, string> = {
  'd2.task_type': 'What kind of task was it?',
  'd2.outcome': 'How did it end?',
  'd2.failure_cause': 'Why did it fail?',
  'd1.run_end': 'How did the phase run end?',
  'd3.divergence_cause': 'Why did the run leave the plan?',
};

/** When a question is asked, and what its first answer will come from. */
const ASKED_WHEN: Record<string, { when: string; first: string }> = {
  'd1.run_end': {
    when: "a plan phase run ends and the rules can't tell",
    first: 'scored phase run',
  },
  'd2.task_type': { when: 'a session finishes', first: 'finished session' },
  'd2.outcome': { when: 'a session finishes', first: 'finished session' },
  'd2.failure_cause': { when: 'a finished session did not ship', first: 'failed session' },
  'd3.divergence_cause': {
    when: 'a phase run lands off its plan',
    first: 'off-plan phase run',
  },
};

/** The sentence for a question id; an unknown id reads as itself. */
export function questionSentence(id: string): string {
  return QUESTION_SENTENCE[id] ?? id;
}

export interface StatusSegment {
  text: string;
  /** Highlighted inline (the design's coloured phrase). */
  mark?: 'amber' | 'red';
}

export interface StatusSentence {
  text: string;
  tone: 'neutral' | 'amber';
  segments: StatusSegment[];
  /** No answers yet — the card renders as a dashed placeholder. */
  empty: boolean;
}

function pct(v: number): string {
  return `${String(Math.round(v * 100))} %`;
}

function guessed(q: QuestionStats): string {
  const times = q.calls === 1 ? 'once' : `${String(q.calls)} times`;
  if (q.errors === 0) return `Guessed ${times}.`;
  const errs = q.errors === 1 ? 'once' : `${String(q.errors)} times`;
  return `Guessed ${times}, ${errs} it couldn't answer.`;
}

/**
 * Over-confident and wrong: at least 80 % of answers sit in the top confidence
 * bucket (≥ 0.9) while it matches the operator in under half of the checks.
 */
export function isSuspicious(q: QuestionStats): boolean {
  if (q.calls === 0 || q.agreement === null) return false;
  const total = q.histogram.reduce((a, b) => a + b, 0);
  if (total === 0) return false;
  const top = q.histogram[q.histogram.length - 1] ?? 0;
  return top / total >= SUSPICIOUS_TOP_SHARE && q.agreement < SUSPICIOUS_AGREEMENT;
}

function build(segments: StatusSegment[], tone: StatusSentence['tone'], empty = false): StatusSentence {
  return { text: segments.map((s) => s.text).join(''), tone, segments, empty };
}

/** The one status sentence a classifier card carries. */
export function statusSentence(q: QuestionStats): StatusSentence {
  if (q.calls === 0) {
    const asked = ASKED_WHEN[q.questionId] ?? { when: 'its trigger fires', first: 'answer' };
    return build(
      [
        {
          text: `No data yet — this question is asked when ${asked.when}. Your first ${asked.first} will populate it.`,
        },
      ],
      'neutral',
      true,
    );
  }
  if (q.withTruth === 0) {
    return build(
      [
        { text: `${guessed(q)} ` },
        { text: "You haven't checked any yet", mark: 'amber' },
        {
          text: `, so we can't say how often it's right. Check ~${String(CHECKS_FOR_TRUST)} in the Inbox and this line becomes a percentage.`,
        },
      ],
      'neutral',
    );
  }
  const agreement = q.agreement ?? q.agreed / q.withTruth;
  const matches = `${UI_TERMS.agreement.ui[0]?.toUpperCase() ?? ''}${UI_TERMS.agreement.ui.slice(1)}`;
  const checked = `${matches} in ${pct(agreement)} of ${String(q.withTruth)} checked.`;
  if (isSuspicious(q)) {
    return build(
      [
        { text: `${guessed(q)} ` },
        { text: 'Suspicious:', mark: 'red' },
        {
          text: ` it is almost always sure of itself, yet ${checked.charAt(0).toLowerCase()}${checked.slice(1, -1)}. Start checking here.`,
        },
      ],
      'amber',
    );
  }
  const more = CHECKS_FOR_TRUST - q.withTruth;
  return build(
    [
      { text: `${guessed(q)} ${checked}` },
      ...(more > 0 ? [{ text: ` Check ~${String(more)} more before trusting the number.` }] : []),
    ],
    'neutral',
  );
}
