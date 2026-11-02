// The sidebar's bottom block (Swarmery v4): the limits button, spend today and one bar
// per subscription, then the daemon's health. With the top header gone on desktop it
// carries what the header's usage chip and status did: the button opens the limits
// panel, and health keeps its version and its plugin-drift badge. Collapsed, the rail
// keeps the bars alone.
//
// Spend is GET /api/stats/today (one aggregate, polled like health). The numbers come
// from the shared usage poller and never from a fetch of their own.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { fetchStatsToday, MOCK } from '../api';
import { useActiveUsageAccount } from '../lib/activeAccount';
import { fmtCost } from '../lib/format';
import { useHealth, versionLabel, versionTitle } from '../lib/health';
import { accountLimit, limitsHead } from '../lib/limits';
import { useUsage } from '../lib/usageData';
import { PluginDriftBadge } from './PluginDriftBadge';
import { LimitsPanel, limitBarClass, limitTextClass } from './usage/LimitsPanel';

const POLL_MS = 60_000;

const HEAD_TONE = { red: 'text-red', brand: 'text-brand', green: 'text-green', faint: 'text-ink-faint' } as const;

function useTodaySpend(): number | null {
  const [cost, setCost] = useState<number | null>(null);
  useEffect(() => {
    let disposed = false;
    const poll = (): void => {
      fetchStatsToday()
        .then((s) => {
          if (!disposed) setCost(s.cost_usd);
        })
        .catch(() => undefined); // the last known number stays; `—` until the first
    };
    poll();
    const timer = setInterval(poll, POLL_MS);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, []);
  return cost;
}

export function HealthFooter({ compact = false }: { compact?: boolean }): JSX.Element {
  const { t } = useLingui();
  const { health, unreachable } = useHealth();
  const { accounts, setModalOpen } = useUsage();
  const today = useTodaySpend();
  const spend = fmtCost(today);
  const [open, setOpen] = useState(false);
  const active = useActiveUsageAccount(open);
  const limits = useMemo(() => accounts.map(accountLimit), [accounts]);
  const head = limitsHead(limits);

  // Registering the open panel bumps the shared poller's cadence, as the chip does.
  useEffect(() => {
    if (!open) return undefined;
    setModalOpen(true);
    return () => setModalOpen(false);
  }, [open, setModalOpen]);

  const bar = (percent: number | null): JSX.Element => (
    <span aria-hidden="true" className={`block overflow-hidden rounded-[2px] bg-line ${compact ? 'h-[3px] w-[18px]' : 'h-1'}`}>
      {percent !== null && (
        <span className={`block h-full ${limitBarClass(percent)}`} style={{ width: `${String(Math.min(percent, 100))}%` }} />
      )}
    </span>
  );

  return (
    <div className="mt-2 border-t border-line pt-1 pb-0.5 font-mono text-[10.5px] leading-[normal] text-ink-dim">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={compact ? t`Subscription limits` : undefined}
        data-tip={t`limits`}
        className={`block w-full cursor-pointer rounded-lg text-left transition-colors hover:bg-surface2 ${
          open ? 'bg-surface2' : ''
        } ${compact ? 'py-2.5' : 'px-2 pt-2.5 pb-2'}`}
      >
        {compact ? (
          <span className="flex flex-col items-center gap-[3px]">
            {limits.map((limit) => (
              <span key={limit.name}>{bar(limit.session === null ? null : Math.round(limit.session.percentUsed))}</span>
            ))}
          </span>
        ) : (
          <>
            <span className="flex items-baseline justify-between gap-2">
              <span className="shrink-0 whitespace-nowrap text-ink-2">{t`${spend} today`}</span>
              <span className={`min-w-0 truncate ${HEAD_TONE[head.tone]}`} title={head.text}>
                {head.text}
              </span>
            </span>
            <span className="mt-1.5 flex flex-col gap-1">
              {limits.map((limit) => {
                const percent = limit.session === null ? null : Math.round(limit.session.percentUsed);
                return (
                  <span key={limit.name} className="grid grid-cols-[44px_minmax(0,1fr)_34px] items-center gap-2 text-[10px]">
                    <span className={`truncate ${limit.state === 'limit' ? 'text-brand' : 'text-ink-3'}`}>{limit.name}</span>
                    {bar(percent)}
                    <span className={`text-right ${percent === null ? 'text-ink-faint' : limitTextClass(percent)}`}>
                      {percent === null ? '—' : `${String(percent)}%`}
                    </span>
                  </span>
                );
              })}
            </span>
          </>
        )}
      </button>
      {/* One line, whatever the width: a wrap here lifts the whole bottom cluster.
          Health gives way, its text in the tooltip. */}
      {!compact && (
        <div className="mt-1 flex items-center justify-end gap-2 px-2 text-ink-faint">
        <span className="flex min-w-0 items-center justify-end gap-1">
          {MOCK ? (
            <>
              <span aria-hidden="true" className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber" />
              <span className="truncate" data-tip={t`mock data`}>
                {t`mock`}
              </span>
            </>
          ) : (
            <>
              <span
                aria-hidden="true"
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${unreachable ? 'bg-red' : 'bg-green'}`}
              />
              {unreachable ? (
                <span className="truncate text-red" data-tip={t`daemon unreachable`}>
                  {t`daemon unreachable`}
                </span>
              ) : (
                <span className="truncate" data-tip={t`daemon healthy`} title={health === null ? undefined : versionTitle(health)}>
                  {health === null ? t`daemon healthy` : versionLabel(health)}
                </span>
              )}
              <PluginDriftBadge health={health} />
            </>
          )}
        </span>
      </div>
      )}
      {open && <LimitsPanel onClose={() => setOpen(false)} activeAccount={active?.account ?? null} spentToday={today} />}
    </div>
  );
}
