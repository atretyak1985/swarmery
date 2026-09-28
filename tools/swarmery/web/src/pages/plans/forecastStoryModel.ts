// Forecast vs actual as a story (Canvas v3 artboard 1c): the scored surprise row
// retold as expected → happened → why → what follows, in the UI's words
// (lib/glossary.ts UI_TERMS). Pure — no React, no fetch — so every sentence is
// unit-tested. The numeric vector and weights stay in ForecastVsActual, folded
// behind "show the score breakdown".

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

/** The `next` line when the executor wrote no divergence paragraph — exported so
 * the view and its tests quote one string. */
export const NO_LESSON =
  'No lesson was proposed: the report has no Where reality diverged paragraph.';

const SIZE_WORDS: Record<string, string> = {
  XS: 'an extra-small',
  S: 'a small',
  M: 'a medium',
  L: 'a large',
  XL: 'an extra-large',
};

const DURATION_WORDS: Record<string, string> = {
  '<30m': 'under 30 min',
  '30-90m': '30 to 90 min',
  '90m-4h': '90 min to 4 h',
  '>4h': 'over 4 h',
};

/** Forecast outcomes (done | partial | blocked, plus the executor's
 * done_with_concerns) as the end of an "expected" sentence. */
const FORECAST_OUTCOME_WORDS: Record<string, string> = {
  done: 'finishing the work',
  done_with_concerns: 'finishing with some concerns',
  partial: 'finishing part of the work',
  blocked: 'ending blocked',
};

/** phasediag outcomes (completed | partial | noop | failed) as a headline tail. */
const ACTUAL_OUTCOME_WORDS: Record<string, string> = {
  noop: 'doing nothing',
  completed: 'with the work done',
  partial: 'with only part of the work done',
  failed: 'in a failure',
};

const NUMBER_WORDS = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten'];

function numberWord(n: number): string {
  return NUMBER_WORDS[n] ?? String(n);
}

function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}

function capitalize(s: string): string {
  return s === '' ? s : `${s.charAt(0).toUpperCase()}${s.slice(1)}`;
}

/** Seconds → "30 seconds" / "12 minutes" / "2 h 5 min". */
function spokenDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 90) return `${String(s)} ${plural(s, 'second', 'seconds')}`;
  const m = Math.round(s / 60);
  if (m < 90) return `${String(m)} minutes`;
  const h = Math.floor(m / 60);
  const rest = m % 60;
  return rest === 0 ? `${String(h)} h` : `${String(h)} h ${String(rest)} min`;
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

  const size = SIZE_WORDS[d.forecastSize];
  const took =
    d.actualDurationS !== null
      ? spokenDuration(d.actualDurationS)
      : (DURATION_WORDS[d.actualDuration] ?? (d.actualDuration === '' ? 'an unmeasured time' : d.actualDuration));
  const ending = ACTUAL_OUTCOME_WORDS[d.actualOutcome] ?? (d.actualOutcome === '' ? '' : `as ${d.actualOutcome}`);
  const headline = `${size === undefined ? 'Planned without a size' : `Planned as ${size} phase`}, it ended in ${took}${
    ending === '' ? '' : ` ${ending}`
  }.`;

  const expectedParts: string[] = [];
  if (size !== undefined) expectedParts.push(capitalize(`${size} phase`));
  const plannedFor = DURATION_WORDS[d.forecastDuration] ?? d.forecastDuration;
  if (plannedFor !== '') expectedParts.push(plannedFor);
  const plannedEnd = FORECAST_OUTCOME_WORDS[d.forecastOutcome] ?? d.forecastOutcome;
  if (plannedEnd !== '') expectedParts.push(plannedEnd);
  let expected = expectedParts.length === 0 ? 'The forecast named no size, duration or outcome.' : `${expectedParts.join(', ')}.`;
  if (d.confidence !== null) {
    let sure = `The planner was ${String(Math.round(d.confidence * 100))} % sure`;
    const added = s.revision?.areasAdded ?? [];
    if (added.length > 0)
      sure += ` — ${UI_TERMS.posterior.ui} it added ${numberWord(added.length)} more ${plural(
        added.length,
        'area',
        'areas',
      )} (${added.map(areaName).join(', ')})`;
    expected += ` ${sure}.`;
  }

  const happenedParts: string[] = [capitalize(took)];
  if (d.actualAreas !== null) {
    if (d.actualAreas.length === 0) happenedParts.push('no code touched');
    else {
      const off = d.unexpectedAreas.length;
      happenedParts.push(
        `touched ${numberWord(d.actualAreas.length)} ${plural(d.actualAreas.length, 'area', 'areas')}${
          off > 0 ? ` (${numberWord(off)} not in the plan)` : ''
        }`,
      );
    }
  }
  const total = p.checkboxesTotal;
  const ticked = p.runCheckboxesBefore === null ? p.checkboxesDone : p.checkboxesDone - p.runCheckboxesBefore;
  happenedParts.push(ticked <= 0 ? `none of the ${String(total)} criteria ticked` : `${String(ticked)} of the ${String(total)} criteria ticked`);
  let happened = `${happenedParts.join(', ')}.`;
  if (d.actualAreas === null) happened += " The diff was not measured, so we can't say which areas it touched.";

  const divergence = extractDivergence(p.completionReport);
  const why =
    divergence !== null
      ? firstSentence(divergence)
      : p.runError !== null && p.runError.trim() !== ''
        ? firstSentence(p.runError)
        : null;

  const next =
    divergence === null
      ? NO_LESSON
      : band === 'on plan'
        ? 'Nothing follows: the run landed on plan.'
        : 'A lesson can be drawn from the report’s Where reality diverged paragraph.';

  return { band, headline, expected, happened, why, next };
}
