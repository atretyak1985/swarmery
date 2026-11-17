// What each diagnosis blocker MEANS for the operator, and what to do about it.
//
// The daemon proves the facts (phasediag: which branch, how many commits, which
// base, which decision); this module turns each proven fact into the three
// things the run-outcome modal must say about it:
//   • tier  — does it stop a retry, stop the phase from counting as done, wait
//             on the operator's decision, or is it only advisory?
//   • title — what blocks, in one translated sentence;
//   • steps — how to resolve it, as numbered, translated instructions, plus the
//             exact git commands where the fix happens outside the dashboard.
//
// The server's English `summary`/`detail` stay as the evidence line under each
// card; the guidance is composed here so it is translated and keyed on kind.

import type { I18n } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import type { PhaseBlocker, ProviderTerms } from '../../api/types';

/** How a blocker stands in the operator's way, most urgent first. */
export type BlockerTier = 'decide' | 'blocks-retry' | 'blocks-done' | 'advisory';

export const TIER_ORDER: readonly BlockerTier[] = ['decide', 'blocks-retry', 'blocks-done', 'advisory'];

export interface BlockerGuide {
  tier: BlockerTier;
  /** What blocks — one sentence. */
  title: string;
  /** How to resolve it, in order. */
  steps: string[];
  /** Exact shell commands, run in the project's repo; copyable, never translated. */
  commands: string[];
  /** This blocker is answered by deleting `branch` (and the delete is the fix, not a loss). */
  deletable: 'fix' | 'discard' | null;
  /** The keep-the-work action: push `branch` and open a change request for it —
   *  through the owning phase's land route (`phase`, the phase id is the
   *  branch's own) or the orphan-branch route (`orphan`, no phase row). */
  land: 'phase' | 'orphan' | null;
}

/** The guard's allow option id (phasediag.GuardOptionAllowID): with protected
 *  edits on the branch, the only answer that lets a retry keep the work. */
export const GUARD_ALLOW_OPTION = 'allow-add-to-scope';

/** The protected files the stopped run's branch changed; [] when none (or an
 *  older daemon that does not say). */
export function protectedEdits(b: PhaseBlocker): string[] {
  return b.protectedChanged ?? [];
}

function inspect(b: PhaseBlocker): string[] {
  const branch = b.branch ?? '';
  const base = b.base ?? '';
  if (branch === '' || base === '') return [];
  // i18n-ignore — a shell command, never translated
  return [`git log --oneline ${base}..${branch}`];
}

function merge(b: PhaseBlocker): string[] {
  const branch = b.branch ?? '';
  const base = b.base ?? '';
  if (branch === '' || base === '') return [];
  // i18n-ignore — shell commands, never translated
  return [`git switch ${base}`, `git merge --no-ff ${branch}`];
}

/** The guide for one blocker. Unknown kinds (a newer daemon) fall back to the
 *  server's own summary as an advisory line, never to silence. */
export function guideFor(b: PhaseBlocker, i18n: I18n, terms: ProviderTerms): BlockerGuide {
  const branch = b.branch ?? '';
  const base = b.base ?? i18n._(msg`the base branch`);
  const commits = plural(b.commitsAhead ?? 0, {
    one: '# commit',
    few: '# commits',
    many: '# commits',
    other: '# commits',
  });
  const decisionId = b.decisionId ?? '';
  const change = terms.change.toLowerCase();
  const changeShort = terms.changeShort;
  // With protected edits on the branch, only the guard's allow keeps the work
  // (phasediag.AnswerContinuesBranch) — the copy must not promise more.
  const files = protectedEdits(b).join(', ');
  switch (b.kind) {
    case 'decision-open':
      return {
        tier: 'decide',
        title: i18n._(msg`The executor stopped and is waiting for your decision ${decisionId}`),
        steps: [
          i18n._(msg`Read the executor's reason and pick one of its options below — or write your own answer.`),
          i18n._(msg`Your answer is recorded under ## Decisions in the phase doc and handed to the next run.`),
          files === ''
            ? i18n._(msg`Then press Retry run: it continues on this phase's branch, so the work already done is kept.`)
            : i18n._(
                msg`The branch holds protected edits (${files}). Only "Allow (add to scope)" keeps them: a retry then continues on this branch. Any other answer leaves the branch to be merged or deleted before a retry.`,
              ),
        ],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'branch-awaits-decision':
      return {
        tier: 'decide',
        title: i18n._(msg`The stopped run's work (${commits}) is on ${branch}`),
        steps: [
          files === ''
            ? i18n._(msg`Keep it: answering the decision above lets the retry continue on these commits.`)
            : i18n._(
                msg`To keep it, answer "Allow (add to scope)" above: it holds protected edits (${files}), and any other answer leaves the branch to be merged or deleted before a retry.`,
              ),
          i18n._(msg`Delete the branch only to throw this work away and run the phase from scratch.`),
        ],
        commands: inspect(b),
        deletable: 'discard',
        land: null,
      };
    case 'branch-continues':
      return {
        tier: 'advisory',
        title: i18n._(msg`Retry run continues on ${branch} (${commits}) with your answer to ${decisionId}`),
        steps: [
          i18n._(msg`Nothing to resolve — press Retry run; the work already done is kept.`),
          i18n._(msg`Delete the branch only to throw this work away and run the phase from scratch.`),
        ],
        commands: inspect(b),
        deletable: 'discard',
        land: null,
      };
    case 'branch-dirty':
      if (decisionId !== '') {
        // Answered, but the answer did not accept the protected edits on the
        // branch, so the run engine will not build on it.
        return {
          tier: 'blocks-retry',
          title: i18n._(
            msg`Your answer to ${decisionId} did not accept the protected edits on ${branch} — a retry cannot build on it`,
          ),
          steps: [
            i18n._(msg`Check what is on it: the commits are listed under the evidence line below.`),
            i18n._(msg`Work you want: press Open ${changeShort} — it pushes the branch and opens a ${change} into ${base}. Once that is merged, press Retry run.`),
            i18n._(msg`Work you don't want: press Delete branch, and the retry recreates it from ${base}.`),
          ],
          commands: [...inspect(b), ...merge(b)],
          deletable: 'fix',
          land: 'phase',
        };
      }
      return {
        tier: 'blocks-retry',
        title: i18n._(msg`A previous run left ${commits} on ${branch} — a retry cannot start while they are there`),
        steps: [
          i18n._(msg`Check what is on it: the commits are listed under the evidence line below.`),
          i18n._(msg`Work you want: press Open ${changeShort} — it pushes the branch and opens a ${change} into ${base}. Once that is merged, press Retry run.`),
          i18n._(msg`Work you don't want: press Delete branch, and the retry recreates it from ${base}.`),
        ],
        commands: [...inspect(b), ...merge(b)],
        deletable: 'fix',
        land: 'phase',
      };
    case 'branch-blocks-retry':
      return {
        tier: 'advisory',
        title: i18n._(msg`An empty leftover branch ${branch} is still there`),
        steps: [i18n._(msg`Nothing to do — Retry run deletes it automatically.`)],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'own-worktree':
      return {
        tier: 'advisory',
        title: i18n._(msg`The previous run's worktree is still checked out on its branch`),
        steps: [i18n._(msg`Nothing to do — Retry run picks that worktree up and continues the work.`)],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'dep-incomplete':
      return {
        tier: 'blocks-retry',
        title: i18n._(msg`A phase this one depends on is not finished`),
        steps: [
          i18n._(msg`Open that phase and finish it first — run it, or tick its criteria if its work is already done.`),
          i18n._(msg`Until then Retry run is refused: this phase builds on that work.`),
        ],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'dep-unmerged':
      // Not a refusal: the run engine stacks this phase's run on the dependency's
      // branch (phaserun.resolveRunBase), so a retry sees that code. It refuses
      // only when several unmerged dependency branches have diverged.
      return {
        tier: 'advisory',
        title: i18n._(msg`A dependency is ticked, but its code is still only on ${branch}`),
        steps: [
          i18n._(
            msg`Retry run starts on top of ${branch}, so it sees that code. It is refused only if several unmerged dependency branches have diverged.`,
          ),
          i18n._(msg`To land it: press Open ${changeShort} — it pushes ${branch} and opens a ${change} into ${base}.`),
        ],
        commands: merge(b),
        deletable: null,
        land: 'phase',
      };
    case 'verify-failed':
      return {
        tier: 'blocks-done',
        title: i18n._(msg`The verifier could not confirm the criteria this run ticked`),
        steps: [
          i18n._(msg`Read the verifier's reasons in the evidence below.`),
          i18n._(msg`If the work is wrong: Retry run. If the criteria are wrong: Revise plan.`),
        ],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'unverified':
      return {
        tier: 'blocks-done',
        title: i18n._(msg`Every criterion is ticked, but nobody has graded the work yet`),
        steps: [i18n._(msg`Press Verify again to grade the branch as it stands.`)],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'unreported':
      return {
        tier: 'blocks-done',
        title: i18n._(msg`The work is done, but the phase doc has no Completion Report`),
        steps: [
          i18n._(
            msg`Fill the ## Completion Report section of the phase doc: what shipped, files and commits, verification output, deviations.`,
          ),
        ],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'no-criteria':
      return {
        tier: 'blocks-done',
        title: i18n._(msg`The phase doc has no acceptance criteria, so no run can prove progress`),
        steps: [i18n._(msg`Add "- [ ]" acceptance-criteria checkboxes to the phase doc, or press Revise plan.`)],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'no-lesson':
      return {
        tier: 'advisory',
        title: i18n._(msg`The plan has no recorded lesson`),
        steps: [
          i18n._(msg`Optional: add a "### Lesson N" entry under ## Lessons Learned in phases/09-retrospective.md.`),
        ],
        commands: [],
        deletable: null,
        land: null,
      };
    case 'orphan-branch':
      if (branch === '') {
        return { tier: 'advisory', title: b.summary, steps: [], commands: [], deletable: null, land: null };
      }
      return {
        tier: 'advisory',
        title: i18n._(msg`${branch} holds ${commits} from an older run that matches no phase now`),
        steps: [
          i18n._(msg`It does not block this phase — Retry run works without touching it.`),
          i18n._(
            msg`Work you want: press Open ${changeShort} to push it and open a ${change} into ${base}. Otherwise delete it.`,
          ),
        ],
        commands: [...inspect(b), ...merge(b)],
        deletable: 'fix',
        land: 'orphan',
      };
    default:
      return { tier: 'advisory', title: b.summary, steps: [], commands: [], deletable: null, land: null };
  }
}

/** The one sentence the modal opens with: what to do FIRST. */
export function nextStep(tiers: ReadonlySet<BlockerTier>, outcome: string, i18n: I18n): string {
  if (tiers.has('decide')) {
    return i18n._(msg`Answer the executor's decision below, then press Retry run.`);
  }
  if (tiers.has('blocks-retry')) {
    return i18n._(msg`Resolve what blocks a retry (below), then press Retry run.`);
  }
  if (tiers.has('blocks-done')) {
    return i18n._(msg`The run itself can go on; resolve what keeps the phase from counting as done.`);
  }
  if (outcome === 'completed') {
    return i18n._(msg`Nothing blocks this phase.`);
  }
  return i18n._(
    msg`Nothing in the repo stops a retry. Read what the executor said, then press Retry run or Revise plan.`,
  );
}

/** Why Retry run must not be pressed yet, or null when it may. Mirrors the
 *  refusals the daemon itself makes (phaserun.Start: unmet dependencies, a dirty
 *  branch, an unanswered stop) — every kind in the `blocks-retry` tier must be
 *  one of them, or the tier heading says "blocks a retry" over an enabled button. */
export function retryRefusal(blockers: readonly PhaseBlocker[], i18n: I18n): string | null {
  if (blockers.some((b) => b.kind === 'decision-open')) {
    return i18n._(msg`answer the executor's decision first — until then a retry is refused`);
  }
  if (blockers.some((b) => b.kind === 'dep-incomplete')) {
    return i18n._(msg`finish the phase this one depends on first — until then a retry is refused`);
  }
  if (blockers.some((b) => b.kind === 'branch-dirty')) {
    return i18n._(msg`merge or delete the leftover branch first — until then a retry is refused`);
  }
  return null;
}

/** Where the `kind: stop` block sits in `text`: the prose before it, the prose
 *  after it, and whether the block runs to the end of the text (a truncated
 *  message cut off inside it). null when there is no block. A fenced block ends
 *  at its closing fence; a bare one at the first blank line. */
function cutStopBlock(text: string): { before: string; after: string; runsToEnd: boolean } | null {
  const m = /(?:^|\n)(```ya?ml\s*\n)?kind:\s*stop\b/.exec(text);
  if (m === null) return null;
  const from = m.index + m[0].length;
  let end: number;
  if (m[1] !== undefined) {
    const close = text.indexOf('\n```', from);
    end = close === -1 ? text.length : close + '\n```'.length;
  } else {
    const blank = text.indexOf('\n\n', from);
    end = blank === -1 ? text.length : blank;
  }
  return { before: text.slice(0, m.index).trim(), after: text.slice(end).trim(), runsToEnd: end === text.length };
}

/** The executor's last message without the `kind: stop` YAML block, which the
 *  decision card already shows as options. Prose after the block is kept.
 *  Returns the text unchanged when no block is found or nothing would remain. */
export function withoutStopBlock(text: string): string {
  const cut = cutStopBlock(text);
  if (cut === null) return text;
  const kept = [cut.before, cut.after].filter((s) => s !== '').join('\n\n');
  return kept === '' ? text : kept;
}

/** The agent message as the modal shows it. `hideStop` cuts the stop block (the
 *  decision card shows its options). The `…` truncation marker stays whenever
 *  the visible text runs to where the message was cut — it is dropped only when
 *  the cut fell inside the hidden block. */
export function agentMessageView(text: string, truncated: boolean, hideStop: boolean): string {
  const marker = truncated ? '…' : '';
  if (!hideStop) return `${text}${marker}`;
  const cut = cutStopBlock(text);
  const shown = withoutStopBlock(text);
  if (cut === null || shown === text) return `${text}${marker}`;
  return cut.runsToEnd ? shown : `${shown}${marker}`;
}
