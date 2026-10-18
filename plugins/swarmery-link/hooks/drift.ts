// The drift line (option `planBand`): once per main-loop turn that edited a
// file while the session's plan phase is In progress (or was picked in the
// terminal with /plan), one `$.model.classify` asks whether
// the turn stayed on the phase. `off-phase` is reported to the daemon and
// drawn as one line beneath the answer. register.ts calls it from its one
// turn.complete hook; a subagent's turn never reaches it.

import type { EngineInterface } from 'claude-code'
import { DRIFT_LABELS, DRIFT_TIMEOUT_MS, driftInput, driftLine, driftReason, isDriftWatched } from './lib/plan'
import { type PlanAccess, type Planner, bounded, postPlan } from './plan'

/**
 * The drift line for turn `turnId`, or undefined: no plan, a phase that is
 * neither In progress nor picked in the terminal, no edit, a turn
 * already classified, a label other than `off-phase`, or a classification
 * that failed or ran past DRIFT_TIMEOUT_MS. Never rejects.
 */
export async function checkDrift($: EngineInterface, planner: Planner, access: PlanAccess, turnId: string, answer: string): Promise<string | undefined> {
  const turn = planner.turn
  const view = planner.view
  if (turn === undefined || turn.turnId !== turnId || turn.isClassified || turn.edits.length === 0) return undefined
  if (view === undefined || access.token === undefined || !isDriftWatched(view)) return undefined
  planner.turn = { ...turn, isClassified: true }
  let label: string | undefined
  try {
    const asked = $.model.classify(driftInput(view.phase, answer, turn.edits), DRIFT_LABELS).then(
      (l): string | undefined => l,
      (): string | undefined => undefined,
    )
    const reply = await bounded($, asked, DRIFT_TIMEOUT_MS)
    label = reply === 'timeout' ? undefined : reply
  } catch {
    return undefined
  }
  if (label !== 'off-phase') return undefined
  const reason = driftReason(turn.edits)
  void postPlan($, planner, access, 'drift', {
    session: access.sessionId,
    taskId: view.taskId,
    phaseId: view.phase.id,
    verdict: label,
    reason,
  })
  return driftLine(view.phase.n, reason)
}
