// One quota window inside a provider card: header (label · percentage · eye
// toggle), the progress bar with an elapsed-time marker, a footer (inverse
// percentage · "resets in …" · absolute reset chip) and the pace line.
//
// Presentational only — every number arrives computed from the daemon
// (internal/usage); nothing here re-derives pace or percentages.

import { useLingui } from '@lingui/react/macro';
import type { UsagePace, UsageWindow } from '../../api/types';
import { fmtResetAt, fmtResetsIn } from './format';

/**
 * Bar fill colour keyed to ACTUAL usage, never to the display mode: in
 * Remaining mode the bar shrinks as the quota burns, but a nearly-exhausted
 * window must still read red rather than "mostly empty, therefore green".
 */
function fillClass(percentUsed: number): string {
  if (percentUsed > 90) return 'bg-red/80';
  if (percentUsed > 70) return 'bg-amber/80';
  return 'bg-green/80';
}

/**
 * Pace vocabulary — READ THIS BEFORE CHANGING THE COLOURS. It reads backwards
 * at first pass and is kept verbatim from the daemon's contract (UsagePace):
 *
 *   'ahead'    = burning quota FASTER than the window elapses → over-pace → RED
 *   'behind'   = burning SLOWER than the window elapses → under-pace → GREEN
 *   'on-track' = inside the ±5-point dead band → GREEN
 *
 * So `ahead` is the warning state and `behind` is the good one. Do not "fix"
 * this by swapping red and green.
 */
function paceTone(status: UsagePace['status']): { className: string; glyph: string } {
  if (status === 'ahead') return { className: 'text-red', glyph: '!' };
  if (status === 'behind') return { className: 'text-green', glyph: '↗' };
  return { className: 'text-green', glyph: '✓' };
}

export function UsageWindowRow({
  w,
  mode,
  hidden,
  onToggleHidden,
  now,
}: {
  w: UsageWindow;
  mode: 'used' | 'remaining';
  hidden: boolean;
  onToggleHidden: () => void;
  now: number;
}): JSX.Element {
  const { t } = useLingui();
  const remainingMode = mode === 'remaining';
  const used = String(Math.round(w.percentUsed));
  const left = String(Math.round(w.percentLeft));
  const label = w.label;

  const headerText = remainingMode ? t`${left}% remaining` : t`${used}% used`;
  const footerText = remainingMode ? t`${used}% used` : t`${left}% left`;
  const barPercent = Math.min(
    100,
    Math.max(0, remainingMode ? Math.round(w.percentLeft) : Math.round(w.percentUsed)),
  );

  // Server text wins; recompute only when the window carries an instant but no
  // text, so the countdown stays live between the modal's 30s polls.
  const resetText =
    w.resetText ?? (w.resetAt !== undefined ? fmtResetsIn(w.resetAt, now) : undefined);
  const resetChip = w.resetAt !== undefined ? fmtResetAt(w.resetAt, now) : '';

  const pace = w.pace;
  const tone = pace === undefined ? null : paceTone(pace.status);
  // Elapsed marker mirrors with the bar: in Remaining mode the bar fills from
  // the left with what is LEFT, so "you are here" sits at 100 - percentElapsed.
  const markerPercent =
    pace === undefined
      ? 0
      : Math.min(100, Math.max(0, remainingMode ? 100 - pace.percentElapsed : pace.percentElapsed));

  return (
    <div className={`rounded-[9px] border border-line px-2.5 py-2 ${hidden ? 'opacity-55' : ''}`}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="min-w-0 truncate font-mono text-[11.5px] text-ink">{w.label}</span>
        <span className="flex shrink-0 items-baseline gap-1.5">
          {!hidden && (
            <span className="font-mono text-[10.5px] tabular-nums text-ink-dim">{headerText}</span>
          )}
          <button
            type="button"
            onClick={onToggleHidden}
            aria-label={hidden ? t`Show ${label}` : t`Hide ${label}`}
            aria-pressed={hidden}
            data-tip={hidden ? t`show this window` : t`hide this window`}
            className={`rounded-[5px] px-1 leading-none transition-colors hover:bg-surface2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand ${
              hidden ? 'text-ink-faint' : 'text-ink-dim hover:text-ink'
            }`}
          >
            <span aria-hidden="true" className="font-mono text-[11px]">
              ◉
            </span>
          </button>
        </span>
      </div>

      {!hidden && (
        <>
          {/* Wrapper is the positioning context for the elapsed marker; the
              track itself clips the fill so the rounded ends stay clean. */}
          <div className="relative mt-1.5">
            <div
              className="h-[8px] overflow-hidden rounded-full bg-field"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={barPercent}
              aria-label={`${w.label}: ${headerText}`}
            >
              <div
                className={`h-full rounded-full ${fillClass(w.percentUsed)}`}
                style={{ width: `${barPercent.toFixed(1)}%` }}
              />
            </div>
            {pace !== undefined && (
              <span
                aria-hidden="true"
                className="absolute top-0 h-[8px] w-[2px] rounded-full bg-ink-faint"
                style={{ left: `${markerPercent.toFixed(1)}%`, marginLeft: '-1px' }}
              />
            )}
          </div>

          <div className="mt-1 flex items-baseline justify-between gap-2 font-mono text-[10px]">
            <span className="tabular-nums text-ink-dim">{footerText}</span>
            <span className="flex min-w-0 shrink items-baseline gap-1.5">
              {resetText !== undefined && resetText !== '' && (
                <span className="truncate italic text-ink-faint">{resetText}</span>
              )}
              {resetChip !== '' && (
                <span className="shrink-0 rounded-[5px] bg-field px-1.5 py-0.5 whitespace-nowrap text-ink-dim">
                  {resetChip}
                </span>
              )}
            </span>
          </div>

          {pace !== undefined && tone !== null && (
            <div className={`mt-1 flex items-baseline gap-1.5 font-mono text-[10px] ${tone.className}`}>
              <span aria-hidden="true" className="shrink-0">
                {tone.glyph}
              </span>
              <span className="min-w-0 break-words">{pace.message}</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}
