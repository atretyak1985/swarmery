// What a plan reads as, from the Epic the daemon sends: phase status against the
// completion gate, and the numbers the list row and the plan header show. Pure, so
// the two can never disagree, and nothing here invents a field: the spend is the
// sum of the sessions linked to the plan, because the API carries no cost per phase
// and no forecast in dollars (plan D-5); the goal is a sentence of the README,
// because the API carries no goal at all.

import { plural, t } from '@lingui/core/macro';
import type { BoardColumn, Epic, EpicPhase } from '../../api/types';
import type { GlyphState } from '../../components/place/StateGlyph';

/** A board column that counts as "resolved" for the dependency gate. */
function isResolvedColumn(col: BoardColumn | null): boolean {
  return col === 'done' || col === 'archived';
}

export type PhaseStatus = 'pending' | 'in_progress' | 'done' | 'unverified' | 'unreported' | 'blocked';

/** Derives a phase's display status from checkbox progress, a live run, and the
 * dependency gate. The result stays a coarse LIFECYCLE state — whether a process
 * is attached to it is resolved by `phaseChip` at the call site. */
export function phaseStatus(p: EpicPhase, resolvedSeqs: Set<number>): PhaseStatus {
  // An activated phase whose board task is resolved is done regardless of
  // checkbox progress — the board is the source of truth once dispatched.
  if (isResolvedColumn(p.boardColumn)) return 'done';
  // THE completion gate, computed by the daemon (internal/phasegate) — not a
  // client-side `done === total`, which is how this file used to answer and how it
  // came to disagree with the daemon about the same row. `unverified` means the
  // criteria are all ticked and the verification the doc asked for never landed.
  if (p.completionState === 'complete') return 'done';
  if (p.completionState === 'unverified') return 'unverified';
  if (p.completionState === 'unreported') return 'unreported';
  // The doc's own `Status: In progress` marker wins over the dependency gate —
  // an executor writing it is literally working on the phase right now.
  // (`done` must be earned by ticking every checkbox; a `done` marker alone is ignored.)
  if (p.docStatus === 'in_progress') return 'in_progress';
  // A run in flight is the strongest statement that can be made about a phase —
  // stronger than a dependency gate computed from checkboxes the run is in the
  // middle of ticking. Without this a phase whose dependency sits at 11/12
  // renders `blocked` while its executor is demonstrably working.
  if (p.runState === 'running') return 'in_progress';
  if (p.dependsOn.some((seq) => !resolvedSeqs.has(seq))) return 'blocked';
  if (p.checkboxesDone > 0 || p.boardColumn === 'in_progress' || p.boardColumn === 'in_review')
    return 'in_progress';
  return 'pending';
}

/** Which seq numbers are "resolved" — their board task is done/archived OR the
 * daemon's completion gate calls them complete. Renders depends-on badges and
 * feeds the phase-status derivation.
 *
 * The gate, never a local derivation: a process that exited 0 having ticked
 * nothing is not a completed phase, and neither is one whose criteria are all
 * ticked while the verification it asked for never landed. Re-deriving either
 * rule here is what let the client offer a Run button the daemon then refuses. */
export function computeResolvedSeqs(phases: EpicPhase[]): Set<number> {
  const s = new Set<number>();
  for (const p of phases) {
    // `completionState`, never a local checkbox comparison: the daemon's dependency
    // gate refuses a fully-ticked phase whose verification never landed, and a
    // client that marked it resolved would offer a Run button the daemon then
    // rejects. One gate, one answer.
    if (isResolvedColumn(p.boardColumn) || p.completionState === 'complete') s.add(p.seq);
  }
  return s;
}

/** A plan is complete when every phase reads done — the gate for the plan
 * rail's Summary tab. */
export function planComplete(epic: Epic, resolvedSeqs: Set<number>): boolean {
  return epic.phases.length > 0 && epic.phases.every((p) => phaseStatus(p, resolvedSeqs) === 'done');
}

/** Blocked on the operator: a decision holds it, or every open criterion is one
 * only a person closes. `[LAND]` criteria are closed by landing, so they do not
 * keep a manual phase off this list. */
export function phaseNeedsYou(p: EpicPhase): boolean {
  if (p.decisionHold != null || p.guardDecision != null || p.reviewDecision != null) return true;
  const open = p.checkboxesTotal - p.checkboxesDone;
  return p.manualOpen > 0 && p.manualOpen + p.landOpen >= open;
}

export function needYouCount(epic: Epic): number {
  return epic.phases.filter(phaseNeedsYou).length;
}

export function phasesLanded(epic: Epic, resolvedSeqs: Set<number>): number {
  return epic.phases.filter((p) => phaseStatus(p, resolvedSeqs) === 'done').length;
}

/** What the plan's sessions cost, or null while none of them is priced: an unknown
 * spend is not $0. */
export function planSpent(epic: Epic): number | null {
  const priced = epic.linkedSessions.flatMap((s) => (s.costUsd === null ? [] : [s.costUsd]));
  return priced.length === 0 ? null : priced.reduce((sum, c) => sum + c, 0);
}

/** Whole dollars, as the design has them, once cents stop mattering. */
export function fmtSpent(usd: number): string {
  return usd < 10 ? `$${usd.toFixed(2)}` : `$${Math.round(usd).toString()}`;
}

/** The latest sign of life on the plan: a phase doc written, or a run started. */
export function planUpdatedAt(epic: Epic): string | null {
  const stamps = epic.phases.flatMap((p) => [p.docUpdatedAt, p.runStartedAt]);
  stamps.push(epic.startedAt);
  let latest: string | null = null;
  let latestMs = Number.NEGATIVE_INFINITY;
  for (const iso of stamps) {
    if (iso === null) continue;
    const ms = new Date(iso).getTime();
    if (ms > latestMs) {
      latest = iso;
      latestMs = ms;
    }
  }
  return latest;
}

/** A plan has no branch of its own: this is the branch its latest phase run
 * committed on, and null for a plan that never ran. */
export function planBranch(epic: Epic): string | null {
  let branch: string | null = null;
  let latestMs = Number.NEGATIVE_INFINITY;
  for (const p of epic.phases) {
    if (p.runBranch === '') continue;
    const ms = p.runStartedAt === null ? 0 : new Date(p.runStartedAt).getTime();
    if (ms >= latestMs) {
      branch = p.runBranch;
      latestMs = ms;
    }
  }
  return branch;
}

/** The plan's glyph in the list. What blocks outranks what runs; a finished or
 * parked plan blocks nothing, whatever its phases still hold. */
export function planGlyph(epic: Epic, resolvedSeqs: Set<number>): GlyphState {
  const complete = planComplete(epic, resolvedSeqs);
  if (epic.status === 'archived') return complete ? 'done' : 'stopped';
  if (needYouCount(epic) > 0) return 'needs';
  if (epic.planRun?.runState === 'running' || epic.phases.some((p) => p.runState === 'running')) return 'working';
  if (epic.planRun?.runState === 'failed') return 'failed';
  if (epic.status === 'paused') return 'stopped';
  return complete ? 'done' : 'pending';
}

/** The list row's one sentence: `3 of 7 phases landed · 2 need you · $86`. */
export function planSummary(epic: Epic, resolvedSeqs: Set<number>): string {
  const landed = String(phasesLanded(epic, resolvedSeqs));
  const total = String(epic.phases.length);
  const needs = needYouCount(epic);
  const parts = [t`${landed} of ${total} phases landed`];
  if (needs > 0) {
    parts.push(plural(needs, { one: '# needs you', few: '# need you', many: '# need you', other: '# need you' }));
  } else if (epic.phases.some((p) => p.runState === 'running')) {
    parts.push(t`phase running`);
  } else if (epic.status === 'paused') {
    parts.push(t`paused`);
  } else if (planComplete(epic, resolvedSeqs)) {
    parts.push(t`all landed`);
  } else if (epic.rollup.done === 0) {
    parts.push(t`not started`);
  }
  const spent = planSpent(epic);
  if (spent !== null) parts.push(fmtSpent(spent));
  return parts.join(' · ');
}

/** The body of one `## <heading>` markdown section (up to the next `## `
 * heading), or null when the doc has no such section / it is empty. */
export function extractSection(md: string, heading: string): string | null {
  const lines = md.split('\n');
  const want = `## ${heading}`.toLowerCase();
  const start = lines.findIndex((l) => l.trim().toLowerCase() === want);
  if (start === -1) return null;
  const body: string[] = [];
  for (const line of lines.slice(start + 1)) {
    if (/^##\s/.test(line)) break;
    body.push(line);
  }
  const text = body.join('\n').trim();
  return text === '' ? null : text;
}

/** Sections a plan README states its aim in, plain words first. */
// i18n-ignore — headings matched in the doc, never shown
const GOAL_SECTIONS = ['In plain words', 'Objective', 'Goal'];

/** The plan's goal as one sentence: the first sentence of the README's first aim
 * section that opens with prose, or null when none does. */
export function planGoal(readme: string): string | null {
  for (const heading of GOAL_SECTIONS) {
    const body = extractSection(readme, heading);
    if (body === null) continue;
    const paragraph = (body.split(/\n\s*\n/)[0] ?? '').replace(/\s+/g, ' ').replace(/[*`]/g, '').trim();
    if (paragraph === '' || /^[|#>-]/.test(paragraph)) continue;
    // A stop counts only before a capital, so `e.g. this` does not end the goal.
    return /^.*?[.!?…](?=\s+[\p{Lu}]|$)/u.exec(paragraph)?.[0] ?? paragraph;
  }
  return null;
}
