// The Review tab's verdict sentence and next-step card (plan D19/D21/D23): one
// pure function from the phase's landing, host status, guard decision, run
// branch and open criteria to what happened and what to do about it. `canLand`
// / `canReturn` (landingModel.ts) stay the one source of truth for whether an
// action is reachable at all — this only adds the WORDS: a one-sentence verdict,
// at most one primary and one secondary action, and a reason for each action
// this state does not offer.
//
// `push`, `pr` and `return` keep sending exactly the request bodies they always
// did (landPhase); this module only decides which of them to put forward and
// how to talk about the rest. `Find <change>` and the small always-reachable
// "Return to agent" control are not part of the card's primary/secondary pair —
// they stay their own small controls (PhaseReview.tsx), because they are not
// "the next step toward landing" so much as a lookup and an escape hatch.

import { t } from '@lingui/core/macro';
import type { EpicPhase, NeedsYouDecision, PhaseLanding, PhaseLandingState, ProviderTerms } from '../../api/types';
import { isEmptyGuardDecision, isStopDecision } from './guardDecisionView';
import { canLand, canReturn, type LandingPhase } from './landingModel';

export type VerdictTone = 'success' | 'warn' | 'danger' | 'neutral';

/** A one-sentence verdict: a tone (rendered as a dot, never color alone), a bold
 *  lead clause and the rest of the sentence — the same shape as a session
 *  header's one-line state. */
export interface ReviewVerdict {
  tone: VerdictTone;
  lead: string;
  rest: string;
}

export type ReviewActionId = 'push' | 'pr' | 'return' | 'answer-decision' | 'close-guard';

export interface ReviewAction {
  id: ReviewActionId;
  label: string;
  /** The "why" sentence under the action's title on the next-step card. */
  reason: string;
}

export interface UnavailableAction {
  id: 'push' | 'pr' | 'return';
  reason: string;
}

export interface ReviewNextStep {
  verdict: ReviewVerdict;
  primary: ReviewAction | null;
  secondary: ReviewAction | null;
  /** Shown under the card when `primary` is null: why there is nothing to do. */
  note: string | null;
  unavailable: UnavailableAction[];
}

/** The fields `reviewNextStep` reads off a phase. */
export type ReviewNextStepPhase = LandingPhase &
  Pick<EpicPhase, 'manualOpen' | 'landOpen' | 'checkboxesDone' | 'checkboxesTotal'> & {
    landing: PhaseLanding | null;
    guardDecision?: NeedsYouDecision | null;
  };

export interface ReviewNextStepInput {
  phase: ReviewNextStepPhase;
  /** The git/host error loadError / landing.error surfaced, so it can be named
   *  as "unavailable because of a git error" without repeating the raw text —
   *  the raw text belongs to the "what git said" disclosure alone. */
  gitErrored: boolean;
  /** The git review endpoint answered 409 no-run-branch: the run happened, but
   *  its branch is gone now — distinct from a phase that never ran. */
  branchGone: boolean;
  terms: ProviderTerms;
}

const UNAVAIL_GUARD = (): string => t`a guard decision is still open — nothing lands until it is answered`;
const UNAVAIL_NO_BRANCH = (): string => t`this phase has no run branch yet`;
const UNAVAIL_BRANCH_GONE = (): string => t`the run branch is gone`;
const UNAVAIL_MERGED = (): string => t`already merged — there is nothing left to push`;
const UNAVAIL_MANUAL_ONLY = (): string => t`only manual checks are left open, and only a person closes those`;
const UNAVAIL_GIT_ERROR = (): string => t`the last attempt failed — see what git said below`;

function allUnavailable(reason: string): UnavailableAction[] {
  return [
    { id: 'push', reason },
    { id: 'pr', reason },
    { id: 'return', reason },
  ];
}

/** Whether the phase's open criteria are entirely `[MANUAL]` ones — landing has
 *  nothing left to do once that is true. */
function manualOnly(phase: ReviewNextStepInput['phase']): boolean {
  const open = phase.checkboxesTotal - phase.checkboxesDone;
  return open > 0 && phase.manualOpen > 0 && phase.manualOpen + phase.landOpen >= open;
}

const stateOf = (landing: PhaseLanding | null): PhaseLandingState => landing?.state ?? 'none';

export function reviewNextStep({ phase, gitErrored, branchGone, terms }: ReviewNextStepInput): ReviewNextStep {
  const change = terms.change;
  const landing = phase.landing;
  const decision = phase.guardDecision ?? null;

  // 1. A guard decision is open: nothing else can happen until it is answered.
  // D26: an empty decision (nothing an "allow" would add to scope) closes from
  // the Guard row alone — the card shows no primary for it, so there is never
  // a second "Close · allow nothing" button on the tab.
  if (decision != null) {
    const empty = !isStopDecision(decision) && isEmptyGuardDecision(decision);
    return {
      // An empty decision never reads as the reason anything stopped — it is
      // a formality with nothing to allow, not a blocker worth alarming over.
      verdict: empty
        ? { tone: 'neutral', lead: t`Nothing to land yet`, rest: t`— an empty guard check is open; close it from the Guard row.` }
        : {
            tone: 'danger',
            lead: t`Decision ${decision.decisionId} is still open`,
            rest: t`— the guard stopped the run and is waiting for your answer.`,
          },
      primary: empty
        ? null
        : {
            id: 'answer-decision',
            label: t`Answer the decision`,
            reason: t`The guard stopped the run on protected paths; answer here to let it continue.`,
          },
      secondary: null,
      note: empty ? t`There is nothing this decision would add to scope — close it from the Guard row below.` : null,
      unavailable: allUnavailable(UNAVAIL_GUARD()),
    };
  }

  // 7. The phase never ran: nothing to land, nothing to review.
  if (!branchGone && phase.runState === 'idle' && (phase.runSessionUuid ?? null) === null && stateOf(landing) === 'none') {
    return {
      verdict: { tone: 'neutral', lead: t`This phase has not run yet`, rest: t`— there is nothing to review.` },
      primary: null,
      secondary: null,
      note: t`Run the phase first; a review needs a finished run.`,
      unavailable: allUnavailable(UNAVAIL_NO_BRANCH()),
    };
  }

  // 4. The run happened, but its branch is gone now.
  if (branchGone) {
    const merged = stateOf(landing) === 'merged';
    return {
      verdict: merged
        ? { tone: 'success', lead: t`Merged`, rest: t`— and the run branch has since been deleted.` }
        : { tone: 'neutral', lead: t`The run branch is gone`, rest: t`— there is nothing left to push or return.` },
      primary: null,
      secondary: null,
      note: merged ? t`Nothing left to do here.` : t`The branch this run used no longer exists.`,
      unavailable: allUnavailable(UNAVAIL_BRANCH_GONE()),
    };
  }

  // 6. Only manual criteria remain: landing has nothing further to offer.
  if (manualOnly(phase) && stateOf(landing) !== 'returned') {
    return {
      verdict: { tone: 'neutral', lead: t`Only manual checks are left`, rest: t`— there is nothing more to land here.` },
      primary: null,
      secondary: null,
      note: t`Close the remaining checks by hand; landing will not do it for you.`,
      unavailable: allUnavailable(UNAVAIL_MANUAL_ONLY()),
    };
  }

  const state = stateOf(landing);
  const landable = canLand(phase);
  const returnable = canReturn(phase);
  const prStatus = landing?.prStatus ?? null;

  // 2 & 3. Merged — cleanly, or with a red CI/review on the merged change.
  if (state === 'merged') {
    const ciRed = prStatus?.ci === 'failure';
    const reviewRed = prStatus?.review === 'changes_requested';
    if (ciRed || reviewRed) {
      return {
        verdict: {
          tone: 'warn',
          lead: t`Merged`,
          rest: ciRed ? t`— but CI on the merged ${change} failed.` : t`— but review on the merged ${change} did not pass.`,
        },
        primary: null,
        secondary: null,
        note: t`It already landed; a red check here is informational only.`,
        unavailable: allUnavailable(UNAVAIL_MERGED()),
      };
    }
    return {
      verdict: { tone: 'success', lead: t`Merged`, rest: t`— there is nothing left to do here.` },
      primary: null,
      secondary: null,
      note: t`This phase already landed.`,
      unavailable: allUnavailable(UNAVAIL_MERGED()),
    };
  }

  // 1. A change request is open: the branch is pushed, and if its review (or
  // CI) came back red, the only next step this tab offers is sending the
  // phase back — `canLand` deliberately stops offering Push once a change
  // request exists (landingModel.ts), so there is no "push a fix" button here.
  if (state === 'pr_open') {
    const reviewFailed = prStatus?.review === 'changes_requested';
    const ciFailed = prStatus?.ci === 'failure';
    const red = reviewFailed || ciFailed;
    return {
      verdict: {
        tone: red ? 'danger' : 'neutral',
        lead: reviewFailed
          ? t`Review on the open ${change} did not pass`
          : ciFailed
            ? t`CI on the open ${change} failed`
            : t`A ${change} is open`,
        rest: red ? t`— fix it on the branch, or send the phase back to its agent.` : t`— waiting on CI and review.`,
      },
      primary: returnable
        ? {
            id: 'return',
            label: t`Return to agent…`,
            reason: t`Sends the phase back with your feedback; a new run can push a fix.`,
          }
        : null,
      secondary: null,
      note: returnable ? null : t`Nothing to do here until the run finishes.`,
      unavailable: [
        { id: 'push', reason: t`a ${change} is already open for this branch` },
        { id: 'pr', reason: t`a ${change} is already open for this branch` },
      ],
    };
  }

  // The ordinary landable states: nothing landed yet, pushed, or returned —
  // `pr_open` and `merged` are handled above and return before reaching here.
  if (landable) {
    return {
      verdict:
        state === 'pushed'
          ? { tone: 'neutral', lead: t`Pushed`, rest: t`— no ${change} open yet.` }
          : state === 'returned'
            ? { tone: 'neutral', lead: t`Returned to the agent`, rest: t`— re-run it, then push again.` }
            : { tone: 'neutral', lead: t`Ready to land`, rest: t`— nothing has been pushed yet.` },
      primary: { id: 'pr', label: t`Push + open ${change}`, reason: t`Pushes the run's branch and opens a ${change} for it.` },
      secondary: { id: 'push', label: t`Push`, reason: t`Pushes the branch without opening a ${change}.` },
      note: null,
      unavailable: gitErrored ? [{ id: 'push', reason: UNAVAIL_GIT_ERROR() }] : [],
    };
  }

  // Anything else (e.g. the run is still live): nothing to offer, no reason to
  // invent — reviewNextStep returns neutral and no action rather than guess.
  return {
    verdict: { tone: 'neutral', lead: t`Nothing to land right now`, rest: t`— check back once the run finishes.` },
    primary: null,
    secondary: null,
    note: null,
    unavailable: [],
  };
}
