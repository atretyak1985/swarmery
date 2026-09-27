// Typed client for the complexity-routing views (Go DTOs in internal/route,
// handlers in internal/api/route.go). Like calibration, the report never carries
// a group under minSamples — only how many were hidden — and the UI re-checks
// the gate before drawing anything.

import { MOCK } from '../api';

export type RouteSurface = 'dispatch' | 'phaserun';

export interface RouteGroup {
  surface: RouteSurface;
  tier?: string;
  /** The model that RAN, normalised to its family ("sonnet"). */
  model?: string;
  /** The router's pick — divergent cells only. */
  pick?: string;
  n: number;
  failures: number;
  failRate: number;
  costN: number;
  meanCost: number | null;
  p90Cost: number | null;
  agree: number;
  /** Divergent cells: the same surface + tier when the pick itself ran. */
  pickRan?: RouteGroup;
}

export interface RouteSection {
  groups: RouteGroup[];
  hiddenGroups: number;
  hiddenRuns: number;
}

export interface RouteReport {
  surface: '' | RouteSurface;
  days: number;
  minSamples: number;
  rows: number;
  unsettled: number;
  byTier: RouteSection;
  byModel: RouteSection;
  divergent: RouteSection;
  hiddenGroups: number;
}

export interface RouteDecision {
  surface: RouteSurface;
  subject: string;
  mode: 'shadow' | 'active';
  score: number;
  tier: string;
  pickModel: string;
  pickEffort: string;
  pickPlaybook: string;
  reasons: string[];
  applied: boolean;
  usedModel: string;
  usedEffort: string;
  usedPlaybook: string;
  wonRung: string;
  outcome: string | null;
  verifyStatus: string | null;
  costUsd: number | null;
  createdAt: string;
}

const EMPTY: RouteSection = { groups: [], hiddenGroups: 0, hiddenRuns: 0 };

const MOCK_REPORT: RouteReport = {
  surface: '',
  days: 30,
  minSamples: 20,
  rows: 64,
  unsettled: 3,
  byTier: {
    groups: [
      {
        surface: 'dispatch',
        tier: 'S',
        n: 43,
        failures: 1,
        failRate: 0.0233,
        costN: 41,
        meanCost: 0.21,
        p90Cost: 0.44,
        agree: 0.12,
      },
    ],
    hiddenGroups: 4,
    hiddenRuns: 21,
  },
  byModel: {
    groups: [
      {
        surface: 'dispatch',
        model: 'sonnet',
        n: 52,
        failures: 2,
        failRate: 0.0385,
        costN: 50,
        meanCost: 0.26,
        p90Cost: 0.61,
        agree: 0.3,
      },
    ],
    hiddenGroups: 2,
    hiddenRuns: 12,
  },
  divergent: EMPTY,
  hiddenGroups: 6,
};

/** GET /api/route/report?surface=&days= */
export async function fetchRouteReport(surface: '' | RouteSurface, days: number): Promise<RouteReport> {
  if (MOCK) return MOCK_REPORT;
  const params = new URLSearchParams({ days: String(days) });
  if (surface !== '') params.set('surface', surface);
  const path = `/api/route/report?${params.toString()}`;
  const res = await fetch(path);
  if (!res.ok) throw new Error(`GET ${path}: ${String(res.status)}`);
  return (await res.json()) as RouteReport;
}

/** GET /api/route/decision?subject= — null when the subject has none (404). */
export async function fetchRouteDecision(subject: string): Promise<RouteDecision | null> {
  if (MOCK) return null;
  const path = `/api/route/decision?subject=${encodeURIComponent(subject)}`;
  const res = await fetch(path);
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`GET ${path}: ${String(res.status)}`);
  return (await res.json()) as RouteDecision;
}
