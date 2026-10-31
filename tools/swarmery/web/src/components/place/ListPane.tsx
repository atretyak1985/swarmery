// The list column of a place (handoff §1): a header with the title, a mono
// sub-line, the search field and an optional `+ new`; a scrolling body of groups;
// a footer of key hints. Every place has this same shape, so the row keys are
// wired here once rather than in each page.
//
// Collapsed (lib/panels.ts) it shows a 34px strip, from `desk` up only. The full
// pane stays mounted behind `desk:hidden` (D-2): the page's refs and scroll sentinel
// hold on to its nodes, and j/k have to keep moving the selection.

import { Trans, useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import { togglePanel, useListBadge, usePanels } from '../../lib/panels';
import { useGlobalKeys, type GlobalKeyHandlers } from '../../lib/useGlobalKeys';

export type ListKeys = Pick<GlobalKeyHandlers, 'onNext' | 'onPrev' | 'onPrimary' | 'onSecondary' | 'onEscape'>;

export function ListPane({
  title,
  sub,
  query,
  onQueryChange,
  searchHint,
  onNew,
  tabs,
  strip,
  empty,
  keys = {},
  primaryHint,
  secondaryHint,
  children,
}: {
  title: string;
  /** The mono line under the title: `4 sessions · 2 need you`. */
  sub?: string;
  query: string;
  onQueryChange: (next: string) => void;
  searchHint: string;
  /** Sessions and Plans only. */
  onNew?: () => void;
  /** Optional filter row under the search field. */
  tabs?: ReactNode;
  /** The list's tab strip (ListTabs), on the header's foot. */
  strip?: ReactNode;
  /** Rendered instead of the groups when there is nothing to list. */
  empty?: ReactNode;
  keys?: ListKeys;
  /** What `e` does here; shown in the footer. Defaults to "open". */
  primaryHint?: string;
  /** What `x` does here; the hint is hidden when `x` does nothing. */
  secondaryHint?: string;
  children?: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  const { list: collapsed } = usePanels();
  const badge = useListBadge();
  useGlobalKeys(keys);
  return (
    <section
      aria-label={title}
      // Below `desk` the list and the detail share one scroll (D-6): shrinking here
      // would squeeze the rows away behind the detail.
      className="flex min-h-0 min-w-0 shrink-0 flex-col bg-bg desk:shrink desk:border-r desk:border-line"
    >
      {collapsed && (
        <button
          type="button"
          onClick={() => togglePanel('list')}
          aria-label={t`expand list · ]`}
          data-tip={t`expand list · ]`}
          className="hidden min-h-0 flex-1 flex-col items-center gap-2.5 py-3.5 transition-colors hover:bg-surface desk:flex"
        >
          <span aria-hidden="true" className="font-mono text-[11px] leading-[normal] text-ink-dim">
            ▸
          </span>
          <span className="rotate-180 text-[12px] leading-[normal] font-semibold tracking-[0.02em] text-ink-2 [writing-mode:vertical-rl]">
            {title}
          </span>
          {badge !== null && (
            <span className="rounded-full bg-brand-bg px-1.5 font-mono text-[10px] leading-4 font-bold text-brand">
              {badge}
            </span>
          )}
        </button>
      )}
      <div className={collapsed ? 'contents desk:hidden' : 'contents'}>
        <div className="px-[14px] pt-[14px] pb-2">
          <div className="flex items-baseline gap-2">
            <h1 className="m-0 text-[15px] leading-[normal] font-semibold tracking-[-0.01em] text-ink">{title}</h1>
            <button
              type="button"
              onClick={() => togglePanel('list')}
              aria-label={t`collapse list · ]`}
              data-tip={t`collapse list · ]`}
              className="ml-auto hidden h-5 w-[22px] self-center rounded-[5px] border border-transparent p-0 font-mono text-[11px] leading-[normal] text-ink-faint transition-colors hover:border-line-strong hover:text-ink desk:block"
            >
              ◂
            </button>
          </div>
          {sub !== undefined && (
            <div className="-mt-0.5 flex items-baseline gap-2">
              <span className="font-mono text-[10.5px] leading-[normal] text-ink-faint">{sub}</span>
            </div>
          )}
          <div className="mt-[10px] flex items-center gap-1.5">
            <input
              type="search"
              value={query}
              onChange={(e) => onQueryChange(e.target.value)}
              placeholder={searchHint}
              aria-label={searchHint}
              className="min-w-0 flex-1 rounded-[6px] border border-line bg-surface px-[9px] py-[5px] font-mono text-[11px] leading-[normal] text-ink outline-none focus:border-line-strong"
            />
            {onNew !== undefined && (
              <button
                type="button"
                onClick={onNew}
                className="rounded-[6px] border border-brand bg-brand-bg px-[10px] py-[5px] font-mono text-[11px] leading-[normal] font-bold whitespace-nowrap text-brand"
              >
                <Trans>+ new</Trans>
              </button>
            )}
          </div>
          {tabs !== undefined && <div className="mt-[10px] flex flex-wrap gap-1">{tabs}</div>}
          {strip}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{empty ?? children}</div>
        <div className="hidden flex-wrap gap-x-3 gap-y-1 border-t border-line px-[14px] py-2 font-mono text-[10px] leading-[normal] text-ink-faint desk:flex">
          <span>{t`j/k select`}</span>
          {/* A msgid of its own: bare "open" is taken by the status, "відкрито". */}
          <span>{primaryHint === undefined ? t`e open` : `e ${primaryHint}`}</span>
          {secondaryHint !== undefined && <span>{`x ${secondaryHint}`}</span>}
          <span>{t`1–5 places`}</span>
          <span>{t`[ ] \\ panels`}</span>
        </div>
      </div>
    </section>
  );
}
