// "Spend · 7 days" (handoff §3): seven bars ending today, today in the accent, and
// the week's total at the head. The series is the overview stats' trailing one, so
// it needs no endpoint of its own. The design's "forecast $210" is left out: there
// is no dollar forecast to read (plan D-5).

import { useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { fetchStatsOverview } from '../../api';
import type { StatsSeriesPoint } from '../../api/types';
import { fmtCost, fmtDateTime, isoDay } from '../../lib/format';
import { SectionHead } from './panel';

const DAYS = 7;

const dayCost = (p: StatsSeriesPoint): number => p.cost_usd ?? 0;

export function SpendWeek({ slug }: { slug: string | null }): JSX.Element {
  const { t } = useLingui();
  const [series, setSeries] = useState<StatsSeriesPoint[] | null>(null);

  useEffect(() => {
    let live = true;
    fetchStatsOverview(isoDay(), slug ?? undefined)
      .then((s) => {
        if (live) setSeries(s.series.slice(-DAYS));
      })
      .catch(() => {
        if (live) setSeries(null);
      });
    return () => {
      live = false;
    };
  }, [slug]);

  const days = series ?? [];
  const total = series === null ? null : days.reduce((sum, p) => sum + dayCost(p), 0);
  const top = Math.max(...days.map(dayCost), 0);
  return (
    <section data-testid="spend-week" className="mt-4 min-w-0">
      <SectionHead label={t`Spend · 7 days`}>
        <span className="font-mono text-[10.5px] leading-[normal] text-ink-dim">{fmtCost(total)}</span>
      </SectionHead>
      {days.length > 0 && (
        <>
          <div className="mt-2 flex h-11 items-end gap-1">
            {days.map((p, i) => (
              <div
                key={p.day}
                data-tip={fmtCost(p.cost_usd)}
                style={{ height: `${String(top > 0 ? Math.round((dayCost(p) / top) * 100) : 0)}%` }}
                className={`min-h-px flex-1 rounded-t-[2px] ${i === days.length - 1 ? 'bg-brand' : 'bg-line-strong'}`}
              />
            ))}
          </div>
          <div className="mt-1 flex justify-between font-mono text-[9.5px] leading-[normal] text-ink-faint">
            {days.map((p, i) => (
              <span key={p.day}>
                {i === days.length - 1 ? t`today` : fmtDateTime(`${p.day}T12:00:00`, { weekday: 'short' })}
              </span>
            ))}
          </div>
        </>
      )}
    </section>
  );
}
