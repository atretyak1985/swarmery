// The loop map model (Canvas v3 phase 4, artboard 1a) — pure, no React.
//
// Five stages, Plan → Run → Measure → Learn → Change, each answering one
// question with one number, one sentence and a link to the place that holds
// it. `waiting` marks a stage where something waits on the operator (the card
// turns amber); `alert` is the part of the sentence that says what.
//
// Every number comes from an existing fetcher (Today.tsx wires them); the
// epic-derived ones are counted here by `epicCounts`. A stage with no data
// teaches what will appear there (artboard-1 principle 6) — never "0 · n/a".
// Surprise bands come from lib/offPlan.ts only.
//
// Copy goes through the Lingui macros at call time (buildLoop runs inside a
// render), so the stages follow the active locale. Counts are grouped by
// fmtCount and passed into the plural branches as `${count}` rather than `#`,
// which would format them with the locale's own separators.

import { plural, t } from '@lingui/core/macro';
import type { Epic } from '../../api/types';
import { offPlanBand } from '../../lib/offPlan';
import { PLACES, resolvePlaceHref, type PlaceId } from '../../lib/nav';

export type StageId = 'plan' | 'run' | 'measure' | 'learn' | 'change';

export interface Stage {
  id: StageId;
  /** 1-based position, shown as "1 · Plan". */
  step: number;
  title: string;
  n: number;
  unit: string;
  sentence: string;
  /** Amber tail of the sentence ('' when nothing needs the operator). */
  alert: string;
  href: string;
  linkLabel: string;
  waiting: boolean;
}

export interface LoopInputs {
  epics: readonly Epic[];
  liveSessions: number;
  pendingApprovals: number;
  stoppedPhases: number;
  scoredRuns: number;
  farOffPlan: number;
  classifierCalls: number;
  classifierChecked: number;
  lessonCandidates: number;
  advisorFindings: number;
  retirements: number;
  verifiedChanges: number;
  gatheringProof: number;
}

export const LOOP_WINDOW_MS = 7 * 24 * 3600_000;

/** Thousands grouped with a no-break space, as 1a writes them ("1 434"). */
export function fmtCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
}

function within(iso: string | null, now: number): boolean {
  if (iso === null) return false;
  const at = Date.parse(iso);
  return !Number.isNaN(at) && now - at <= LOOP_WINDOW_MS;
}

/** The epic-derived numbers of the loop, over the last 7 days. */
export function epicCounts(
  epics: readonly Epic[],
  now: number = Date.now(),
): { scoredRuns: number; farOffPlan: number; stoppedPhases: number } {
  let scoredRuns = 0;
  let farOffPlan = 0;
  let stoppedPhases = 0;
  for (const e of epics) {
    for (const p of e.phases) {
      if (p.surprise !== null && within(p.surprise.computedAt, now)) {
        scoredRuns += 1;
        if (offPlanBand(p.surprise.index) !== 'on plan') farOffPlan += 1;
      }
      if (p.runOutcome === 'noop' && within(p.runEndedAt, now)) stoppedPhases += 1;
    }
  }
  return { scoredRuns, farOffPlan, stoppedPhases };
}

function placeHref(id: PlaceId, slug: string | null, lastProject: string | null): string {
  const place = PLACES.find((p) => p.id === id);
  return place === undefined ? '/' : resolvePlaceHref(place, slug, lastProject);
}

function planStage(i: LoopInputs, href: string): Stage {
  const active = i.epics.filter((e) => e.status === 'active');
  const forecasted = active.reduce((n, e) => n + e.phases.filter((p) => p.forecasts.length > 0).length, 0);
  const plans = active.length;
  let sentence: string;
  if (plans === 0) sentence = t`Appears after the first plan is written — phases and what each one expects.`;
  else if (forecasted === 0)
    sentence = t`No phase carries a forecast yet — add one so a run has something to be measured against.`;
  else {
    const count = fmtCount(forecasted);
    sentence = plural(forecasted, {
      one: `${count} phase carries a forecast — what the planner expects before anything runs.`,
      few: `${count} phases carry a forecast — what the planner expects before anything runs.`,
      many: `${count} phases carry a forecast — what the planner expects before anything runs.`,
      other: `${count} phases carry a forecast — what the planner expects before anything runs.`,
    });
  }
  return {
    id: 'plan',
    step: 1,
    title: t`Plan`,
    n: plans,
    unit: plural(plans, { one: 'plan', few: 'plans', many: 'plans', other: 'plans' }),
    sentence,
    alert: '',
    href,
    linkLabel: t`Plans →`,
    waiting: false,
  };
}

function runStage(i: LoopInputs, href: string): Stage {
  const parts: string[] = [];
  const { stoppedPhases, pendingApprovals } = i;
  if (stoppedPhases > 0) {
    const count = fmtCount(stoppedPhases);
    parts.push(
      plural(stoppedPhases, {
        one: `${count} phase stopped with nothing done.`,
        few: `${count} phases stopped with nothing done.`,
        many: `${count} phases stopped with nothing done.`,
        other: `${count} phases stopped with nothing done.`,
      }),
    );
  }
  const alert =
    pendingApprovals > 0
      ? plural(pendingApprovals, {
          one: '# approval waiting.',
          few: '# approvals waiting.',
          many: '# approvals waiting.',
          other: '# approvals waiting.',
        })
      : '';
  let sentence = parts.join(' ');
  if (sentence === '' && alert === '') {
    sentence =
      i.liveSessions === 0
        ? t`Nothing running right now. Sessions show up here the moment one starts.`
        : t`Running clean — nothing stopped, nothing waiting on you.`;
  }
  return {
    id: 'run',
    step: 2,
    title: t`Run`,
    n: i.liveSessions,
    unit: t`live`,
    sentence,
    alert,
    href,
    linkLabel: t`Sessions →`,
    waiting: pendingApprovals > 0,
  };
}

function measureStage(i: LoopInputs, href: string): Stage {
  const { scoredRuns, classifierCalls } = i;
  if (scoredRuns === 0 && classifierCalls === 0) {
    return {
      id: 'measure',
      step: 3,
      title: t`Measure`,
      n: 0,
      unit: t`runs scored`,
      sentence: t`Appears after the first finished phase run.`,
      alert: '',
      href,
      linkLabel: t`Health →`,
      waiting: false,
    };
  }
  const parts: string[] = [];
  if (scoredRuns > 0) {
    const farOff = fmtCount(i.farOffPlan);
    parts.push(i.farOffPlan > 0 ? t`${farOff} landed far from the plan.` : t`All landed close to the plan.`);
  }
  let alert = '';
  if (classifierCalls > 0) {
    const count = fmtCount(classifierCalls);
    const labelled = plural(classifierCalls, {
      one: `The classifier labelled ${count} session —`,
      few: `The classifier labelled ${count} sessions —`,
      many: `The classifier labelled ${count} sessions —`,
      other: `The classifier labelled ${count} sessions —`,
    });
    if (i.classifierChecked === 0) {
      parts.push(labelled);
      alert = t`none checked by you yet.`;
    } else {
      const checked = fmtCount(i.classifierChecked);
      parts.push(t`${labelled} ${checked} checked by you.`);
    }
  }
  return {
    id: 'measure',
    step: 3,
    title: t`Measure`,
    n: scoredRuns,
    unit: plural(scoredRuns, { one: 'run scored', few: 'runs scored', many: 'runs scored', other: 'runs scored' }),
    sentence: parts.join(' '),
    alert,
    href,
    linkLabel: t`Health →`,
    waiting: false,
  };
}

function learnStage(i: LoopInputs, href: string): Stage {
  const { lessonCandidates, advisorFindings, retirements } = i;
  const n = lessonCandidates + advisorFindings + retirements;
  const parts: string[] = [];
  if (lessonCandidates > 0) {
    const count = fmtCount(lessonCandidates);
    parts.push(
      plural(lessonCandidates, {
        one: `${count} lesson candidate`,
        few: `${count} lesson candidates`,
        many: `${count} lesson candidates`,
        other: `${count} lesson candidates`,
      }),
    );
  }
  if (advisorFindings > 0) {
    const count = fmtCount(advisorFindings);
    parts.push(
      plural(advisorFindings, {
        one: `${count} Advisor finding`,
        few: `${count} Advisor findings`,
        many: `${count} Advisor findings`,
        other: `${count} Advisor findings`,
      }),
    );
  }
  if (retirements > 0) {
    const count = fmtCount(retirements);
    parts.push(
      plural(retirements, {
        one: `${count} lesson that may have stopped helping`,
        few: `${count} lessons that may have stopped helping`,
        many: `${count} lessons that may have stopped helping`,
        other: `${count} lessons that may have stopped helping`,
      }),
    );
  }
  const list = parts.join(', ');
  const sentence =
    n === 0
      ? t`Nothing to review. Lessons and Advisor findings land here after runs are measured.`
      : t`${list}. Nothing changes until you say so.`;
  return {
    id: 'learn',
    step: 4,
    title: t`Learn`,
    n,
    unit: plural(n, { one: 'proposal', few: 'proposals', many: 'proposals', other: 'proposals' }),
    sentence,
    alert: '',
    href,
    linkLabel: n > 0 ? t`Review in Inbox →` : t`Inbox →`,
    waiting: n > 0,
  };
}

function changeStage(i: LoopInputs, href: string): Stage {
  const parts: string[] = [];
  const { verifiedChanges, gatheringProof } = i;
  if (verifiedChanges > 0) {
    const count = fmtCount(verifiedChanges);
    parts.push(
      plural(verifiedChanges, {
        one: `${count} change proved it helped.`,
        few: `${count} changes proved they helped.`,
        many: `${count} changes proved they helped.`,
        other: `${count} changes proved they helped.`,
      }),
    );
  }
  if (gatheringProof > 0) {
    const count = fmtCount(gatheringProof);
    parts.push(
      plural(gatheringProof, {
        one: `${count} change still gathering proof.`,
        few: `${count} changes still gathering proof.`,
        many: `${count} changes still gathering proof.`,
        other: `${count} changes still gathering proof.`,
      }),
    );
  }
  return {
    id: 'change',
    step: 5,
    title: t`Change`,
    n: verifiedChanges,
    unit: t`verified`,
    sentence:
      parts.length === 0
        ? t`Appears after you accept a change and it has run long enough to prove itself.`
        : parts.join(' '),
    alert: '',
    href,
    linkLabel: t`Proof →`,
    waiting: false,
  };
}

/**
 * The five stages for a scope. `slug` null = All projects; `lastProject`
 * resolves the project-only Plans link there (lib/nav.ts resolvePlaceHref).
 */
export function buildLoop(i: LoopInputs, slug: string | null, lastProject: string | null = null): Stage[] {
  return [
    planStage(i, placeHref('plans', slug, lastProject)),
    runStage(i, placeHref('sessions', slug, lastProject)),
    measureStage(i, placeHref('health', slug, lastProject)),
    learnStage(i, placeHref('inbox', slug, lastProject)),
    changeStage(i, `${placeHref('learning', slug, lastProject)}?tab=proof`),
  ];
}
