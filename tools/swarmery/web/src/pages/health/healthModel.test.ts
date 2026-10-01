// @vitest-environment jsdom
//
// Health model (Canvas v3 phase 5). Pure functions over the retro payloads:
// the range presets, the strip's "this window" cell (with the delta against
// the previous window), the one sentence (with its honest fallback) and the
// agent ranking.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/health
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { describe, expect, it } from 'vitest';
import type { RetroAgentRow, RetroAgentsResp, RetroFrictionResp } from '../../api/types';
import {
  FALLBACK_SENTENCE,
  HEALTH_PRESETS,
  autoModeRow,
  countRecs,
  daysFromParam,
  frictionCount,
  oneSentence,
  rangeFor,
  topAgents,
  windowCell,
  windowCellText,
} from './healthModel';

function row(
  agent: string,
  runs: number,
  rate: number,
  prev: { runs: number; rate: number },
  cost = 1,
): RetroAgentRow {
  return {
    agent,
    runs,
    sessions: runs,
    cost_usd: cost,
    tokens_out: 0,
    errors: Math.round(runs * rate),
    error_rate: rate,
    avg_ms: null,
    p95_ms: null,
    success_rate: null,
    re_dispatch_rate: null,
    eval: null,
    prev: { runs: prev.runs, errors: 0, error_rate: prev.rate, cost_usd: 0 },
  };
}

function resp(agents: RetroAgentRow[], mainCost = 10): RetroAgentsResp {
  return {
    from: '2026-09-15',
    to: '2026-09-28',
    approx: false,
    main: { cost_usd: mainCost, tokens_out: 0, errors: 0 },
    agents,
  };
}

describe('rangeFor', () => {
  it('covers 7/14/30/90 local days ending today, inclusive', () => {
    expect(HEALTH_PRESETS).toEqual([7, 14, 30, 90]);
    expect(rangeFor(7, '2026-09-28')).toEqual({ from: '2026-09-22', to: '2026-09-28' });
    expect(rangeFor(14, '2026-09-28')).toEqual({ from: '2026-09-15', to: '2026-09-28' });
    expect(rangeFor(30, '2026-09-28')).toEqual({ from: '2026-08-30', to: '2026-09-28' });
    expect(rangeFor(90, '2026-09-28')).toEqual({ from: '2026-07-01', to: '2026-09-28' });
  });

  it('reads ?days= as a preset, defaulting to 14', () => {
    expect(daysFromParam('30')).toBe(30);
    expect(daysFromParam(null)).toBe(14);
    expect(daysFromParam('13')).toBe(14);
    expect(daysFromParam('abc')).toBe(14);
  });
});

describe('windowCell', () => {
  it('sums runs, weights failures by runs and compares with the previous window', () => {
    const a = resp([
      row('impl', 100, 0.1, { runs: 50, rate: 0.3 }, 20),
      row('review', 42, 0.14, { runs: 50, rate: 0.1 }, 18),
    ]);
    const c = windowCell(a);
    expect(c.runs).toBe(142);
    expect(c.failedPct).toBeCloseTo(((10 + 42 * 0.14) / 142) * 100, 5);
    expect(c.prevFailedPct).toBeCloseTo(20, 5);
    expect(c.costUsd).toBe(48);
    const t = windowCellText(c);
    expect(t.value).toBe('142 runs · 11 % failed');
    expect(t.delta).toBe('↓ from 20 %');
    expect(t.better).toBe(true);
  });

  it('has no previous rate when the previous window had no runs', () => {
    const c = windowCell(resp([row('impl', 12, 0.25, { runs: 0, rate: 0 })]));
    expect(c.prevFailedPct).toBeNull();
    expect(windowCellText(c).delta).toBeNull();
  });
});

describe('oneSentence', () => {
  it('falls back honestly under 10 runs', () => {
    expect(oneSentence(resp([row('impl', 9, 0.5, { runs: 40, rate: 0.1 })]))).toBe(FALLBACK_SENTENCE);
    expect(oneSentence(resp([]))).toBe(FALLBACK_SENTENCE);
  });

  it('names the halving and the agent that carried it', () => {
    const a = resp([
      row('implementation-agent', 60, 0.1, { runs: 60, rate: 0.5 }),
      row('reviewer', 40, 0.05, { runs: 40, rate: 0.05 }),
    ]);
    expect(oneSentence(a)).toBe(
      'The fleet is failing half as often as the 14 days before; almost all of the change is implementation-agent.',
    );
  });

  it('says so when nothing moved', () => {
    const a = resp([row('impl', 50, 0.1, { runs: 50, rate: 0.1 })]);
    expect(oneSentence(a)).toMatch(/^The fleet is failing about as often as the 14 days before/);
  });

  it('without a previous window names the worst agent', () => {
    const a = resp([
      row('impl', 20, 0.5, { runs: 0, rate: 0 }),
      row('review', 20, 0.05, { runs: 0, rate: 0 }),
    ]);
    expect(oneSentence(a)).toBe('The fleet failed 28 % of its runs in this window; most failures come from impl.');
  });
});

describe('topAgents', () => {
  it('ranks by failed runs (error_rate × runs), then by runs', () => {
    const a = resp([
      row('a', 100, 0.05, { runs: 0, rate: 0 }), // 5 failed
      row('b', 10, 0.9, { runs: 0, rate: 0 }), // 9 failed
      row('c', 50, 0, { runs: 0, rate: 0 }),
      row('d', 60, 0, { runs: 0, rate: 0 }),
    ]);
    expect(topAgents(a, 3).map((r) => r.agent)).toEqual(['b', 'a', 'd']);
  });
});

describe('counts', () => {
  it('counts removable friction and filters recommendations by status client-side', () => {
    const f: RetroFrictionResp = {
      denied_tools: [
        { tool: 'Bash', denied: 7, calls: 12, has_rule: false },
        { tool: 'Read', denied: 2, calls: 9, has_rule: true },
      ],
      error_groups: [
        { key: 'k1', example: 'x', count: 4, last_ts: '', sessions: [] },
        { key: 'k2', example: 'y', count: 1, last_ts: '', sessions: [] },
      ],
      approvals: { resolved: 0, avg_resolve_sec: null, wait_total_min: 0, pending: 0 },
      approx: false,
    };
    expect(frictionCount(f)).toBe(2);
    const recs = [{ status: 'proposed' }, { status: 'verified' }, { status: 'accepted' }] as Parameters<
      typeof countRecs
    >[0];
    expect(countRecs(recs, ['proposed'])).toBe(1);
    expect(countRecs(recs, ['accepted', 'adopted'])).toBe(1);
  });
});

describe('autoModeRow', () => {
  it('reads quiet when no check went without a verdict', () => {
    expect(autoModeRow({ noVerdictLastHour: 0, sessionsLastHour: 0, lastAt: null, alerting: false }, null)).toEqual({
      tone: 'quiet',
      text: 'answering — no check went without a verdict in the last hour',
    });
  });

  it('counts the last hour, its sessions and the last one seen', () => {
    const one = { noVerdictLastHour: 1, sessionsLastHour: 1, lastAt: '2026-09-28T11:30:47.643Z', alerting: false };
    expect(autoModeRow(one, '12 min ago')).toEqual({
      tone: 'seen',
      text: '1 check got no verdict in the last hour · in 1 session · last 12 min ago',
    });
  });

  it('is alerting while the alert is open, whatever the count', () => {
    const burst = { noVerdictLastHour: 9, sessionsLastHour: 2, lastAt: '2026-09-28T06:38:57.594Z', alerting: true };
    expect(autoModeRow(burst, '1 min ago')).toEqual({
      tone: 'alerting',
      text: '9 checks got no verdict in the last hour · in 2 sessions · last 1 min ago',
    });
    expect(autoModeRow({ ...burst, noVerdictLastHour: 0, sessionsLastHour: 0, lastAt: null }, null).tone).toBe(
      'alerting',
    );
  });
});
