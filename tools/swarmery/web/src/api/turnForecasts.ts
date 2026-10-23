// Typed client for the swarmery link turn-forecast calibration
// (mods-swarmery-link phase 10; Go DTO turnForecastsDTO in
// internal/api/link_forecast.go, docs/mods-link-protocol.md "Forecast").
// Advisory data only: the daemon applies no threshold and raises no alert.

import { MOCK } from '../api';
import type { TurnForecastsResp } from './types';
import { apiFetch } from '../lib/dashboardSession';

/** The windows the panel offers; the API clamps any value to [1, 90]. */
export const TURN_FORECAST_DAYS = [14, 30, 90] as const;
export type TurnForecastDays = (typeof TURN_FORECAST_DAYS)[number];

const MOCK_RESP: TurnForecastsResp = {
  days: 14,
  n: 13,
  tooFew: true,
  bins: [
    { lo: 0, hi: 0.2, n: 2, meanConfidence: 0.115, hitRate: 0 },
    { lo: 0.2, hi: 0.4, n: 0, meanConfidence: null, hitRate: null },
    { lo: 0.4, hi: 0.6, n: 4, meanConfidence: 0.52, hitRate: 0.5 },
    { lo: 0.6, hi: 0.8, n: 5, meanConfidence: 0.71, hitRate: 0.6 },
    { lo: 0.8, hi: 1, n: 2, meanConfidence: 0.9, hitRate: 0.5 },
  ],
  top: [
    {
      id: 12,
      sessionId: 1,
      sessionUuid: 's-a',
      projectId: 1,
      runId: 'r-1',
      turnSeq: 12,
      startedAt: '2026-10-13T09:12:00Z',
      surprise: 0.7795,
      top: 'tests_miss',
      model: 'haiku',
    },
    {
      id: 7,
      sessionId: 1,
      sessionUuid: 's-a',
      projectId: 1,
      runId: 'r-1',
      turnSeq: 7,
      startedAt: '2026-10-13T08:40:00Z',
      surprise: 0.512,
      top: 'unexpected_files',
      model: 'haiku',
    },
    {
      id: 3,
      sessionId: 2,
      sessionUuid: 's-b',
      projectId: 1,
      runId: 'r-2',
      turnSeq: 3,
      startedAt: '2026-10-12T16:05:00Z',
      surprise: 0.304,
      top: 'steps_miss',
      model: 'haiku',
    },
  ],
  avgForecastTokens: 115.38,
};

/** GET /api/insights/turn-forecasts?projectId=&days= — projectId must be the
 *  NUMERIC id (useProjectScope), omitted for the whole fleet. */
export async function fetchTurnForecasts(
  days: number,
  projectId: string | null,
): Promise<TurnForecastsResp> {
  if (MOCK) return { ...MOCK_RESP, days };
  const qs = new URLSearchParams({ days: String(days) });
  if (projectId !== null) qs.set('projectId', projectId);
  const path = `/api/insights/turn-forecasts?${qs.toString()}`;
  const res = await apiFetch(path);
  if (!res.ok) throw new Error(`GET ${path}: ${String(res.status)}`);
  return (await res.json()) as TurnForecastsResp;
}
