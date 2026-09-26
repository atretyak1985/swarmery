// Typed client for the forecast calibration view (learning-loop phase 16,
// Go DTOs in internal/calibration). The API never returns a group with fewer
// than minSamples non-post-hoc samples — only how many were hidden — and the
// UI re-checks the gate before drawing anything.

import { MOCK } from '../api';

export type CalibrationDim = 'agent' | 'model' | 'effort' | 'project';

export interface CalibrationBucket {
  lo: number;
  hi: number;
  n: number;
  meanConfidence: number;
  heldRate: number;
}

export interface CalibrationGroup {
  key: Partial<Record<CalibrationDim, string>>;
  samples: number;
  areaHitRate: number | null;
  bandAccuracy: number | null;
  outcomeAccuracy: number | null;
  meanSurprise: number;
  buckets: CalibrationBucket[];
}

export interface CalibrationReport {
  dims: CalibrationDim[];
  minSamples: number;
  groups: CalibrationGroup[];
  hiddenGroups: number;
  hiddenRuns: number;
}

const MOCK_REPORT: CalibrationReport = {
  dims: ['model', 'effort'],
  minSamples: 20,
  groups: [
    {
      key: { model: 'claude-opus-5-5', effort: 'high' },
      samples: 24,
      areaHitRate: 0.62,
      bandAccuracy: 0.48,
      outcomeAccuracy: 0.79,
      meanSurprise: 0.31,
      buckets: [
        { lo: 0.4, hi: 0.6, n: 6, meanConfidence: 0.52, heldRate: 0.5 },
        { lo: 0.6, hi: 0.8, n: 12, meanConfidence: 0.7, heldRate: 0.58 },
        { lo: 0.8, hi: 1, n: 6, meanConfidence: 0.85, heldRate: 0.67 },
      ],
    },
  ],
  hiddenGroups: 3,
  hiddenRuns: 17,
};

/** GET /api/calibration?by=… */
export async function fetchCalibration(dims: CalibrationDim[]): Promise<CalibrationReport> {
  if (MOCK) return MOCK_REPORT;
  const path = `/api/calibration?by=${encodeURIComponent(dims.join(','))}`;
  const res = await fetch(path);
  if (!res.ok) throw new Error(`GET ${path}: ${String(res.status)}`);
  return (await res.json()) as CalibrationReport;
}
