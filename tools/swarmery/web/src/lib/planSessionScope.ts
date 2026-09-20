// Plan-sessions column view model — WHICH sessions the Plans page shows to the
// right of the phase timeline, as one pure function. No React, no DOM: the
// column renders whatever this module returns.
//
// The whole point is the phase slice. A plan's session column is the union of
// two sources — the ?planTask= grouping (`sessions`, resolved server-side from
// the stamped run branch / worktree cwd, see internal/api/session_plan_group.go)
// and the plan task's own task_sessions links (`linked`). Only the FIRST source
// carries a phase coordinate, and only on rows whose `planGroup.role` is
// 'phase':
//
//   • the plan-run controller (`role === 'controller'`) owns the PLAN, not a
//     phase, so it has no phaseId to match;
//   • `linked` rows (the `linked` / `inferred` badges) are attached to the plan
//     TASK by a cwd+time heuristic (internal/wsingest/wsingest.go) and carry no
//     phase coordinate at all, by construction.
//
// So nothing is ever inferred here: the phase slice is strictly
// `planGroup.phaseId === activePhaseId`, and everything else stays reachable in
// the 'all' slice. `totalCount` is always the whole plan, in either slice, so
// the header can say how many rows the slice is hiding.

import type { LinkedSession, Session } from '../api/types';

/** Which slice of the plan's session column is shown. */
export type PlanSessionScope = 'phase' | 'all';

export interface PlanSessionScopeInput {
  /** Sessions the server attributed to this plan (?planTask=). */
  sessions: Session[];
  /** The plan's task_sessions links. They carry no phase coordinate by construction. */
  linked: LinkedSession[];
  /** The phase whose details are open; null while the timeline is shown. */
  activePhaseId: number | null;
  /** The slice the operator picked by hand; null means the automatic rule applies. */
  picked: PlanSessionScope | null;
}

export interface PlanSessionScopeResult {
  /** Sessions to render, in plan order. */
  sessions: Session[];
  /** Linked-only rows; always empty in the phase slice. */
  linked: LinkedSession[];
  /** The slice actually in effect. */
  scope: PlanSessionScope;
  /** How many sessions belong to the active phase; null when no phase is open. */
  phaseCount: number | null;
  /** Total rows in the plan (both sources, deduped) — never the current slice. */
  totalCount: number;
  /** True when a phase slice was available but empty, so the UI fell back to 'all'. */
  fellBack: boolean;
}

/** Controller sessions sort above the phases; a phase sorts by its seq. */
export function planSessionOrder(s: Session): number {
  if (s.planGroup?.role === 'phase') return s.planGroup.phaseSeq ?? Number.MAX_SAFE_INTEGER;
  return -1;
}

/**
 * Decide the plan session column's contents: ordering, dedup of the two
 * sources, the phase slice, and the auto-fallback to the full plan.
 *
 * Pure: no side effects, no clock, no network.
 */
export function scopePlanSessions(input: PlanSessionScopeInput): PlanSessionScopeResult {
  const { sessions, linked, activePhaseId, picked } = input;

  // Plan order, not recency: the controller heads the column and the phases
  // follow the timeline they sit next to. Ties (subagents of one phase) keep
  // chronological order.
  const ordered = [...sessions].sort(
    (a, b) => planSessionOrder(a) - planSessionOrder(b) || a.startedAt.localeCompare(b.startedAt),
  );

  // The union's second half: links the ?planTask= grouping cannot see. Deduped
  // by uuid, so a run BOTH rules find appears once, in its plan-ordered position.
  const seen = new Set(sessions.map((s) => s.sessionUuid));
  const linkedOnly = linked
    .filter((l) => !seen.has(l.sessionUuid))
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));

  const totalCount = ordered.length + linkedOnly.length;

  // The phase slice, computed even when it is not the slice in effect — the
  // header shows its size either way (that `#N (0)` IS the fallback's marker).
  const phaseSessions =
    activePhaseId === null
      ? []
      : ordered.filter((s) => s.planGroup?.role === 'phase' && s.planGroup.phaseId === activePhaseId);
  const phaseCount = activePhaseId === null ? null : phaseSessions.length;

  // No phase open ⇒ always the whole plan; a hand-picked slice beats the
  // automatic rule; otherwise narrow only when there is something to narrow to.
  const scope: PlanSessionScope =
    activePhaseId === null ? 'all' : (picked ?? (phaseSessions.length > 0 ? 'phase' : 'all'));

  return {
    sessions: scope === 'phase' ? phaseSessions : ordered,
    linked: scope === 'phase' ? [] : linkedOnly,
    scope,
    phaseCount,
    totalCount,
    fellBack: activePhaseId !== null && picked === null && phaseSessions.length === 0,
  };
}
