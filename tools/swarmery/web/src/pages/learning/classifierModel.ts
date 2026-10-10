// The classifier tab's copy model (Canvas v3 phase 6, artboard 1e). Pure: each
// question becomes a sentence, each mode a word from the code→UI dictionary,
// and each card one status sentence built from its stats. The component only
// lays these out, so every wording rule is unit-tested here.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural, t } from '@lingui/core/macro';
import type { DecideMode, QuestionStats } from '../../api/decisions';
import { i18n } from '../../i18n';
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

export const QUESTION_SENTENCE: Record<string, MessageDescriptor> = {
  'd2.task_type': msg`What kind of task was it?`,
  'd2.outcome': msg`How did it end?`,
  'd2.failure_cause': msg`Why did it fail?`,
  'd1.run_end': msg`How did the phase run end?`,
  'd3.divergence_cause': msg`Why did the run leave the plan?`,
};

/** When a question is asked, and what its first answer will come from. */
const ASKED_WHEN: Record<string, { when: MessageDescriptor; first: MessageDescriptor }> = {
  'd1.run_end': {
    when: msg`a plan phase run ends and the rules can't tell`,
    first: msg`scored phase run`,
  },
  'd2.task_type': { when: msg`a session finishes`, first: msg`finished session` },
  'd2.outcome': { when: msg`a session finishes`, first: msg`finished session` },
  'd2.failure_cause': { when: msg`a finished session did not ship`, first: msg`failed session` },
  'd3.divergence_cause': {
    when: msg`a phase run lands off its plan`,
    first: msg`off-plan phase run`,
  },
};

/** The sentence for a question id; an unknown id reads as itself. */
export function questionSentence(id: string): string {
  const sentence = QUESTION_SENTENCE[id];
  return sentence !== undefined ? i18n._(sentence) : id;
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
  const calls = q.calls;
  const times = plural(calls, {
    1: 'once',
    one: '# times',
    few: '# times',
    many: '# times',
    other: '# times',
  });
  if (q.errors === 0) return t`Guessed ${times}.`;
  const errors = q.errors;
  const errs = plural(errors, {
    1: 'once',
    one: '# times',
    few: '# times',
    many: '# times',
    other: '# times',
  });
  return t`Guessed ${times}, ${errs} it couldn't answer.`;
}

/**
 * Over-confident and wrong: at least 80 % of answers sit in the top confidence
 * bucket (≥ 0.9) while it matches the operator in under half of the checks.
 */
function moreChecks(more: number): string {
  const count = String(more);
  return t`Check ~${count} more before trusting the number.`;
}

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
    const asked = ASKED_WHEN[q.questionId] ?? { when: msg`its trigger fires`, first: msg`answer` };
    const when = i18n._(asked.when);
    const first = i18n._(asked.first);
    return build(
      [
        {
          text: t`No data yet — this question is asked when ${when}. Your first ${first} will populate it.`,
        },
      ],
      'neutral',
      true,
    );
  }
  if (q.withTruth === 0) {
    const target = String(CHECKS_FOR_TRUST);
    return build(
      [
        { text: `${guessed(q)} ` },
        { text: t`You haven't checked any yet`, mark: 'amber' },
        {
          text: t`, so we can't say how often it's right. Check ~${target} in the Inbox and this line becomes a percentage.`,
        },
      ],
      'neutral',
    );
  }
  const agreement = q.agreement ?? q.agreed / q.withTruth;
  const matches = `${UI_TERMS.agreement.ui[0]?.toUpperCase() ?? ''}${UI_TERMS.agreement.ui.slice(1)}`;
  const percent = pct(agreement);
  const total = String(q.withTruth);
  const checked = t`${matches} in ${percent} of ${total} checked.`;
  if (isSuspicious(q)) {
    const lowered = `${checked.charAt(0).toLowerCase()}${checked.slice(1, -1)}`;
    return build(
      [
        { text: `${guessed(q)} ` },
        { text: t`Suspicious:`, mark: 'red' },
        {
          text: ` ${t`it is almost always sure of itself, yet ${lowered}. Start checking here.`}`,
        },
      ],
      'amber',
    );
  }
  const more = CHECKS_FOR_TRUST - q.withTruth;
  return build(
    [
      { text: `${guessed(q)} ${checked}` },
      ...(more > 0 ? [{ text: ` ${moreChecks(more)}` }] : []),
    ],
    'neutral',
  );
}
