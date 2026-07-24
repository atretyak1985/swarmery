// Header popover: Claude subscription-usage windows with a pace indicator
// (fusion phase 14), adapted from Fusion's Command-Center usage widget. Data is
// a telemetry ESTIMATE (see internal/api/usage.go — the OAuth path is out of
// policy), so every window is plainly badged "estimate" and never presented as
// exact. Same hairline visual language as the other header controls.
//
// Per window: a progress bar, a used-% / remaining toggle, a "resets in …"
// countdown, and a pace line (red when over pace). A Refresh button bypasses the
// daemon's 60s cache; while open, the popover auto-refreshes every 60s.

import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchUsage } from '../api';
import type { UsageResp, UsageWindow } from '../api/types';
import { fmtTokens } from '../lib/format';

const AUTO_REFRESH_MS = 60_000;

/** Forward duration to a reset instant → "2h 12m" / "13m" / "now". */
function fmtResetsIn(iso: string, now: number): string {
  const ms = new Date(iso).getTime() - now;
  if (!Number.isFinite(ms) || ms <= 0) return 'now';
  const totalMin = Math.floor(ms / 60_000);
  const h = Math.floor(totalMin / 60);
  const m = totalMin % 60;
  if (h >= 24) {
    const d = Math.floor(h / 24);
    return `${String(d)}d ${String(h % 24)}h`;
  }
  if (h > 0) return `${String(h)}h ${String(m)}m`;
  return `${String(m)}m`;
}

/** Pace fraction → human label. Positive = over (burning fast), negative = under. */
function paceLabel(pace: number): { text: string; over: boolean } {
  const pct = Math.round(Math.abs(pace) * 100);
  if (pct === 0) return { text: 'on pace', over: false };
  return { text: `${String(pct)}% ${pace > 0 ? 'over' : 'under'} pace`, over: pace > 0 };
}

function WindowRow({
  w,
  now,
  showRemaining,
}: {
  w: UsageWindow;
  now: number;
  showRemaining: boolean;
}): JSX.Element {
  const pct = Math.min(1, Math.max(0, w.usedPct));
  const over = w.usedPct >= 1;
  const pace = paceLabel(w.pace);
  const remaining = Math.max(0, w.limit - w.used);
  return (
    <div className="rounded-[9px] border border-line px-2.5 py-2">
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-mono text-[11.5px] text-ink">{w.label}</span>
        <span className="font-mono text-[10.5px] tabular-nums text-ink-dim">
          {showRemaining
            ? `${fmtTokens(remaining)} left`
            : `${(w.usedPct * 100).toFixed(0)}% used`}
        </span>
      </div>
      <div
        className="relative mt-1.5 h-[8px] overflow-hidden rounded-full bg-field"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(w.usedPct * 100)}
        aria-label={`${w.label} usage`}
      >
        <div
          className={`h-full rounded-full ${over ? 'bg-red/80' : 'bg-brand/75'}`}
          style={{ width: `${(pct * 100).toFixed(1)}%` }}
        />
      </div>
      <div className="mt-1 flex items-baseline justify-between gap-2 font-mono text-[10px]">
        <span className={pace.over ? 'text-red' : 'text-ink-faint'}>{pace.text}</span>
        <span className="text-ink-faint">resets in {fmtResetsIn(w.resetsAt, now)}</span>
      </div>
    </div>
  );
}

export function UsagePopover(): JSX.Element {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<UsageResp | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [showRemaining, setShowRemaining] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const ref = useRef<HTMLDivElement>(null);

  const load = useCallback((fresh: boolean): void => {
    setLoading(true);
    fetchUsage(fresh)
      .then((r) => {
        setData(r);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setLoading(false));
  }, []);

  // Fetch on first open; auto-refresh + tick the countdown while open.
  useEffect(() => {
    if (!open) return undefined;
    load(false);
    setNow(Date.now());
    const refresh = window.setInterval(() => load(true), AUTO_REFRESH_MS);
    const tick = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => {
      window.clearInterval(refresh);
      window.clearInterval(tick);
    };
  }, [open, load]);

  // Close on outside click.
  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e: MouseEvent): void => {
      if (ref.current !== null && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  const anyOver = data?.windows.some((w) => w.pace > 0) ?? false;

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label="Subscription usage"
        title="Subscription usage (estimate)"
        className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[11px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
      >
        <span className={anyOver ? 'text-red' : 'text-ink-2'}>◔</span> usage
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="subscription usage"
          className="absolute right-0 z-30 mt-2 w-[300px] rounded-xl border border-line bg-surface p-3"
        >
          <div className="flex items-center justify-between">
            <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
              usage · estimate
            </div>
            <button
              type="button"
              onClick={() => load(true)}
              disabled={loading}
              className="rounded-[6px] border border-line px-2 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2 disabled:opacity-50"
            >
              {loading ? '…' : 'refresh'}
            </button>
          </div>

          {error !== null && <div className="mt-2 text-[11px] text-red">{error}</div>}

          {data !== null && !data.configured && (
            <div className="mt-2 text-[11px] leading-snug text-ink-dim">
              set <code className="text-ink-2">SWARMERY_USAGE_LIMITS</code> (JSON window quotas) to
              track subscription usage. Until then no limits are known.
            </div>
          )}

          {data !== null && data.configured && (
            <>
              <div className="mt-2 flex justify-end">
                <button
                  type="button"
                  onClick={() => setShowRemaining((v) => !v)}
                  className="font-mono text-[10px] text-ink-faint underline decoration-dotted underline-offset-2 hover:text-ink-dim"
                >
                  show {showRemaining ? 'used %' : 'remaining'}
                </button>
              </div>
              <div className="mt-1.5 flex flex-col gap-2">
                {data.windows.map((w) => (
                  <WindowRow key={w.key} w={w} now={now} showRemaining={showRemaining} />
                ))}
              </div>
            </>
          )}

          <div className="mt-2.5 border-t border-line pt-2 font-mono text-[9.5px] leading-snug text-ink-faint">
            estimated from indexed token telemetry across all projects — not an exact subscription
            reading.
          </div>
        </div>
      )}
    </div>
  );
}
