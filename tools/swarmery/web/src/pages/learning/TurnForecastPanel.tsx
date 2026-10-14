// Turn forecasts (mods-swarmery-link phase 10): the swarmery link mod guesses,
// at the start of every turn, which files it will touch, whether tests pass and
// how many steps it takes; the daemon scores the guess against what happened
// (internal/turnforecast). This panel reads GET /api/insights/turn-forecasts:
// a calibration curve (declared confidence × hit rate per bin, against the
// perfectly-calibrated diagonal) and the window's ten most surprising turns.
//
// Advisory only: no thresholds, no alerts. Fewer than 30 scored turns shows a
// notice and still renders what exists. A turn_surprise WS frame (or the
// socket's reconnect / reconcile tick) is only ever a refetch signal.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  CartesianGrid,
  Line,
  LineChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { fetchTurnForecasts, TURN_FORECAST_DAYS, type TurnForecastDays } from '../../api/turnForecasts';
import type { TurnForecastBin, TurnForecastsResp, TurnSurprise } from '../../api/types';
import { fmtDateTime, fmtNum } from '../../lib/format';
import { useProjectScope } from '../../lib/scope';
import { useTheme } from '../../lib/theme';
import { useLiveUpdates } from '../../lib/ws';

/** One plotted calibration point — a bin that holds at least one scored turn. */
export interface CalibrationPoint {
  /** Mean declared confidence of the bin's turns (0..1). */
  x: number;
  /** Share of the bin's turns whose forecast held (0..1). */
  y: number;
  n: number;
  lo: number;
  hi: number;
}

/** The points handed to the chart. An empty bin (n = 0, or null mean / hit
 *  rate) is ABSENT — never plotted as 0, which would read as "always wrong". */
export function calibrationPoints(bins: readonly TurnForecastBin[]): CalibrationPoint[] {
  return bins.flatMap((b) =>
    b.n > 0 && b.meanConfidence !== null && b.hitRate !== null
      ? [{ x: b.meanConfidence, y: b.hitRate, n: b.n, lo: b.lo, hi: b.hi }]
      : [],
  );
}

const COMPONENT_LABELS: Record<string, MessageDescriptor> = {
  unexpected_files: msg`touched files it did not forecast`,
  missed_files: msg`forecast files it never touched`,
  tests_miss: msg`tests outcome missed`,
  steps_miss: msg`step count off`,
  overconfidence: msg`confident and wrong`,
};

function pct(v: number | null): string {
  return v === null ? '—' : `${String(Math.round(v * 100))}%`;
}

/** Chart colours resolved from the active theme: one series hue for the
 *  curve, neutral ink for the reference diagonal, grid and ticks. Recharts SVG
 *  props cannot take CSS vars, so the values are read when the theme flips. */
function useCurveColors(): { line: string; grid: string; axis: string; tick: string; surface: string } {
  const { theme, palette } = useTheme();
  return useMemo(() => {
    const cs = getComputedStyle(document.documentElement);
    const v = (name: string): string => cs.getPropertyValue(name).trim();
    return {
      line: v('--color-series-1'),
      grid: v('--color-chart-grid'),
      axis: v('--color-chart-axis'),
      tick: v('--color-chart-tick'),
      surface: v('--color-surface'),
    };
    // theme + palette are the triggers: the vars change value when either flips.
  }, [theme, palette]);
}

function PointTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: { payload?: CalibrationPoint }[];
}): JSX.Element | null {
  const p = payload?.[0]?.payload;
  if (active !== true || p === undefined) return null;
  const range = `${p.lo.toFixed(1)}–${p.hi.toFixed(1)}`;
  const said = pct(p.x);
  const held = pct(p.y);
  return (
    <div className="rounded border border-line bg-surface px-2 py-1 font-mono text-[10.5px] text-ink">
      <div className="text-ink-dim">
        <Trans>confidence {range}</Trans>
      </div>
      <div>
        <Trans>
          said {said}, held {held}
        </Trans>
      </div>
      <div className="text-ink-faint">
        {plural(p.n, { one: '# turn', few: '# turns', many: '# turns', other: '# turns' })}
      </div>
    </div>
  );
}

function CalibrationCurve({ bins }: { bins: readonly TurnForecastBin[] }): JSX.Element {
  const { t } = useLingui();
  const colors = useCurveColors();
  const points = calibrationPoints(bins);
  const ticks = [0, 0.2, 0.4, 0.6, 0.8, 1];
  return (
    <figure className="m-0">
      <div className="h-[240px] w-full max-w-[420px]" aria-hidden="true">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={points} margin={{ top: 8, right: 12, bottom: 18, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke={colors.grid} />
            <XAxis
              type="number"
              dataKey="x"
              domain={[0, 1]}
              ticks={ticks}
              tick={{ fontSize: 10, fill: colors.tick }}
              tickFormatter={(v: number) => pct(v)}
              tickLine={false}
              axisLine={{ stroke: colors.axis }}
              label={{ value: t`declared confidence`, position: 'insideBottom', offset: -10, fontSize: 10, fill: colors.tick }}
            />
            <YAxis
              type="number"
              domain={[0, 1]}
              ticks={ticks}
              tick={{ fontSize: 10, fill: colors.tick }}
              tickFormatter={(v: number) => pct(v)}
              tickLine={false}
              axisLine={false}
              width={40}
            />
            <ReferenceLine
              segment={[
                { x: 0, y: 0 },
                { x: 1, y: 1 },
              ]}
              stroke={colors.tick}
              strokeDasharray="4 4"
              strokeWidth={1}
              ifOverflow="extendDomain"
            />
            <Tooltip content={<PointTooltip />} cursor={false} />
            <Line
              type="linear"
              dataKey="y"
              stroke={colors.line}
              strokeWidth={2}
              dot={{ r: 4, fill: colors.line, stroke: colors.surface, strokeWidth: 2 }}
              activeDot={{ r: 6, fill: colors.line, stroke: colors.surface, strokeWidth: 2 }}
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
      <figcaption className="mt-1 flex flex-wrap items-center gap-3 font-mono text-[10px] text-ink-dim">
        <span className="inline-flex items-center gap-1">
          <span className="inline-block h-[2px] w-4 bg-series-1" />
          <Trans>hit rate per confidence bin</Trans>
        </span>
        <span className="inline-flex items-center gap-1">
          <span className="inline-block w-4 border-t border-dashed border-ink-faint" />
          <Trans>perfectly calibrated</Trans>
        </span>
      </figcaption>
    </figure>
  );
}

/** The chart's text alternative: every bin, empty ones included, as a table. */
function BinsTable({ bins }: { bins: readonly TurnForecastBin[] }): JSX.Element {
  return (
    <table className="mt-3 w-full max-w-[420px] border-collapse font-mono text-[10.5px]">
      <caption className="pb-1 text-left text-[11px] text-ink-dim">
        <Trans>Calibration by confidence bin</Trans>
      </caption>
      <thead>
        <tr className="border-b border-line text-left text-ink-faint">
          <th scope="col" className="py-1 pr-3 font-normal">
            <Trans>confidence</Trans>
          </th>
          <th scope="col" className="py-1 pr-3 text-right font-normal">
            <Trans>turns</Trans>
          </th>
          <th scope="col" className="py-1 pr-3 text-right font-normal">
            <Trans>said</Trans>
          </th>
          <th scope="col" className="py-1 text-right font-normal">
            <Trans>held</Trans>
          </th>
        </tr>
      </thead>
      <tbody>
        {bins.map((b) => (
          <tr key={b.lo} className="border-b border-line/50 text-ink-dim">
            <th scope="row" className="py-1 pr-3 text-left font-normal">
              {`${b.lo.toFixed(1)}–${b.hi.toFixed(1)}`}
            </th>
            <td className="py-1 pr-3 text-right">{b.n}</td>
            <td className="py-1 pr-3 text-right">{pct(b.meanConfidence)}</td>
            <td className="py-1 text-right">{pct(b.hitRate)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function SurprisesTable({ rows, slug }: { rows: readonly TurnSurprise[]; slug: string | null }): JSX.Element {
  const { i18n } = useLingui();
  const base = slug === null ? '' : `/p/${encodeURIComponent(slug)}`;
  return (
    <table className="mt-2 w-full border-collapse text-[12px]">
      <caption className="pb-1 text-left text-[11px] text-ink-dim">
        <Trans>Most surprising turns</Trans>
      </caption>
      <thead>
        <tr className="border-b border-line text-left font-mono text-[10px] text-ink-faint">
          <th scope="col" className="py-1 pr-3 text-right font-normal">
            <Trans>surprise</Trans>
          </th>
          <th scope="col" className="py-1 pr-3 font-normal">
            <Trans>biggest miss</Trans>
          </th>
          <th scope="col" className="py-1 pr-3 font-normal">
            <Trans>model</Trans>
          </th>
          <th scope="col" className="py-1 pr-3 font-normal">
            <Trans>started</Trans>
          </th>
          <th scope="col" className="py-1 font-normal">
            <Trans>turn</Trans>
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const label = COMPONENT_LABELS[r.top];
          const session = String(r.sessionId);
          const seq = String(r.turnSeq);
          return (
            <tr key={r.id} className="border-b border-line/50">
              <td className="py-1 pr-3 text-right font-mono text-ink">{r.surprise.toFixed(2)}</td>
              <td className="py-1 pr-3 text-ink-dim">{label === undefined ? r.top : i18n._(label)}</td>
              <td className="py-1 pr-3 font-mono text-[11px] text-ink-dim">{r.model}</td>
              <td className="py-1 pr-3 font-mono text-[11px] text-ink-dim">{fmtDateTime(r.startedAt)}</td>
              <td className="py-1">
                <Link to={`${base}/sessions/${session}`} className="font-mono text-[11px] text-brand hover:underline">
                  <Trans>
                    session {session} · turn {seq}
                  </Trans>
                </Link>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function TurnForecastPanel(): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug: string }>();
  const projectSlug = slug ?? null;
  const { id: projectId, pending } = useProjectScope(projectSlug);
  const [days, setDays] = useState<TurnForecastDays>(14);
  const [data, setData] = useState<TurnForecastsResp | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback((): void => {
    // A scoped page waits for the project list: an unscoped answer arriving
    // last would otherwise paint the whole fleet on a project page.
    if (pending) return;
    fetchTurnForecasts(days, projectId)
      .then((r) => {
        setData(r);
        setErr(null);
      })
      .catch((e: unknown) => setErr(String(e)));
  }, [days, projectId, pending]);

  useEffect(load, [load]);

  // Both callbacks only refetch: onReconnect also fires on the socket's 60 s
  // reconcile tick, and a turn_surprise payload is one row, not the aggregate.
  useLiveUpdates(
    (m) => {
      if (m.type === 'turn_surprise') load();
    },
    load,
  );

  const n = data?.n ?? 0;
  const tokens = data?.avgForecastTokens ?? null;
  const tokensText = tokens === null ? '—' : fmtNum(Math.round(tokens));

  return (
    <section className="mt-2 max-w-3xl" aria-labelledby="turn-forecasts-heading">
      <h2 id="turn-forecasts-heading" className="text-sm font-normal text-ink">
        <Trans>Turn forecasts</Trans>
      </h2>
      <p className="mt-1 text-[12px] text-ink-dim">
        <Trans>
          At the start of each turn the swarmery link mod forecasts the files it will touch, whether
          tests pass and how many steps it takes; the daemon scores that against what happened. A
          forecast held when its surprise stayed under 0.25. Advisory only — nothing here gates or
          alerts.
        </Trans>
      </p>
      <div className="mt-2 flex flex-wrap items-center gap-3 font-mono text-[10.5px] text-ink-dim">
        <fieldset className="inline-flex gap-1" aria-label={t`window`}>
          {TURN_FORECAST_DAYS.map((d) => (
            <button
              key={d}
              type="button"
              aria-pressed={days === d}
              onClick={() => setDays(d)}
              className={`rounded border px-1.5 py-px font-mono text-[10px] ${
                days === d ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
              }`}
            >
              {plural(d, { one: '# day', few: '# days', many: '# days', other: '# days' })}
            </button>
          ))}
        </fieldset>
        {data !== null && (
          <>
            <span>{plural(n, { one: '# scored turn', few: '# scored turns', many: '# scored turns', other: '# scored turns' })}</span>
            <span>
              <Trans>avg forecast cost {tokensText} tokens</Trans>
            </span>
          </>
        )}
      </div>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      {data !== null &&
        (n === 0 ? (
          <div className="mt-3 rounded border border-line p-3 text-[12px] text-ink-dim">
            <p className="m-0 text-ink">
              <Trans>No scored turns in this window.</Trans>
            </p>
            <p className="mt-1 mb-0">
              <Trans>
                Turn forecasts come from the swarmery link mod. Turn on its <code>forecast</code> option and
                every turn that starts from your own prompt gets one cheap forecast; it is scored once the
                turn completes.
              </Trans>
            </p>
          </div>
        ) : (
          <>
            {data.tooFew && (
              <p role="status" className="mt-3 mb-0 rounded border border-line px-2 py-1 text-[12px] text-ink-dim">
                <Trans>n is too small to read (fewer than 30 turns) — what exists is shown as is.</Trans>
              </p>
            )}
            <div className="mt-3">
              <CalibrationCurve bins={data.bins} />
              <BinsTable bins={data.bins} />
            </div>
            {data.top.length > 0 && <SurprisesTable rows={data.top} slug={projectSlug} />}
          </>
        ))}
    </section>
  );
}
