// Inline-SVG glyphs for the terminal dock's window controls (maximize, full
// screen, minimize). 16×16, drawn in currentColor so they follow the button's
// text colour, and aria-hidden: every button carries its own aria-label.

import type { ReactNode } from 'react';

function Icon({ children }: { children: ReactNode }): JSX.Element {
  return (
    <svg
      aria-hidden="true"
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {children}
    </svg>
  );
}

/** One window frame: grow the dock over the page column. */
export function MaximizeIcon(): JSX.Element {
  return (
    <Icon>
      <rect x="3" y="3" width="10" height="10" rx="1" />
    </Icon>
  );
}

/** Two stacked frames: give the page column back. */
export function RestoreIcon(): JSX.Element {
  return (
    <Icon>
      <rect x="3" y="5.5" width="7.5" height="7.5" rx="1" />
      <path d="M5.5 5.5V4a1 1 0 0 1 1-1H12a1 1 0 0 1 1 1v5.5a1 1 0 0 1-1 1h-1.5" />
    </Icon>
  );
}

/** Four corners pointing out: take the whole screen. */
export function FullscreenIcon(): JSX.Element {
  return (
    <Icon>
      <path d="M3 6V3h3M10 3h3v3M13 10v3h-3M6 13H3v-3" />
    </Icon>
  );
}

/** Four corners pointing in: leave full screen. */
export function ExitFullscreenIcon(): JSX.Element {
  return (
    <Icon>
      <path d="M6 3v3H3M13 6h-3V3M10 13v-3h3M3 10h3v3" />
    </Icon>
  );
}

/** A single bar: hide the dock, its terminals keep running. */
export function MinimizeIcon(): JSX.Element {
  return (
    <Icon>
      <path d="M3.5 8h9" />
    </Icon>
  );
}
