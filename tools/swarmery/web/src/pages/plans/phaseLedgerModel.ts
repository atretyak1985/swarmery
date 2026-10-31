// What one line of the phase ledger says (handoff §6): glyph, stage, the note under
// the name, the cost and the runs. Pure, and built only from what the daemon sends.
// A phase has no cost field and its forecast has no dollars (plan D-5), so its cost
// is the sum of the sessions the daemon grouped under it, and its runs are those
// same sessions.

import { plural, t } from '@lingui/core/macro';
import type { EpicPhase, Session, SessionStatus } from '../../api/types';
import type { GlyphState } from '../../components/place/StateGlyph';
import { offPlanBand, type OffPlanBand } from '../../lib/offPlan';
import { isUnresolvedOutcome, outcomeChipLabel } from './phaseChips';
import { phaseNeedsYou, phaseStatus, type PhaseStatus } from './planModel';

export type PhaseStage = 'landed' | 'review' | 'exec' | 'decision' | 'manual' | 'none';

/** A decision holds the phase: a guard stop, a review, or a planning decision. */
export function heldByDecision(p: EpicPhase): boolean {
  return p.decisionHold != null || p.guardDecision != null || p.reviewDecision != null;
}

/** Where the phase is. What waits on the operator outranks everything, because a
 * done phase can still hold an open review decision. */
export function phaseStage(p: EpicPhase, status: PhaseStatus): PhaseStage {
  if (heldByDecision(p)) return 'decision';
  if (phaseNeedsYou(p)) return 'manual';
  if (status === 'done') return 'landed';
  if (p.verifyRunning === true || p.landing?.state === 'pushed' || p.landing?.state === 'pr_open') return 'review';
  if (p.runState === 'running' || status === 'in_progress' || status === 'unverified' || status === 'unreported') {
    return 'exec';
  }
  return 'none';
}

export function stageLabel(stage: PhaseStage): string {
  switch (stage) {
    case 'landed':
      return t`landed`;
    case 'review':
      return t`review`;
    case 'exec':
      return t`exec`;
    case 'decision':
      return t`decision`;
    case 'manual':
      return t`manual`;
    case 'none':
      return '—';
  }
}

export function phaseGlyph(p: EpicPhase, status: PhaseStatus): GlyphState {
  if (phaseNeedsYou(p)) return 'needs';
  if (p.runState === 'running' || p.verifyRunning === true) return 'working';
  if (status === 'done') return 'done';
  // Its stage reads review: the run ended, and the work is still moving through landing.
  if (p.landing?.state === 'pushed' || p.landing?.state === 'pr_open') return 'working';
  if (p.runState === 'failed' || p.runOutcome === 'failed') return 'failed';
  if (p.runState === 'blocked') return 'stopped';
  return 'pending';
}

/** The sentence at the top of an open line: what is happening, and whose move it is. */
export function phaseLead(p: EpicPhase, status: PhaseStatus, depsUnmet: readonly number[], note: string): string {
  const stage = phaseStage(p, status);
  if (stage === 'decision') {
    return p.guardDecision != null
      ? t`The guard stopped the run. Nothing moves until you answer.`
      : t`The run waits on your decision. Nothing moves until you answer.`;
  }
  if (stage === 'manual') return t`Every automatic criterion is green; only you can close the rest.`;
  if (stage === 'review') return t`Code is written; the reviewer is reading the diff.`;
  if (p.runState === 'running') return t`The executor is working.`;
  if (status === 'done') return t`Landed.`;
  if (depsUnmet.length === 0 && status === 'pending') return t`Ready to run.`;
  return `${note.charAt(0).toUpperCase()}${note.slice(1)}.`;
}

export interface PhaseVerdict {
  text: string;
  tone: 'red' | 'green' | 'brand' | 'faint';
}

/** The verdict at the foot of an open line, against the phase's forecast when one
 * exists: no band, no claim about the plan. */
export function phaseVerdict(p: EpicPhase, status: PhaseStatus, ranAtAll: boolean): PhaseVerdict | null {
  const stage = phaseStage(p, status);
  const band = phaseCostBand(p);
  const off = band !== null && band !== 'on plan';
  if (p.guardDecision != null) return { text: t`stopped off plan`, tone: 'red' };
  if (stage === 'decision') return null;
  if (stage === 'manual') return { text: t`ready to hand off`, tone: 'brand' };
  if (status === 'done') {
    if (band === null) return { text: t`landed`, tone: 'faint' };
    return off ? { text: t`landed off plan`, tone: 'red' } : { text: t`landed on plan`, tone: 'green' };
  }
  if (p.runState === 'running' || stage === 'review') {
    if (band === null) return { text: t`running`, tone: 'faint' };
    return off ? { text: t`running · off plan`, tone: 'red' } : { text: t`running · on plan so far`, tone: 'green' };
  }
  return ranAtAll ? null : { text: t`not run yet`, tone: 'faint' };
}

/** The one line under the phase's name. */
export function phaseNote(
  p: EpicPhase,
  status: PhaseStatus,
  depsUnmet: readonly number[],
  elapsed: string,
  /** The host's word for a change request ("PR"); null until it loads. */
  changeShort: string | null,
): string {
  const guard = p.guardDecision ?? null;
  if (guard !== null) {
    const id = guard.decisionId;
    const trigger = guard.trigger ?? '';
    return trigger === '' ? t`guard stopped the run · ${id}` : t`guard stopped the run · ${id} · ${trigger}`;
  }
  const review = p.reviewDecision ?? null;
  if (review !== null) {
    const id = review.decisionId;
    return t`the review waits on you · ${id}`;
  }
  const held = p.decisionHold?.decisions[0]?.decisionId;
  if (held !== undefined) return t`waits on decision ${held}`;
  if (phaseNeedsYou(p)) {
    const manual = p.manualOpen;
    return plural(manual, {
      one: '# criterion [MANUAL] — only you can close it',
      few: '# criteria [MANUAL] — only you can close them',
      many: '# criteria [MANUAL] — only you can close them',
      other: '# criteria [MANUAL] — only you can close them',
    });
  }
  if (p.runState === 'running') return elapsed === '' ? t`running` : t`running · ${elapsed}`;
  if (p.verifyRunning === true) return t`verifying`;
  if (status === 'done') {
    const number = p.landing?.prNumber ?? null;
    const change = changeShort === null ? `#${String(number)}` : `${changeShort} #${String(number)}`;
    const landed = number === null ? t`landed` : t`landed · ${change}`;
    const back = p.reopens?.length ?? 0;
    if (back === 0) return landed;
    const again = plural(back, {
      one: 'came back once',
      few: 'came back # times',
      many: 'came back # times',
      other: 'came back # times',
    });
    return `${landed} · ${again}`;
  }
  if (isUnresolvedOutcome(p.runOutcome)) return outcomeChipLabel(p, p.runOutcome);
  if (status === 'unverified') return t`the verification never landed`;
  if (status === 'unreported') return t`no completion report`;
  if (depsUnmet.length > 0) {
    const deps = depsUnmet.join(', ');
    return t`starts when ${deps} lands`;
  }
  if (status === 'in_progress') {
    const done = String(p.checkboxesDone);
    return plural(p.checkboxesTotal, {
      one: `${done} of # criterion`,
      few: `${done} of # criteria`,
      many: `${done} of # criteria`,
      other: `${done} of # criteria`,
    });
  }
  return t`not started`;
}

/** The phase a person came to look at: the one running, else the next one that can
 * run, so its Run is on screen without a click. Null when neither exists. */
export function defaultOpenSeq(phases: readonly EpicPhase[], resolvedSeqs: Set<number>, planActive: boolean): number | null {
  const running = phases.find((p) => p.runState === 'running');
  if (running !== undefined) return running.seq;
  if (!planActive) return null;
  const next = phases.find(
    (p) =>
      phaseStatus(p, resolvedSeqs) !== 'done' &&
      !phaseNeedsYou(p) &&
      p.dependsOn.every((seq) => resolvedSeqs.has(seq)),
  );
  return next?.seq ?? null;
}

/** The criteria line of the expansion: `6/8 · 1 [MANUAL] · 1 [LAND] pending`. */
export function criteriaLine(p: EpicPhase): string {
  const parts = [`${String(p.checkboxesDone)}/${String(p.checkboxesTotal)}`];
  if (p.manualOpen > 0) parts.push(`${String(p.manualOpen)} [MANUAL]`);
  if (p.landOpen > 0) {
    const land = String(p.landOpen);
    parts.push(t`${land} [LAND] pending`);
  }
  return parts.join(' · ');
}

/** The sessions the daemon grouped under this phase, oldest first. */
export function phaseRuns(sessions: readonly Session[] | null, phaseId: number): Session[] {
  return (sessions ?? [])
    .filter((s) => s.planGroup?.role === 'phase' && s.planGroup.phaseId === phaseId)
    .sort((a, b) => a.startedAt.localeCompare(b.startedAt));
}

/** What the phase's runs cost, or null while none of them is priced. */
export function phaseCost(runs: readonly Session[]): number | null {
  const priced = runs.flatMap((s) => (s.costUsd == null ? [] : [s.costUsd]));
  return priced.length === 0 ? null : priced.reduce((sum, c) => sum + c, 0);
}

/** The bar's tone: a verdict only where the daemon has one (off-plan band), and
 * neutral otherwise, because no dollar forecast exists to compare with. */
export function phaseCostBand(p: EpicPhase): OffPlanBand | null {
  return p.surprise == null ? null : offPlanBand(p.surprise.index);
}

const SESSION_GLYPH: Record<SessionStatus, GlyphState> = {
  active: 'working',
  waiting_approval: 'needs',
  awaiting_reply: 'needs',
  idle: 'pending',
  completed: 'done',
  killed: 'stopped',
};

export function sessionGlyph(status: SessionStatus): GlyphState {
  return SESSION_GLYPH[status];
}
