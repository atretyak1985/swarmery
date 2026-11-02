// The glyph on every column toggle (sidebar, list, context rail): a panel outline
// with the column it hides drawn as a bar on that side. It replaced the ◂ ▸ « »
// characters, which read too faint at 11 px. Decorative: the button carries the
// accessible name.

export interface PanelIconProps {
  /** Which edge the toggled column sits on. */
  side: 'left' | 'right';
  className?: string;
}

export function PanelIcon({ side, className }: PanelIconProps): JSX.Element {
  const bar = side === 'left' ? 5.5 : 10.5;
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 16 14"
      width="16"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      className={className}
    >
      <rect x="1.25" y="1.25" width="13.5" height="11.5" rx="3" />
      <line x1={bar} y1="4.5" x2={bar} y2="9.5" />
    </svg>
  );
}
