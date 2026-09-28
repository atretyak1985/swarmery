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

/** "1 phase" / "12 phases". */
export function plural(n: number, one: string, many = `${one}s`): string {
  return `${fmtCount(n)} ${n === 1 ? one : many}`;
}

/** Thousands grouped with a no-break space, as 1a writes them ("1 434"). */
export function fmtCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
}

function within(iso: string | null, now: number): boolean {
  if (iso === null) return false;
  const t = Date.parse(iso);
  return !Number.isNaN(t) && now - t <= LOOP_WINDOW_MS;
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
  let sentence: string;
  if (active.length === 0) sentence = 'Appears after the first plan is written — phases and what each one expects.';
  else if (forecasted === 0) sentence = 'No phase carries a forecast yet — add one so a run has something to be measured against.';
  else
    sentence = `${plural(forecasted, 'phase')} ${forecasted === 1 ? 'carries' : 'carry'} a forecast — what the planner expects before anything runs.`;
  return {
    id: 'plan',
    step: 1,
    title: 'Plan',
    n: active.length,
    unit: active.length === 1 ? 'plan' : 'plans',
    sentence,
    alert: '',
    href,
    linkLabel: 'Plans →',
    waiting: false,
  };
}

function runStage(i: LoopInputs, href: string): Stage {
  const parts: string[] = [];
  if (i.stoppedPhases > 0) {
    parts.push(`${plural(i.stoppedPhases, 'phase')} stopped with nothing done.`);
  }
  const alert = i.pendingApprovals > 0 ? `${plural(i.pendingApprovals, 'approval')} waiting.` : '';
  let sentence = parts.join(' ');
  if (sentence === '' && alert === '') {
    sentence =
      i.liveSessions === 0
        ? 'Nothing running right now. Sessions show up here the moment one starts.'
        : 'Running clean — nothing stopped, nothing waiting on you.';
  }
  return {
    id: 'run',
    step: 2,
    title: 'Run',
    n: i.liveSessions,
    unit: 'live',
    sentence,
    alert,
    href,
    linkLabel: 'Sessions →',
    waiting: i.pendingApprovals > 0,
  };
}

function measureStage(i: LoopInputs, href: string): Stage {
  if (i.scoredRuns === 0 && i.classifierCalls === 0) {
    return {
      id: 'measure',
      step: 3,
      title: 'Measure',
      n: 0,
      unit: 'runs scored',
      sentence: 'Appears after the first finished phase run.',
      alert: '',
      href,
      linkLabel: 'Health →',
      waiting: false,
    };
  }
  const parts: string[] = [];
  if (i.scoredRuns > 0) {
    parts.push(
      i.farOffPlan > 0 ? `${fmtCount(i.farOffPlan)} landed far from the plan.` : 'All landed close to the plan.',
    );
  }
  let alert = '';
  if (i.classifierCalls > 0) {
    const labelled = `The classifier labelled ${plural(i.classifierCalls, 'session')} — `;
    if (i.classifierChecked === 0) {
      parts.push(labelled.trimEnd());
      alert = 'none checked by you yet.';
    } else {
      parts.push(`${labelled}${fmtCount(i.classifierChecked)} checked by you.`);
    }
  }
  return {
    id: 'measure',
    step: 3,
    title: 'Measure',
    n: i.scoredRuns,
    unit: i.scoredRuns === 1 ? 'run scored' : 'runs scored',
    sentence: parts.join(' '),
    alert,
    href,
    linkLabel: 'Health →',
    waiting: false,
  };
}

function learnStage(i: LoopInputs, href: string): Stage {
  const n = i.lessonCandidates + i.advisorFindings + i.retirements;
  const parts: string[] = [];
  if (i.lessonCandidates > 0) parts.push(plural(i.lessonCandidates, 'lesson candidate'));
  if (i.advisorFindings > 0) parts.push(plural(i.advisorFindings, 'Advisor finding'));
  if (i.retirements > 0) {
    parts.push(
      `${plural(i.retirements, 'lesson')} that may have stopped helping`,
    );
  }
  const sentence =
    n === 0
      ? 'Nothing to review. Lessons and Advisor findings land here after runs are measured.'
      : `${parts.join(', ')}. Nothing changes until you say so.`;
  return {
    id: 'learn',
    step: 4,
    title: 'Learn',
    n,
    unit: n === 1 ? 'proposal' : 'proposals',
    sentence,
    alert: '',
    href,
    linkLabel: n > 0 ? 'Review in Inbox →' : 'Inbox →',
    waiting: n > 0,
  };
}

function changeStage(i: LoopInputs, href: string): Stage {
  const parts: string[] = [];
  if (i.verifiedChanges > 0) {
    parts.push(`${plural(i.verifiedChanges, 'change')} proved ${i.verifiedChanges === 1 ? 'it' : 'they'} helped.`);
  }
  if (i.gatheringProof > 0) {
    parts.push(`${plural(i.gatheringProof, 'change')} still gathering proof.`);
  }
  return {
    id: 'change',
    step: 5,
    title: 'Change',
    n: i.verifiedChanges,
    unit: 'verified',
    sentence:
      parts.length === 0
        ? 'Appears after you accept a change and it has run long enough to prove itself.'
        : parts.join(' '),
    alert: '',
    href,
    linkLabel: 'Proof →',
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
