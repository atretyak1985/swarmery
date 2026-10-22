// Typed client for guard decisions (verify-not-trust phase 4): a phase run that
// touched protected paths raises a `phase_decision` the operator answers —
// allow the paths into scope (they join the guard block's `allow:` list) or
// reject. Go: internal/api/phase_decision_answer.go → phaserun.AnswerGuardDecision.

import { MOCK } from '../api';
import type { EpicPhase, GuardSummary } from './types';

/** The guard decision options (Go phaserun.GuardOptions). */
export type GuardOption = 'allow-add-to-scope' | 'reject';

/**
 * POST /api/epics/{taskId}/phases/{phaseId}/decisions/{decisionId} — answers the
 * decision. Throws with the server's `error` on any non-2xx (400 unknown option,
 * 404 unknown decision, 409 already answered or the phase is running).
 */
export async function answerGuardDecision(
  taskId: number,
  phaseId: number,
  decisionId: string,
  option: string,
  note: string,
): Promise<void> {
  if (MOCK) return;
  const path = `/api/epics/${String(taskId)}/phases/${String(phaseId)}/decisions/${encodeURIComponent(decisionId)}`;
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ option, note }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `POST ${path} → ${String(res.status)}`);
  }
}

/** answerGuardDecision under its general name: the endpoint answers every
 *  per-phase decision — guard (allow / reject) and review (repeat-review /
 *  accept-as-is, verify-not-trust phase 9) alike. */
export const answerPhaseDecision = answerGuardDecision;

/** The guard check a phase shows: its open decision's (the one the operator must
 *  act on), else its latest review's. */
export function phaseGuard(phase: Pick<EpicPhase, 'guardDecision' | 'reviews'>): GuardSummary | null {
  return phase.guardDecision?.guard ?? phase.reviews?.[0]?.guard ?? null;
}

/** What a guard check found, as the badge shows it. */
export interface GuardFindings {
  changed: string[];
  blocked: number;
  docCopyMismatch: boolean;
  unreadable: boolean;
}

/** The findings of a guard check; null when there are none (or no check). */
export function guardFindings(guard: GuardSummary | null | undefined): GuardFindings | null {
  if (guard == null) return null;
  const f: GuardFindings = {
    changed: guard.protectedChanged ?? [],
    blocked: guard.guardBlocksTotal ?? guard.guardBlocks?.length ?? 0,
    docCopyMismatch: guard.docCopyMismatch === true,
    unreadable: guard.parseError != null,
  };
  return f.changed.length > 0 || f.blocked > 0 || f.docCopyMismatch || f.unreadable ? f : null;
}
