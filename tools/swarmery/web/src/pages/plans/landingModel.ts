// Pure rules of a plan phase's landing flow (the Review tab, the landing chip,
// the deps-unmerged "Open PR/MR" buttons). Every label is built from the
// provider's `terms` the daemon sends — nothing here, and nothing that calls
// it, branches on which provider a project uses (SC-11).

import type { EpicPhase, PhaseLanding, PhaseLandingState, ProviderTerms } from '../../api/types';

/** The landing fields these rules read. `landing` is optional because a phase
 * fixture written before the landing lifecycle existed carries none; a
 * missing landing reads as `none`. */
export type LandingPhase = Pick<EpicPhase, 'runState' | 'runSessionUuid'> & {
  landing?: Pick<PhaseLanding, 'state'> | null;
};

function stateOf(phase: LandingPhase): PhaseLandingState {
  return phase.landing?.state ?? 'none';
}

/** The landing state in the operator's words, e.g. "PR open" / "MR open". */
export function landingLabel(state: PhaseLandingState, terms: ProviderTerms): string {
  switch (state) {
    case 'none':
      return 'not landed';
    case 'ready':
      return 'ready to land';
    case 'pushed':
      return 'pushed';
    case 'pr_open':
      return `${terms.changeShort} open`;
    case 'merged':
      return 'merged';
    case 'returned':
      return 'returned to agent';
  }
}

/** The states a run branch can be pushed (and a change request opened) from:
 * nothing landed yet, already pushed (push again, or open the change request
 * now), or returned to the agent and re-run. */
const LANDABLE: ReadonlySet<PhaseLandingState> = new Set<PhaseLandingState>(['none', 'ready', 'pushed', 'returned']);

/**
 * Whether Push / Push + open <change> may be offered: the run is not live,
 * the phase has run at all (an idle phase has no branch), and the landing
 * lifecycle is not past the push (an open change request or a merge). The
 * server stays the judge — this only keeps dead buttons off the screen.
 */
export function canLand(phase: LandingPhase): boolean {
  if (phase.runState === 'running' || phase.runState === 'idle') return false;
  return LANDABLE.has(stateOf(phase));
}

/** Whether the phase can be sent back to its agent: not live, has run, not merged. */
export function canReturn(phase: LandingPhase): boolean {
  if (phase.runState === 'running' || phase.runState === 'idle') return false;
  return stateOf(phase) !== 'merged';
}

/**
 * Whether the phase panel shows a Review tab. The phase DTO does not carry its
 * run branch, so "has a branch" is approximated by "has ever run": a run
 * state other than idle, a run session, or any landing progress. A phase that
 * never ran has nothing to review; the review endpoint's 409 `no-run-branch`
 * covers the rest.
 */
export function hasReviewTab(phase: LandingPhase): boolean {
  // `?? null`: like `landing`, a fixture may omit runSessionUuid entirely.
  return phase.runState !== 'idle' || (phase.runSessionUuid ?? null) !== null || stateOf(phase) !== 'none';
}

/** The change-request link text, `${terms.changeShort} #<n>` ("PR #77"), or
 * null when no change request was opened. Without a number it is the bare
 * short term. */
export function prLinkText(landing: Pick<PhaseLanding, 'prUrl' | 'prNumber'>, terms: ProviderTerms): string | null {
  if (landing.prUrl === null || landing.prUrl === '') return null;
  return landing.prNumber !== null ? `${terms.changeShort} #${String(landing.prNumber)}` : terms.changeShort;
}

/** The phase id a phase-run branch names (`swarm/phase-123` → 123), or null for
 * any other branch. */
export function branchToPhaseId(branch: string): number | null {
  const m = /^swarm\/phase-(\d+)$/.exec(branch.trim());
  if (m === null) return null;
  const id = Number(m[1]);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

/** What the phase card's landing chip shows: its text, and the change-request
 * URL when the chip is a link. */
export interface LandingChip {
  state: Exclude<PhaseLandingState, 'none'>;
  text: string;
  href: string | null;
}

/**
 * The phase card's landing chip, or null when there is nothing to say (the
 * phase has not reached the landing flow). An open change request with a URL
 * is a link labelled `prLinkText` ("PR #12"); a merge gets a check mark; every
 * other state is `landingLabel`.
 */
export function landingChip(
  landing: Pick<PhaseLanding, 'state' | 'prUrl' | 'prNumber'>,
  terms: ProviderTerms,
): LandingChip | null {
  const { state } = landing;
  if (state === 'none') return null;
  if (state === 'pr_open') {
    const text = prLinkText(landing, terms);
    if (text !== null && landing.prUrl !== null) return { state, text, href: landing.prUrl };
  }
  const label = landingLabel(state, terms);
  return { state, text: state === 'merged' ? `${label} ✓` : label, href: null };
}
