// The one header band of a detail column (Swarmery v4): the page colour, a line under
// it, and 18px above whatever each place puts inside. A session, a plan and a phase
// keep their tabs in the band, so they sit on its foot (`flush`); the rest close it
// with 16px. The band is the first thing in the column, and a blocker or a notice
// stands above its content, inside it.
//
// `bleed` is for a band inside a padded column: it reaches back out to the column's
// edges, the design's `margin: 0 -28px`.
//
// The band stays put while the detail scrolls under it, from `desk` up: below it the
// list and the detail share one scroll and a tall band would take the screen. Its
// parent must hold the body too, because sticky stops at the parent's edge. Its height
// goes to `--detail-band-h`, for whatever anchors or sticks in the scroll beneath it.
//
// A phase nests under its plan (`stack`): the plan's band stays on top and publishes
// its height, and the phase's band, one step lighter, sticks right under it.

import { Trans } from '@lingui/react/macro';
import { type ReactNode, useEffect, useRef } from 'react';
import { Link } from 'react-router-dom';

const BAND_HEIGHT_VAR = '--detail-band-h';
const PARENT_BAND_HEIGHT_VAR = '--detail-parent-band-h';

export function DetailHeader({
  banner,
  flush = false,
  bleed = false,
  stack,
  children,
}: {
  /** Full-width strips above the content: BlockerBanner, NoticeBand. */
  banner?: ReactNode;
  /** Tabs close the band, so it has no foot of its own. */
  flush?: boolean;
  bleed?: boolean;
  /** Two bands in one column: `parent` on top, `child` under it. */
  stack?: 'parent' | 'child' | undefined;
  children: ReactNode;
}): JSX.Element {
  const child = stack === 'child';
  const heightVar = stack === 'parent' ? PARENT_BAND_HEIGHT_VAR : BAND_HEIGHT_VAR;
  const bandRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const band = bandRef.current;
    if (band === null || typeof ResizeObserver === 'undefined') return;
    const root = document.documentElement;
    const observer = new ResizeObserver(() => {
      root.style.setProperty(heightVar, `${String(band.offsetHeight)}px`);
    });
    observer.observe(band);
    return () => {
      observer.disconnect();
      root.style.removeProperty(heightVar);
    };
  }, [heightVar]);

  return (
    <div
      ref={bandRef}
      style={child ? { top: `var(${PARENT_BAND_HEIGHT_VAR}, 0px)` } : undefined}
      className={`shrink-0 border-b border-line desk:sticky ${
        child ? 'bg-surface desk:z-10' : 'bg-bg desk:top-0 desk:z-20'
      } ${bleed ? '-mx-4 desk:-mx-7' : ''}`}
    >
      {banner}
      <div className={`px-4 pt-[18px] desk:px-7 ${flush ? '' : 'pb-4'}`}>{children}</div>
    </div>
  );
}

/** What blocks on the operator, above everything else on a plan or a phase. */
export function BlockerBanner({
  to,
  lead,
  detail,
}: {
  to: string;
  lead: string;
  /** Grey, after the lead: `2 guard stops, 1 manual check`. */
  detail?: string | undefined;
}): JSX.Element {
  return (
    <Link
      to={to}
      className="flex items-center gap-2.5 border-b border-brand bg-brand-bg px-4 py-[9px] text-[12.5px] leading-[normal] text-ink desk:px-7"
    >
      <span aria-hidden="true" className="h-2 w-2 shrink-0 animate-blink-dot rounded-full bg-brand" />
      <span className="min-w-0">
        <b className="font-semibold">{lead}</b>
        {detail !== undefined && detail !== '' && <span className="text-ink-dim"> · {detail}</span>}
      </span>
      <span className="ml-auto font-mono text-[10.5px] whitespace-nowrap text-brand">
        <Trans>Needs you →</Trans>
      </span>
    </Link>
  );
}

/** A plain state line above the band's content: paused, archived. */
export function NoticeBand({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="border-b border-line px-4 py-2 font-mono text-[11px] leading-[normal] text-ink-dim desk:px-7">
      {children}
    </div>
  );
}
