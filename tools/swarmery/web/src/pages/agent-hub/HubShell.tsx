// HubShell — the reusable split-pane "hub" layout: a searchable/filterable
// roster on the left, a tabbed detail pane on the right. Built for the Agent Hub
// (fusion phase 17) and deliberately GENERIC so the System Hub (phase 18) mounts
// the same shell with a different roster source and different tabs.
//
// Contract for Phase 18 (System Hub) reuse
// ----------------------------------------
// HubShell is data-agnostic. It owns: the two-pane responsive grid, the roster
// search box + optional filter chips, the roster scroll list (render-prop rows),
// the empty/loading/error states, the tab bar, and the "which tab is active"
// state (URL-synced by the caller via activeTab/onTab). It owns NO fetching and
// NO knowledge of agents/skills — the caller supplies:
//   • roster:      T[] | null            (null = loading)
//   • rosterError: string | null
//   • selectedKey / onSelect             (stable id per row; caller controls URL)
//   • renderRow(item, selected)          (a roster card body)
//   • rowKey(item) / rowMatches(item, q) (identity + client-side search)
//   • filters?                           (optional chip row, fully caller-defined)
//   • tabs / activeTab / onTab           (tab ids + labels; caller renders panel)
//   • children                           (the active tab's panel for the selection)
//   • detailHeader?                      (sticky identity header above the tabs)
//   • title / rosterEmptyLabel / searchPlaceholder
// Phase 18 will pass skills/commands/hooks/templates as T, its own row renderer,
// and its own tab set — no fork, no copy. Keep this component free of any
// agent-specific import.

import { i18n } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useMemo, useState, type ReactNode } from 'react';
import { Empty, ErrorBox, Loading } from '../../components/ui';

/** One selectable tab. `id` is stable (URL-synced by the caller). */
export interface HubTab {
  id: string;
  label: string;
  /** Optional count badge (neutral tone). */
  badge?: number;
}

export interface HubShellProps<T> {
  /** Page title shown above the roster. Omit to skip the `<h1>` entirely — used
   * when the shell is embedded under an outer tab bar that owns the heading. */
  title?: string;
  /** When true AND nothing is selected, the roster fills the full width and the
   * detail pane is not rendered at all (reverts to the split-pane on selection).
   * Defaults off so the standard placeholder-rail behaviour is unchanged. */
  hideDetailWhenUnselected?: boolean;
  /** Roster rows, or null while loading. */
  roster: T[] | null;
  rosterError: string | null;
  /** Retry handler for the roster error state. */
  onRosterRetry?: () => void;
  /** Stable identity per roster row (used for selection + React keys). */
  rowKey: (item: T) => string;
  /** Client-side search predicate (query is pre-lowercased). */
  rowMatches: (item: T, query: string) => boolean;
  /** Roster card body for one row. */
  renderRow: (item: T, selected: boolean) => ReactNode;
  /** Currently selected row key (null = nothing selected). */
  selectedKey: string | null;
  onSelect: (key: string | null) => void;
  /** Optional caller-defined filter chip row, rendered under the search box. */
  filters?: ReactNode;
  /** Full-width bar rendered above the split area (e.g. scope filter chips),
   * spanning both the roster and detail columns — like the toolkit catalog. */
  topBar?: ReactNode;
  /** Placeholder for the roster search input. */
  searchPlaceholder?: string;
  /** Empty-roster message. */
  rosterEmptyLabel?: string;
  /** Tabs for the detail pane. */
  tabs: HubTab[];
  activeTab: string;
  onTab: (id: string) => void;
  /** Sticky identity header above the tab bar (e.g. name + health + actions). */
  detailHeader?: ReactNode;
  /** Placeholder shown in the detail pane when no row is selected. */
  detailPlaceholder?: ReactNode;
  /** The active tab's panel for the current selection. */
  children?: ReactNode;
}

export function HubShell<T>({
  title,
  hideDetailWhenUnselected = false,
  roster,
  rosterError,
  onRosterRetry,
  rowKey,
  rowMatches,
  renderRow,
  selectedKey,
  onSelect,
  filters,
  topBar,
  searchPlaceholder: searchPlaceholderProp,
  rosterEmptyLabel: rosterEmptyLabelProp,
  tabs,
  activeTab,
  onTab,
  detailHeader,
  detailPlaceholder,
  children,
}: HubShellProps<T>): JSX.Element {
  const { t } = useLingui();
  const searchPlaceholder = searchPlaceholderProp ?? t`filter…`;
  const rosterEmptyLabel = rosterEmptyLabelProp ?? t`nothing here yet`;
  const [query, setQuery] = useState('');

  const filtered = useMemo(() => {
    if (roster === null) return null;
    const q = query.trim().toLowerCase();
    return q === '' ? roster : roster.filter((r) => rowMatches(r, q));
  }, [roster, query, rowMatches]);

  const selected = selectedKey !== null;
  // Full-width roster mode: caller opted in AND nothing is selected. The detail
  // pane is omitted entirely and the roster is not constrained to a grid column.
  const rosterOnly = hideDetailWhenUnselected && !selected;

  const rosterList = (
    <>
      {filtered === null && <Loading label={t`roster…`} />}
      {filtered !== null && filtered.length === 0 && (
        <Empty>{roster !== null && roster.length > 0 ? t`no matches` : rosterEmptyLabel}</Empty>
      )}
      {filtered !== null &&
        filtered.map((item) => {
          const key = rowKey(item);
          const isSel = key === selectedKey;
          return (
            <button
              key={key}
              type="button"
              onClick={() => onSelect(isSel ? null : key)}
              aria-current={isSel ? 'true' : undefined}
              className={`block w-full border-b border-line-soft px-3.5 py-3 text-left transition-colors last:border-b-0 ${
                isSel ? 'bg-surface2' : 'hover:bg-surface'
              }`}
            >
              {renderRow(item, isSel)}
            </button>
          );
        })}
    </>
  );

  return (
    <div className="flex h-full flex-col px-4 pt-6 pb-6 desk:px-10 desk:pt-[34px] desk:pb-[34px]">
      {title !== undefined && (
        <h1 className="mb-4 font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
          {title}
        </h1>
      )}

      {topBar !== undefined && <div className="mb-4 shrink-0">{topBar}</div>}

      {rosterError !== null ? (
        <ErrorBox message={rosterError} {...(onRosterRetry !== undefined ? { onRetry: onRosterRetry } : {})} />
      ) : (
        <div
          className={
            rosterOnly
              ? 'flex min-h-0 flex-1 flex-col'
              : 'min-h-0 flex-1 wide:grid wide:grid-cols-[minmax(300px,380px)_minmax(0,1fr)] wide:gap-6 wide:overflow-hidden'
          }
        >
          {/* Roster pane: search + optional filters (fixed) + scroll list. */}
          <div className="flex min-h-0 flex-col">
            <div className="shrink-0 pb-3">
              <div className="relative">
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
                  placeholder={searchPlaceholder}
                  aria-label={searchPlaceholder}
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
              {filters !== undefined && <div className="mt-2.5">{filters}</div>}
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto rounded-xl border border-line [-webkit-overflow-scrolling:touch]">
              {rosterList}
            </div>
          </div>

          {/* Detail pane: sticky header + tab bar + active panel. On narrow
              viewports it stacks under the roster (the grid collapses). Omitted
              entirely in rosterOnly mode (full-width roster, nothing selected). */}
          {!rosterOnly && (
          <div className="mt-6 flex min-h-0 flex-col overflow-hidden rounded-xl border border-line bg-surface wide:mt-0">
            {!selected ? (
              <div className="flex flex-1 items-center justify-center p-8">
                {detailPlaceholder ?? (
                  <Empty>
                    <Trans>select a row</Trans>
                  </Empty>
                )}
              </div>
            ) : (
              <>
                {detailHeader !== undefined && (
                  <div className="shrink-0 border-b border-line px-[18px] pt-4 pb-3">{detailHeader}</div>
                )}
                <div
                  className="shrink-0 flex gap-1 overflow-x-auto border-b border-line px-[18px] [-webkit-overflow-scrolling:touch]"
                  role="tablist"
                >
                  {tabs.map((tab) => (
                    <button
                      key={tab.id}
                      type="button"
                      role="tab"
                      aria-selected={activeTab === tab.id}
                      onClick={() => onTab(tab.id)}
                      className={`-mb-px shrink-0 border-b-2 px-3 py-[8px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
                        activeTab === tab.id
                          ? 'border-brand text-brand'
                          : 'border-transparent text-ink-dim hover:text-ink'
                      }`}
                    >
                      {tab.label}
                      {tab.badge !== undefined && tab.badge > 0 && (
                        <span className="ml-1.5 inline-flex h-[16px] min-w-[16px] items-center justify-center rounded-full bg-line-strong px-1 align-middle font-mono text-[9.5px] font-bold text-ink-dim">
                          {tab.badge}
                        </span>
                      )}
                    </button>
                  ))}
                </div>
                <div
                  className="min-h-0 flex-1 overflow-y-auto px-[18px] py-4 [-webkit-overflow-scrolling:touch]"
                  role="tabpanel"
                >
                  {children}
                </div>
              </>
            )}
          </div>
          )}
        </div>
      )}
    </div>
  );
}

/** Health dot thresholds shared by the roster + profile header:
 * <30% failed-run share = green, <60% = amber, else red. */
export function healthTone(failedShare: number): { dot: string; label: string } {
  if (failedShare < 0.3) return { dot: 'bg-green', label: i18n._(msg`healthy`) };
  if (failedShare < 0.6) return { dot: 'bg-amber', label: i18n._(msg`degraded`) };
  return { dot: 'bg-red', label: i18n._(msg`unhealthy`) };
}
