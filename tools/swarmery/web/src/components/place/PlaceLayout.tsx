// The frame of a place (handoff §1): list · detail beside the shell's sidebar, so
// together sidebar 196px · list clamp(280px, 28%, 360px) · detail. The sidebar
// belongs to the shell, which is why the list's 28% is 28vw here: the design takes
// it of the whole grid, and this grid starts after the sidebar. It must keep
// resolving to the same 360px at 1440.
//
// Below `desk` it is one column, the list and then the detail in one scroll
// (D-6), never a horizontal scroller. A route rendering this declares
// `handle: { fill: true }` so the shell hands it the height.

import type { ReactNode } from 'react';

export function PlaceLayout({ list, detail }: { list: ReactNode; detail: ReactNode }): JSX.Element {
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto desk:grid desk:grid-cols-[clamp(280px,28vw,360px)_minmax(0,1fr)] desk:overflow-hidden">
      {list}
      <div className="min-h-0 min-w-0 bg-surface desk:overflow-y-auto">{detail}</div>
    </div>
  );
}
