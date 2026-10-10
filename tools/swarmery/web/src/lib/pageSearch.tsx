// Page-scoped header search: one search input lives in the app header and
// filters the CURRENT page's content (sessions by title, projects by name,
// system components by name, …). The query is shared through this context —
// the header owns the input, each page reads the query and filters its list.
// The query resets on navigation so a filter never leaks between sections.

import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { t } from '@lingui/core/macro';
import { useLocation } from 'react-router-dom';

interface PageSearchValue {
  /** Raw header-search text (trim/lowercase at the point of use). */
  query: string;
  setQuery: (q: string) => void;
}

const PageSearchContext = createContext<PageSearchValue>({
  query: '',
  setQuery: () => undefined,
});

export function PageSearchProvider({ children }: { children: ReactNode }): JSX.Element {
  const [query, setQuery] = useState('');
  const { pathname } = useLocation();
  // Clear the filter when moving to another section — a title filter on
  // /sessions must not carry over to /projects.
  useEffect(() => {
    setQuery('');
  }, [pathname]);
  const value = useMemo(() => ({ query, setQuery }), [query]);
  return <PageSearchContext.Provider value={value}>{children}</PageSearchContext.Provider>;
}

/** Header input binds to this (raw value + setter). */
export function usePageSearchControl(): PageSearchValue {
  return useContext(PageSearchContext);
}

/** Pages read the normalised (trimmed, lowercased) query to filter their list. */
export function usePageSearch(): string {
  return useContext(PageSearchContext).query.trim().toLowerCase();
}

/** Placeholder for the header input per route — null hides the input on pages
 * that have no searchable list (analytics, docs, detail views). */
export function pageSearchPlaceholder(pathname: string): string | null {
  if (pathname === '/') return t`filter sessions by title…`;
  // Sessions matches plan titles and phase names too (lib/sessionsView), so the
  // placeholder promises what it actually searches. Project mode renders the
  // same page under /p/<slug>/sessions and needs the same box.
  if (pathname === '/sessions' || /^\/p\/[^/]+\/sessions$/.test(pathname)) {
    return t`filter by title or plan…`;
  }
  if (pathname === '/projects') return t`filter projects by name…`;
  // The Approvals page (rules + history) lives at approvals/manage since the
  // Inbox took over /approvals; project mode renders it under /p/<slug>/.
  if (/^(\/p\/[^/]+)?\/approvals\/manage$/.test(pathname)) return t`filter approvals…`;
  // /system(/*) is the tabbed System shell — its embedded hubs carry their own
  // in-pane search box, so the header search is hidden there (like /agents).
  return null;
}
