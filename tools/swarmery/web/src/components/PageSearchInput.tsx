// In-page search box (moved out of the app header): filters the current page's
// list via the shared PageSearch context. Renders the right placeholder per
// route and hides itself on pages with no searchable list (returns null). Drop
// it at the top of a searchable page's body — sessions, projects, approvals,
// the command deck. The query resets on navigation (PageSearchProvider), so a
// filter never leaks between sections.

import { useLingui } from '@lingui/react/macro';
import { useLocation } from 'react-router-dom';
import { pageSearchPlaceholder, usePageSearchControl } from '../lib/pageSearch';

export function PageSearchInput({ className = '' }: { className?: string }): JSX.Element | null {
  const { t } = useLingui();
  const { pathname } = useLocation();
  const { query, setQuery } = usePageSearchControl();
  const placeholder = pageSearchPlaceholder(pathname);
  if (placeholder === null) return null;
  return (
    <div className={`relative w-full sm:max-w-[300px] ${className}`}>
      <span
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-[13px] leading-none text-ink-faint"
      >
        ⌕
      </span>
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
        className="w-full rounded-[9px] border border-line-strong bg-field py-[6px] pr-8 pl-7 font-mono text-[12px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-ink-dim"
      />
      {query !== '' && (
        <button
          type="button"
          onClick={() => setQuery('')}
          aria-label={t`clear filter`}
          className="absolute top-1/2 right-2 -translate-y-1/2 font-mono text-[13px] leading-none text-ink-dim transition-colors hover:text-ink"
        >
          ×
        </button>
      )}
    </div>
  );
}
