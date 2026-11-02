// The disclosure mark on everything that folds open: a 12 px chevron pointing right
// when closed and down when open. It replaced the ▸ ▾ characters, which at 9–11 px
// were too small to tell a foldable row from plain text. It draws in its own colour
// (ink-dim) rather than the faint ink most fold labels use, so the affordance reads
// at a glance. Decorative: the toggle carries aria-expanded.

export interface ChevronProps {
  open: boolean;
  className?: string;
}

export function Chevron({ open, className = 'text-ink-dim' }: ChevronProps): JSX.Element {
  return (
    <svg
      aria-hidden="true"
      data-chevron={open ? 'open' : 'closed'}
      viewBox="0 0 12 12"
      width="12"
      height="12"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`inline-block shrink-0 align-[-2px] transition-transform duration-150 ${open ? 'rotate-90' : ''} ${className}`}
    >
      <path d="M4.5 2.5 8 6l-3.5 3.5" />
    </svg>
  );
}
