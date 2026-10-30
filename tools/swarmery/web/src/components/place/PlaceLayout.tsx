// The frame of a place (handoff §1): list · detail beside the shell's sidebar, so
// together sidebar 196px · list clamp(280px, 28%, 360px) · detail. The sidebar
// belongs to the shell, which is why the list's 28% is 28vw here: the design takes
// it of the whole grid, and this grid starts after the sidebar. It must keep
// resolving to the same 360px at 1440.
//
// Below `desk` it is one column, never a horizontal scroller (D-6): the list and
// the detail in one scroll, or just one of them when `narrow` says which (an open
// session should not sit under the whole list). `h-full` takes the shell's height
// on a route that keeps the shell's scroller, and `flex-1` the same on a fill
// route. The grid row is `minmax(0,1fr)`: an auto row would grow to the list's
// content and the list would never scroll.

import type { ReactNode } from 'react';

export type NarrowPane = 'both' | 'list' | 'detail';

export function PlaceLayout({
  list,
  detail,
  narrow = 'both',
}: {
  list: ReactNode;
  detail: ReactNode;
  /** Below `desk`: which pane shows. From `desk` up both always do. */
  narrow?: NarrowPane;
}): JSX.Element {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col overflow-y-auto desk:grid desk:grid-cols-[clamp(280px,28vw,360px)_minmax(0,1fr)] desk:grid-rows-[minmax(0,1fr)] desk:overflow-hidden">
      <div className={narrow === 'detail' ? 'hidden desk:contents' : 'contents'}>{list}</div>
      <div
        className={`min-h-0 min-w-0 bg-surface desk:block desk:overflow-y-auto ${
          narrow === 'list' ? 'hidden' : narrow === 'detail' ? 'flex-1' : ''
        }`}
      >
        {detail}
      </div>
    </div>
  );
}
