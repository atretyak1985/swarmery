// Forecast vs actual as a story (Canvas v3 artboard 1c): the scored surprise row
// retold as expected → happened → why → what follows, in the UI's words
// (lib/glossary.ts UI_TERMS). Pure — no React, no fetch — so every sentence is
// unit-tested. The numeric vector and weights stay in ForecastVsActual, folded
// behind "show the score breakdown".

import { i18n, type MessageDescriptor } from '@lingui/core';
import { msg, plural, t } from '@lingui/core/macro';
import type { EpicPhase } from '../../api/types';
import { UI_TERMS } from '../../lib/glossary';
import { offPlanBand, type OffPlanBand } from '../../lib/offPlan';
import { extractDivergence } from './ForecastVsActual';

export interface ForecastStory {
  band: OffPlanBand;
  headline: string;
  expected: string;
  happened: string;
  why: string | null;
  next: string;
}

const NO_LESSON_MESSAGE = msg`No lesson was proposed: the report has no Where reality diverged paragraph.`;

/** The `next` line when the executor wrote no divergence paragraph, as English
 * source text — exported so the tests quote one string. The story itself carries
 * the active locale's rendering of the same message. */
export const NO_LESSON: string = NO_LESSON_MESSAGE.message ?? '';

const SIZE_WORDS: Record<string, MessageDescriptor> = {
  XS: msg`an extra-small`,
  S: msg`a small`,
  M: msg`a medium`,
  L: msg`a large`,
  XL: msg`an extra-large`,
};

const DURATION_WORDS: Record<string, MessageDescriptor> = {
  '<30m': msg`under 30 min`,
  '30-90m': msg`30 to 90 min`,
  '90m-4h': msg`90 min to 4 h`,
  '>4h': msg`over 4 h`,
};

/** Forecast outcomes (done | partial | blocked, plus the executor's
 * done_with_concerns) as the end of an "expected" sentence. */
const FORECAST_OUTCOME_WORDS: Record<string, MessageDescriptor> = {
  done: msg`finishing the work`,
  done_with_concerns: msg`finishing with some concerns`,
  partial: msg`finishing part of the work`,
  blocked: msg`ending blocked`,
};

/** phasediag outcomes (completed | partial | noop | failed) as a headline tail. */
const ACTUAL_OUTCOME_WORDS: Record<string, MessageDescriptor> = {
  noop: msg`doing nothing`,
  completed: msg`with the work done`,
  partial: msg`with only part of the work done`,
  failed: msg`in a failure`,
};

/** The active locale's text for `key` in one of the word maps above. */
function word(map: Record<string, MessageDescriptor>, key: string): string | undefined {
  const message = map[key];
  return message === undefined ? undefined : i18n._(message);
}

/** Counts are digits: a spelled-out English number cannot be translated. */
function numberWord(n: number): string {
  return String(n);
}

/** "area" / "areas" after a digit count. */
function areaWord(n: number): string {
  return plural(n, { one: 'area', few: 'areas', many: 'areas', other: 'areas' });
}

function capitalize(s: string): string {
  return s === '' ? s : `${s.charAt(0).toUpperCase()}${s.slice(1)}`;
}

/** Seconds → "30 seconds" / "12 minutes" / "2 h 5 min". */
function spokenDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 90) return plural(s, { one: '# second', few: '# seconds', many: '# seconds', other: '# seconds' });
  const m = Math.round(s / 60);
  if (m < 90) return plural(m, { one: '# minute', few: '# minutes', many: '# minutes', other: '# minutes' });
  const hours = String(Math.floor(m / 60));
  const rest = m % 60;
  const minutes = String(rest);
  return rest === 0 ? t`${hours} h` : t`${hours} h ${minutes} min`;
}

/** The last path segment — `apps/api/src/modules/bids` → `bids`. */
function areaName(area: string): string {
  const parts = area.split('/').filter((x) => x !== '');
  return parts[parts.length - 1] ?? area;
}

function firstSentence(text: string): string {
  const flat = text.replace(/\s+/g, ' ').trim();
  const m = /^.+?[.!?](?=\s|$)/.exec(flat);
  return m === null ? flat : m[0];
}

/** null when the run was not scored — never a zero-score story. `== null` because
 * a DTO that predates the surprise column omits the field entirely. */
export function forecastStory(p: EpicPhase): ForecastStory | null {
  const s = p.surprise;
  if (s == null) return null;
  const d = s.detail;
  const band = offPlanBand(s.index);

  const size = word(SIZE_WORDS, d.forecastSize);
  const unmeasured = t`an unmeasured time`;
  const took =
    d.actualDurationS !== null
      ? spokenDuration(d.actualDurationS)
      : (word(DURATION_WORDS, d.actualDuration) ?? (d.actualDuration === '' ? unmeasured : d.actualDuration));
  const outcome = d.actualOutcome;
  const ending = word(ACTUAL_OUTCOME_WORDS, outcome) ?? (outcome === '' ? '' : t`as ${outcome}`);
  const tail = ending === '' ? '' : ` ${ending}`;
  const headline =
    size === undefined
      ? t`Planned without a size, it ended in ${took}${tail}.`
      : t`Planned as ${size} phase, it ended in ${took}${tail}.`;

  const expectedParts: string[] = [];
  if (size !== undefined) expectedParts.push(capitalize(t`${size} phase`));
  const plannedFor = word(DURATION_WORDS, d.forecastDuration) ?? d.forecastDuration;
  if (plannedFor !== '') expectedParts.push(plannedFor);
  const plannedEnd = word(FORECAST_OUTCOME_WORDS, d.forecastOutcome) ?? d.forecastOutcome;
  if (plannedEnd !== '') expectedParts.push(plannedEnd);
  let expected =
    expectedParts.length === 0 ? t`The forecast named no size, duration or outcome.` : `${expectedParts.join(', ')}.`;
  if (d.confidence !== null) {
    const percent = String(Math.round(d.confidence * 100));
    let sure = t`The planner was ${percent} % sure`;
    const added = s.revision?.areasAdded ?? [];
    if (added.length > 0) {
      const posterior = UI_TERMS.posterior.ui;
      const count = numberWord(added.length);
      const areas = areaWord(added.length);
      const names = added.map(areaName).join(', ');
      sure += ` ${t`— ${posterior} it added ${count} more ${areas} (${names})`}`;
    }
    expected += ` ${sure}.`;
  }

  const happenedParts: string[] = [capitalize(took)];
  if (d.actualAreas !== null) {
    if (d.actualAreas.length === 0) happenedParts.push(t`no code touched`);
    else {
      const off = d.unexpectedAreas.length;
      const count = numberWord(d.actualAreas.length);
      const areas = areaWord(d.actualAreas.length);
      const offCount = numberWord(off);
      const offNote = off > 0 ? ` ${t`(${offCount} not in the plan)`}` : '';
      happenedParts.push(t`touched ${count} ${areas}${offNote}`);
    }
  }
  const total = String(p.checkboxesTotal);
  const tickedCount = p.runCheckboxesBefore === null ? p.checkboxesDone : p.checkboxesDone - p.runCheckboxesBefore;
  const ticked = String(tickedCount);
  happenedParts.push(
    tickedCount <= 0 ? t`none of the ${total} criteria ticked` : t`${ticked} of the ${total} criteria ticked`,
  );
  let happened = `${happenedParts.join(', ')}.`;
  if (d.actualAreas === null) happened += ` ${t`The diff was not measured, so we can't say which areas it touched.`}`;

  const divergence = extractDivergence(p.completionReport);
  const why =
    divergence !== null
      ? firstSentence(divergence)
      : p.runError !== null && p.runError.trim() !== ''
        ? firstSentence(p.runError)
        : null;

  const next =
    divergence === null
      ? i18n._(NO_LESSON_MESSAGE)
      : band === 'on plan'
        ? t`Nothing follows: the run landed on plan.`
        : t`A lesson can be drawn from the report’s Where reality diverged paragraph.`;

  return { band, headline, expected, happened, why, next };
}
