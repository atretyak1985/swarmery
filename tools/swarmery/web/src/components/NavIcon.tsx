// The sidebar's icon set (phase 3, D-12): one outline per place, drawn the same
// way as PanelIcon — plain stroke primitives on a shared viewBox, never a glyph
// character, so every row reads at the same visual weight. The design handoff
// (Swarmery v4.dc.html, ICON) that named these shapes is not checked into this
// repo; the outlines below are a standalone redraw with the same semantics —
// expect the operator to ask for touch-ups once they see a screenshot.
//
// name is PlaceId so every sidebar/mobile-nav row has exactly one icon, plus
// 'search' for the ⌘K action row, which is not a place.

import type { PlaceId } from '../lib/nav';

export type NavIconName = PlaceId | 'search';

const PATHS: Record<NavIconName, JSX.Element> = {
  today: (
    <>
      <path d="M4 10.5h16" />
      <path d="M8 3v3M16 3v3" />
      <rect x="4" y="5" width="16" height="15" rx="2" />
      <path d="M9.5 15l2-3.5 2 2.5 2-3" />
    </>
  ),
  inbox: (
    <>
      <path d="M3 12h5l1.6 2.5h4.8L16 12h5" />
      <path d="M5.3 5.4A1.5 1.5 0 0 1 6.7 4.5h10.6a1.5 1.5 0 0 1 1.4.9L21 12v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-6l2.3-6.6z" />
    </>
  ),
  'needs-you': (
    <>
      <path d="M6 3v18" />
      <path d="M6 4h11l-2.3 3.3L17 11H6" />
    </>
  ),
  sessions: (
    <>
      <path d="M5 6.5l6 5.5-6 5.5" />
      <path d="M13 18h7" />
    </>
  ),
  plans: (
    <>
      <rect x="6" y="4.5" width="12" height="16" rx="2" />
      <path d="M9.5 4a1.5 1.5 0 0 1 1.5-1.5h2A1.5 1.5 0 0 1 14.5 4" />
      <path d="M9 11h6M9 14.5h6M9 17.5h3.5" />
    </>
  ),
  health: (
    <path d="M12 20s-7.2-4.4-9.6-8.7C1 8.2 2.6 4.8 6 4.8c2 0 3.2 1.2 4 2.3.8-1.1 2-2.3 4-2.3 3.4 0 5 3.4 3.6 6.5C19.2 15.6 12 20 12 20z" />
  ),
  learning: (
    <>
      <path d="M4 20l0.9-4 10.6-10.6 3.1 3.1L8 19.1 4 20z" />
      <path d="M13.2 6.7l3.1 3.1" />
    </>
  ),
  knowledge: (
    <>
      <path d="M12 6.2C11 5 9.3 4.3 6.4 4.3c-0.9 0-1.6.7-1.6 1.6v11c0 .9.7 1.6 1.6 1.6 2.9 0 4.6.7 5.6 1.9 1-1.2 2.7-1.9 5.6-1.9.9 0 1.6-.7 1.6-1.6v-11c0-.9-.7-1.6-1.6-1.6-2.9 0-4.6.7-5.6 1.9z" />
      <path d="M12 6.2v13.2" />
    </>
  ),
  docs: (
    <>
      <path d="M7.5 3.5h6l4 4v12a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-15a1 1 0 0 1 1-1z" />
      <path d="M13.5 3.5V8h4" />
      <path d="M9.5 12.5h5M9.5 15.8h5" />
    </>
  ),
  system: (
    <>
      <rect x="4" y="4" width="7" height="7" rx="1.2" />
      <rect x="13" y="4" width="7" height="7" rx="1.2" />
      <rect x="4" y="13" width="7" height="7" rx="1.2" />
      <rect x="13" y="13" width="7" height="7" rx="1.2" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M12 3v3.2M12 17.8V21M4.4 7.6l2.8 1.6M16.8 14.8l2.8 1.6M4.4 16.4l2.8-1.6M16.8 9.2l2.8-1.6" />
    </>
  ),
  search: (
    <>
      <circle cx="10.8" cy="10.8" r="6.8" />
      <path d="M20 20l-4.4-4.4" />
    </>
  ),
};

export function NavIcon({ name, size = 16 }: { name: NavIconName; size?: number }): JSX.Element {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {PATHS[name]}
    </svg>
  );
}
