// The browser tab title of whatever is open (plans-deep-links phase 2, SC-11):
// history dropdowns and bookmarks can only tell entries apart by their title,
// and index.html's is a constant "Swarmery".
//
// `null` leaves the title alone — a parent that nests a child with its own
// title (PlansPlace around Plans) must stand down, because React runs a child's
// effects BEFORE its parent's and the parent would otherwise win.

import { useEffect } from 'react';

/** Set `document.title` while mounted; restore the previous one on unmount. */
export function useDocumentTitle(title: string | null): void {
  useEffect(() => {
    if (title === null) return;
    const prev = document.title;
    document.title = title;
    return () => {
      document.title = prev;
    };
  }, [title]);
}
