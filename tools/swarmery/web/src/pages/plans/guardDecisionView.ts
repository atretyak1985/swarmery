// Shared view model for a phase's open guard/stop decision (NeedsYouDecision):
// what kind it is, the mono facts line, and the option list `DecisionCard`
// renders — the one place this gets turned into words, read by both the Needs
// You queue (pages/NeedsYou.tsx) and the Review tab's in-place reply (plan D24).
// Nothing here answers a decision; that stays answerGuardDecision (api/guard.ts).

import { plural, t } from '@lingui/core/macro';
import type { DecisionCardProps } from '../../components/place/DecisionCard';
import { guardFindings } from '../../api/guard';
import type { NeedsYouDecision } from '../../api/types';

export const ALLOW_OPTION = 'allow-add-to-scope';

/** An agent-declared stop (verify-not-trust phase 10), as opposed to a plain
 *  guard allow/reject decision — a stop carries its own trigger or lets the
 *  operator answer in their own words. */
export function isStopDecision(decision: NeedsYouDecision): boolean {
  return decision.trigger !== undefined || decision.freeText === true;
}

/** Whether `decision` is the plain two-way guard shape: exactly
 *  allow-add-to-scope + reject, nothing to pick from. */
export function isTwoWayGuardDecision(decision: NeedsYouDecision): boolean {
  if (isStopDecision(decision)) return false;
  const ids = decision.options.map((o) => o.id);
  return decision.options.length === 2 && ids.includes(ALLOW_OPTION) && ids.includes('reject');
}

/** D26: a plain guard decision whose `allow-add-to-scope` would add nothing —
 *  no protected path was actually named for it. Answering "allow" and
 *  "reject" come to the same place, so the Review tab offers one plain close
 *  instead of a choice nobody needs to make. */
export function isEmptyGuardDecision(decision: NeedsYouDecision): boolean {
  if (isStopDecision(decision)) return false;
  return (decision.paths ?? []).length === 0;
}

/** One part of the decision's mono facts line. */
export interface GuardDecisionFact {
  key: string;
  text: string;
}

/** What the guard found, as mono facts: protected files touched, a doc-copy
 *  mismatch, blocked attempts, an unreadable guard block. */
export function guardCheckFacts(decision: NeedsYouDecision): GuardDecisionFact[] {
  const f = guardFindings(decision.guard);
  const facts: GuardDecisionFact[] = [];
  if (f !== null && f.changed.length > 0) {
    facts.push({
      key: 'changed',
      text: plural(f.changed.length, {
        one: 'touched # protected file',
        few: 'touched # protected files',
        many: 'touched # protected files',
        other: 'touched # protected files',
      }),
    });
  }
  if (f?.docCopyMismatch === true) facts.push({ key: 'doc-copy', text: t`doc copy mismatch` });
  if (f !== null && f.blocked > 0) {
    facts.push({
      key: 'blocked',
      text: plural(f.blocked, {
        one: '# blocked attempt',
        few: '# blocked attempts',
        many: '# blocked attempts',
        other: '# blocked attempts',
      }),
    });
  }
  if (f?.unreadable === true) facts.push({ key: 'unreadable', text: t`guard block unreadable` });
  return facts;
}

/** Every fact the decision carries: the stop's trigger (when it is one), the
 *  guard check's own facts, broken-contract flags and the paths an "allow"
 *  would add — the same parts NeedsYou's PhaseDecision card shows, in the same
 *  order, so the Review tab's in-place reply reads identically. */
export function guardDecisionFacts(decision: NeedsYouDecision): GuardDecisionFact[] {
  const isStop = isStopDecision(decision);
  const trigger = decision.trigger !== undefined && decision.trigger !== '' ? decision.trigger : t`no trigger`;
  const flagList = (decision.flags ?? []).join(', ');
  const pathList = (decision.paths ?? []).join(', ');
  return [
    ...(isStop ? [{ key: 'trigger', text: trigger }] : []),
    ...guardCheckFacts(decision),
    ...(flagList === '' ? [] : [{ key: 'flags', text: t`contract broken: ${flagList}` }]),
    ...(pathList === '' ? [] : [{ key: 'paths', text: t`allow releases: ${pathList}` }]),
  ];
}

/** `decision.options`, mapped onto what `DecisionCard` renders — unchanged
 *  labels and consequences, just the option without one dropped to a bare id
 *  + label pair (DecisionCard treats an absent consequence as "none"). */
export function guardDecisionOptions(decision: NeedsYouDecision): NonNullable<DecisionCardProps['options']> {
  return decision.options.map((o) =>
    o.consequence === undefined || o.consequence === '' ? { id: o.id, label: o.label } : { id: o.id, label: o.label, consequence: o.consequence },
  );
}

/** The card's title: the stop's one-sentence reason, or the decision's question. */
export function guardDecisionTitle(decision: NeedsYouDecision): string {
  if (!isStopDecision(decision)) return decision.question;
  return decision.reason !== undefined && decision.reason !== '' ? decision.reason : decision.question;
}
