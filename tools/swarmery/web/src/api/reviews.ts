// Typed client for code reviews (Go DTO `reviewDTO` in internal/api/reviews.go,
// rows of phase_reviews, migration 0106). Two producers write them: the review
// stage of a `**Review:** on` phase run (scope `phase`) and the plan branch
// review that reads every phase's run branch once the plan is complete (scope
// `plan`). The Inbox lists the unacknowledged plan reviews; a phase's Runs tab
// shows its latest phase review. MOCK mode serves nothing, so both surfaces
// render their empty state offline.

import { MOCK } from '../api';
import type { GuardSummary } from './types';
import { apiFetch } from '../lib/dashboardSession';

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
  /** The run's guard check (migration 0124); `{}` before it, absent from an older daemon. */
  guard?: GuardSummary;
}

/**
 * A finding is a line that STARTS with its severity — `P0` / `P1`, after an
 * optional list marker, bold or bracket — which is what the reviewer contract
 * asks for. A line that only mentions a severity ("no P0/P1 findings") is not one.
 */
const FINDING_LINE = /^\s*(?:[-*•]|\d+[.)])?\s*(?:\*\*|\[)?P[01]\b/;

/**
 * The entries of the `findings:` list of a `kind: review` yaml block (the
 * machine copy the review stage stores since migration 0123), or null when the
 * text carries no such block. An entry is a `- ` line at the list's own indent.
 */
function countBlockFindings(text: string): number | null {
  const block = /```ya?ml\n((?:(?!```)[\s\S])*?^kind:\s*review\b[\s\S]*?)```/m.exec(text);
  if (block === null) return null;
  let inList = false;
  let indent: number | null = null;
  let n = 0;
  for (const line of (block[1] ?? '').split('\n')) {
    if (!inList) {
      if (/^findings:\s*(\[\s*\])?\s*(#.*)?$/.test(line)) {
        if (line.includes('[')) return 0;
        inList = true;
      }
      continue;
    }
    if (/^\S/.test(line)) break; // the next top-level key
    const item = /^(\s*)- /.exec(line);
    if (item === null) continue;
    const at = item[1]?.length ?? 0;
    indent ??= at;
    if (at === indent) n++;
  }
  return n;
}

/** The number of findings in a review's findings text: the entries of its
 *  `kind: review` block when it carries one, else its P0/P1 lines. */
export function countFindings(findings: string): number {
  return countBlockFindings(findings) ?? findings.split('\n').filter((line) => FINDING_LINE.test(line)).length;
}

async function failure(res: Response, what: string): Promise<Error> {
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  return new Error(body.error ?? `${what}: ${String(res.status)}`);
}

/** GET /api/reviews?scope=plan&unacked=1 — the plan reviews still waiting on the operator, newest first. */
export async function fetchUnackedPlanReviews(): Promise<Review[]> {
  if (MOCK) return [];
  const path = '/api/reviews?scope=plan&unacked=1';
  const res = await apiFetch(path);
  if (!res.ok) throw await failure(res, `GET ${path}`);
  return (await res.json()) as Review[];
}

/** GET /api/epics/{taskId}/phases/{phaseId}/reviews — the phase's reviews, newest first. */
export async function fetchPhaseReviews(taskId: number, phaseId: number): Promise<Review[]> {
  if (MOCK) return [];
  const path = `/api/epics/${String(taskId)}/phases/${String(phaseId)}/reviews`;
  const res = await apiFetch(path);
  if (!res.ok) throw await failure(res, `GET ${path}`);
  return (await res.json()) as Review[];
}

/** POST /api/reviews/{id}/ack — acknowledge a review; it leaves the Inbox. Idempotent. */
export async function ackReview(id: number): Promise<Review | null> {
  if (MOCK) return null;
  const path = `/api/reviews/${String(id)}/ack`;
  const res = await apiFetch(path, { method: 'POST' });
  if (!res.ok) throw await failure(res, `POST ${path}`);
  return (await res.json()) as Review;
}
