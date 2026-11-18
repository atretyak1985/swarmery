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
//
// A collapsed list (lib/panels.ts) leaves a 34px strip, only from `desk` up: below
// it there is one column and nothing to collapse (D-3).
//
// This component's own root is `[data-place-layout]`, main's (or the workspace
// shell's) direct child on a fill route — so the mobile scroll cannot live on
// that root itself (scripts/screenshot.mjs's assertNoNestedVerticalScroll bans a
// scrolling direct child of the shell scroller). The `overflow-y-auto` instead
// sits on an inner wrapper, one level deeper, that collapses to `desk:contents`
// so the grid at `desk` still sees `list`'s and `detail`'s wrappers as its direct
// items, unchanged from before this wrapper existed. That wrapper is also
// `flex flex-col` below `desk`: the detail pane's `flex-1` (narrow === 'detail')
// needs a flex parent to claim the wrapper's full height, or SessionPane's own
// `h-full` root resolves to its content height instead and its internal
// scroller (panelRef) stops scrolling — the header scrolls away with it.
//
// When both panes show below `desk` (`narrow === 'both'`), they are still two
// islands, not one flat stack: each gets the desk frame, 8px apart and 8px in
// from the edges, so the list never reads as part of the detail under it.

import type { ReactNode } from 'react';
import { PANEL_EASE, usePanels } from '../../lib/panels';

export type NarrowPane = 'both' | 'list' | 'detail';

export function PlaceLayout({
  list,
  detail,
  bareDetail = false,
  narrow = 'both',
}: {
  list: ReactNode;
  detail: ReactNode;
  /** Strips the detail pane's own border, background and scroll (Plans only,
   * D-17): `detail` builds its own two stacked islands — a fixed band and a
   * scrolling one, 8px apart — instead of sharing this one frame. Below `desk`
   * there is still one column: `detail` keeps flowing plainly, same as a framed
   * pane would. */
  bareDetail?: boolean;
  /** Below `desk`: which pane shows. From `desk` up both always do. */
  narrow?: NarrowPane;
}): JSX.Element {
  const { list: collapsed } = usePanels();
  const stacked = narrow === 'both';
  const columns = collapsed
    ? 'desk:grid-cols-[34px_minmax(0,1fr)]'
    : 'desk:grid-cols-[clamp(280px,28vw,360px)_minmax(0,1fr)]';
  return (
    <div
      data-place-layout=""
      className={`flex h-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden transition-[grid-template-columns] desk:grid desk:grid-rows-[minmax(0,1fr)] desk:gap-2 ${columns} ${PANEL_EASE}`}
    >
      <div
        className={`flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto desk:contents ${stacked ? 'gap-2 p-2' : ''}`}
      >
        <div
          className={
            narrow === 'detail'
              ? 'hidden desk:contents'
              : stacked
                ? 'flex shrink-0 flex-col overflow-hidden rounded-xl border border-line desk:contents'
                : 'contents'
          }
        >
          {list}
        </div>
        <div
          className={`min-h-0 min-w-0 ${
            bareDetail
              ? 'desk:flex desk:min-h-0 desk:flex-col desk:gap-2'
              : 'bg-surface desk:block desk:overflow-y-auto desk:rounded-xl desk:border desk:border-line'
          } ${narrow === 'list' ? 'hidden' : narrow === 'detail' ? 'flex-1' : ''} ${
            stacked ? 'max-desk:shrink-0 max-desk:overflow-hidden max-desk:rounded-xl max-desk:border max-desk:border-line' : ''} ${stacked && bareDetail ? 'max-desk:bg-bg' : ''
          }`}
        >
          {detail}
        </div>
      </div>
    </div>
  );
}
