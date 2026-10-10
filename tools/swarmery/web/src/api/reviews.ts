// Typed client for code reviews (Go DTO `reviewDTO` in internal/api/reviews.go,
// rows of phase_reviews, migration 0106). Two producers write them: the review
// stage of a `**Review:** on` phase run (scope `phase`) and the plan branch
// review that reads every phase's run branch once the plan is complete (scope
// `plan`). The Inbox lists the unacknowledged plan reviews; a phase's Runs tab
// shows its latest phase review. MOCK mode serves nothing, so both surfaces
// render their empty state offline.

import { MOCK } from '../api';

export type ReviewScope = 'phase' | 'plan';
export type ReviewVerdict = 'pass' | 'fail' | 'inconclusive';

/** One review (GET /api/reviews, GET /api/epics/{taskId}/phases/{phaseId}/reviews). */
export interface Review {
  id: number;
  scope: ReviewScope;
  /** The plan's workspace task id. */
  taskId: number;
  planTitle: string;
  /** The plan's project slug; "" when the task is gone. */
  projectSlug: string;
  /** Null for a plan review. */
  phaseId: number | null;
  phaseName: string;
  /** The reviewer's own session. */
  sessionUuid: string;
  /** The run a phase review graded; "" for a plan review. */
  runSessionUuid: string;
  /** The set of run-branch tips a plan review read; "" for a phase review. */
  branchSetKey: string;
  verdict: ReviewVerdict;
  /** `<class>: <detail>` for an inconclusive review, else the reviewer's reasons. */
  detail: string;
  /** The reviewer's findings block — the tail of its output, ending in the VERDICT line. */
  findings: string;
  /** 0 for the first review of a run, 1 for the review of its fix re-run. */
  fixRound: number;
  costUsd: number | null;
  treeBefore: string;
  treeAfter: string;
  startedAt: string;
  finishedAt: string | null;
  ackedAt: string | null;
}

/**
 * A finding is a line that STARTS with its severity — `P0` / `P1`, after an
 * optional list marker, bold or bracket — which is what the reviewer contract
 * asks for. A line that only mentions a severity ("no P0/P1 findings") is not one.
 */
const FINDING_LINE = /^\s*(?:[-*•]|\d+[.)])?\s*(?:\*\*|\[)?P[01]\b/;

/** The number of P0/P1 findings in a review's findings block. */
export function countFindings(findings: string): number {
  return findings.split('\n').filter((line) => FINDING_LINE.test(line)).length;
}

async function failure(res: Response, what: string): Promise<Error> {
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  return new Error(body.error ?? `${what}: ${String(res.status)}`);
}

/** GET /api/reviews?scope=plan&unacked=1 — the plan reviews still waiting on the operator, newest first. */
export async function fetchUnackedPlanReviews(): Promise<Review[]> {
  if (MOCK) return [];
  const path = '/api/reviews?scope=plan&unacked=1';
  const res = await fetch(path);
  if (!res.ok) throw await failure(res, `GET ${path}`);
  return (await res.json()) as Review[];
}

/** GET /api/epics/{taskId}/phases/{phaseId}/reviews — the phase's reviews, newest first. */
export async function fetchPhaseReviews(taskId: number, phaseId: number): Promise<Review[]> {
  if (MOCK) return [];
  const path = `/api/epics/${String(taskId)}/phases/${String(phaseId)}/reviews`;
  const res = await fetch(path);
  if (!res.ok) throw await failure(res, `GET ${path}`);
  return (await res.json()) as Review[];
}

/** POST /api/reviews/{id}/ack — acknowledge a review; it leaves the Inbox. Idempotent. */
export async function ackReview(id: number): Promise<Review | null> {
  if (MOCK) return null;
  const path = `/api/reviews/${String(id)}/ack`;
  const res = await fetch(path, { method: 'POST' });
  if (!res.ok) throw await failure(res, `POST ${path}`);
  return (await res.json()) as Review;
}
