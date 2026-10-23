// Typed client for the plan's decision → phase → evidence table (verify-not-trust
// phase 12, D19). Go: internal/api/evidence.go → wsingest.LoadDecisionTable.
// Every declared decision (`D-n`, the README's `## Decisions`) and spec criterion
// (`SC-n`, spec.md) with the phases that claim it, the Completion Report / plan
// review evidence for it, and its chip. MOCK mode serves an empty table, so the
// plan tab renders without it offline.

import { MOCK } from '../api';
import type { PlanReviewState } from './types';

export type { PlanReviewState };

/** A row's chip. */
export type DecisionRowStatus = 'evidence' | 'remaining' | 'not-implemented' | 'not-verified';

/** One decision_evidence row. */
export interface EvidenceRef {
  kind: 'report' | 'pr' | 'review';
  status: 'evidence' | 'not-implemented' | 'missing';
  excerpt: string;
  /** 0 for the daemon's plan review, which belongs to no phase. */
  phaseSeq: number;
  phaseName: string;
}

export interface DecisionRow {
  id: string;
  kind: 'decision' | 'criterion';
  text: string;
  status: DecisionRowStatus;
  /** The not-implemented reason, or the not-verified reason class; "" otherwise. */
  reason: string;
  /** The seqs of the phases whose `**Covers:**` / `**Depends on decisions:**` claim it. */
  claimedBy: number[];
  evidence: EvidenceRef[];
}

export interface DecisionTable {
  rows: DecisionRow[];
  planReview: PlanReviewState;
  counts: Partial<Record<DecisionRowStatus, number>>;
}

const EMPTY: DecisionTable = {
  rows: [],
  planReview: { status: '', verdict: '', reasonClass: '', at: '' },
  counts: {},
};

/** GET /api/epics/{taskId}/evidence. Throws on any non-2xx. */
export async function fetchDecisionTable(taskId: number): Promise<DecisionTable> {
  if (MOCK) return EMPTY;
  const path = `/api/epics/${String(taskId)}/evidence`;
  const res = await fetch(path);
  if (!res.ok) throw new Error(`GET ${path}: ${String(res.status)}`);
  const got = (await res.json()) as Partial<DecisionTable> | null;
  // An older daemon (or a test double) may answer without the table's fields.
  return {
    rows: Array.isArray(got?.rows) ? got.rows : [],
    planReview: got?.planReview ?? EMPTY.planReview,
    counts: got?.counts ?? {},
  };
}
