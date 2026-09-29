// The SW◆RMERY wordmark shared by both shell headers (App.tsx, the fleet shell,
// and workspace/WorkspaceShell.tsx, the project shell). The "A" is the brand
// mark — the same nested hexagons as public/favicon.svg, drawn inline with
// currentColor so it follows the theme's brand token instead of the favicon's
// fixed palette.

/** The brand mark alone: an outer hexagon ring around a solid inner hexagon. */
export function BrandMark({ className = '' }: { className?: string }): JSX.Element {
  return (
    <svg viewBox="0 0 32 32" aria-hidden="true" focusable="false" className={className} fill="currentColor">
      <path
        fillRule="evenodd"
        d="M16 2.4 4.22 9.2v13.6L16 29.6l11.78-6.8V9.2Z M16 6.9 8.12 11.45v9.1L16 25.1l7.88-4.55v-9.1Z"
      />
      <path d="M16 11.5 12.1 13.75v4.5L16 20.5l3.9-2.25v-4.5Z" />
    </svg>
  );
}

/** "SW", the mark in place of the A, "RMERY". The accessible name comes from
 * the surrounding link's aria-label; the mark itself is decorative. */
export function Wordmark(): JSX.Element {
  return (
    <>
      SW
      <BrandMark className="mx-[0.12em] h-[1.05em] w-[1.05em] shrink-0 text-brand" />
      RMERY
    </>
  );
}
