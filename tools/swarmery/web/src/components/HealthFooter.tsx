// The sidebar's bottom block (handoff §2): `$12.40 today · plan 41 %`, the plan
// bar, then `resets 16:00` with the daemon's health on the same line. With the top
// header gone on desktop this block carries what the header's usage chip and
// status did: clicking it opens the same Usage modal, and health keeps its version
// and its plugin-drift badge.
//
// Spend is GET /api/stats/today (one aggregate, polled like health). The plan
// numbers come from the shared usage poller and never from a fetch of their own.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { fetchStatsToday, MOCK } from '../api';
import type { UsageProvider, UsageWindow } from '../api/types';
import { useActiveUsageAccount } from '../lib/activeAccount';
import { fmtCost } from '../lib/format';
import { useHealth, versionLabel, versionTitle } from '../lib/health';
import { useUsage } from '../lib/usageData';
import { PluginDriftBadge } from './PluginDriftBadge';
import { fmtResetAt } from './usage/format';
import { UsageModal } from './usage/UsageModal';

const POLL_MS = 60_000;

/** The session window of the first healthy provider, same rule as the usage chip. */
function planWindow(providers: readonly UsageProvider[]): UsageWindow | null {
  const healthy = providers.find((p) => p.status === 'ok');
  if (healthy === undefined) return null;
  return healthy.windows.find((w) => w.key.startsWith('session')) ?? healthy.windows[0] ?? null;
}

/** Over-pace burns red at any percentage: the chip's rule, never the bar's width. */
function barTone(w: UsageWindow): string {
  return w.percentUsed > 90 || w.pace?.status === 'ahead' ? 'bg-red' : 'bg-brand';
}

/** The absolute clock when the window has one; the server's own text otherwise. */
function resetLabel(w: UsageWindow | null, plain: (at: string) => string): string {
  if (w === null) return '';
  if (w.resetAt !== undefined) {
    const at = fmtResetAt(w.resetAt, Date.now());
    if (at !== '') return plain(at);
  }
  return w.resetText ?? '';
}

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

export function HealthFooter(): JSX.Element {
  const { t } = useLingui();
  const { health, unreachable } = useHealth();
  const { providers, setModalOpen } = useUsage();
  const spend = fmtCost(useTodaySpend());
  const [open, setOpen] = useState(false);
  const active = useActiveUsageAccount(open);

  // Registering the open modal bumps the shared poller's cadence, as the chip does.
  useEffect(() => {
    if (!open) return undefined;
    setModalOpen(true);
    return () => setModalOpen(false);
  }, [open, setModalOpen]);

  const plan = planWindow(providers);
  const percent = plan === null ? null : Math.round(plan.percentUsed);
  const reset = resetLabel(plan, (at) => t`resets ${at}`);

  return (
    <div className="mt-3 border-t border-line px-2 pt-2 pb-0.5 font-mono text-[10.5px] leading-[normal] text-ink-dim">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t`Subscription usage`}
        data-tip={t`spend today · plan window`}
        className="block w-full cursor-pointer text-left transition-colors hover:text-ink"
      >
        <span className="flex justify-between">
          <span>{t`${spend} today`}</span>
          {percent !== null && <span>{t`plan ${percent} %`}</span>}
        </span>
        <span className="mt-1.5 block h-1 overflow-hidden rounded-[2px] bg-line">
          {plan !== null && (
            <span
              className={`block h-full ${barTone(plan)}`}
              style={{ width: `${String(Math.min(plan.percentUsed, 100))}%` }}
            />
          )}
        </span>
      </button>
      <div className="mt-1 flex flex-wrap items-center justify-between gap-x-2 text-ink-faint">
        <span>{reset}</span>
        <span className="flex min-w-0 items-center gap-1">
          {MOCK ? (
            <>
              <span aria-hidden="true" className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber" />
              <span className="truncate">{t`mock data`}</span>
            </>
          ) : (
            <>
              <span
                aria-hidden="true"
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${unreachable ? 'bg-red' : 'bg-green'}`}
              />
              {unreachable ? (
                <span className="truncate text-red">{t`daemon unreachable`}</span>
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
      {/* The modal's desktop popover anchors to its parent's right edge, built for
          a chip in the top-right corner. This fixed box hands it one that opens
          to the right of the rail, inside the viewport. */}
      <span className="fixed top-2 left-[204px] z-40 block w-[380px]">
        <UsageModal open={open} onClose={() => setOpen(false)} active={active} />
      </span>
    </div>
  );
}
