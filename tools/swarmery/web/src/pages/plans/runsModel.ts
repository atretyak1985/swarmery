// The Runs tab (plan D-27…D-31): a pure function from a phase's runs and a
// plan's sessions to a one-sentence verdict, and the session cards' roles and
// groups. No React, no DOM — the tab renders whatever this returns.
//
// A session's `title` is the start of its boilerplate service prompt (every
// headless phase run opens the same way), so it never names a card or a
// tooltip — the role (from `planGroup`) does, with the session's own one-line
// `why` as the short summary when the daemon has one. A session with no
// `planGroup` at all (a `linked`/`inferred` row, or an older daemon) reads as
// a generic "session", never guessed at.
//
// Attempt grouping (D-29): the data carries no explicit attempt number, so a
// phase's runs — oldest first — are read as one attempt per session. A phase
// that also ran a subagent under the same phaseId (a session count THIS module
// cannot tell apart from a reopen) over-counts its attempts by exactly that
// many; that is a known approximation, not a guess at a fact the daemon could
// have sent — see the phase doc's risk note and the Completion Report.

import { t } from '@lingui/core/macro';
import type { Epic, EpicPhase, PhaseReopen, Session } from '../../api/types';
import { isUnresolvedOutcome, outcomeChipLabel } from './phaseChips';
import type { PhaseStatus } from './planModel';

export type VerdictTone = 'success' | 'warn' | 'danger' | 'neutral';

/** A one-sentence verdict for the Runs tab: a tone, a bold lead clause and the
 *  rest of the sentence — the same shape the Review tab's verdict uses. */
export interface RunsVerdict {
  tone: VerdictTone;
  lead: string;
  rest: string;
}

export function runsVerdict(
  phase: Pick<EpicPhase, 'runOutcome' | 'checkboxesDone' | 'checkboxesTotal'>,
  status: PhaseStatus,
  running: boolean,
): RunsVerdict {
  if (running) {
    return { tone: 'neutral', lead: t`Running`, rest: t`— the executor is working on this phase.` };
  }
  if (status === 'done') {
    return { tone: 'success', lead: t`Done`, rest: t`— every criterion is checked.` };
  }
  if (phase.runOutcome === 'idle') {
    return { tone: 'neutral', lead: t`No runs yet`, rest: t`— this phase starts when the previous one lands.` };
  }
  if (isUnresolvedOutcome(phase.runOutcome)) {
    const label = outcomeChipLabel(phase as EpicPhase, phase.runOutcome);
    return {
      tone: phase.runOutcome === 'failed' ? 'danger' : 'warn',
      lead: t`The last run did not finish the phase`,
      rest: t`— ${label}.`,
    };
  }
  const done = String(phase.checkboxesDone);
  const total = String(phase.checkboxesTotal);
  return { tone: 'neutral', lead: t`In progress`, rest: t`— ${done}/${total} criteria checked.` };
}

/** A card's role: the only thing it is ever named by. `session` is the
 *  fallback for a row with no `planGroup` — a linked/inferred row, never a
 *  guess from the title. */
export type RunRole = 'executor' | 'controller' | 'session';

export function runRole(s: Pick<Session, 'planGroup'>): RunRole {
  const role = s.planGroup?.role;
  if (role === 'controller') return 'controller';
  if (role === 'phase') return 'executor';
  return 'session';
}

export function runRoleLabel(role: RunRole): string {
  if (role === 'controller') return t`plan run`;
  if (role === 'executor') return t`executor`;
  return t`session`;
}

/** The card's name: the role, plus the session's own one-line `why` when the
 *  daemon summarised one. Never the service prompt (`s.title`). */
export function runCardTitle(s: Pick<Session, 'planGroup' | 'why'>): string {
  const label = runRoleLabel(runRole(s));
  const summary = (s.why ?? '').trim();
  return summary === '' ? label : `${label} — ${summary}`;
}

/** What the group's cards cost, or null while none of them is priced. */
function sumCost(sessions: readonly Session[]): number | null {
  const priced = sessions.flatMap((s) => (s.costUsd == null ? [] : [s.costUsd]));
  return priced.length === 0 ? null : priced.reduce((a, b) => a + b, 0);
}

export interface RunGroup {
  key: string;
  label: string;
  /** Why this attempt started (a reopen's reason); null for the first attempt
   *  or a group this module does not key to one. */
  reason: string | null;
  costUsd: number | null;
  sessions: Session[];
}

/** "this phase" scope (D-29): one group per attempt, oldest first — the
 *  reopen that (re)started attempt N+1 labels group N+1's reason. A phase run
 *  with no reopens has exactly one group, whatever its session count. */
export function phaseAttemptGroups(runs: readonly Session[], reopens: readonly PhaseReopen[]): RunGroup[] {
  const sorted = [...runs].sort((a, b) => a.startedAt.localeCompare(b.startedAt));
  const orderedReopens = [...reopens].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  return sorted.map((s, i) => {
    const n = i + 1;
    return {
      key: s.sessionUuid,
      label: n === 1 ? t`Attempt 1` : t`Attempt ${String(n)}`,
      reason: orderedReopens[i - 1]?.reason ?? null,
      costUsd: sumCost([s]),
      sessions: [s],
    };
  });
}

/** "whole plan" scope (D-29): one group per phase, in plan order, with the
 *  plan-run controller's own sessions as a group ahead of all of them. A phase
 *  with no sessions of its own has no group. */
export function planPhaseGroups(epic: Pick<Epic, 'phases'>, sessions: readonly Session[]): RunGroup[] {
  const groups: RunGroup[] = [];
  const controller = sessions.filter((s) => s.planGroup?.role === 'controller');
  if (controller.length > 0) {
    groups.push({ key: 'controller', label: t`Plan run`, reason: null, costUsd: sumCost(controller), sessions: controller });
  }
  for (const p of epic.phases) {
    const rows = sessions.filter((s) => s.planGroup?.role === 'phase' && s.planGroup.phaseId === p.id);
    if (rows.length === 0) continue;
    const seq = String(p.seq);
    groups.push({ key: `phase:${String(p.id)}`, label: t`#${seq} ${p.name}`, reason: null, costUsd: sumCost(rows), sessions: rows });
  }
  return groups;
}
