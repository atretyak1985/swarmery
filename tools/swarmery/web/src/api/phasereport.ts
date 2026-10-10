// Typed client for the phase-run baseline (Go internal/phasereport, handler
// internal/api/phasereport.go) and the phase reopen ledger (phase_reopen.go).
// The report is bookkeeping, not statistics: every row comes back, however small.

import { MOCK } from '../api';
import type { PhaseReopen, ReopenCaughtBy } from './types';

export interface PhaseRunsRow {
  /** Stable key — runs, runs_noop, noop_push_pr, reopens_operator, verifier_class:<c> … */
  key: string;
  label: string;
  n: number;
  /** Set on cost rows only. */
  costUsd?: number;
  /** Derived by a heuristic rather than read from a recorded fact. */
  estimated: boolean;
}

export interface PhaseRunsReport {
  /** Inclusive window edges, RFC3339 UTC. */
  from: string;
  to: string;
  rows: PhaseRunsRow[];
  /** Runs that ended in the window with no phase_actuals row — beside the rows, never in them. */
  fallbackRows: { n: number; byOutcome: Record<string, number> };
  notes: string[];
}

export const MOCK_PHASE_RUNS_REPORT: PhaseRunsReport = {
  from: '2026-09-10T00:00:00Z',
  to: '2026-10-09T23:59:59Z',
  rows: [
    { key: 'runs', label: 'phase runs', n: 91, estimated: false },
    { key: 'runs_completed', label: 'completed', n: 18, estimated: false },
    { key: 'runs_partial', label: 'partial', n: 23, estimated: false },
    { key: 'runs_noop', label: 'noop', n: 48, estimated: false },
    { key: 'runs_failed', label: 'failed', n: 2, estimated: false },
    { key: 'noop_push_pr', label: 'noop · waiting on push/PR', n: 16, estimated: true },
    { key: 'noop_manual', label: 'noop · waiting on a manual step', n: 6, estimated: true },
    { key: 'noop_repeat', label: 'noop repeat (same base as the noop before)', n: 15, estimated: false },
    { key: 'noop_cost', label: 'noop cost', n: 48, costUsd: 34.85, estimated: false },
    { key: 'reopens', label: 'reopened phases', n: 1, estimated: false },
    { key: 'reopens_operator', label: 'reopened · caught by operator', n: 1, estimated: false },
  ],
  fallbackRows: { n: 0, byOutcome: {} },
  notes: [],
};

/** GET /api/phaseruns/report?from=&to= — both YYYY-MM-DD, inclusive. */
export async function fetchPhaseRunsReport(from: string, to: string): Promise<PhaseRunsReport> {
  if (MOCK) return MOCK_PHASE_RUNS_REPORT;
  const params = new URLSearchParams({ from, to });
  const path = `/api/phaseruns/report?${params.toString()}`;
  const res = await fetch(path);
  if (!res.ok) throw new Error(await errorText(res, `GET ${path}`));
  return (await res.json()) as PhaseRunsReport;
}

export interface PhaseReopensResp {
  reopens: PhaseReopen[];
  /** The doc's ticked criteria, normalised exactly as the reopen matches them. */
  ticked: string[];
}

export interface PhaseReopenRequest {
  reason: string;
  fixUrl?: string;
  caughtBy: ReopenCaughtBy;
  criteria: string[];
}

function phasePath(taskId: number, phaseId: number, tail: string): string {
  return `/api/epics/${String(taskId)}/phases/${String(phaseId)}/${tail}`;
}

/** GET …/reopens — the phase's reopen history and its ticked criteria. */
export async function fetchPhaseReopens(taskId: number, phaseId: number): Promise<PhaseReopensResp> {
  if (MOCK) return { reopens: [], ticked: ['POST creates a line item', 'DELETE removes it'] };
  const path = phasePath(taskId, phaseId, 'reopens');
  const res = await fetch(path);
  if (!res.ok) throw new Error(await errorText(res, `GET ${path}`));
  return (await res.json()) as PhaseReopensResp;
}

/** POST …/reopen — record the reopen and untick the named criteria. */
export async function reopenPhase(
  taskId: number,
  phaseId: number,
  body: PhaseReopenRequest,
): Promise<{ reopen: PhaseReopen; unticked: number }> {
  if (MOCK) {
    return {
      reopen: { id: 1, fixUrl: '', ...body, createdAt: new Date().toISOString() },
      unticked: body.criteria.length,
    };
  }
  const res = await fetch(phasePath(taskId, phaseId, 'reopen'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await errorText(res, 'reopen failed'));
  return (await res.json()) as { reopen: PhaseReopen; unticked: number };
}

/** The server's `{error}` message when there is one, else `fallback: status`. */
async function errorText(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: unknown };
    if (typeof body.error === 'string' && body.error !== '') return body.error;
  } catch {
    // not JSON — fall through
  }
  return `${fallback}: ${String(res.status)}`;
}
